package catalog

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"regexp"
	"slices"
	"strings"
)

const sesIdentitySchema = `CREATE TABLE IF NOT EXISTS aws_ses_domain_identities (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, region TEXT NOT NULL, domain TEXT NOT NULL,
 zone_id TEXT NOT NULL, marker TEXT NOT NULL UNIQUE, first_token TEXT NOT NULL,
 second_token TEXT NOT NULL, third_token TEXT NOT NULL, UNIQUE(region,domain))`
const sesIdentityColumns = "domain,zone_id,marker,first_token,second_token,third_token"

var sesDomainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var sesSelector = regexp.MustCompile(`^[a-z2-7]{32}$`)

type sesLocalIdentity struct {
	domain, zone, marker string
	tokens               [3]string
}

func sesDomainName(name string) bool {
	if !strings.HasSuffix(name, ".test") || len(name)+len("._domainkey.")+32 > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if !sesDomainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func sesChallenge() (string, error) {
	var bytes [20]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes[:])), nil
}

func readSESIdentity(row interface{ Scan(...any) error }) (sesLocalIdentity, error) {
	var i sesLocalIdentity
	err := row.Scan(&i.domain, &i.zone, &i.marker, &i.tokens[0], &i.tokens[1], &i.tokens[2])
	if err != nil {
		return i, err
	}
	if !sesDomainName(i.domain) || i.zone == "" || !sesSelector.MatchString(i.marker) {
		return i, errors.New("invalid saved SES domain identity")
	}
	for k, token := range i.tokens {
		if !sesSelector.MatchString(token) || slices.Contains(i.tokens[:k], token) {
			return i, errors.New("invalid saved SES selector set")
		}
	}
	return i, nil
}

func (i sesLocalIdentity) output(verified bool, summary bool, creating bool) map[string]any {
	status := "PENDING"
	if verified {
		status = "SUCCESS"
	}
	if summary {
		return map[string]any{"IdentityType": "DOMAIN", "IdentityName": i.domain, "VerificationStatus": status}
	}
	tokens := make([]any, 0, 3)
	for _, token := range i.tokens {
		tokens = append(tokens, token)
	}
	result := map[string]any{"IdentityType": "DOMAIN", "VerifiedForSendingStatus": verified, "DkimAttributes": map[string]any{"Tokens": tokens, "Status": status}}
	if !creating {
		result["VerificationStatus"] = status
	}
	return result
}

