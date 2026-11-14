package aws

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

func sourceTestZIP(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	if err := w.SetComment("keep archive comment"); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate, Comment: "keep entry comment"}
		h.SetMode(0640)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func sourceFixture(t *testing.T) (*Gateway, LambdaFunction) {
	t.Helper()
	_, store, identities := lambdaState(t)
	f := LambdaFunction{Name: "editor", Region: "us-east-1", Role: "arn:aws:iam::000000000000:role/executor", Handler: "pkg.index.handler", Revision: "original", Timeout: 7, Description: "keep me", Environment: map[string]string{"SETTING": "keep"}, Tags: map[string]string{"owner": "keep"}, Code: sourceTestZIP(t, map[string][]byte{"pkg/index.py": []byte("from helper import answer\ndef handler(event,context): return answer\n"), "helper.py": []byte("answer = 42\n"), "pkg/__init__.py": {}})}
	if err := store.save(&f, true, ""); err != nil {
		t.Fatal(err)
	}
	return &Gateway{Lambdas: store, Identities: identities}, f
}
func sourceRequest(g *Gateway, method, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	g.ServeLambdaSource(w, httptest.NewRequest(method, "/_simulator/v1/lambda/editor/source", strings.NewReader(body)))
	return w
}
func sourceBody(source, revision string) string {
	b, _ := json.Marshal(map[string]string{"Source": source, "RevisionId": revision})
	return string(b)
}
func sourceResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestLambdaSourceReadUpdatePreservesPackageAndConfiguration(t *testing.T) {
	g, before := sourceFixture(t)
	w := sourceRequest(g, http.MethodGet, "")
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Body.String())
	}
	data := sourceResponse(t, w)
	if data["Path"] != "pkg/index.py" || data["RevisionId"] != "original" || !strings.Contains(data["Source"].(string), "from helper import answer") {
		t.Fatal(data)
	}
	source := "from helper import answer\ndef handler(event,context): return answer + 1\n"
	w = sourceRequest(g, http.MethodPut, sourceBody(source, "original"))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	result := sourceResponse(t, w)
	if result["Source"] != source || result["RevisionId"] == "original" {
		t.Fatal(result)
	}
	after, err := g.Lambdas.get(before.Region, before.Name)
	if err != nil {
		t.Fatal(err)
	}
	if after.Role != before.Role || after.Handler != before.Handler || after.Timeout != before.Timeout || after.Description != before.Description || !reflect.DeepEqual(after.Environment, before.Environment) || !reflect.DeepEqual(after.Tags, before.Tags) {
		t.Fatal("configuration changed")
	}
	oldZIP, _ := zip.NewReader(bytes.NewReader(before.Code), int64(len(before.Code)))
	newZIP, err := pythonruntime.ValidateCode(after.Code, after.Handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(newZIP.File) != len(oldZIP.File) || newZIP.Comment != oldZIP.Comment {
		t.Fatal("ZIP structure changed")
	}
	for i, old := range oldZIP.File {
		now := newZIP.File[i]
		if now.Name != old.Name || now.Comment != old.Comment || now.Mode() != old.Mode() {
			t.Fatal("metadata changed")
		}
		if old.Name == "pkg/index.py" {
			continue
		}
		a, _ := old.OpenRaw()
		b, _ := now.OpenRaw()
		aa, _ := io.ReadAll(a)
		bb, _ := io.ReadAll(b)
		if !bytes.Equal(aa, bb) {
			t.Fatal("untouched module compressed data changed")
		}
	}
	// A fresh handler/read observes the persisted source, not an in-memory draft.
	w = sourceRequest(&Gateway{Lambdas: g.Lambdas, Identities: g.Identities}, http.MethodGet, "")
	if sourceResponse(t, w)["Source"] != source {
		t.Fatal("source not persisted")
	}
	if w = sourceRequest(g, http.MethodPut, sourceBody("stale", "original")); w.Code != 412 {
		t.Fatal("stale write accepted", w.Code)
	}
}

func TestLambdaSourceDeleteRecreateRejectsOldRevision(t *testing.T) {
	g, f := sourceFixture(t)
	if _, err := g.Lambdas.db.Exec("DELETE FROM aws_lambda_functions"); err != nil {
		t.Fatal(err)
	}
	f.Revision = "replacement"
	f.Role = "arn:aws:iam::000000000000:role/replacement"
	if err := g.Lambdas.save(&f, true, ""); err != nil {
		t.Fatal(err)
	}
	w := sourceRequest(g, http.MethodPut, sourceBody("overwrite", "original"))
	if w.Code != 412 {
		t.Fatal(w.Code, w.Body.String())
	}
	now, _ := g.Lambdas.get(f.Region, f.Name)
	if now.Revision != f.Revision || !bytes.Equal(now.Code, f.Code) || now.Role != f.Role {
		t.Fatal("recreated function changed")
	}
}

