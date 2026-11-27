package cognitoidp

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	"github.com/golang-jwt/jwt/v5"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

const sampleMailClaim = "urn:example:practice:mail"
const sampleNameClaim = "urn:example:practice:name"

func hostedProfileFor(f hostedSignedIDP) federationProviderInput {
	in := f.provider("Entra")
	in.Mapping = map[string]string{"email": sampleMailClaim, "email_verified": "email_verified", "name": sampleNameClaim}
	return in
}

func hostedTypedClaim(name, kind, value string) saml.Attribute {
	return saml.Attribute{Name: name, Values: []saml.AttributeValue{{Type: kind, Value: value}}}
}

func hostedExchangeRequest(l hostedLogin, code, origin string) *http.Request {
	r := hostedFixtureRequest(http.MethodPost, hostedPrefix+l.id+"/"+hostedToken, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {l.client}, "code": {code},
		"redirect_uri": {l.callback}, "code_verifier": {l.verifier},
	})
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	return r
}

func hostedServe(b *hostedBrowser, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}

func hostedFixturePublicKeys(t *testing.T, b *hostedBrowser, id string) map[string]*rsa.PublicKey {
	t.Helper()
	w := hostedServe(b, hostedFixtureRequest(http.MethodGet, hostedPrefix+id+"/"+hostedJWKS, nil))
	var body struct {
		Keys []struct{ Kty, Use, Alg, Kid, N, E string }
	}
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" ||
		w.Header().Get("Cache-Control") != "no-store" || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Keys) != 2 {
		t.Fatal("live pool did not publish two usable verifying keys", w.Code)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range body.Keys {
		rawN, nErr := base64.RawURLEncoding.DecodeString(k.N)
		rawE, eErr := base64.RawURLEncoding.DecodeString(k.E)
		mod, exp := new(big.Int).SetBytes(rawN), new(big.Int).SetBytes(rawE)
		if nErr != nil || eErr != nil || mod.BitLen() < 2048 || !exp.IsInt64() || exp.Int64() != 65537 ||
			k.Kty != "RSA" || k.Use != "sig" || k.Alg != "RS256" || k.Kid == "" || keys[k.Kid] != nil {
			t.Fatal("JWKS has unusable signing material")
		}
		key := &rsa.PublicKey{N: mod, E: int(exp.Int64())}
		if k.Kid != hostedKeyID(key) {
			t.Fatal("JWKS key id does not identify the corresponding public key")
		}
		keys[k.Kid] = key
	}
	return keys
}

