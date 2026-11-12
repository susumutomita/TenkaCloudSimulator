package server

import (
	"crypto/rand"
	"encoding/base64"
	"io/fs"
	"net/http"
	"strings"
)

// consoleAssetsHandler authorizes only this response's bundled editor styles.
// Scripts remain same-origin-only; no unsafe-inline/eval or remote assets.
func consoleAssetsHandler(web fs.FS) http.Handler {
	files := http.FileServer(http.FS(web))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			files.ServeHTTP(w, r)
			return
		}
		page, err := fs.ReadFile(web, "index.html")
		if err != nil {
			http.Error(w, "Console unavailable", 500)
			return
		}
		nonceBytes := make([]byte, 24)
		if _, err = rand.Read(nonceBytes); err != nil {
			http.Error(w, "Console unavailable", 500)
			return
		}
		nonce := base64.RawStdEncoding.EncodeToString(nonceBytes)
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "style-src 'self';") {
			http.Error(w, "Console policy unavailable", 500)
			return
		}
		w.Header().Set("Content-Security-Policy", strings.Replace(csp, "style-src 'self';", "style-src 'self' 'nonce-"+nonce+"';", 1))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write([]byte(strings.Replace(string(page), "<head>", "<head><meta name=\"editor-style-nonce\" content=\""+nonce+"\">", 1)))
	})
}