func TestLambdaSourceAuthorizationAndSessionBoundaries(t *testing.T) {
	g, f := sourceFixture(t)
	for _, tc := range []struct {
		actions, method string
		want            int
	}{
		{`"lambda:InvokeFunction"`, "GET", 403}, {`"lambda:UpdateFunctionCode"`, "PUT", 403},
		{`"lambda:GetFunction"`, "GET", 200}, {`"lambda:GetFunction"`, "PUT", 403},
		{`["lambda:GetFunction","lambda:UpdateFunctionCode"]`, "PUT", 200},
	} {
		p, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":` + tc.actions + `,"Resource":"` + lambdaARN(f.Region, f.Name) + `"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = g.Identities.Set("editor-user", p); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(tc.method, "/_simulator/v1/lambda/editor/source", strings.NewReader(sourceBody("# changed", f.Revision)))
		r.Header.Set("X-Simulator-Principal", "editor-user")
		w := httptest.NewRecorder()
		g.ServeLambdaSource(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.actions, tc.method, w.Code, w.Body.String())
		}
	}
	// Even operator/default principal cannot widen an immutable session snapshot
	// or a parameter-scoped console session to Lambda source disclosure.
	deny, err := core.ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"*","Resource":"*"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{context.WithValue(context.Background(), sdkPolicyContextKey{}, deny), context.WithValue(context.Background(), consoleParameterBoundaryKey{}, consoleParameterBoundary{})} {
		r := httptest.NewRequest("GET", "/_simulator/v1/lambda/editor/source", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		g.ServeLambdaSource(w, r)
		if w.Code != 403 || strings.Contains(w.Body.String(), "Source") {
			t.Fatal("boundary leaked source", w.Code)
		}
	}
}

func TestLambdaSourceRejectsInvalidWireAndDoesNotMutate(t *testing.T) {
	g, f := sourceFixture(t)
	for _, body := range []string{`{}`, `null`, `{"Source":"x"}`, `{"Source":null,"RevisionId":"original"}`, `{"Source":"x","RevisionId":""}`, `{"Source":"x","RevisionId":"original","Path":"helper.py"}`, `{"Source":"x","Source":"y","RevisionId":"original"}`, `{"source":"x","RevisionId":"original"}`, sourceBody("x", "original") + `{}`, string([]byte{'{', 0xff, '}'})} {
		if w := sourceRequest(g, "PUT", body); w.Code != 400 {
			t.Fatal("invalid request accepted", body, w.Code)
		}
	}
	for _, target := range []string{"/_simulator/v1/lambda/editor/source?Qualifier=x", "/_simulator/v1/lambda/editor/source/", "/_simulator/v1/lambda/a/b/source"} {
		r := httptest.NewRequest("GET", target, nil)
		w := httptest.NewRecorder()
		g.ServeLambdaSource(w, r)
		if w.Code != 400 {
			t.Fatal(target, w.Code)
		}
	}
	for _, header := range []string{"Authorization", "X-Amz-Target", "X-Amz-Security-Token"} {
		r := httptest.NewRequest("GET", "/_simulator/v1/lambda/editor/source", nil)
		r.Header.Set(header, "unsupported")
		w := httptest.NewRecorder()
		g.ServeLambdaSource(w, r)
		if w.Code < 400 {
			t.Fatal(header)
		}
	}
	w := sourceRequest(g, "POST", "")
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
	now, _ := g.Lambdas.get(f.Region, f.Name)
	if !bytes.Equal(now.Code, f.Code) || now.Revision != f.Revision {
		t.Fatal("invalid input mutated state")
	}
}

