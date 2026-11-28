package main

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/susumutomita/TenkaCloudSimulator/internal/azure/entraidp"
)

// The app starts before Cognito issues the signed request that starts Entra's
// clock. Allow five existing handler budgets for the normal automatic local
// handoffs before issuance and after code entry. This is not a guarantee for a
// browser suspended between requests; an expired callback still cannot log in.
const localSSOAttemptLifetime = entraidp.AuthnRequestLifetime + 5*localSSOHandlerTimeout

type localSSOPending struct {
	browser, verifier, nonce string
	expires                  time.Time
}

type localSSORelyingParty struct {
	client                                     *http.Client
	issuer, clientID, origin, callback, cookie string
	mu                                         sync.Mutex
	pending                                    map[string]localSSOPending
}

func newLocalSSORelyingParty(client *http.Client, issuer, clientID, origin string) *localSSORelyingParty {
	return &localSSORelyingParty{client: client, issuer: issuer, clientID: clientID, origin: origin,
		callback: origin + "/callback", cookie: "tcsim-rp-" + localSSOEntropy(), pending: make(map[string]localSSOPending)}
}

func (p *localSSORelyingParty) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	peer, e := netip.ParseAddrPort(r.RemoteAddr)
	if e != nil || peer.Addr().String() != "127.0.0.1" || r.TLS == nil || "https://"+r.Host != p.origin ||
		r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawPath != "" || r.URL.IsAbs() ||
		r.URL.ForceQuery || len(r.URL.RawQuery) > 2048 || r.Header.Get("Sec-Fetch-Site") == "cross-site" ||
		r.Header.Get("Authorization") != "" || len(r.Header.Values("Origin")) > 1 ||
		(r.Header.Get("Origin") != "" && r.Header.Get("Origin") != p.origin) {
		http.Error(w, "Local sample request refused.", http.StatusForbidden)
		return
	}
	for field := range r.Header {
		k := strings.ToLower(field)
		if k == "forwarded" || strings.HasPrefix(k, "x-forwarded-") {
			http.Error(w, "Local sample request refused.", http.StatusForbidden)
			return
		}
	}
	switch {
	case r.URL.Path == "/" && r.URL.RawQuery == "":
		h.Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Local SSO</title><h1>Local Cognito + Entra</h1><p>This sample uses one synthetic identity. It does not sign in to an AWS or Microsoft account.</p><a href="/start">Sign in with local Entra</a>`)
	case r.URL.Path == "/start" && r.URL.RawQuery == "":
		p.start(w, r)
	case r.URL.Path == "/callback":
		if p.finish(w, r) != nil {
			http.Error(w, "Local sign-in could not be verified. If you already submitted the one-time code, restart local-sso for a fresh run. Otherwise return to the original sample URL.", http.StatusBadRequest)
		}
	default:
		http.NotFound(w, r)
	}
}
func (p *localSSORelyingParty) start(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	state := localSSOEntropy()
	item := localSSOPending{browser: localSSOEntropy(), verifier: localSSOEntropy(), nonce: localSSOEntropy(), expires: now.Add(localSSOAttemptLifetime)}
	p.mu.Lock()
	for k, v := range p.pending {
		if !now.Before(v.expires) {
			delete(p.pending, k)
		}
	}
	if len(p.pending) >= 4 {
		p.mu.Unlock()
		http.Error(w, "Local sign-in is busy; wait for an earlier attempt to expire.", http.StatusTooManyRequests)
		return
	}
	p.pending[state] = item
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: p.cookie, Value: item.browser, Path: "/callback", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode, MaxAge: int(localSSOAttemptLifetime.Seconds())})
	hash := sha256.Sum256([]byte(item.verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {p.clientID}, "redirect_uri": {p.callback}, "scope": {"openid"},
		"state": {state}, "nonce": {item.nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])},
		"code_challenge_method": {"S256"}, "identity_provider": {"Entra"}}
	http.Redirect(w, r, p.issuer+"/oauth2/authorize?"+q.Encode(), http.StatusSeeOther)
}

func localSSOOpaque(s string) bool {
	if len(s) != 43 {
		return false
	}
	b, e := base64.RawURLEncoding.DecodeString(s)
	return e == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}
func (p *localSSORelyingParty) finish(w http.ResponseWriter, r *http.Request) error {
	refused := errors.New("local SSO callback refused")
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil || len(q) != 2 || len(q["state"]) != 1 || len(q["code"]) != 1 ||
		!localSSOOpaque(q.Get("state")) || !localSSOOpaque(q.Get("code")) {
		return refused
	}
	cookies := r.CookiesNamed(p.cookie)
	if len(cookies) != 1 || !localSSOOpaque(cookies[0].Value) {
		return refused
	}
	state := q.Get("state")
	p.mu.Lock()
	prior, ok := p.pending[state]
	ok = ok && time.Now().Before(prior.expires) && subtle.ConstantTimeCompare([]byte(prior.browser), []byte(cookies[0].Value)) == 1
	if ok {
		delete(p.pending, state)
	}
	p.mu.Unlock()
	if !ok {
		return refused
	}
	http.SetCookie(w, &http.Cookie{Name: p.cookie, Path: "/callback", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	form := url.Values{"grant_type": {"authorization_code"}, "code": {q.Get("code")}, "redirect_uri": {p.callback},
		"client_id": {p.clientID}, "code_verifier": {prior.verifier}}
	req, e := http.NewRequestWithContext(r.Context(), http.MethodPost, p.issuer+"/oauth2/token", strings.NewReader(form.Encode()))
	if e != nil {
		return refused
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	wire, e := localSSODo(p.client, req)
	if e != nil {
		return refused
	}
	var result struct {
		Identity string `json:"id_token"`
	}
	if json.Unmarshal(wire, &result) != nil || result.Identity == "" || len(result.Identity) > 8192 {
		return refused
	}
	jwks, e := localSSOGet(r.Context(), p.client, p.issuer+"/.well-known/jwks.json")
	if e != nil || !localSSOVerifyJWT(jwks, result.Identity, p.issuer, p.clientID, prior.nonce) {
		return refused
	}
	// Do not expose claims, authorization codes or JWTs. The acknowledgement
	// proves this synthetic flow precisely; it creates no simulator session.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, e = fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>Local sign-in verified</title><h1>Local sign-in verified</h1><p>The synthetic Entra identity passed Cognito and the sample app's callback checks. No AWS, Microsoft, or simulator session was created.</p>`)
	return e
}

