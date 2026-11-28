package catalog

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http/httptest"
	"net/mail"
	"reflect"
	"strings"
	"testing"
)

func sesDecodedPart(t *testing.T, body io.Reader, encoding string) string {
	t.Helper()
	if encoding != "base64" {
		t.Fatal("unexpected transfer encoding", encoding)
	}
	data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, body))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSESPreviewStoredTemplateAndIAM(t *testing.T) {
	h := newHarness(t, sesV2)
	const template = "/v2/email/templates/welcome"
	const action = "ses:TestRenderEmailTemplate arn:aws:ses:us-east-1:000000000000:template/"
	const name = "美乃 =?utf-8?B?YQ==?= & <safe> {{again}}"
	subject := strings.Repeat("長い件名", 8) + " {{name}}"
	content := map[string]any{"Subject": subject, "Text": "Dear {{ name }}!\nWelcome.", "Html": "<p>{{name}}</p>"}
	expect(t, h.rest("sesv2", "POST", "/v2/email/templates", map[string]any{"TemplateName": "welcome", "TemplateContent": content}), 200, "")
	before := h.rest("sesv2", "GET", template, nil)
	expect(t, before, 200, "")
	request := map[string]any{"TemplateData": `{"name":"` + name + `"}`}
	r := h.rest("sesv2", "POST", template+"/render", request)
	expect(t, r, 200, "")
	if len(r.body) != 1 || h.policy.calls[len(h.policy.calls)-1] != "simulator-admin "+action+"welcome" {
		t.Fatal("unexpected response members or IAM check", r.body, h.policy.calls)
	}
	m, err := mail.ReadMessage(strings.NewReader(str(r.body["RenderedTemplate"])))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || len(m.Header["Subject"]) != 1 || decoded != strings.ReplaceAll(subject, "{{name}}", name) || m.Header.Get("MIME-Version") != "1.0" {
		t.Fatal("incorrect subject or MIME header", decoded, err, m.Header)
	}
	media, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
	if err != nil || media != "multipart/alternative" {
		t.Fatal(media, err)
	}
	reader := multipart.NewReader(m.Body, params["boundary"])
	for _, want := range []struct{ media, body string }{{"text/plain", "Dear " + name + "!\nWelcome."}, {"text/html", "<p>" + name + "</p>"}} {
		p, err := reader.NextRawPart()
		if err != nil {
			t.Fatal(err)
		}
		mt, params, err := mime.ParseMediaType(p.Header.Get("Content-Type"))
		if err != nil || mt != want.media || params["charset"] != "utf-8" || sesDecodedPart(t, p, p.Header.Get("Content-Transfer-Encoding")) != want.body {
			t.Fatal("incorrect MIME part", mt, params, err)
		}
	}
	if _, err := reader.NextRawPart(); err != io.EOF {
		t.Fatal("unexpected trailing MIME part", err)
	}
	for _, name := range []string{"welcome", "missing"} {
		h.policy.deny[action+name] = true
		expect(t, h.rest("sesv2", "POST", "/v2/email/templates/"+name+"/render", request), 403, "AccessDeniedException")
		delete(h.policy.deny, action+name)
	}
	expect(t, h.rest("sesv2", "POST", "/v2/email/templates/missing/render", request), 404, "NotFoundException")
	other := httptest.NewRequest("POST", template+"/render", strings.NewReader(`{"TemplateData":"{}"}`))
	sign(other, "ses", "ap-northeast-1")
	expect(t, h.do(other), 404, "NotFoundException")
	for _, data := range []string{`{}`, `{"name":null}`, `{"name":2}`, `{"name":{}}`, `{"name":[]}`, `{"name":"a","name":"b"}`, `{"name":"a"}{}`, `[]`, `{"name":"bad\nheader"}`, `{"name":"bad\theader"}`, `{"name":"a"`} {
		response := h.rest("sesv2", "POST", template+"/render", map[string]any{"TemplateData": data})
		expect(t, response, 400, "BadRequestException")
		if _, ok := response.body["RenderedTemplate"]; ok {
			t.Fatal("failure returned partial preview")
		}
	}
	now := h.rest("sesv2", "GET", template, nil)
	expect(t, now, 200, "")
	if !reflect.DeepEqual(now.body, before.body) {
		t.Fatal("preview changed stored template")
	}
	for _, rd := range h.engine.Describe()[0].Resources {
		if rd.Kind == "emailTemplate" && rd.Ops[string(Read)].Name != "GetEmailTemplate" {
			t.Fatal("Console read changed", rd.Ops[string(Read)])
		}
	}
}

func TestSESPreviewSingleBodyAndLimits(t *testing.T) {
	for _, tc := range []struct{ field, media, source string }{{"Text", "text/plain", ""}, {"Html", "text/html", "<p>こんにちは</p>"}} {
		result, err := renderSESTemplate(context.Background(), map[string]any{tc.field: tc.source}, "{}")
		if err != nil {
			t.Fatal(err)
		}
		m, err := mail.ReadMessage(strings.NewReader(str(result["RenderedTemplate"])))
		if err != nil {
			t.Fatal(err)
		}
		mt, params, err := mime.ParseMediaType(m.Header.Get("Content-Type"))
		if err != nil || mt != tc.media || params["charset"] != "utf-8" || sesDecodedPart(t, m.Body, m.Header.Get("Content-Transfer-Encoding")) != tc.source {
			t.Fatal("incorrect single part", mt, err)
		}
	}
	for _, content := range []map[string]any{
		{}, {"Subject": "Only subject"}, {"Text": 123},
		{"Text": "{{person.name}}"}, {"Text": "{{#if name}}yes{{/if}}"}, {"Text": "{{{name}}}"},
		{"Text": `\{{name}}`}, {"Text": "{{"}, {"Text": "}}"}, {"Text": "{{! comment }}"},
		{"Text": strings.Repeat("a", maxRequestBytes)}, {"Text": strings.Repeat("{{name}}", 24)},
	} {
		got, err := renderSESTemplate(context.Background(), content, `{"name":"`+strings.Repeat("x", 12000)+`"}`)
		if err == nil || got != nil {
			t.Fatal("unexpected preview on rejected template")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := renderSESTemplate(ctx, map[string]any{"Text": "hello"}, "{}"); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal("canceled read", got, err)
	}
}