func TestLambdaSourcePackageFailuresAreBounded(t *testing.T) {
	_, f := sourceFixture(t)
	for name, code := range map[string][]byte{
		"corrupt":          []byte("not a zip"),
		"compressed limit": bytes.Repeat([]byte{'x'}, pythonruntime.MaxCodeBytes+1),
		"expanded limit":   sourceTestZIP(t, map[string][]byte{"pkg/index.py": bytes.Repeat([]byte{'x'}, pythonruntime.MaxExpandedBytes+1)}),
		"invalid utf8":     sourceTestZIP(t, map[string][]byte{"pkg/index.py": {0xff}}),
		"missing handler":  sourceTestZIP(t, map[string][]byte{"other.py": []byte("x")}),
		"unsafe path":      sourceTestZIP(t, map[string][]byte{"pkg/index.py": []byte("x"), "../bad.py": []byte("x")}),
	} {
		t.Run(name, func(t *testing.T) {
			bad := f
			bad.Code = code
			if _, _, err := lambdaSavedSource(bad); err == nil {
				t.Fatal("invalid package read")
			}
			if _, err := lambdaReplaceSource(bad, "x"); err == nil {
				t.Fatal("invalid package updated")
			}
		})
	}
	for _, source := range []string{string([]byte{0xff}), strings.Repeat("a", pythonruntime.MaxExpandedBytes+1)} {
		if _, err := lambdaReplaceSource(f, source); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	// Total expanded size is enforced across the replacement plus preserved files.
	if _, err := lambdaReplaceSource(f, strings.Repeat("a", pythonruntime.MaxExpandedBytes)); err == nil {
		t.Fatal("combined expanded quota exceeded")
	}
	var bounded lambdaSourceBuffer
	if _, err := bounded.Write(make([]byte, pythonruntime.MaxCodeBytes+1)); err == nil || bounded.Len() != 0 {
		t.Fatal("output cap not enforced")
	}
}

func TestLambdaSourceDuplicateAndCRCFailClosed(t *testing.T) {
	_, f := sourceFixture(t)
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for i := 0; i < 2; i++ {
		entry, err := w.Create("pkg/index.py")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte("duplicate")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Code = b.Bytes()
	if _, _, err := lambdaSavedSource(f); err == nil {
		t.Fatal("duplicate module accepted")
	}
	b.Reset()
	w = zip.NewWriter(&b)
	entry, err := w.CreateHeader(&zip.FileHeader{Name: "pkg/index.py", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = entry.Write([]byte("distinct source marker")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Code = bytes.Clone(b.Bytes())
	at := bytes.Index(f.Code, []byte("distinct source marker"))
	if at < 0 {
		t.Fatal("missing fixture marker")
	}
	f.Code[at] = 'X'
	if _, _, err := lambdaSavedSource(f); err == nil {
		t.Fatal("corrupt CRC accepted")
	}
}

func TestLambdaSourceStorageFailureLeavesOriginal(t *testing.T) {
	g, f := sourceFixture(t)
	if _, err := g.Lambdas.db.Exec(`CREATE TRIGGER reject_source BEFORE UPDATE ON aws_lambda_functions BEGIN SELECT RAISE(ABORT,'test update rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	w := sourceRequest(g, "PUT", sourceBody("# must not persist", f.Revision))
	if w.Code != 500 {
		t.Fatal(w.Code, w.Body.String())
	}
	now, err := g.Lambdas.get(f.Region, f.Name)
	if err != nil {
		t.Fatal(err)
	}
	if now.Revision != f.Revision || !bytes.Equal(now.Code, f.Code) {
		t.Fatal("partial update")
	}
	if _, err = g.Lambdas.db.Exec("DROP TRIGGER reject_source"); err != nil {
		t.Fatal(err)
	}
	// Corrupt persisted packages are rejected by both HTTP methods, not silently
	// replaced by a single guessed source file.
	f.Code = []byte("corrupt")
	if err = g.Lambdas.save(&f, false, f.Revision); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		w = sourceRequest(g, method, sourceBody("# must not persist", f.Revision))
		if w.Code != 400 {
			t.Fatal(method, w.Code)
		}
	}
}

func TestLambdaSourcePythonEncodingDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		valid        bool
	}{
		{"plain", "value = 'é'\n", true},
		{"latin1 ASCII", "# coding: latin-1\nvalue = 1\n", false},
		{"latin1 UTF8 bytes", "# coding: latin-1\nvalue = 'é'\n", false},
		{"second line", "#!/usr/bin/python\n# coding=cp1252\nvalue=1", false},
		{"blank first", "\n# coding: latin-1\n", false},
		{"CR only", "# comment\r# coding: latin-1\r", false},
		{"not a cookie", "value=1\n# coding: latin-1\n", true},
		{"third line", "# comment\n# comment\n# coding: latin-1\n", true},
		{"utf8", "# coding: utf8\nvalue='é'", true},
		{"underscore alias", "# coding: UTF_8\nvalue='é'", true},
		{"u8 alias", "# coding: u8\nvalue='é'", true},
		{"cp65001 alias", "# coding: cp65001\nvalue='é'", true},
		{"BOM", "\ufeffvalue='é'", true},
		{"BOM UTF8", "\ufeff# coding: utf-8\nvalue='é'", true},
		{"BOM conflicting", "\ufeff# coding: latin-1\nvalue=1", false},
		{"BOM incompatible alias", "\ufeff# coding: utf8\nvalue=1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, f := sourceFixture(t)
			if got := lambdaSourceUTF8([]byte(tc.source)); got != tc.valid {
				t.Fatalf("encoding admissibility=%v want %v", got, tc.valid)
			}
			// PUT cannot introduce an encoding which changes Unicode interpretation.
			w := sourceRequest(g, "PUT", sourceBody(tc.source, f.Revision))
			want := 400
			if tc.valid {
				want = 200
			}
			if w.Code != want {
				t.Fatalf("PUT=%d want %d: %s", w.Code, want, w.Body.String())
			}
			// GET must also inspect cookies in existing native/CFN/ZIP packages.
			existing := f
			existing.Code = sourceTestZIP(t, map[string][]byte{"pkg/index.py": []byte(tc.source)})
			saved, _ := g.Lambdas.get(f.Region, f.Name)
			if err := g.Lambdas.save(&existing, false, saved.Revision); err != nil {
				t.Fatal(err)
			}
			w = sourceRequest(g, "GET", "")
			if w.Code != want {
				t.Fatalf("GET=%d want %d: %s", w.Code, want, w.Body.String())
			}
			if tc.valid && sourceResponse(t, w)["Source"] != tc.source {
				t.Fatal("source or BOM was silently changed")
			}
		})
	}
}
