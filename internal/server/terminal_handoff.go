package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
)

type terminalScope struct {
	Target string
	Region string
}
type terminalScopeKey struct{}
type terminalHandoff struct {
	ctx     context.Context
	cancel  context.CancelFunc
	scope   terminalScope
	expires time.Time
	bind    func(*http.Request) (*http.Request, aws.SDKIdentity, error)
}

func (a *Access) issueTerminalHandoff(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var scope terminalScope
	raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if readErr != nil || !terminalJSON(raw) {
		http.Error(w, "Invalid terminal target", 400)
		return
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&scope) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "Invalid terminal target", 400)
		return
	}
	regionOK := false
	for _, region := range aws.Regions {
		regionOK = regionOK || scope.Region == region
	}
	if !regionOK || !regexp.MustCompile(`^i-[a-f0-9]{8,32}$`).MatchString(scope.Target) {
		http.Error(w, "Invalid terminal target", 400)
		return
	}
	bind, err := a.sdk.TerminalBinding(r, scope.Region, scope.Target)
	if err != nil {
		http.Error(w, "Participant STS credentials required", 403)
		return
	}
	_, identity, err := bind(r)
	if err != nil {
		http.Error(w, "Participant session unavailable", 403)
		return
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		http.Error(w, "Terminal handoff unavailable", 500)
		return
	}
	code := hex.EncodeToString(bytes)
	expires := a.now().Add(time.Minute)
	if identity.Expires.Before(expires) {
		expires = identity.Expires
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneTerminalSessions()
	if len(a.terminalHandoffs) >= 1000 {
		http.Error(w, "Terminal handoff limit", 429)
		return
	}
	a.terminalHandoffs[sha256.Sum256([]byte(code))] = terminalHandoff{scope: scope, expires: expires, bind: bind}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"Code": code, "ExpiresAt": expires.Format(time.RFC3339)})
}

// Caller holds a.mu. Expiration never creates replacement authority.
func (a *Access) pruneTerminalSessions() {
	now := a.now()
	for key, value := range a.terminalHandoffs {
		if !now.Before(value.expires) {
			delete(a.terminalHandoffs, key)
		}
	}
	for key, value := range a.terminalBrowsers {
		if !now.Before(value.expires) {
			if value.cancel != nil {
				value.cancel()
			}
			delete(a.terminalBrowsers, key)
		}
	}
}
func (a *Access) redeemTerminalHandoff(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Origin") == "" {
		http.Error(w, "POST required", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil || len(r.PostForm) != 1 || len(r.PostForm["Code"]) != 1 {
		http.Error(w, "Invalid handoff", 400)
		return
	}
	code := r.PostForm.Get("Code")
	if len(code) != 64 {
		http.Error(w, "Invalid handoff", 400)
		return
	}
	key := sha256.Sum256([]byte(code))
	a.mu.Lock()
	handoff, ok := a.terminalHandoffs[key]
	delete(a.terminalHandoffs, key)
	a.mu.Unlock()
	if !ok || !a.now().Before(handoff.expires) {
		http.Error(w, "Handoff unavailable", 403)
		return
	}
	_, identity, err := handoff.bind(r)
	if err != nil {
		http.Error(w, "Participant session unavailable", 403)
		return
	}
	bytes := make([]byte, 32)
	if _, err = rand.Read(bytes); err != nil {
		http.Error(w, "Terminal unavailable", 500)
		return
	}
	token := hex.EncodeToString(bytes)
	handoff.expires = identity.Expires
	a.mu.Lock()
	a.pruneTerminalSessions()
	if len(a.terminalBrowsers) >= 1000 {
		a.mu.Unlock()
		http.Error(w, "Terminal session limit", 429)
		return
	}
	if oldCookie, e := r.Cookie(a.cookie + "_terminal"); e == nil {
		oldKey := sha256.Sum256([]byte(oldCookie.Value))
		if old, exists := a.terminalBrowsers[oldKey]; exists {
			if old.cancel != nil {
				old.cancel()
			}
			delete(a.terminalBrowsers, oldKey)
		}
	}
	handoff.ctx, handoff.cancel = context.WithDeadline(context.Background(), handoff.expires)
	a.terminalBrowsers[sha256.Sum256([]byte(token))] = handoff
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: a.cookie + "_terminal", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, Expires: handoff.expires})
	http.Redirect(w, r, "/_simulator/console/#/terminal", http.StatusSeeOther)
}

// Called only after Access's host/origin checks. This cookie cannot authenticate
// generic AWS, identity, or operator routes. Its retained STS key is never sent.
func (a *Access) terminalBrowserRequest(w http.ResponseWriter, r *http.Request) bool {
	path := r.URL.Path
	terminal := path == "/_simulator/v1/terminal-context" || path == "/_simulator/v1/terminal-control" || strings.HasPrefix(path, "/_simulator/v1/terminal-channel/")
	console := r.Method == "GET" && strings.HasPrefix(path, "/_simulator/console/")
	if !terminal && !console {
		return false
	}
	cookie, err := r.Cookie(a.cookie + "_terminal")
	if err != nil {
		if terminal {
			http.Error(w, "Terminal handoff required", 401)
			return true
		}
		return false
	}
	if r.Header.Get("Authorization") != "" || r.Header.Get("X-Simulator-Token") != "" || r.Header.Get("X-Amz-Security-Token") != "" || r.URL.RawQuery != "" || (r.URL.RawPath != "" && !strings.HasPrefix(path, "/_simulator/v1/terminal-channel/")) {
		http.Error(w, "Mixed terminal authority", 403)
		return true
	}
	a.mu.Lock()
	s, ok := a.terminalBrowsers[sha256.Sum256([]byte(cookie.Value))]
	a.mu.Unlock()
	if !ok || !a.now().Before(s.expires) {
		http.Error(w, "Terminal session unavailable", 401)
		return true
	}
	if s.ctx == nil || s.ctx.Err() != nil {
		http.Error(w, "Terminal session unavailable", 401)
		return true
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	request, _, err := s.bind(r.WithContext(ctx))
	if err != nil {
		http.Error(w, "Participant session unavailable", 403)
		return true
	}
	if console {
		a.next.ServeHTTP(w, r)
		return true
	}
	if path == "/_simulator/v1/terminal-context" {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.scope)
		return true
	}
	if strings.HasPrefix(path, "/_simulator/v1/terminal-channel/") && r.Header.Get("Origin") == "" {
		http.Error(w, "Terminal Origin required", 403)
		return true
	}
	if path == "/_simulator/v1/terminal-control" && r.Method != "POST" {
		w.WriteHeader(405)
		return true
	}
	request = request.WithContext(context.WithValue(request.Context(), terminalScopeKey{}, s.scope))
	a.next.ServeHTTP(w, request)
	return true
}

// No code is accepted through URL parameters or emitted into a page. The
// password field avoids browser form-history reuse; POST redirects to a clean URL.
func serveTerminalRedemption(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" || r.URL.RawQuery != "" {
		http.Error(w, "Invalid terminal navigation", 400)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="referrer" content="no-referrer"><title>Participant terminal</title><main><h1>Participant terminal / 参加者端末</h1><p>Copy the one-use terminal code from your authenticated Host problem page, then paste it here. This is an explicit manual handoff, not automatic sign-in.</p><form method="post" action="/_simulator/terminal-redeem" autocomplete="off"><label>One-use code <input type="password" name="Code" minlength="64" maxlength="64" required autocomplete="off" spellcheck="false"></label><button type="submit">Open terminal / 端末を開く</button></form></main></html>`)
}
