package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const documentTestContent = `{"schemaVersion":"2.2","mainSteps":[{"action":"aws:runShellScript","name":"hello","inputs":{"runCommand":["echo first"]}}]}`

func documentFixture(t *testing.T) (*Gateway, *SSMDocumentStore) {
	t.Helper()
	db, err := core.New(filepath.Join(t.TempDir(), "documents.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := NewSSMDocumentStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return &Gateway{Store: db, Identities: core.NewIdentities(db)}, store
}
func documentHTTP(g *Gateway, s *SSMDocumentStore, op, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	w := httptest.NewRecorder()
	g.serveSSMDocuments(w, r, s, "us-east-1", op)
	return w
}
func TestSSMDocumentsVersionLifecycle(t *testing.T) {
	_, s := documentFixture(t)
	ctx := context.Background()
	first, err := s.Save(ctx, "us-east-1", "local-doc", documentTestContent, "")
	if err != nil || first.DocumentVersion != "1" {
		t.Fatal(first, err)
	}
	if _, err = s.Save(ctx, "us-east-1", "local-doc", documentTestContent, ""); err == nil {
		t.Fatal("duplicate create accepted")
	}
	secondContent := strings.Replace(documentTestContent, "first", "second", 1)
	second, err := s.Save(ctx, "us-east-1", "local-doc", secondContent, "1")
	if err != nil || second.DocumentVersion != "2" || second.DefaultVersion != "1" {
		t.Fatal(second, err)
	}
	if _, err = s.Save(ctx, "us-east-1", "local-doc", documentTestContent, "1"); err == nil {
		t.Fatal("stale update accepted")
	}
	for selector, want := range map[string]string{"": "first", "$DEFAULT": "first", "1": "first", "$LATEST": "second", "2": "second"} {
		d, e := s.Get(ctx, "us-east-1", "local-doc", selector)
		if e != nil || !strings.Contains(d.Content, want) {
			t.Fatal(selector, d, e)
		}
	}
	for _, selector := range []string{"0", "01", "3", "$BOGUS"} {
		if _, e := s.Get(ctx, "us-east-1", "local-doc", selector); e == nil {
			t.Fatal(selector)
		}
	}
	if _, e := s.Get(ctx, "ap-northeast-1", "local-doc", ""); e == nil {
		t.Fatal("cross region read")
	}
	if err = s.Delete(ctx, "us-east-1", "local-doc"); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, "us-east-1", "local-doc"); err == nil {
		t.Fatal("repeated delete accepted")
	}
	var n int
	if err = s.db.QueryRow("SELECT count(*) FROM aws_ssm_document_versions").Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	recreated, err := s.Save(ctx, "us-east-1", "local-doc", secondContent, "")
	if err != nil || recreated.DocumentVersion != "1" {
		t.Fatal(recreated, err)
	}
}
func TestSSMDocumentsStrictAdmission(t *testing.T) {
	g, s := documentFixture(t)
	for _, body := range []string{`null`, `[]`, `{"Name":"local-doc","Name":"other"}`, `{"name":"local-doc"}`, `{"Name":null}`, `{"Name":"local-doc","Force":false}`, `{"Name":"local-doc"} {}`, `{"Name":"local-doc","Tags":[]}`} {
		if w := documentHTTP(g, s, "DeleteDocument", body); w.Code != 400 {
			t.Fatal(body, w.Code, w.Body.String())
		}
	}
	for _, content := range []string{strings.Replace(documentTestContent, "schemaVersion", "SchemaVersion", 1), strings.Replace(documentTestContent, `"schemaVersion":"2.2"`, `"schemaVersion":"2.2","schemaVersion":"2.2"`, 1), strings.Replace(documentTestContent, "aws:runShellScript", "aws:runDocument", 1), strings.Replace(documentTestContent, "echo first", "{{parameter}}", 1), strings.Replace(documentTestContent, "2.2", "2.0", 1)} {
		if _, e := s.Save(context.Background(), "us-east-1", "local-doc", content, ""); e == nil {
			t.Fatal("invalid content accepted", content)
		}
	}
	for _, name := range []string{"AWS-doc", "amazon-doc", "amzn-doc", "ab", "local/doc"} {
		if _, e := s.Save(context.Background(), "us-east-1", name, documentTestContent, ""); e == nil {
			t.Fatal(name)
		}
	}
}
func TestSSMDocumentsNativeManagement(t *testing.T) {
	g, s := documentFixture(t)
	body, _ := json.Marshal(map[string]any{"Name": "local-doc", "Content": documentTestContent, "DocumentType": "Command", "DocumentFormat": "JSON"})
	if w := documentHTTP(g, s, "CreateDocument", string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, op := range []string{"GetDocument", "DescribeDocument"} {
		if w := documentHTTP(g, s, op, `{"Name":"local-doc"}`); w.Code != 200 {
			t.Fatal(op, w.Code, w.Body.String())
		}
	}
	body, _ = json.Marshal(map[string]any{"Name": "local-doc", "Content": strings.Replace(documentTestContent, "first", "second", 1), "DocumentVersion": "$LATEST"})
	if w := documentHTTP(g, s, "UpdateDocument", string(body)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := documentHTTP(g, s, "ListDocuments", `{}`); w.Code != 200 || !strings.Contains(w.Body.String(), "local-doc") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := documentHTTP(g, s, "DeleteDocument", `{"Name":"local-doc"}`); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSSMDocumentsPaginationAndRollback(t *testing.T) {
	g, s := documentFixture(t)
	ctx := context.Background()
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		if _, e := s.Save(ctx, "us-east-1", name, documentTestContent, ""); e != nil {
			t.Fatal(e)
		}
	}
	first := documentHTTP(g, s, "ListDocuments", `{"MaxResults":1}`)
	var page map[string]any
	if json.Unmarshal(first.Body.Bytes(), &page) != nil {
		t.Fatal(first.Body.String())
	}
	token, _ := page["NextToken"].(string)
	if token == "" {
		t.Fatal(page)
	}
	body, _ := json.Marshal(map[string]any{"MaxResults": 1, "NextToken": token})
	if w := documentHTTP(g, s, "ListDocuments", string(body)); w.Code != 200 || !strings.Contains(w.Body.String(), "bbb") {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := s.Save(ctx, "us-east-1", "ddd", documentTestContent, ""); e != nil {
		t.Fatal(e)
	}
	if w := documentHTTP(g, s, "ListDocuments", string(body)); w.Code != 400 {
		t.Fatal("stale snapshot accepted", w.Body.String())
	}
	if _, e := s.db.Exec(`CREATE TRIGGER document_insert_fault BEFORE INSERT ON aws_ssm_document_versions BEGIN SELECT RAISE(IGNORE); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Save(ctx, "us-east-1", "failed", documentTestContent, ""); e == nil {
		t.Fatal("ignored insert acknowledged")
	}
	if _, e := s.Get(ctx, "us-east-1", "failed", ""); e == nil {
		t.Fatal("partial create remained")
	}
}

func TestSSMDocumentsIAMUpdateRequiresReadAndDenialPreserves(t *testing.T) {
	g, s := documentFixture(t)
	ctx := context.Background()
	if _, err := s.Save(ctx, "us-east-1", "local-doc", documentTestContent, ""); err != nil {
		t.Fatal(err)
	}
	arn := "arn:aws:ssm:us-east-1:000000000000:document/local-doc"
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"ssm:UpdateDocument"}, Resource: core.StringList{arn}}}}
	if err := g.Identities.Set("editor", policy); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"Name": "local-doc", "Content": strings.Replace(documentTestContent, "first", "second", 1), "DocumentVersion": "1"})
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
		r.Header.Set("X-Simulator-Principal", "editor")
		w := httptest.NewRecorder()
		g.serveSSMDocuments(w, r, s, "us-east-1", "UpdateDocument")
		return w
	}
	if w := call(); w.Code != 403 {
		t.Fatal("update without read admitted", w.Code, w.Body.String())
	}
	d, e := s.Get(ctx, "us-east-1", "local-doc", "$LATEST")
	if e != nil || d.DocumentVersion != "1" {
		t.Fatal(d, e)
	}
	policy.Statement = append(policy.Statement, core.Statement{Effect: "Allow", Action: core.StringList{"ssm:GetDocument"}, Resource: core.StringList{arn}})
	if err := g.Identities.Set("editor", policy); err != nil {
		t.Fatal(err)
	}
	if w := call(); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	policy.Statement = append(policy.Statement, core.Statement{Effect: "Deny", Action: core.StringList{"ssm:GetDocument"}, Resource: core.StringList{arn}})
	if err := g.Identities.Set("editor", policy); err != nil {
		t.Fatal(err)
	}
	if w := call(); w.Code != 403 {
		t.Fatal("explicit denial ignored", w.Code, w.Body.String())
	}
}
func TestSSMDocumentsPersistenceAndSeparateDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "documents.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSSMDocumentStore(db.Database())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = s.Save(context.Background(), "us-east-1", "local-doc", documentTestContent, ""); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s, err = NewSSMDocumentStore(reopened.Database())
	if err != nil {
		t.Fatal(err)
	}
	if d, e := s.Get(context.Background(), "us-east-1", "local-doc", ""); e != nil || d.Content != documentTestContent {
		t.Fatal(d, e)
	}
	_, peer := documentFixture(t)
	if _, e := peer.Get(context.Background(), "us-east-1", "local-doc", ""); e == nil {
		t.Fatal("separate DB leaked")
	}
}

func TestSSMDocumentsConcurrentExpectedVersion(t *testing.T) {
	_, s := documentFixture(t)
	ctx := context.Background()
	if _, e := s.Save(ctx, "us-east-1", "local-doc", documentTestContent, ""); e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, value := range []string{"second", "third"} {
		go func(value string) {
			<-start
			_, e := s.Save(ctx, "us-east-1", "local-doc", strings.Replace(documentTestContent, "first", value, 1), "1")
			results <- e
		}(value)
	}
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("expected one concurrent writer, got %d", success)
	}
	d, e := s.Get(ctx, "us-east-1", "local-doc", "$LATEST")
	if e != nil || d.DocumentVersion != "2" {
		t.Fatal(d, e)
	}
}
func TestSSMDocumentsGlobalQuotaAcrossRegions(t *testing.T) {
	_, s := documentFixture(t)
	ctx := context.Background()
	for i := 0; i < ssmDocumentLimit; i++ {
		region := "us-east-1"
		if i%2 == 1 {
			region = "ap-northeast-1"
		}
		if _, e := s.Save(ctx, region, fmt.Sprintf("doc-%03d", i), documentTestContent, ""); e != nil {
			t.Fatal(i, e)
		}
	}
	if _, e := s.Save(ctx, "eu-west-1", "overflow", documentTestContent, ""); e == nil {
		t.Fatal("region bypassed global quota")
	}
	if _, e := s.Get(ctx, "eu-west-1", "overflow", ""); e == nil {
		t.Fatal("rejected create persisted")
	}
	if e := s.Delete(ctx, "us-east-1", "doc-000"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Save(ctx, "eu-west-1", "replacement", documentTestContent, ""); e != nil {
		t.Fatal(e)
	}
}
func TestSSMDocumentsRejectCreateVersionAndInvalidateUpdatedPage(t *testing.T) {
	g, s := documentFixture(t)
	ctx := context.Background()
	raw, _ := json.Marshal(map[string]any{"Name": "local-doc", "Content": documentTestContent, "DocumentVersion": "1"})
	if w := documentHTTP(g, s, "CreateDocument", string(raw)); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, name := range []string{"aaa", "bbb"} {
		if _, e := s.Save(ctx, "us-east-1", name, documentTestContent, ""); e != nil {
			t.Fatal(e)
		}
	}
	w := documentHTTP(g, s, "ListDocuments", `{"MaxResults":1}`)
	var page map[string]any
	if json.Unmarshal(w.Body.Bytes(), &page) != nil {
		t.Fatal(w.Body.String())
	}
	token, _ := page["NextToken"].(string)
	if token == "" {
		t.Fatal(page)
	}
	if _, e := s.Save(ctx, "us-east-1", "aaa", strings.Replace(documentTestContent, "first", "second", 1), "1"); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal(map[string]any{"MaxResults": 1, "NextToken": token})
	if w = documentHTTP(g, s, "ListDocuments", string(raw)); w.Code != 400 {
		t.Fatal("update failed to invalidate cursor", w.Body.String())
	}
}
func TestSSMDocumentsEscapedContentBoundary(t *testing.T) {
	g, s := documentFixture(t)
	// Literal '<' is escaped to six bytes by encoding/json in each envelope.
	// Eight bounded steps permit exactly 65536 content bytes without violating
	// the independent per-command or description bounds.
	doc := ssmDocumentContent{SchemaVersion: "2.2"}
	for i := 0; i < 8; i++ {
		step := struct {
			Action string `json:"action"`
			Name   string `json:"name"`
			Inputs struct {
				RunCommand []string `json:"runCommand"`
			} `json:"inputs"`
		}{Action: "aws:runShellScript", Name: fmt.Sprintf("step%d", i)}
		step.Inputs.RunCommand = []string{strings.Repeat("<", 8000)}
		doc.MainSteps = append(doc.MainSteps, step)
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if e := encoder.Encode(doc); e != nil {
		t.Fatal(e)
	}
	// JSON whitespace counts toward the content byte bound but does not alter
	// the definition. This keeps each literal command below 8192 bytes.
	content := strings.TrimSuffix(buf.String(), "\n")
	if len(content) > 65536 {
		t.Fatal(len(content))
	}
	content += strings.Repeat(" ", 65536-len(content))
	raw, _ := json.Marshal(map[string]any{"Name": "boundary", "Content": content})
	if len(raw) >= 512<<10 || len(raw) <= 64<<10 {
		t.Fatal("escaped envelope fixture size", len(raw))
	}
	if w := documentHTTP(g, s, "CreateDocument", string(raw)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, _ = json.Marshal(map[string]any{"Name": "oversized", "Content": content + " "})
	if w := documentHTTP(g, s, "CreateDocument", string(raw)); w.Code != 400 || !strings.Contains(w.Body.String(), "MaxDocumentSizeExceeded") {
		t.Fatal(w.Code, w.Body.String())
	}
	raw, _ = json.Marshal(map[string]any{"Name": "envelope", "Content": strings.Repeat("<", 90000)})
	if len(raw) <= 512<<10 {
		t.Fatal("envelope fixture too small")
	}
	if w := documentHTTP(g, s, "CreateDocument", string(raw)); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestSSMDocumentsGetIsSelectOnlyAndKeepsConnectionWritable(t *testing.T) {
	_, s := documentFixture(t)
	ctx := context.Background()
	if _, e := s.Save(ctx, "us-east-1", "local-doc", documentTestContent, ""); e != nil {
		t.Fatal(e)
	}
	var before, after, readonly int
	if e := s.db.QueryRow("SELECT total_changes()").Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Get(ctx, "us-east-1", "local-doc", "$LATEST"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Get(ctx, "us-east-1", "missing-doc", "$LATEST"); e == nil {
		t.Fatal("missing read accepted")
	}
	if e := s.db.QueryRow("SELECT total_changes()").Scan(&after); e != nil {
		t.Fatal(e)
	}
	if before != after {
		t.Fatalf("Get wrote database: %d -> %d", before, after)
	}
	if e := s.db.QueryRow("PRAGMA query_only").Scan(&readonly); e != nil || readonly != 0 {
		t.Fatal("Get changed connection permissions", readonly, e)
	}
	if _, e := s.Save(ctx, "us-east-1", "local-doc", strings.Replace(documentTestContent, "first", "after-read", 1), "1"); e != nil {
		t.Fatal("read poisoned later write", e)
	}
}
