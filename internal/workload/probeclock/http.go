package probeclock

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
)

// SignedHandler binds all provider replies, not only the clock sample, to the
// private bootstrap key, requested path/body, fresh nonce and HTTP status.
func (c *Clock) SignedHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := r.Header.Get("X-Simulator-Clock-Nonce")
		n, e := hex.DecodeString(nonce)
		if e != nil || len(n) != 32 || hex.EncodeToString(n) != nonce {
			http.Error(w, "Clock nonce required", 400)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8193))
		if e != nil || len(raw) > 8192 {
			http.Error(w, "Clock request exceeds bound", 400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		requestDigest := sha256.Sum256(raw)
		recorder := &signedResponse{header: make(http.Header)}
		next.ServeHTTP(recorder, r)
		if recorder.status == 0 {
			recorder.status = 200
		}
		if recorder.overflow {
			http.Error(w, "Clock reply exceeds bound", 500)
			return
		}
		for k, v := range recorder.header {
			w.Header()[k] = v
		}
		digest := sha256.Sum256(recorder.body.Bytes())
		mac := hmac.New(sha256.New, c.key[:])
		fmt.Fprintf(mac, "TCS-RESPONSE-2\n%s\n%s\n%s\n%s\n%x\n%d\n%x\n", c.domain, nonce, r.Method, r.URL.EscapedPath(), requestDigest, recorder.status, digest)
		w.Header().Set("X-Simulator-Clock-Domain", c.domain)
		w.Header().Set("X-Simulator-Clock-MAC", hex.EncodeToString(mac.Sum(nil)))
		w.WriteHeader(recorder.status)
		_, _ = w.Write(recorder.body.Bytes())
	})
}

type signedResponse struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (w *signedResponse) Header() http.Header { return w.header }
func (w *signedResponse) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
}
func (w *signedResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	if len(b) > 65536-w.body.Len() {
		w.overflow = true
		return len(b), nil
	}
	return w.body.Write(b)
}
