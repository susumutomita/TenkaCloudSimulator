package cognitoidp

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const hostedFixtureOrigin = "https://127.0.0.1:18446"
const hostedFixtureCallback = "https://127.0.0.1:18447/callback?app=fixture"

func newHostedFixture(t *testing.T, providers ...federationProviderInput) (*hostedBrowser, *federationPublisher, Scope, string, string) {
	t.Helper()
	_, db := federationTestPool(t, filepath.Join(t.TempDir(), "hosted.sqlite"))
	s, err := NewStoreWithLocalFederationConfig(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"012345678901", "us-east-1"}
	p, err := s.create(t.Context(), scope, "canonical hosted fixture")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewHostedBrowser(s, scope.Account, hostedFixtureOrigin)
	if err != nil {
		t.Fatal(err)
	}
	pub := federationPublisherFor(t, s, scope, federationAllow)
	if len(providers) == 0 {
		in := federationProviderFixture(t, "Entra")
		in.MetadataFile = strings.ReplaceAll(in.MetadataFile, "http://127.0.0.1:", "https://127.0.0.1:")
		providers = []federationProviderInput{in}
	}
	names := make([]string, 0, len(providers))
	for _, in := range providers {
		if _, err := pub.addProvider(t.Context(), p.ID, in); err != nil {
			t.Fatal(err)
		}
		names = append(names, in.Name)
	}
	client, err := pub.addClient(t.Context(), p.ID, federationClientInput{
		Name: "hosted fixture Client", Callbacks: []string{hostedFixtureCallback}, Providers: names, Scopes: []string{"openid", "email"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.(*hostedBrowser), pub, scope, p.ID, client.ID
}

func hostedFixtureRequest(method, path string, values url.Values) *http.Request {
	var body string
	if method == http.MethodPost {
		body = values.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "127.0.0.1:18446"
	r.TLS = &tls.ConnectionState{}
	r.RemoteAddr = "127.0.0.1:18448"
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return r
}

func TestHostedHTTPSAuthorizeUsesCanonicalSignedConfig(t *testing.T) {
	b, pub, scope, id, client := newHostedFixture(t)
	path := hostedPrefix + id + "/" + hostedAuthorize
	form := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {hostedFixtureCallback},
		"scope": {"openid email"}, "state": {"opaque-test-state"}, "nonce": {"opaque-test-nonce"},
		"code_challenge": {hostedDigest(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}, "identity_provider": {"Entra"}}
	r := hostedFixtureRequest(http.MethodGet, path+"?"+form.Encode(), nil)
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	if w.Code != http.StatusFound {
		t.Fatal("registered browser binding not accepted", w.Code)
	}
	out, err := url.Parse(w.Header().Get("Location"))
	if err != nil || out.Scheme != "https" || out.Host != "127.0.0.1:18081" || out.Path != "/sso" ||
		out.Query().Get("SAMLRequest") == "" || out.Query().Get("Signature") == "" {
		t.Fatal("did not produce signed request to exact registered local destination")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly ||
		cookies[0].Path != hostedPrefix+id+"/" || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("missing bound secure cookie")
	}
	var old federationRevision
	if err := b.transition(t.Context(), scope, id, nil, func(_ federationConfig, rev federationRevision, s *hostedState) (bool, error) {
		old = rev
		p, ok := s.Pending[hostedDigest(out.Query().Get("RelayState"))]
		if !ok || p.RequestID == "" || p.Browser != hostedDigest(cookies[0].Value) || p.Nonce != "opaque-test-nonce" {
			t.Fatal("pending state was not bound to the canonical pool")
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.addClient(t.Context(), id, federationClientInput{
		Name: "second client", Callbacks: []string{hostedFixtureCallback}, Providers: []string{"Entra"}, Scopes: []string{"openid"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.transition(t.Context(), scope, id, &old, func(_ federationConfig, _ federationRevision, _ *hostedState) (bool, error) {
		t.Fatal("stale callback entered mutation")
		return false, nil
	}); !errors.Is(err, errFederationStale) {
		t.Fatal("lost epoch/revision fence", err)
	}
	if err := b.transition(t.Context(), scope, id, nil, func(_ federationConfig, _ federationRevision, s *hostedState) (bool, error) {
		if len(s.Pending) != 0 {
			t.Fatal("old pending state remained after canonical config edit")
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, alter := range map[string]func(*http.Request){
		"wrong host":                          func(r *http.Request) { r.Host = "localhost:18446" },
		"no TLS":                              func(r *http.Request) { r.TLS = nil },
		"non-loopback peer":                   func(r *http.Request) { r.RemoteAddr = "192.0.2.20:9328" },
		"untrusted cross-origin":              func(r *http.Request) { r.Header.Set("Origin", "https://127.0.0.1:4001") },
		"forwarded":                           func(r *http.Request) { r.Header.Set("X-Forwarded-Host", "127.0.0.1:18446") },
		"attempt to auth provider middleware": func(r *http.Request) { r.Header.Set("Authorization", "Bearer synthetic") },
		"aliased path":                        func(r *http.Request) { r.URL.RawPath = hostedPrefix + id + "/oauth2/%61uthorize" },
	} {
		t.Run(name, func(t *testing.T) {
			req := hostedFixtureRequest(http.MethodGet, path+"?"+form.Encode(), nil)
			alter(req)
			rec := httptest.NewRecorder()
			b.ServeHTTP(rec, req)
			if rec.Code < 400 || rec.Header().Get("Location") != "" || rec.Header().Get("Set-Cookie") != "" {
				t.Fatal("unapproved transport admitted", rec.Code)
			}
		})
	}
}

// A deliberately seeded synthetic grant exercises PKCE, signed output and
// transactional consumption only; this is not SAML acceptance evidence.
func TestHostedPKCEExactOriginAndOneUse(t *testing.T) {
	b, _, scope, id, client := newHostedFixture(t)
	path := hostedPrefix + id + "/" + hostedToken
	now := time.Now().UTC()
	code := hostedRandom()
	verifier := strings.Repeat("x", 43)
	if err := b.transition(t.Context(), scope, id, nil, func(_ federationConfig, _ federationRevision, s *hostedState) (bool, error) {
		s.Codes[hostedDigest(code)] = hostedGrant{Client: client, Provider: "Entra", Redirect: hostedFixtureCallback,
			Challenge: hostedDigest(verifier), Scope: "openid email", Nonce: "fixture-nonce", Subject: "opaque-subject",
			Attributes: map[string]string{"email": "synthetic@example.invalid"}, AuthTime: now, Until: now.Add(time.Minute)}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	v := url.Values{"grant_type": {"authorization_code"}, "client_id": {client}, "code": {code}, "redirect_uri": {hostedFixtureCallback}, "code_verifier": {verifier}}
	call := func(origin string, want int) *httptest.ResponseRecorder {
		r := hostedFixtureRequest(http.MethodPost, path, v)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("token endpoint: status %d != %d", w.Code, want)
		}
		return w
	}
	if denied := call("https://127.0.0.1:9999", 400); denied.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("nonmatching Origin received CORS")
	}
	v.Set("code_verifier", strings.Repeat("y", 43))
	call("https://127.0.0.1:18447", 400)
	v.Set("code_verifier", verifier)
	w := call("https://127.0.0.1:18447", 200)
	if w.Header().Get("Access-Control-Allow-Origin") != "https://127.0.0.1:18447" {
		t.Fatal("missing callback exact CORS")
	}
	var output struct {
		Identity string `json:"id_token"`
		Access   string `json:"access_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	for tokenUse, text := range map[string]string{"id": output.Identity, "access": output.Access} {
		key := &b.idKey.PublicKey
		if tokenUse == "access" {
			key = &b.accessKey.PublicKey
		}
		m := jwt.MapClaims{}
		j, err := jwt.ParseWithClaims(text, m, func(token *jwt.Token) (any, error) {
			if token.Header["kid"] != hostedKeyID(key) {
				return nil, errInvalid
			}
			return key, nil
		}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(b.root(id)), jwt.WithExpirationRequired())
		if err != nil || !j.Valid || m["token_use"] != tokenUse || m["sub"] != "opaque-subject" {
			t.Fatalf("invalid %s signature/claims", tokenUse)
		}
		if tokenUse == "id" && (m["nonce"] != "fixture-nonce" || m["aud"] != client || m["email"] != "synthetic@example.invalid") ||
			tokenUse == "access" && (m["client_id"] != client || m["scope"] != "openid email" || m["email"] != nil) {
			t.Fatal("unsafe cross-token claims")
		}
	}
	call("https://127.0.0.1:18447", 400)
	if err := b.store.delete(t.Context(), scope, id); err != nil {
		t.Fatal(err)
	}
	readHost := hostedFixtureRequest(http.MethodGet, hostedPrefix+id+"/"+hostedJWKS, nil)
	jwks := httptest.NewRecorder()
	b.ServeHTTP(jwks, readHost)
	if jwks.Code != 400 {
		t.Fatal("deleted pool still served signing metadata")
	}
}

func TestHostedAuthorizeOptionalNonce(t *testing.T) {
	b, _, scope, id, client := newHostedFixture(t)
	path := hostedPrefix + id + "/" + hostedAuthorize
	form := url.Values{
		"response_type": {"code"}, "client_id": {client}, "redirect_uri": {hostedFixtureCallback},
		"scope": {"openid"}, "state": {"opaque-state"},
		"code_challenge": {hostedDigest(strings.Repeat("n", 43))}, "code_challenge_method": {"S256"},
		"identity_provider": {"Entra"},
	}
	call := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		b.ServeHTTP(w, hostedFixtureRequest(http.MethodGet, path+"?"+form.Encode(), nil))
		return w
	}
	for name, values := range map[string][]string{
		"empty":     {""},
		"duplicate": {"first", "second"},
	} {
		t.Run(name, func(t *testing.T) {
			form["nonce"] = values
			w := call()
			delete(form, "nonce")
			if w.Code != 400 || w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("malformed explicit nonce was admitted", w.Code)
			}
		})
	}
	for _, missing := range []string{"state", "code_challenge", "identity_provider"} {
		value := form[missing]
		delete(form, missing)
		w := call()
		form[missing] = value
		if w.Code != 400 || w.Header().Get("Location") != "" {
			t.Fatal("other required OAuth field became optional", missing, w.Code)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		w := call()
		if w.Code != http.StatusFound {
			t.Fatal("absence of nonce was not accepted", w.Code)
		}
		u, err := url.Parse(w.Header().Get("Location"))
		if err != nil || u.Query().Has("nonce") {
			t.Fatal("nonce escaped the service-provider state")
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatal("omitting nonce removed request's browser binding")
		}
		if err := b.transition(t.Context(), scope, id, nil,
			func(_ federationConfig, _ federationRevision, s *hostedState) (bool, error) {
				p, ok := s.Pending[hostedDigest(u.Query().Get("RelayState"))]
				if !ok || p.Browser != hostedDigest(cookies[0].Value) || p.State != "opaque-state" ||
					!hostedCanonicalNonce(p.Nonce) || seen[p.Nonce] || (p.Nonce == cookies[0].Value) {
					t.Fatal("missing fresh server nonce in the exact correlated pending login")
				}
				seen[p.Nonce] = true
				if len(s.Pending) != i+1 {
					t.Fatal("a rejected login wrote pending state")
				}
				return false, nil
			}); err != nil {
			t.Fatal(err)
		}
	}
}
