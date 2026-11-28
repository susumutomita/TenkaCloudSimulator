package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const sesSinkSchema = `CREATE TABLE IF NOT EXISTS aws_ses_sink_messages (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, region TEXT NOT NULL, id TEXT UNIQUE NOT NULL,
 sender TEXT NOT NULL, recipient TEXT NOT NULL, template TEXT NOT NULL, accepted_at TEXT NOT NULL,
 outcome TEXT NOT NULL CHECK(outcome IN ('delivered','rejected')), reason TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS aws_ses_sink_region ON aws_ses_sink_messages(region,seq);
CREATE TABLE IF NOT EXISTS aws_ses_local_inbox (
 id TEXT PRIMARY KEY REFERENCES aws_ses_sink_messages(id) ON DELETE CASCADE, mime BLOB NOT NULL, sha256 BLOB NOT NULL CHECK(length(sha256)=32))`

const localSESMailbox = "inbox@inbox.ses.test"
const maxSESMIME = 256 * 1024

var sesMailboxPart = regexp.MustCompile(`^[a-zA-Z0-9]+([._+-][a-zA-Z0-9]+)*$`)
var sesTemplateName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type sesSubmission struct {
	from, domain, to, template string
	templateData               string
	parts                      map[string]string
}

func sesOnlyMembers(obj map[string]any, allowed ...string) error {
	for key := range obj {
		if !slices.Contains(allowed, key) {
			return invalid("Unsupported local SendEmail member %s.", key)
		}
	}
	return nil
}

func sesAddress(addr string) (string, bool) {
	local, domain, ok := strings.Cut(addr, "@")
	return domain, ok && len(addr) <= 254 && len(local) <= 64 && sesMailboxPart.MatchString(local) && sesDomainName(domain)
}

func sesSimpleText(obj map[string]any) (string, error) {
	if err := sesOnlyMembers(obj, "Data", "Charset"); err != nil {
		return "", err
	}
	v, ok := obj["Data"].(string)
	if !ok || !utf8.ValidString(v) {
		return "", invalid("A local message part requires UTF-8 Data.")
	}
	if raw, exists := obj["Charset"]; exists && !strings.EqualFold(dataText(raw), "UTF-8") {
		return "", invalid("Only UTF-8 message parts are supported.")
	}
	return v, nil
}

func readSESSubmission(input map[string]any) (sesSubmission, error) {
	var v sesSubmission
	if err := sesOnlyMembers(input, "FromEmailAddress", "Destination", "Content"); err != nil {
		return v, err
	}
	v.from = dataText(input["FromEmailAddress"])
	var ok bool
	v.domain, ok = sesAddress(v.from)
	if !ok {
		return v, invalid("Use a bare ASCII sender address at a local .test domain.")
	}
	dest, _ := input["Destination"].(map[string]any)
	if err := sesOnlyMembers(dest, "ToAddresses"); err != nil {
		return v, err
	}
	to, _ := dest["ToAddresses"].([]any)
	if len(to) != 1 {
		return v, invalid("Supply exactly one local To address.")
	}
	v.to = dataText(to[0])
	domain, ok := sesAddress(v.to)
	if !ok || domain != "inbox.ses.test" {
		return v, invalid("The local mailbox sink accepts one @inbox.ses.test destination.")
	}
	content, _ := input["Content"].(map[string]any)
	if len(content) != 1 {
		return v, invalid("Use exactly one Simple or Template email content.")
	}
	if template, ok := content["Template"].(map[string]any); ok {
		if err := sesOnlyMembers(template, "TemplateName", "TemplateData"); err != nil {
			return v, err
		}
		v.template = dataText(template["TemplateName"])
		v.templateData, ok = template["TemplateData"].(string)
		if !sesTemplateName.MatchString(v.template) || !ok {
			return v, invalid("Supply a saved template name and a JSON TemplateData string.")
		}
		return v, nil
	}
	simple, ok := content["Simple"].(map[string]any)
	if !ok {
		return v, invalid("This sink accepts Simple and saved Template content.")
	}
	if err := sesOnlyMembers(simple, "Subject", "Body"); err != nil {
		return v, err
	}
	subject, _ := simple["Subject"].(map[string]any)
	text, err := sesSimpleText(subject)
	if err != nil {
		return v, err
	}
	v.parts = map[string]string{"Subject": text}
	body, _ := simple["Body"].(map[string]any)
	if len(body) == 0 {
		return v, invalid("Write a Text or Html message body.")
	}
	if err := sesOnlyMembers(body, "Text", "Html"); err != nil {
		return v, err
	}
	for name, raw := range body {
		field, _ := raw.(map[string]any)
		part, err := sesSimpleText(field)
		if err != nil {
			return v, err
		}
		v.parts[name] = part
	}
	return v, nil
}

