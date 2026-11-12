package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The document API refuses malformed, out-of-scope or unsupported requests
// before any store access, with the documented error code.
func TestSSMDocumentAPIRefusesInvalidRequests(t *testing.T) {
	g, s := documentFixture(t)
	create := `{"Name":"doc-a","Content":` + jsonQuote(documentTestContent) + `}`
	if w := documentHTTP(g, s, "CreateDocument", create); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for name, c := range map[string]struct{ op, body, code string }{
		"unknown operation":   {"SendCommand", `{}`, "UnknownOperationException"},
		"not JSON":            {"GetDocument", `nope`, "ValidationException"},
		"JSON null":           {"GetDocument", `null`, "ValidationException"},
		"field of other op":   {"DeleteDocument", `{"Name":"doc-a","Content":"x"}`, "ValidationException"},
		"invalid name":        {"GetDocument", `{"Name":"bad name"}`, "ValidationException"},
		"AWS-owned name":      {"GetDocument", `{"Name":"AWS-RunShellScript"}`, "ValidationException"},
		"YAML format":         {"CreateDocument", `{"Name":"doc-b","Content":"{}","DocumentFormat":"YAML"}`, "ValidationException"},
		"session type":        {"CreateDocument", `{"Name":"doc-b","Content":"{}","DocumentType":"Session"}`, "ValidationException"},
		"update w/o version":  {"UpdateDocument", `{"Name":"doc-a","Content":"{}"}`, "InvalidDocumentVersion"},
		"zero MaxResults":     {"ListDocuments", `{"MaxResults":0}`, "ValidationException"},
		"too many MaxResults": {"ListDocuments", `{"MaxResults":51}`, "ValidationException"},
		"oversized body":      {"CreateDocument", `{"Name":"doc-b","Content":"` + strings.Repeat("a", 513<<10) + `"}`, "ValidationException"},
	} {
		w := documentHTTP(g, s, c.op, c.body)
		if w.Code < 400 || !strings.Contains(w.Body.String(), c.code) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// A console parameter handoff never reaches document management.
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"Name":"doc-a"}`))
	r = r.WithContext(context.WithValue(r.Context(), consoleParameterBoundaryKey{}, consoleParameterBoundary{name: "/p", region: "us-east-1"}))
	w := httptest.NewRecorder()
	g.serveSSMDocuments(w, r, s, "us-east-1", "GetDocument")
	if w.Code != 400 && w.Code != 403 || !strings.Contains(w.Body.String(), "AccessDenied") {
		t.Fatal("console handoff reached documents", w.Code, w.Body.String())
	}
	// No store configured.
	if w := documentHTTP(g, nil, "GetDocument", `{"Name":"doc-a"}`); !strings.Contains(w.Body.String(), "UnknownOperationException") {
		t.Fatal("nil store", w.Body.String())
	}
	// An unknown non-administrator principal has no policy to evaluate.
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"Name":"doc-a"}`))
	r.Header.Set("X-Simulator-Principal", "ghost-user")
	w = httptest.NewRecorder()
	g.serveSSMDocuments(w, r, s, "us-east-1", "GetDocument")
	if w.Code == 200 {
		t.Fatal("unknown principal read a document")
	}
}

func TestSSMDocumentListCursorsAreBoundToRequestAndSnapshot(t *testing.T) {
	g, s := documentFixture(t)
	for _, name := range []string{"doc-a", "doc-b", "doc-c"} {
		if w := documentHTTP(g, s, "CreateDocument", `{"Name":"`+name+`","Content":`+jsonQuote(documentTestContent)+`}`); w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	page := func(body string) (int, map[string]any) {
		w := documentHTTP(g, s, "ListDocuments", body)
		out := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	code, first := page(`{"MaxResults":2}`)
	token, _ := first["NextToken"].(string)
	if code != 200 || len(first["DocumentIdentifiers"].([]any)) != 2 || token == "" {
		t.Fatal(code, first)
	}
	if code, rest := page(`{"MaxResults":2,"NextToken":"` + token + `"}`); code != 200 || len(rest["DocumentIdentifiers"].([]any)) != 1 || rest["NextToken"] != nil {
		t.Fatal(code, rest)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(token)
	var cursor ssmDocumentCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatal(err)
	}
	forge := func(edit func(*ssmDocumentCursor)) string {
		c := cursor
		edit(&c)
		b, _ := json.Marshal(c)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	for name, tok := range map[string]string{
		"not base64":      "!!!",
		"oversized":       strings.Repeat("A", 2049),
		"other region":    forge(func(c *ssmDocumentCursor) { c.Region = "eu-west-1" }),
		"other principal": forge(func(c *ssmDocumentCursor) { c.Principal = "someone" }),
		"other size":      forge(func(c *ssmDocumentCursor) { c.Size = 1 }),
		"stale snapshot":  forge(func(c *ssmDocumentCursor) { c.Fingerprint = strings.Repeat("0", 64) }),
		"zero offset":     forge(func(c *ssmDocumentCursor) { c.Offset = 0 }),
		"past the end":    forge(func(c *ssmDocumentCursor) { c.Offset = 4 }),
		"misaligned":      forge(func(c *ssmDocumentCursor) { c.Offset = 1 }),
	} {
		if code, out := page(`{"MaxResults":2,"NextToken":"` + tok + `"}`); code == 200 {
			t.Errorf("%s token accepted: %v", name, out)
		}
	}
	// Changing the document set invalidates outstanding cursors.
	if w := documentHTTP(g, s, "DeleteDocument", `{"Name":"doc-c"}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if code, _ := page(`{"MaxResults":2,"NextToken":"` + token + `"}`); code == 200 {
		t.Fatal("cursor survived a snapshot change")
	}
}

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