func localSSOVerifyJWT(raw []byte, token, issuer, client, nonce string) bool {
	var set struct {
		Keys []struct{ Kid, Alg, Kty, Use, N, E string }
	}
	if json.Unmarshal(raw, &set) != nil || len(set.Keys) != 2 {
		return false
	}
	keys := map[string]*rsa.PublicKey{}
	for _, j := range set.Keys {
		n, e := base64.RawURLEncoding.DecodeString(j.N)
		if e != nil {
			return false
		}
		ex, e := base64.RawURLEncoding.DecodeString(j.E)
		if e != nil {
			return false
		}
		bigN := new(big.Int).SetBytes(n)
		if !localSSOOpaque(j.Kid) || j.Alg != "RS256" || j.Kty != "RSA" || j.Use != "sig" || keys[j.Kid] != nil ||
			bigN.BitLen() != 2048 || !new(big.Int).SetBytes(ex).IsInt64() || new(big.Int).SetBytes(ex).Int64() != 65537 {
			return false
		}
		keys[j.Kid] = &rsa.PublicKey{N: bigN, E: 65537}
	}
	claims := jwt.MapClaims{}
	validated, e := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		id, ok := t.Header["kid"].(string)
		if !ok || keys[id] == nil {
			return nil, errors.New("local SSO signing key refused")
		}
		return keys[id], nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(issuer), jwt.WithAudience(client), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	sub, subErr := claims.GetSubject()
	actualNonce, ok := claims["nonce"].(string)
	return e == nil && validated.Valid && subErr == nil && sub != "" && claims["token_use"] == "id" && ok &&
		subtle.ConstantTimeCompare([]byte(actualNonce), []byte(nonce)) == 1
}
