package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"path/filepath"
	"strings"
	"testing"
)

const sesSendPath = "/v2/email/outbound-emails"
const sesSinkEast = "/_simulator/v1/ses/sink/us-east-1/messages"

func localSendBody(recipient string, simple bool) []byte {
	content := map[string]any{"Template": map[string]any{"TemplateName": "welcome", "TemplateData": `{"who":"visitor"}`}}
	if simple {
		content = map[string]any{"Simple": map[string]any{"Subject": map[string]any{"Data": "ようこそ"}, "Body": map[string]any{"Text": map[string]any{"Data": "{{untouched}}"}, "Html": map[string]any{"Data": "<i>body</i>", "Charset": "UTF-8"}}}}
	}
	body, _ := json.Marshal(map[string]any{"FromEmailAddress": "sender@owned.test", "Destination": map[string]any{"ToAddresses": []string{recipient}}, "Content": content})
	return body
}

func localSend(c sesDomainClient, recipient string, simple bool, status int) string {
	c.t.Helper()
	raw := c.call("ses", "POST", sesSendPath, string(localSendBody(recipient, simple)), status)
	var r map[string]any
	if err := json.Unmarshal(raw, &r); err != nil {
		c.t.Fatal(err)
	}
	value, _ := r["MessageId"].(string)
	if status == 200 && (len(value) != 32 || len(r) != 1) || status != 200 && value != "" {
		c.t.Fatalf("unexpected submission receipt: %d %q", status, raw)
	}
	return value
}

func localSinkSummary(c sesDomainClient) map[string]map[string]any {
	c.t.Helper()
	var result struct {
		Messages []map[string]any `json:"messages"`
	}
	raw := c.call("", "GET", sesSinkEast, "", 200)
	if err := json.Unmarshal(raw, &result); err != nil || result.Messages == nil {
		c.t.Fatal("invalid local sink list", err, string(raw))
	}
	out := make(map[string]map[string]any, len(result.Messages))
	for _, r := range result.Messages {
		out[r["messageId"].(string)] = r
	}
	return out
}

func verifyLocalMIME(t *testing.T, raw []byte, id, subject, text, html string) {
	t.Helper()
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	s, err := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if err != nil || s != subject || message.Header.Get("From") != "sender@owned.test" || message.Header.Get("To") != "inbox@inbox.ses.test" || message.Header.Get("Message-ID") != "<"+id+"@inbox.ses.test>" {
		t.Fatal("actual stored MIME headers differ")
	}
	if _, err := mail.ParseDate(message.Header.Get("Date")); err != nil {
		t.Fatal(err)
	}
	kind, attrs, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	if html == "" {
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
		if err != nil || kind != "text/plain" || string(data) != text {
			t.Fatal("template bytes differ", err)
		}
		return
	}
	if kind != "multipart/alternative" {
		t.Fatal("expected complete alternative MIME")
	}
	parts := multipart.NewReader(message.Body, attrs["boundary"])
	for _, want := range []string{text, html} {
		p, err := parts.NextRawPart()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, p))
		if err != nil || string(data) != want {
			t.Fatal("simple MIME bytes differ", err)
		}
	}
}

func TestSESLocalMailboxDeliveryAndRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owner.db")
	owner, closeOwner := openSESDomainClient(t, path, "a")
	defer func() { closeOwner() }()
	peer, closePeer := openSESDomainClient(t, filepath.Join(t.TempDir(), "peer.db"), "b")
	defer closePeer()
	const inbox = "inbox@inbox.ses.test"
	const missing = "absent@inbox.ses.test"
	const domain = "owned.test"
	zone := owner.zone(domain, "mail-sink")
	tokens := owner.identity("POST", domain, 200)[1:]
	localSend(owner, inbox, true, 400)
	for _, token := range tokens {
		owner.record(zone, "CREATE", domain, token, token+".dkim.amazonses.com", 200)
	}
	owner.call("ses", "POST", "/v2/email/templates", `{"TemplateName":"welcome","TemplateContent":{"Subject":"Hello {{who}}","Text":"Body {{who}}"}}`, 200)
	first := localSend(owner, inbox, true, 200)
	rejected := localSend(owner, missing, true, 200)
	templated := localSend(owner, inbox, false, 200)
	if first == rejected || rejected == templated || first == templated {
		t.Fatal("distinct submissions require distinct IDs")
	}
	if err := closeOwner(); err != nil {
		t.Fatal(err)
	}
	owner, closeOwner = openSESDomainClient(t, path, "a")
	records := localSinkSummary(owner)
	if len(records) != 3 || records[first]["outcome"] != "delivered" || records[templated]["template"] != "welcome" || records[rejected]["outcome"] != "rejected" || records[rejected]["reason"] != "mailbox-not-found" || records[rejected]["contentBytes"] != float64(0) {
		t.Fatal("durable delivery and refusal facts differ")
	}
	verifyLocalMIME(t, owner.call("", "GET", sesSinkEast+"/"+first+"/content", "", 200), first, "ようこそ", "{{untouched}}", "<i>body</i>")
	verifyLocalMIME(t, owner.call("", "GET", sesSinkEast+"/"+templated+"/content", "", 200), templated, "Hello visitor", "Body visitor", "")
	owner.call("", "GET", sesSinkEast+"/"+rejected+"/content", "", 404)
	peer.call("", "GET", sesSinkEast+"/"+first, "", 404)
	if len(localSinkSummary(peer)) != 0 {
		t.Fatal("peer saw sender's receipts")
	}
	owner.call("", "GET", strings.Replace(sesSinkEast, "us-east-1", "us-west-2", 1)+"/"+first, "", 404)
	owner.call("ses", "GET", "/v2/email/insights/"+first+"/", "", 501)
	before, _ := json.Marshal(localSinkSummary(owner))
	owner.record(zone, "DELETE", domain, tokens[0], tokens[0]+".dkim.amazonses.com", 200)
	localSend(owner, inbox, true, 400)
	localSend(owner, inbox, false, 400)
	owner.record(zone, "CREATE", domain, tokens[0], tokens[0]+".dkim.amazonses.com", 200)
	localSend(owner, "external@example.org", true, 400)
	const identity = "arn:aws:ses:us-east-1:000000000000:identity/owned.test"
	const template = "arn:aws:ses:us-east-1:000000000000:template/welcome"
	allow := func(resources string) {
		t.Helper()
		owner.call("", "PUT", "/_simulator/v1/iam/principals/mailer/policy", fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ses:SendEmail","Resource":%s}]}`, resources), 200)
	}
	allow(fmt.Sprintf("%q", identity))
	data := owner.call("", "POST", "/_simulator/v1/access", `{"Principal":"mailer"}`, 200)
	var session struct{ AccessToken string }
	if err := json.Unmarshal(data, &session); err != nil || session.AccessToken == "" {
		t.Fatal("no participant token")
	}
	mailer := owner
	mailer.token = session.AccessToken
	mailer.call("", "GET", sesSinkEast, "", 403)
	mailer.call("", "DELETE", sesSinkEast+"/"+first, "", 403)
	localSend(mailer, inbox, false, 403)
	allow(fmt.Sprintf("%q", template))
	localSend(mailer, inbox, false, 403)
	after, _ := json.Marshal(localSinkSummary(owner))
	if !bytes.Equal(before, after) {
		t.Fatal("hard refusal changed an accepted envelope or mailbox")
	}
	allow(fmt.Sprintf("[%q,%q]", identity, template))
	permitted := localSend(mailer, inbox, false, 200)
	owner.call("ses", "DELETE", "/v2/email/templates/welcome", "", 200)
	localSend(mailer, inbox, false, 404)
	for _, id := range []string{first, rejected, templated, permitted} {
		owner.call("", "DELETE", sesSinkEast+"/"+id, "", 204)
		owner.call("", "GET", sesSinkEast+"/"+id, "", 404)
		owner.call("", "GET", sesSinkEast+"/"+id+"/content", "", 404)
	}
	if len(localSinkSummary(owner)) != 0 {
		t.Fatal("cleanup retained envelopes")
	}
	owner.identity("DELETE", domain, 200)
	for _, token := range tokens {
		owner.record(zone, "DELETE", domain, token, token+".dkim.amazonses.com", 200)
	}
	owner.call("route53", "DELETE", "/2013-04-01/hostedzone/"+zone, "", 200)
}
