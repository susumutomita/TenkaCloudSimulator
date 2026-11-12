package server

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Handler-level guards are defence in depth behind Access routing, so they are
// exercised directly as well as through the full Access stack.
func TestTerminalHandoffIssuanceRefusesMalformedTargets(t *testing.T) {
	a, c, _ := terminalFixture(t)
	w := httptest.NewRecorder()
	a.issueTerminalHandoff(w, httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/terminal-handoffs", nil))
	if w.Code != 405 {
		t.Fatal("GET issuance", w.Code)
	}
	for name, body := range map[string]string{
		"not JSON":      `{`,
		"unknown field": `{"Target":"i-12345678","Region":"us-east-1","Port":22}`,
		"trailing":      `{"Target":"i-12345678","Region":"us-east-1"}{}`,
		"region":        `{"Target":"i-12345678","Region":"mars-1"}`,
		"short target":  `{"Target":"i-1234","Region":"us-east-1"}`,
		"upper target":  `{"Target":"i-ABCDEF12","Region":"us-east-1"}`,
		"oversized":     `{"Target":"i-12345678","Region":"us-east-1","x":"` + strings.Repeat("a", 5000) + `"}`,
	} {
		if w := handoffRequest(a, signedTerminalHandoff(c, body)); w.Code == 200 {
			t.Errorf("%s issued a code", name)
		}
	}
	a.mu.Lock()
	issued := len(a.terminalHandoffs)
	a.mu.Unlock()
	if issued != 0 {
		t.Fatal("refused targets retained codes", issued)
	}
	// A code never outlives the participant session that requested it.
	a.now = func() time.Time { return time.Now().Add(14*time.Minute + 30*time.Second) }
	terminalCode(t, a, c)
	a.mu.Lock()
	for _, h := range a.terminalHandoffs {
		if h.expires.After(time.Now().Add(15 * time.Minute)) {
			t.Error("code outlives session", h.expires)
		}
	}
	a.mu.Unlock()
}

func TestTerminalHandoffBoundsAndPrunesRetainedState(t *testing.T) {
	a, c, _ := terminalFixture(t)
	canceled := 0
	past := time.Now().Add(-time.Second)
	a.mu.Lock()
	a.terminalHandoffs[[32]byte{1}] = terminalHandoff{expires: past}
	a.terminalBrowsers[[32]byte{2}] = terminalHandoff{expires: past, cancel: func() { canceled++ }}
	a.terminalBrowsers[[32]byte{3}] = terminalHandoff{expires: past}
	a.mu.Unlock()
	code := terminalCode(t, a, c)
	a.mu.Lock()
	_, staleCode := a.terminalHandoffs[[32]byte{1}]
	_, staleBrowser := a.terminalBrowsers[[32]byte{2}]
	a.mu.Unlock()
	if staleCode || staleBrowser || canceled != 1 {
		t.Fatal("expired terminal state retained", staleCode, staleBrowser, canceled)
	}
	future := time.Now().Add(time.Hour)
	a.mu.Lock()
	for n := 0; len(a.terminalHandoffs) < 1000; n++ {
		a.terminalHandoffs[sha256.Sum256([]byte{byte(n), byte(n >> 8), 9})] = terminalHandoff{expires: future}
	}
	a.mu.Unlock()
	if w := handoffRequest(a, signedTerminalHandoff(c, `{"Target":"i-12345678","Region":"us-east-1"}`)); w.Code != 429 {
		t.Fatal("handoff quota", w.Code)
	}
	a.mu.Lock()
	for n := 0; len(a.terminalBrowsers) < 1000; n++ {
		a.terminalBrowsers[sha256.Sum256([]byte{byte(n), byte(n >> 8), 7})] = terminalHandoff{expires: future}
	}
	a.mu.Unlock()
	if w := terminalRedeem(a, code); w.Code != 429 || len(w.Result().Cookies()) != 0 {
		t.Fatal("browser quota", w.Code)
	}
}

func TestTerminalRedemptionRefusesMalformedForms(t *testing.T) {
	a, c, _ := terminalFixture(t)
	direct := func(method, target, body, origin string) int {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		a.redeemTerminalHandoff(w, r)
		return w.Code
	}
	base := "http://127.0.0.1:4566/_simulator/terminal-redeem"
	origin := "http://127.0.0.1:4566"
	code := strings.Repeat("a", 64)
	for name, got := range map[string]int{
		"GET":         direct("GET", base, "", origin),
		"query":       direct("POST", base+"?Code="+code, "", origin),
		"no origin":   direct("POST", base, "Code="+code, ""),
		"extra field": direct("POST", base, url.Values{"Code": {code}, "x": {"1"}}.Encode(), origin),
		"two codes":   direct("POST", base, url.Values{"Code": {code, code}}.Encode(), origin),
		"short code":  direct("POST", base, "Code=abc", origin),
		"unknown":     direct("POST", base, "Code="+code, origin),
	} {
		if got < 400 {
			t.Errorf("%s redeemed: %d", name, got)
		}
	}
	for _, r := range []*http.Request{httptest.NewRequest("POST", "http://127.0.0.1:4566/_simulator/terminal", nil), httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/terminal?Code=x", nil)} {
		w := httptest.NewRecorder()
		serveTerminalRedemption(w, r)
		if w.Code != 400 || strings.Contains(w.Body.String(), "<form") {
			t.Fatal("redemption page served for", r.Method, r.URL.RawQuery)
		}
	}
	// Redeeming again from the same browser replaces, and cancels, the old session.
	first := terminalRedeem(a, terminalCode(t, a, c)).Result().Cookies()[0]
	firstKey := sha256.Sum256([]byte(first.Value))
	a.mu.Lock()
	oldLife := a.terminalBrowsers[firstKey].ctx
	a.mu.Unlock()
	r := httptest.NewRequest("POST", base, strings.NewReader(url.Values{"Code": {terminalCode(t, a, c)}}.Encode()))
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(first)
	if w := handoffRequest(a, r); w.Code != 303 {
		t.Fatal("second redemption", w.Code)
	}
	a.mu.Lock()
	_, kept := a.terminalBrowsers[firstKey]
	a.mu.Unlock()
	if kept || oldLife.Err() == nil {
		t.Fatal("replaced terminal session survived")
	}
}

func TestTerminalBrowserCookieGrantsOnlyScopedRoutes(t *testing.T) {
	a, c, _ := terminalFixture(t)
	if w := handoffRequest(a, httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/terminal-context", nil)); w.Code != 401 {
		t.Fatal("terminal route without cookie", w.Code)
	}
	cookie := terminalRedeem(a, terminalCode(t, a, c)).Result().Cookies()[0]
	send := func(method, path string, edit func(*http.Request)) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, nil)
		r.AddCookie(cookie)
		if edit != nil {
			edit(r)
		}
		return handoffRequest(a, r)
	}
	w := send("GET", "/_simulator/v1/terminal-context", nil)
	var scope terminalScope
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &scope) != nil || scope.Target != "i-12345678" || scope.Region != "us-east-1" {
		t.Fatal("terminal context", w.Code, w.Body.String())
	}
	for name, got := range map[string]int{
		"context POST":     send("POST", "/_simulator/v1/terminal-context", nil).Code,
		"control GET":      send("GET", "/_simulator/v1/terminal-control", nil).Code,
		"channel origin":   send("GET", "/_simulator/v1/terminal-channel/x", nil).Code,
		"mixed bearer":     send("POST", "/_simulator/v1/terminal-control", func(r *http.Request) { r.Header.Set("X-Simulator-Token", "x") }).Code,
		"mixed SigV4":      send("POST", "/_simulator/v1/terminal-control", func(r *http.Request) { r.Header.Set("Authorization", "AWS4-HMAC-SHA256 x") }).Code,
		"forged cookie":    handoffRequest(a, withCookie(httptest.NewRequest("GET", "http://127.0.0.1:4566/_simulator/v1/terminal-context", nil), &http.Cookie{Name: cookie.Name, Value: "forged"})).Code,
		"console unscoped": send("GET", "/_simulator/console/app.js", nil).Code,
	} {
		if got == 200 || got == 204 {
			t.Errorf("%s allowed: %d", name, got)
		}
	}
	// Ending the browser lifetime ends the cookie even before expiry.
	key := sha256.Sum256([]byte(cookie.Value))
	a.mu.Lock()
	s := a.terminalBrowsers[key]
	a.mu.Unlock()
	s.cancel()
	if got := send("GET", "/_simulator/v1/terminal-context", nil).Code; got != 401 {
		t.Fatal("canceled terminal session used", got)
	}
	a.mu.Lock()
	s.ctx = nil
	a.terminalBrowsers[key] = s
	a.mu.Unlock()
	if got := send("GET", "/_simulator/v1/terminal-context", nil).Code; got != 401 {
		t.Fatal("unbound terminal session used", got)
	}
}

func withCookie(r *http.Request, c *http.Cookie) *http.Request { r.AddCookie(c); return r }