func TestHostedSignedScopesProjectConfiguredClaimsUsingPublicSigningKeys(t *testing.T) {
	f := newHostedSignedIDP(t)
	b, pub, _, id, _ := newHostedFixture(t, hostedProfileFor(f))
	client, err := pub.addClient(t.Context(), id, federationClientInput{
		Name: "scoped browser fixture", Callbacks: []string{hostedFixtureCallback}, Providers: []string{"Entra"},
		Scopes: []string{"openid", "email", "profile"},
	})
	if err != nil {
		t.Fatal(err)
	}
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "tester@example.invalid"}
	for _, tc := range []struct {
		name, scopes string
		verified     bool
		browser      bool
	}{
		{"explicit browser profile", "openid email profile", true, true},
		{"server email only", "openid email", false, false},
		{"no attributes in basic sign-in", "openid", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := []saml.Attribute{
				hostedObjectIDs(fixtureEntraAlice),
				hostedTypedClaim(sampleMailClaim, "xs:string", "tester@example.invalid"),
				hostedTypedClaim("email_verified", "xs:boolean", strconv.FormatBool(tc.verified)),
				hostedTypedClaim(sampleNameClaim, "xs:string", "Practice Developer"),
				hostedTypedClaim("urn:example:training-group", "xs:string", "training-organizers"),
			}
			l := beginHostedSignedLoginWithScopes(t, b, id, client.ID, "Entra", hostedFixtureCallback, fixtureHostedState, tc.scopes)
			cb, _ := assertHostedCompletion(t, l, f.post(t, b, l, "Entra", subject, attrs...))
			var origin string
			if tc.browser {
				origin = hostedOrigin(cb)
			}
			w := hostedServe(b, hostedExchangeRequest(l, cb.Query().Get("code"), origin))
			var response struct {
				Identity  string `json:"id_token"`
				Access    string `json:"access_token"`
				TokenType string `json:"token_type"`
				Expires   int    `json:"expires_in"`
				Scope     string `json:"scope"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Scope != tc.scopes ||
				response.TokenType != "Bearer" || response.Expires != int(hostedTokenTTL.Seconds()) ||
				w.Header().Get("Access-Control-Allow-Origin") != origin || (w.Header().Get("Vary") == "Origin") != tc.browser {
				t.Fatal("successful scoped exchange returned the wrong wire contract", w.Code)
			}
			keys := hostedFixturePublicKeys(t, b, id)
			parse := func(encoded string) (jwt.MapClaims, string) {
				t.Helper()
				claims := jwt.MapClaims{}
				token, err := jwt.ParseWithClaims(encoded, claims, func(token *jwt.Token) (any, error) {
					kid, _ := token.Header["kid"].(string)
					if key := keys[kid]; key != nil {
						return key, nil
					}
					return nil, errors.New("token key is not in the published JWKS")
				}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(b.root(id)), jwt.WithExpirationRequired())
				if err != nil || !token.Valid {
					t.Fatal("cannot verify scoped token from the public JWKS", err)
				}
				return claims, token.Header["kid"].(string)
			}
			identity, idKID := parse(response.Identity)
			access, accessKID := parse(response.Access)
			sub, _ := identity["sub"].(string)
			if idKID == accessKID || !hostedCanonicalNonce(sub) || access["sub"] != sub || identity["aud"] != client.ID ||
				identity["nonce"] != fixtureHostedNonce || identity["token_use"] != "id" || access["token_use"] != "access" ||
				access["client_id"] != client.ID || access["scope"] != tc.scopes || access["aud"] != nil || access["nonce"] != nil {
				t.Fatal("identity and access token projections or signing keys were mixed")
			}
			wantsEmail, wantsProfile := slices.Contains(strings.Fields(tc.scopes), "email"), slices.Contains(strings.Fields(tc.scopes), "profile")
			if wantsEmail && (identity["email"] != "tester@example.invalid" || identity["email_verified"] != tc.verified) ||
				!wantsEmail && (identity["email"] != nil || identity["email_verified"] != nil) ||
				wantsProfile && identity["name"] != "Practice Developer" || !wantsProfile && identity["name"] != nil {
				t.Fatal("ID token attributes do not match its explicitly authorized scopes")
			}
			for _, claim := range []string{"email", "email_verified", "name"} {
				if access[claim] != nil {
					t.Fatal("an identity attribute crossed the access token boundary", claim)
				}
			}
			for _, claim := range []string{sampleMailClaim, sampleNameClaim, "urn:example:training-group", entraObjectIdentifier} {
				if access[claim] != nil || identity[claim] != nil {
					t.Fatal("an unrequested assertion source attribute crossed a token boundary", claim)
				}
			}
		})
	}
}

func TestHostedSignedDirectoryMappingAcceptsCorrectionsWithoutBurningPendingLogin(t *testing.T) {
	f := newHostedSignedIDP(t)
	b, _, _, id, client := newHostedFixture(t, hostedProfileFor(f))
	idClaim := hostedObjectIDs(fixtureEntraAlice)
	mail := hostedTypedClaim(sampleMailClaim, "xs:string", "tester@example.invalid")
	verified := hostedTypedClaim("email_verified", "xs:boolean", "true")
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "tester@example.invalid"}

	for _, tc := range []struct {
		name  string
		attrs []saml.Attribute
	}{
		{"missing required mail", []saml.Attribute{idClaim, verified}},
		{"directory forgot verified status", []saml.Attribute{idClaim, mail}},
		{"invalid directory mail", []saml.Attribute{idClaim, hostedTypedClaim(sampleMailClaim, "xs:string", "not-an-address"), verified}},
		{"mail typed as bool", []saml.Attribute{idClaim, hostedTypedClaim(sampleMailClaim, "xs:boolean", "true"), verified}},
		{"directory configured mail twice", []saml.Attribute{idClaim, mail, verified, mail}},
		{"unexpected email status word", []saml.Attribute{idClaim, mail, hostedTypedClaim("email_verified", "xs:string", "unconfirmed")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := beginHostedSignedLoginWithScopes(t, b, id, client, "Entra", hostedFixtureCallback, fixtureHostedState, "openid email")
			rejected := f.post(t, b, l, "Entra", subject, tc.attrs...)
			if rejected.Code != http.StatusBadRequest || rejected.Header().Get("Location") != "" ||
				rejected.Header().Get("Set-Cookie") != "" || strings.Contains(rejected.Body.String(), "Continue") {
				t.Fatal("an invalid directory mapping produced browser continuation")
			}
			w := f.post(t, b, l, "Entra", subject, idClaim, mail, verified)
			cb, _ := assertHostedCompletion(t, l, w)
			if got := hostedServe(b, hostedExchangeRequest(l, cb.Query().Get("code"), "")); got.Code != http.StatusOK {
				t.Fatal("valid directory correction could not finish the original pending login", got.Code)
			}
		})
	}
}

func TestHostedBrowserConfigurationUsesExactLiveHTTPSRegistration(t *testing.T) {
	f := newHostedSignedIDP(t)
	old := f.provider("Keycloak")
	old.MetadataFile = strings.ReplaceAll(old.MetadataFile, "https://127.0.0.1:", "http://127.0.0.1:")
	b, pub, scope, id, client := newHostedFixture(t, hostedProfileFor(f), old)
	httpCallback := strings.Replace(hostedFixtureCallback, "https:", "http:", 1)
	registeredHTTP, err := pub.addClient(t.Context(), id, federationClientInput{
		Name: "legacy metadata client", Callbacks: []string{httpCallback}, Providers: []string{"Entra"}, Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	independent, err := b.store.create(t.Context(), scope, "a pool before federation setup")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, pool, client, provider, callback, scopes string
	}{
		{"no registration in pool", independent.ID, client, "Entra", hostedFixtureCallback, "openid"},
		{"not an application callback", id, client, "Entra", hostedFixtureCallback + "&view=preview", "openid"},
		{"scope not authorized by app client", id, client, "Entra", hostedFixtureCallback, "openid profile"},
		{"HTTP callback remains metadata only", id, registeredHTTP.ID, "Entra", httpCallback, "openid"},
		{"HTTP broker remains metadata only", id, client, "Keycloak", hostedFixtureCallback, "openid"},
		{"scope not offered by broker", id, client, "Keycloak", hostedFixtureCallback, "openid email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{"response_type": {"code"}, "client_id": {tc.client}, "redirect_uri": {tc.callback},
				"scope": {tc.scopes}, "state": {fixtureHostedState}, "nonce": {fixtureHostedNonce},
				"code_challenge": {hostedDigest(strings.Repeat("v", 43))}, "code_challenge_method": {"S256"}, "identity_provider": {tc.provider}}
			w := hostedServe(b, hostedFixtureRequest(http.MethodGet, hostedPrefix+tc.pool+"/"+hostedAuthorize+"?"+q.Encode(), nil))
			if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
				t.Fatal("an unready hosted configuration produced a browser binding", w.Code)
			}
		})
	}
	if err := b.transition(t.Context(), scope, id, nil, func(_ federationConfig, _ federationRevision, state *hostedState) (bool, error) {
		if len(state.Pending) != 0 || len(state.Codes) != 0 || len(state.Replay) != 0 {
			t.Fatal("unsupported configuration altered the live browser state")
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	scratch, err := NewStore(b.store.db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewHostedBrowser(scratch, scope.Account, hostedFixtureOrigin); err == nil {
		t.Fatal("normal pool store unexpectedly started hosted federation")
	}
	for _, origin := range []string{"https://127.0.0.1:18446", "https://[::1]:18446"} {
		if err := ValidateLocalHostedOrigin(origin); err != nil {
			t.Fatal("canonical HTTPS origin refused", err)
		}
	}
	for _, origin := range []string{"http://127.0.0.1:18446", "https://localhost:18446", "https://127.0.0.1:18446/"} {
		if err := ValidateLocalHostedOrigin(origin); err == nil {
			t.Fatal("noncanonical origin reached the browser profile", origin)
		}
	}
	if _, err := b.ServiceProvider(t.Context(), "not-a-pool-id"); !errors.Is(err, errInvalid) {
		t.Fatal("invalid scoped pool identifier accepted", err)
	}
	if err := b.store.delete(t.Context(), scope, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ServiceProvider(t.Context(), id); !errors.Is(err, errMissing) {
		t.Fatal("deleted pool still has service-provider identity", err)
	}
}

func TestHostedCanonicalConfigurationReopensButBrowserGenerationDoesNot(t *testing.T) {
	ctx := t.Context()
	f := newHostedSignedIDP(t)
	path := filepath.Join(t.TempDir(), "hosted-restart.sqlite")
	open := func() (*core.Store, *Store, *hostedBrowser) {
		t.Helper()
		db, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		s, err := NewStoreWithLocalFederationConfig(db.Database())
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewHostedBrowser(s, "012345678901", hostedFixtureOrigin)
		if err != nil {
			t.Fatal(err)
		}
		return db, s, h.(*hostedBrowser)
	}
	db, s, first := open()
	scope := Scope{"012345678901", "us-east-1"}
	pool, err := s.create(ctx, scope, "reopen hosted browser fixture")
	if err != nil {
		t.Fatal(err)
	}
	pub := federationPublisherFor(t, s, scope, federationAllow)
	provider, err := pub.addProvider(ctx, pool.ID, f.provider("Entra"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := pub.addClient(ctx, pool.ID, federationClientInput{
		Name: "durable relying party", Callbacks: []string{hostedFixtureCallback}, Providers: []string{"Entra"}, Scopes: []string{"openid"},
	})
	if err != nil {
		t.Fatal(err)
	}
	l := beginHostedSignedLogin(t, first, pool.ID, client.ID, "Entra")
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "tester@example.invalid"}
	reply := f.post(t, first, l, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
	cb, _ := assertHostedCompletion(t, l, reply)
	pending := beginHostedSignedLogin(t, first, pool.ID, client.ID, "Entra")
	keys := hostedFixturePublicKeys(t, first, pool.ID)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.transition(context.Background(), scope, pool.ID, nil, func(_ federationConfig, _ federationRevision, _ *hostedState) (bool, error) {
		t.Fatal("closed database attempted a successful browser state transition")
		return false, nil
	}); err == nil {
		t.Fatal("closed database acknowledged a browser transition")
	}
	_, opened, second := open()
	pub = federationPublisherFor(t, opened, scope, federationAllow)
	cfg, _, err := pub.configuration(ctx, pool.ID)
	if err != nil || len(cfg.Providers) != 1 || cfg.Providers[0].RegistrationID != provider.RegistrationID ||
		len(cfg.Clients) != 1 || cfg.Clients[0].ID != client.ID || !slices.Equal(cfg.Clients[0].Callbacks, client.Callbacks) {
		t.Fatal("reopening the native database changed selected trust/application configuration", err)
	}
	for kid := range hostedFixturePublicKeys(t, second, pool.ID) {
		if keys[kid] != nil {
			t.Fatal("a restarted browser unexpectedly shares the previous process's token verification keys")
		}
	}
	stale := hostedServe(second, hostedExchangeRequest(l, cb.Query().Get("code"), ""))
	if stale.Code != http.StatusBadRequest || strings.Contains(stale.Body.String(), "id_token") {
		t.Fatal("the previous process's grant survived restarting its browser", stale.Code)
	}
	if stale := f.post(t, second, pending, "Entra", subject, hostedObjectIDs(fixtureEntraAlice)); stale.Code != http.StatusBadRequest {
		t.Fatal("the old browser correlation survived process generation", stale.Code)
	}
	fresh := beginHostedSignedLogin(t, second, pool.ID, client.ID, "Entra")
	redeemHostedSignedLogin(t, second, fresh, f.post(t, second, fresh, "Entra", subject, hostedObjectIDs(fixtureEntraAlice)))
}

func TestHostedExpiredRowsArePrunedAtTheirExclusiveDeadline(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	live, expired := now.Add(time.Nanosecond), now
	s := hostedState{
		Pending: map[string]hostedPending{"expired": {Until: expired}, "live": {Until: live}},
		Codes:   map[string]hostedGrant{"expired": {Until: expired}, "live": {Until: live}},
		Replay:  map[string]time.Time{"expired": expired, "live": live},
	}
	s.clean(now)
	if len(s.Pending) != 1 || !s.Pending["live"].Until.Equal(live) || len(s.Codes) != 1 ||
		!s.Codes["live"].Until.Equal(live) || len(s.Replay) != 1 || !s.Replay["live"].Equal(live) {
		t.Fatal("pruning an expired login removed or retained the wrong pending/grant/replay record")
	}
}

func TestHostedInvalidClientSubmissionsDoNotConsumeAnActiveLogin(t *testing.T) {
	signer := newHostedSignedIDP(t)
	b, _, _, id, client := newHostedFixture(t, signer.provider("Entra"))
	l := beginHostedSignedLogin(t, b, id, client, "Entra")
	acsPath, tokenPath := hostedPrefix+id+"/"+hostedACS, hostedPrefix+id+"/"+hostedToken
	assertRejected := func(t *testing.T, r *http.Request) {
		t.Helper()
		w := hostedServe(b, r)
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" ||
			w.Header().Get("Set-Cookie") != "" || w.Header().Get("Access-Control-Allow-Origin") != "" ||
			strings.Contains(w.Body.String(), "Continue") || strings.Contains(w.Body.String(), "id_token") {
			t.Fatal("an unsupported request produced an authenticated result", w.Code)
		}
	}
	acs := func(body string, includeCookie bool) *http.Request {
		r := hostedFixtureRequest(http.MethodPost, acsPath, url.Values{"RelayState": {l.relay}, "SAMLResponse": {body}})
		if includeCookie {
			r.AddCookie(l.cookie)
		}
		r.Header.Set("Origin", fixtureHostedIDP)
		return r
	}
	t.Run("browser no longer has its login cookie", func(t *testing.T) {
		assertRejected(t, acs("", false))
	})
	t.Run("directory response was not base64", func(t *testing.T) {
		assertRejected(t, acs("not base64", true))
	})
	t.Run("directory response arrived empty", func(t *testing.T) {
		assertRejected(t, acs("", true))
	})
	t.Run("directory response XML is incomplete", func(t *testing.T) {
		assertRejected(t, acs(base64.StdEncoding.EncodeToString([]byte("<Response")), true))
	})
	t.Run("form submitted from a different local origin", func(t *testing.T) {
		r := acs("", true)
		r.Header.Set("Origin", "https://127.0.0.1:18449")
		assertRejected(t, r)
	})
	subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: "tester@example.invalid"}
	t.Run("directory signing key changed without re-registration", func(t *testing.T) {
		replacement := newHostedSignedIDP(t)
		w := replacement.post(t, b, l, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
		if w.Code != http.StatusBadRequest || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "Continue") {
			t.Fatal("a signed response using the unregistered replacement key continued sign-in", w.Code)
		}
	})

	reply := signer.post(t, b, l, "Entra", subject, hostedObjectIDs(fixtureEntraAlice))
	cb, _ := assertHostedCompletion(t, l, reply)
	freshForm := func() url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "client_id": {l.client}, "code": {cb.Query().Get("code")},
			"redirect_uri": {l.callback}, "code_verifier": {l.verifier}}
	}
	for _, tc := range []struct {
		name, query string
		editForm    func(url.Values)
		editRequest func(*http.Request)
	}{
		{name: "JSON instead of form", editRequest: func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }},
		{name: "form fields in URL", query: "?format=fixture"},
		{name: "unrecognized grant type", editForm: func(v url.Values) { v.Set("grant_type", "client_credentials") }},
		{name: "missing verifier", editForm: func(v url.Values) { v.Del("code_verifier") }},
		{name: "duplicate verifier", editForm: func(v url.Values) { v.Add("code_verifier", l.verifier) }},
		{name: "oversized form", editForm: func(v url.Values) { v.Set("client_id", strings.Repeat("a", 4096)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := freshForm()
			if tc.editForm != nil {
				tc.editForm(form)
			}
			r := hostedFixtureRequest(http.MethodPost, tokenPath+tc.query, form)
			if tc.editRequest != nil {
				tc.editRequest(r)
			}
			assertRejected(t, r)
		})
	}
	redeemHostedSignedLogin(t, b, l, reply)
}

func TestHostedReadOnlyDocumentsRequireTheirCanonicalRoute(t *testing.T) {
	b, _, _, id, _ := newHostedFixture(t)
	prefix := hostedPrefix + id + "/"
	for _, tc := range []struct {
		name, route, origin string
		status              int
	}{
		{"unknown document", prefix + "discovery", "", http.StatusNotFound},
		{"not a pool", hostedPrefix + "unknown/" + hostedMetadata, "", http.StatusNotFound},
		{"metadata rejects extra query", prefix + hostedMetadata + "?format=xml", "", http.StatusBadRequest},
		{"JWKS rejects foreign origin", prefix + hostedJWKS, "https://127.0.0.1:18449", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := hostedFixtureRequest(http.MethodGet, tc.route, nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := hostedServe(b, r)
			if w.Code != tc.status || w.Header().Get("Access-Control-Allow-Origin") != "" ||
				strings.Contains(w.Body.String(), "X509Certificate") || strings.Contains(w.Body.String(), "keys") {
				t.Fatal("unsupported document request exposed a public projection", w.Code)
			}
		})
	}
	hostedFixturePublicKeys(t, b, id)
}

func TestHostedPersistentSlotRejectsIncompleteAndNoncanonicalRecords(t *testing.T) {
	rev := federationRevision{7}
	s, err := readHostedState(nil, "fixture-process", rev)
	if err != nil || s.Revision != rev || s.Pending == nil || s.Codes == nil || s.Replay == nil {
		t.Fatal("empty persistent slot did not produce a fresh state container", err)
	}
	canonical, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte("{"), []byte("{}"), append([]byte(" "), canonical...)} {
		if s, err := readHostedState(raw, "fixture-process", rev); !errors.Is(err, errFederationState) || s.Epoch != "" {
			t.Fatal("damaged stored browser state was mistaken for an empty or authentic slot", err)
		}
	}
	if got, err := readHostedState(canonical, "fixture-process", rev); err != nil || got.Epoch != "fixture-process" || got.Revision != rev {
		t.Fatal("intact canonical state could not be loaded", err)
	}
}
