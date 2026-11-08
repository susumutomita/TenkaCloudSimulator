package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
)

type consoleScopeKey struct{}
type consoleScope struct {
	ParameterName string `json:"parameterName"`
	Region        string `json:"region"`
}

func consoleScopeFromRequest(r *http.Request) any { return r.Context().Value(consoleScopeKey{}) }

type consoleHandoff struct {
	scope       consoleScope
	expires     time.Time
	destination string
	bind        func(*http.Request) (*http.Request, aws.SDKIdentity, error)
}

// A handoff is a short-lived, one-use reference to the original STS session.
// It cannot create an independent credential or choose an operator identity.
func (a *Access) issueConsoleHandoff(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var input struct{ ParameterName, Region string }
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	regionOK := false
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "Invalid console destination", 400)
		return
	}
	for _, region := range aws.Regions {
		regionOK = regionOK || region == input.Region
	}
	if !regionOK || !regexp.MustCompile(`^/?[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`).MatchString(input.ParameterName) || len(input.ParameterName) > 1011 {
		http.Error(w, "Invalid console destination", 400)
		return
	}
	bind, err := a.sdk.ConsoleBinding(r, input.Region, input.ParameterName)
	if err != nil {
		http.Error(w, "Participant STS credentials required", 403)
		return
	}
	_, identity, err := bind(r)
	if err != nil {
		http.Error(w, "Invalid participant session", 401)
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		http.Error(w, "Handoff unavailable", 500)
		return
	}
	code := hex.EncodeToString(random)
	now := a.now()
	expires := now.Add(60 * time.Second)
	if identity.Expires.Before(expires) {
		expires = identity.Expires
	}
	destination := "/_simulator/console/#/ssm?" + url.Values{"region": {input.Region}, "parameter": {input.ParameterName}}.Encode()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneConsoleSessions(now)
	if len(a.handoffs) >= 1000 {
		http.Error(w, "Console-handoff limit reached", 429)
		return
	}
	a.handoffs[sha256.Sum256([]byte(code))] = consoleHandoff{expires: expires, destination: destination, bind: bind, scope: consoleScope{input.ParameterName, input.Region}}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"Code": code, "ExpiresAt": expires.Format(time.RFC3339)})
}

// Caller holds a.mu.
func (a *Access) pruneConsoleSessions(now time.Time) {
	for key, s := range a.handoffs {
		if !now.Before(s.expires) {
			delete(a.handoffs, key)
		}
	}
	for key, s := range a.boundBrowsers {
		if !now.Before(s.expires) {
			delete(a.boundBrowsers, key)
		}
	}
	for key, s := range a.browsers {
		if !now.Before(s.expires) {
			delete(a.browsers, key)
		}
	}
}

func (a *Access) redeemConsoleHandoff(w http.ResponseWriter, r *http.Request) {
	if len(r.PostForm) != 1 || len(r.PostForm["handoff"]) != 1 || r.URL.RawQuery != "" {
		http.Error(w, "Invalid handoff request", 400)
		return
	}
	code := r.PostForm.Get("handoff")
	if len(code) != 64 {
		http.Error(w, "Invalid or expired handoff", 401)
		return
	}
	key := sha256.Sum256([]byte(code))
	a.mu.Lock()
	handoff, ok := a.handoffs[key]
	delete(a.handoffs, key) // Atomic consumption; every failed redemption requires a new handoff.
	a.mu.Unlock()
	if !ok || !a.now().Before(handoff.expires) {
		http.Error(w, "Invalid or expired handoff", 401)
		return
	}
	_, identity, err := handoff.bind(r)
	if err != nil {
		http.Error(w, "Invalid or expired participant session", 401)
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		http.Error(w, "Browser sign-in failed", 500)
		return
	}
	token := hex.EncodeToString(random)
	expires := a.now().Add(15 * time.Minute)
	if identity.Expires.Before(expires) {
		expires = identity.Expires
	}
	a.mu.Lock()
	a.pruneConsoleSessions(a.now())
	oldHash := [32]byte{}
	if c, e := r.Cookie(a.cookie); e == nil {
		oldHash = sha256.Sum256([]byte(c.Value))
	}
	_, oldLegacy := a.browsers[oldHash]
	_, oldBound := a.boundBrowsers[oldHash]
	if len(a.browsers)+len(a.boundBrowsers) >= 1000 && !oldLegacy && !oldBound {
		a.mu.Unlock()
		http.Error(w, "Browser-session limit reached", 429)
		return
	}
	a.revokeBrowser(r)
	handoff.expires = expires
	a.boundBrowsers[sha256.Sum256([]byte(token))] = handoff
	a.mu.Unlock()
	http.SetCookie(w, a.browserCookie(r, token, expires))
	if r.Header.Get("Accept") == "application/json" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"Destination": handoff.destination})
		return
	}
	http.Redirect(w, r, handoff.destination, http.StatusSeeOther)
}

func (a *Access) consoleBrowserRequest(r *http.Request, token string) (*http.Request, string, bool, bool) {
	key := sha256.Sum256([]byte(token))
	a.mu.Lock()
	session, exists := a.boundBrowsers[key]
	a.mu.Unlock()
	if !exists {
		return r, "", false, false
	}
	if !a.now().Before(session.expires) {
		a.mu.Lock()
		delete(a.boundBrowsers, key)
		a.mu.Unlock()
		return r, "", false, true
	}
	request, identity, err := session.bind(r)
	if err != nil {
		return r, "", false, true
	}
	// Native STS credentials have a deliberately bounded service surface. Browser
	// redemption cannot widen it to legacy identity APIs or other cloud providers.
	allowed := r.Method == "GET" && (r.URL.Path == "/_simulator/v1/capabilities" || strings.HasPrefix(r.URL.Path, "/_simulator/console/"))
	nativeHeaders := r.Header.Get("Authorization") == "" && r.Header.Get("X-Amz-Security-Token") == "" && (r.Header.Get("X-Simulator-Service") == "" || r.Header.Get("X-Simulator-Service") == "ssm")
	for _, name := range []string{"Content-Type", "X-Amz-Target", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal"} {
		nativeHeaders = nativeHeaders && len(r.Header.Values(name)) <= 1
	}
	allowed = allowed || (nativeHeaders && r.URL.RawPath == "" && r.Method == "POST" && r.URL.Path == "/" && r.URL.RawQuery == "" && r.Header.Get("Content-Type") == "application/x-amz-json-1.1" && (r.Header.Get("X-Amz-Target") == "AmazonSSM.GetParameter" || r.Header.Get("X-Amz-Target") == "AmazonSSM.PutParameter"))
	if !allowed {
		return r, identity.Principal, false, true
	}
	request = request.WithContext(context.WithValue(request.Context(), consoleScopeKey{}, session.scope))
	return request, identity.Principal, true, true
}