func (q *request) authorizeSESSend(s sesSubmission) error {
	if q.e.cfg.AuthorizeSnapshot == nil {
		return errors.New("SES send policy context unavailable")
	}
	decide, err := q.e.cfg.AuthorizeSnapshot(q.ctx, q.principal)
	if err != nil {
		return err
	}
	if decide == nil {
		return errors.New("SES send policy evaluator unavailable")
	}
	identity := q.formatARN(q.resource(), s.domain, s.domain, nil)
	if err = q.checkDecision(decide("ses:SendEmail", identity)); err != nil {
		return err
	}
	if s.template != "" {
		template := q.formatARN(q.b.spec.resource("emailTemplate"), s.template, s.template, nil)
		return q.checkDecision(decide("ses:SendEmail", template))
	}
	return nil
}

func (q *request) sesSubmitMIME(tx *sql.Tx, s sesSubmission, id string, at time.Time) ([]byte, error) {
	var rendered map[string]any
	var err error
	if s.template == "" {
		rendered, err = composeSESMIME(q.ctx, s.parts)
	} else {
		t, errLookup := find(tx, "sesv2", q.region, "emailTemplate", "", s.template)
		if errors.Is(errLookup, sql.ErrNoRows) || errLookup == nil && t.Name != s.template {
			return nil, Failure("NotFoundException", "The named local template does not exist.")
		}
		if errLookup != nil {
			return nil, errLookup
		}
		rendered, err = renderSESTemplate(q.ctx, t.Document["TemplateContent"], s.templateData)
	}
	if err != nil {
		return nil, err
	}
	data := fmt.Sprintf("From: %s\r\nTo: %s\r\nDate: %s\r\nMessage-ID: <%s@inbox.ses.test>\r\n", s.from, s.to, at.Format(time.RFC1123Z), id) + dataText(rendered["RenderedTemplate"])
	if len(data) > maxSESMIME {
		return nil, invalid("The complete local email exceeds 256 KiB.")
	}
	return []byte(data), nil
}

func (q *request) sendSESEmail() (map[string]any, error) {
	s, err := readSESSubmission(q.input)
	if err != nil {
		return nil, err
	}
	if err = q.authorizeSESSend(s); err != nil {
		return nil, err
	}
	if q.e.cfg.SESDomainDNS == nil {
		return nil, errors.New("local SES DNS composition unavailable")
	}
	tx, err := q.e.store.db.BeginTx(q.ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	identity, err := readSESIdentity(tx.QueryRowContext(q.ctx, "SELECT "+sesIdentityColumns+" FROM aws_ses_domain_identities WHERE region=? AND domain=?", q.region, s.domain))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Failure("MessageRejected", "The sender domain is not verified in this region.")
	}
	if err != nil {
		return nil, err
	}
	zone, proof, err := q.e.cfg.SESDomainDNS(q.ctx, tx, s.domain, identity.tokens[:])
	if err != nil {
		return nil, err
	}
	if !proof || zone != identity.zone {
		return nil, Failure("MessageRejected", "The sender domain is not verified in this region.")
	}
	var count int
	if err = tx.QueryRowContext(q.ctx, "SELECT count(*) FROM aws_ses_sink_messages WHERE region=?", q.region).Scan(&count); err != nil {
		return nil, err
	}
	if count >= 100 {
		return nil, Failure("LimitExceededException", "The local region already holds 100 accepted messages.")
	}
	id, err := sesChallenge()
	if err != nil {
		return nil, err
	}
	at := q.now().UTC()
	raw, err := q.sesSubmitMIME(tx, s, id, at)
	if err != nil {
		return nil, err
	}
	if err = receiveLocalSES(q.ctx, tx, q.region, s, id, at, raw); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"MessageId": id}, nil
}

// The local mailbox accepts bytes only for its configured recipient. The
// envelope and its actual inbox append or refusal are one submitted message.
func receiveLocalSES(ctx context.Context, tx *sql.Tx, region string, s sesSubmission, id string, at time.Time, raw []byte) error {
	outcome, reason := "delivered", ""
	if s.to != localSESMailbox {
		outcome, reason = "rejected", "mailbox-not-found"
	}
	err := exactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_ses_sink_messages(region,id,sender,recipient,template,accepted_at,outcome,reason) VALUES(?,?,?,?,?,?,?,?)", region, id, s.from, s.to, s.template, at.Format(time.RFC3339Nano), outcome, reason))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if outcome == "delivered" {
		if err = exactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_ses_local_inbox(id,mime,sha256) VALUES(?,?,?)", id, raw, digest[:])); err != nil {
			return err
		}
	}
	var saved, matches bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM aws_ses_sink_messages WHERE region=? AND id=? AND sender=? AND recipient=? AND template=? AND accepted_at=? AND outcome=? AND reason=?),
	 CASE WHEN ?='delivered' THEN EXISTS(SELECT 1 FROM aws_ses_local_inbox WHERE id=? AND mime=? AND sha256=?) ELSE NOT EXISTS(SELECT 1 FROM aws_ses_local_inbox WHERE id=?) END`,
		region, id, s.from, s.to, s.template, at.Format(time.RFC3339Nano), outcome, reason, outcome, id, raw, digest[:], id).Scan(&saved, &matches)
	if err != nil {
		return err
	}
	if !saved || !matches {
		return errors.New("local mailbox did not retain the submitted envelope and recipient outcome")
	}
	return nil
}