func (q *request) sesDomainIdentity() (map[string]any, error) {
	name, _ := q.input["EmailIdentity"].(string)
	res := "*"
	if q.op.Kind != List {
		if !sesDomainName(name) {
			return nil, invalid("Use a lowercase ASCII domain under .test with room for the three verification records.")
		}
		res = q.formatARN(q.resource(), name, name, nil)
	}
	if err := q.authorize(res); err != nil {
		return nil, err
	}
	for k := range q.input {
		if (q.op.Kind == List && (k == "NextToken" || k == "PageSize")) || (q.op.Kind != List && k == "EmailIdentity") {
			continue
		}
		return nil, invalid("Unsupported domain identity input member %s.", k)
	}
	if q.e.cfg.SESDomainDNS == nil {
		return nil, errors.New("local SES DNS composition unavailable")
	}
	tx, err := q.e.store.db.BeginTx(q.ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var result map[string]any
	switch q.op.Kind {
	case Create:
		result, err = q.createSESDomain(tx, name)
	case List:
		result, err = q.listSESDomains(tx)
	case Read, Delete:
		var i sesLocalIdentity
		i, err = readSESIdentity(tx.QueryRowContext(q.ctx, "SELECT "+sesIdentityColumns+" FROM aws_ses_domain_identities WHERE region=? AND domain=?", q.region, name))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, Failure("NotFoundException", "The local email identity does not exist.")
		}
		if err != nil {
			return nil, err
		}
		if q.op.Kind == Read {
			result, err = q.projectSESDomain(tx, i, false)
		} else {
			err = exactlyOne(tx.ExecContext(q.ctx, "DELETE FROM aws_ses_domain_identities WHERE region=? AND domain=? AND marker=?", q.region, name, i.marker))
			if err == nil {
				var remains bool
				err = tx.QueryRowContext(q.ctx, "SELECT EXISTS(SELECT 1 FROM aws_ses_domain_identities WHERE region=? AND domain=?)", q.region, name).Scan(&remains)
				if err == nil && remains {
					err = errors.New("deleted SES identity is still present")
				}
			}
			result = map[string]any{}
		}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (q *request) createSESDomain(tx *sql.Tx, name string) (map[string]any, error) {
	var exists bool
	if err := tx.QueryRowContext(q.ctx, "SELECT EXISTS(SELECT 1 FROM aws_ses_domain_identities WHERE region=? AND domain=?)", q.region, name).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, Failure("AlreadyExistsException", "A local domain identity already exists.")
	}
	var count int
	if err := tx.QueryRowContext(q.ctx, "SELECT count(*) FROM aws_ses_domain_identities WHERE region=?", q.region).Scan(&count); err != nil {
		return nil, err
	}
	if count >= 100 {
		return nil, Failure("LimitExceededException", "The local identity limit is 100 per region.")
	}
	zone, _, err := q.e.cfg.SESDomainDNS(q.ctx, tx, name, nil)
	if err != nil {
		return nil, err
	}
	if zone == "" {
		return nil, invalid("Create one exact local public hosted zone before creating the domain identity.")
	}
	i := sesLocalIdentity{domain: name, zone: zone}
	i.marker, err = sesChallenge()
	if err != nil {
		return nil, err
	}
	for k := range i.tokens {
		for {
			v, e := sesChallenge()
			if e != nil {
				return nil, e
			}
			if !slices.Contains(i.tokens[:k], v) {
				i.tokens[k] = v
				break
			}
		}
	}
	err = exactlyOne(tx.ExecContext(q.ctx, "INSERT INTO aws_ses_domain_identities(region,"+sesIdentityColumns+") VALUES(?,?,?,?,?,?,?)", q.region, i.domain, i.zone, i.marker, i.tokens[0], i.tokens[1], i.tokens[2]))
	if err != nil {
		return nil, err
	}
	stored, err := readSESIdentity(tx.QueryRowContext(q.ctx, "SELECT "+sesIdentityColumns+" FROM aws_ses_domain_identities WHERE region=? AND domain=?", q.region, name))
	if err != nil {
		return nil, err
	}
	if stored != i {
		return nil, errors.New("local SES domain identity write did not retain its verification challenge")
	}
	return i.output(false, false, true), nil
}

func (q *request) projectSESDomain(tx *sql.Tx, i sesLocalIdentity, summary bool) (map[string]any, error) {
	zone, verified, err := q.e.cfg.SESDomainDNS(q.ctx, tx, i.domain, i.tokens[:])
	if err != nil {
		return nil, err
	}
	return i.output(zone == i.zone && verified, summary, false), nil
}

func (q *request) listSESDomains(tx *sql.Tx) (map[string]any, error) {
	limit := int64(100)
	var after int64
	if n, ok := q.input["PageSize"]; ok {
		// The REST shape decoder already normalized the query integer.
		size, ok := n.(int64)
		if !ok || size < 1 || size > 100 {
			return nil, invalid("Local PageSize must be 1–100.")
		}
		limit = size
	}
	if raw, ok := q.input["NextToken"]; ok {
		token, _ := raw.(string)
		err := tx.QueryRowContext(q.ctx, "SELECT seq FROM aws_ses_domain_identities WHERE region=? AND marker=?", q.region, token).Scan(&after)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, invalid("Unknown or retired identity page token.")
		}
		if err != nil {
			return nil, err
		}
	}
	rows, err := tx.QueryContext(q.ctx, "SELECT "+sesIdentityColumns+" FROM aws_ses_domain_identities WHERE region=? AND seq>? ORDER BY seq LIMIT ?", q.region, after, limit+1)
	if err != nil {
		return nil, err
	}
	var page []sesLocalIdentity
	for rows.Next() {
		i, e := readSESIdentity(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		page = append(page, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if int64(len(page)) > limit {
		page = page[:len(page)-1]
		out["NextToken"] = page[len(page)-1].marker
	}
	list := make([]any, 0, len(page))
	for _, i := range page {
		v, e := q.projectSESDomain(tx, i, true)
		if e != nil {
			return nil, e
		}
		list = append(list, v)
	}
	out["EmailIdentities"] = list
	return out, nil
}
