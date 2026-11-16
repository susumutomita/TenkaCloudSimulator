package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the composed route and real access wrapper without opening sockets.
func TestLambdaSourceAuthenticatedRestartAndActualHelperInvocation(t *testing.T) {
	state := filepath.Join(t.TempDir(), "source.db")
	operator := strings.Repeat("s", 64)
	start := func() (http.Handler, func()) {
		h, close, err := New(state)
		if err != nil {
			t.Fatal(err)
		}
		access, err := NewAccess(h, operator, false)
		if err != nil {
			close()
			t.Fatal(err)
		}
		return access, func() { _ = close() }
	}
	h, close := start()
	call := func(method, path string, input any, token string, want int) map[string]any {
		t.Helper()
		var body []byte
		if input != nil {
			body, _ = json.Marshal(input)
		}
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, bytes.NewReader(body))
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("X-Simulator-Service", "lambda")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		result := map[string]any{}
		_ = json.Unmarshal(w.Body.Bytes(), &result)
		return result
	}
	call("PUT", "/_simulator/v1/iam/principals/executor/policy", map[string]any{"Version": "2012-10-17", "Statement": []any{map[string]any{"Effect": "Deny", "Action": "*", "Resource": "*"}}}, operator, 200)
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for path, source := range map[string]string{"index.py": "from helper import answer\ndef handler(e,c): return answer\n", "helper.py": "answer = 41\n"} {
		f, err := archive.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(source))
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	input := lambdaInput(t, "saved", "unused")
	input["Code"] = map[string]any{"ZipFile": buffer.Bytes()}
	call("POST", functionRoot, input, operator, 201)
	path := "/_simulator/v1/lambda/saved/source"
	call("GET", path, nil, "", 401)
	source := call("GET", path, nil, operator, 200)
	if source["Path"] != "index.py" || !strings.Contains(source["Source"].(string), "from helper import answer") {
		t.Fatal(source)
	}
	updated := call("PUT", path, map[string]any{"RevisionId": source["RevisionId"], "Source": "from helper import answer\ndef handler(e,c): return answer + 1\n"}, operator, 200)
	call("PUT", path, map[string]any{"RevisionId": source["RevisionId"], "Source": "stale"}, operator, 412)
	close()
	h, close = start()
	defer close()
	reopened := call("GET", path, nil, operator, 200)
	if reopened["RevisionId"] != updated["RevisionId"] || reopened["Source"] != updated["Source"] {
		t.Fatal(reopened)
	}
	r := httptest.NewRequest("POST", "http://127.0.0.1:4566"+functionRoot+"/saved/invocations", strings.NewReader("{}"))
	r.Header.Set("X-Simulator-Token", operator)
	r.Header.Set("X-Simulator-Service", "lambda")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "42" || w.Header().Get("X-Amz-Function-Error") != "" {
		t.Fatalf("helper invocation %d %s", w.Code, w.Body.String())
	}
}
