package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestConsoleEditorStyleNonce(t *testing.T) {
	h := consoleAssetsHandler(fstest.MapFS{"index.html": {Data: []byte("<html><head></head><body>console</body></html>")}, "editor.worker.js": {Data: []byte("// local worker")}})
	previous := ""
	for _, method := range []string{"GET", "GET", "HEAD"} {
		w := httptest.NewRecorder()
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'")
		h.ServeHTTP(w, httptest.NewRequest(method, "/", nil))
		csp := w.Header().Get("Content-Security-Policy")
		if w.Code != 200 || !strings.Contains(csp, "style-src 'self' 'nonce-") || strings.Contains(csp, "unsafe-") || !strings.Contains(csp, "script-src 'self';") {
			t.Fatalf("unsafe or missing nonce policy: %s", csp)
		}
		if previous == csp {
			t.Fatal("nonce reused")
		}
		previous = csp
		if method == "GET" {
			nonce := strings.Split(strings.Split(csp, "'nonce-")[1], "'")[0]
			if !strings.Contains(w.Body.String(), `content="`+nonce+`"`) {
				t.Fatal("nonce mismatch")
			}
		} else if w.Body.Len() != 0 {
			t.Fatal("HEAD body")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 500 {
		t.Fatal("missing base CSP must fail closed")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/editor.worker.js", nil))
	if w.Code != http.StatusOK || w.Body.String() != "// local worker" {
		t.Fatal("local worker not served")
	}
}
