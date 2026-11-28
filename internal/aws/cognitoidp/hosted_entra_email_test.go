package cognitoidp

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/susumutomita/TenkaCloudSimulator/internal/azure/entraidp"
)

var entraEmailFormField = regexp.MustCompile(`name="(flow|SAMLResponse|RelayState)" value="([^"]+)"`)

// Both handlers, SQLite federation state, SAML pin and JWT signing are real.
// HTTP recorders model exact loopback request metadata; this is not a browser.
func hostedFirstPartyEntraEmail(t *testing.T, scopes string, mapping map[string]string, wantACS int) (jwt.MapClaims, string) {
	t.Helper()
	_, db := federationTestPool(t, filepath.Join(t.TempDir(), "entra-email.sqlite"))
	s, err := NewStoreWithLocalFederationConfig(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{"012345678901", "us-east-1"}
	p, err := s.create(t.Context(), scope, "first-party Entra email")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHostedBrowser(s, scope.Account, hostedFixtureOrigin)
	if err != nil {
		t.Fatal(err)
	}
	b := handler.(*hostedBrowser)
	sp, err := b.ServiceProvider(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, code, err := entraidp.New(entraidp.Config{Origin: fixtureHostedIDP, ServiceProvider: entraidp.ServiceProvider{
		EntityID: sp.EntityID, ACS: sp.ACS, SigningCertificateDER: sp.SigningCertificateDER,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	public := f.Public()
	idp := func(method, target string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		u, err := url.Parse(target)
		if err != nil || hostedOrigin(u) != fixtureHostedIDP {
			t.Fatal("IdP request escaped the exact enrolled origin")
		}
		r := httptest.NewRequestWithContext(t.Context(), method, u.RequestURI(), strings.NewReader(values.Encode()))
		r.Host, r.RemoteAddr, r.TLS = u.Host, "127.0.0.1:18082", &tls.ConnectionState{}
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", fixtureHostedIDP)
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		f.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatal("first-party IdP exact signed flow failed", w.Code)
		}
		return w
	}
	fields := func(w *httptest.ResponseRecorder, want ...string) url.Values {
		t.Helper()
		out := url.Values{}
		for _, m := range entraEmailFormField.FindAllStringSubmatch(w.Body.String(), -1) {
			out.Add(m[1], html.UnescapeString(m[2]))
		}
		if len(out) != len(want) {
			t.Fatal("unexpected IdP form shape")
		}
		for _, name := range want {
			if len(out[name]) != 1 {
				t.Fatal("missing or duplicate IdP form member", name)
			}
		}
		return out
	}
	pub := federationPublisherFor(t, s, scope, federationAllow)
	if _, err := pub.addProvider(t.Context(), p.ID, federationProviderInput{
		Name: "Entra", MetadataFile: idp(http.MethodGet, public.MetadataURL, nil, nil).Body.String(), Mapping: mapping,
	}); err != nil {
		t.Fatal("exact local SAML metadata and mapping refused", err)
	}
	client, err := pub.addClient(t.Context(), p.ID, federationClientInput{
		Name: "first-party local test client", Callbacks: []string{hostedFixtureCallback},
		Providers: []string{"Entra"}, Scopes: []string{"openid", "email"},
	})
	if err != nil {
		t.Fatal(err)
	}
	flow := hostedLogin{id: p.ID, client: client.ID, callback: hostedFixtureCallback, state: fixtureHostedState,
		verifier: strings.Repeat("v", 43)}
	challenge := sha256.Sum256([]byte(flow.verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {flow.client}, "redirect_uri": {flow.callback},
		"scope": {scopes}, "state": {flow.state}, "nonce": {fixtureHostedNonce},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"},
		"identity_provider": {"Entra"}}
	base := hostedPrefix + flow.id + "/"
	w := hostedServe(b, hostedFixtureRequest(http.MethodGet, base+hostedAuthorize+"?"+q.Encode(), nil))
	if w.Code != http.StatusFound || len(w.Result().Cookies()) != 1 {
		t.Fatal("registered scopes did not start the hosted SAML flow", w.Code)
	}
	flow.cookie = w.Result().Cookies()[0]
	signIn := idp(http.MethodGet, w.Header().Get("Location"), nil, nil)
	if len(signIn.Result().Cookies()) != 1 {
		t.Fatal("local Entra form missing its browser-bound cookie")
	}
	input := fields(signIn, "flow")
	input.Set("code", code)
	signed := idp(http.MethodPost, public.SSOURL, input, signIn.Result().Cookies()[0])
	acs := hostedFixtureRequest(http.MethodPost, base+hostedACS, fields(signed, "SAMLResponse", "RelayState"))
	acs.Header.Set("Origin", fixtureHostedIDP)
	acs.AddCookie(flow.cookie)
	completion := hostedServe(b, acs)
	if wantACS != http.StatusOK {
		if completion.Code != wantACS || completion.Header().Get("Location") != "" ||
			strings.Contains(completion.Body.String(), "Continue to the application") {
			t.Fatal("missing required signed source did not fail closed", completion.Code)
		}
		return nil, public.Email()
	}
	callback, _ := assertHostedCompletion(t, flow, completion)
	token := hostedServe(b, hostedExchangeRequest(flow, callback.Query().Get("code"), hostedOrigin(callback)))
	var pair struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	if token.Code != http.StatusOK || json.Unmarshal(token.Body.Bytes(), &pair) != nil || pair.Scope != scopes {
		t.Fatal("exact callback and PKCE did not yield scoped tokens", token.Code)
	}
	keys := hostedFixturePublicKeys(t, b, p.ID)
	parse := func(encoded string, extra ...jwt.ParserOption) jwt.MapClaims {
		t.Helper()
		opts := append([]jwt.ParserOption{jwt.WithIssuer(b.root(p.ID)), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"RS256"})}, extra...)
		claims := jwt.MapClaims{}
		verified, err := jwt.ParseWithClaims(encoded, claims, func(token *jwt.Token) (any, error) {
			kid, _ := token.Header["kid"].(string)
			if key := keys[kid]; key != nil {
				return key, nil
			}
			return nil, errors.New("unexpected signing key")
		}, opts...)
		if err != nil || !verified.Valid {
			t.Fatal("consumer could not verify native JWT")
		}
		return claims
	}
	identity := parse(pair.IDToken, jwt.WithAudience(flow.client))
	access := parse(pair.AccessToken)
	sub, _ := identity["sub"].(string)
	if !hostedCanonicalNonce(sub) || sub == public.Email() || identity["token_use"] != "id" ||
		identity["nonce"] != fixtureHostedNonce || access["token_use"] != "access" ||
		access["sub"] != sub || access["client_id"] != flow.client || access["scope"] != scopes {
		t.Fatal("actual tokens did not preserve stable subject, audience or session binding")
	}
	for _, name := range []string{"email_verified", entraidp.EmailClaimURI, entraObjectIdentifier,
		"http://schemas.microsoft.com/identity/claims/tenantid"} {
		_, inID := identity[name]
		_, inAccess := access[name]
		if inID || inAccess {
			t.Fatal("unexpected raw identity or mailbox-verification claim")
		}
	}
	if _, exists := access["email"]; exists {
		t.Fatal("email was disclosed in the access token")
	}
	return identity, public.Email()
}

func TestHostedFirstPartyEntraEmailIsExplicitlyScopedAndNotMailboxVerification(t *testing.T) {
	mapped := map[string]string{"email": entraidp.EmailClaimURI}
	member, memberEmail := hostedFirstPartyEntraEmail(t, "openid email", mapped, http.StatusOK)
	nonmember, nonmemberEmail := hostedFirstPartyEntraEmail(t, "openid email", mapped, http.StatusOK)
	// Each identity was actually authenticated under an independently enrolled
	// local fixture; the application applies the same one-address allowlist.
	allowlist := map[string]bool{memberEmail: true}
	memberClaim, memberOK := member["email"].(string)
	nonmemberClaim, nonmemberOK := nonmember["email"].(string)
	if !memberOK || !nonmemberOK || memberClaim != memberEmail || nonmemberClaim != nonmemberEmail ||
		!allowlist[memberClaim] || allowlist[nonmemberClaim] || member["sub"] == nonmember["sub"] {
		t.Fatal("authenticated local accounts must remain distinct at an exact allowlist")
	}
	t.Run("openid only does not disclose email", func(t *testing.T) {
		ordinary, _ := hostedFirstPartyEntraEmail(t, "openid", mapped, http.StatusOK)
		if _, exists := ordinary["email"]; exists {
			t.Fatal("signed IdP attribute escaped requested scope")
		}
	})
	t.Run("wrong source mapping still refused", func(t *testing.T) {
		hostedFirstPartyEntraEmail(t, "openid email", map[string]string{"email": "urn:fixture:unavailable"}, http.StatusBadRequest)
	})
	t.Run("no fabricated verification despite required mapping", func(t *testing.T) {
		hostedFirstPartyEntraEmail(t, "openid email", map[string]string{
			"email": entraidp.EmailClaimURI, "email_verified": "email_verified",
		}, http.StatusBadRequest)
	})
}
