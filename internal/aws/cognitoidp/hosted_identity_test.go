//go:build cognito_hosted

package cognitoidp

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"
	"github.com/golang-jwt/jwt/v5"
	dsig "github.com/russellhaering/goxmldsig"
)

const (
	fixtureEntraAlice  = "02a94351-0532-4f1f-b1bd-d7362471dd30"
	fixtureEntraBob    = "ae64c6ed-b921-462c-98c9-d620c2b3e7cd"
	fixtureHostedIDP   = "https://127.0.0.1:18081"
	fixtureHostedNonce = "fixture-signed-assertion-nonce"
	fixtureHostedState = "fixture-callback-state"
)

type hostedSignedIDP struct {
	sign *dsig.SigningContext
	meta string
}

func newHostedSignedIDP(t *testing.T) hostedSignedIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cert := &x509.Certificate{SerialNumber: big.NewInt(43), NotBefore: now.Add(-time.Hour),
		NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sign, err := dsig.NewSigningContext(key, [][]byte{der})
	if err != nil {
		t.Fatal(err)
	}
	if err := sign.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatal(err)
	}
	sign.Canonicalizer = dsig.MakeC14N10ExclusiveCanonicalizerWithPrefixList("")
	return hostedSignedIDP{sign, strings.ReplaceAll(federationXMLFixture(now, der), "http://127.0.0.1:18081", fixtureHostedIDP)}
}

func (f hostedSignedIDP) provider(name string) federationProviderInput {
	return federationProviderInput{Name: name, MetadataFile: f.meta}
}

type hostedLogin struct {
	auth                        saml.AuthnRequest
	cookie                      *http.Cookie
	relay, verifier, client, id string
}

func beginHostedSignedLogin(t *testing.T, b *hostedBrowser, id, client, provider string) hostedLogin {
	t.Helper()
	verifier := strings.Repeat("v", 43)
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {hostedFixtureCallback},
		"scope": {"openid"}, "state": {fixtureHostedState}, "nonce": {fixtureHostedNonce},
		"code_challenge": {hostedDigest(verifier)}, "code_challenge_method": {"S256"}, "identity_provider": {provider}}
	w := httptest.NewRecorder()
	b.ServeHTTP(w, hostedFixtureRequest(http.MethodGet, hostedPrefix+id+"/"+hostedAuthorize+"?"+q.Encode(), nil))
	u, err := url.Parse(w.Header().Get("Location"))
	cookies := w.Result().Cookies()
	if w.Code != http.StatusFound || err != nil || len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly ||
		hostedOrigin(u) != fixtureHostedIDP || u.Query().Get("Signature") == "" {
		t.Fatalf("valid authorize did not reach its exact signed IdP route: %d", w.Code)
	}
	compressed, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLRequest"))
	if err != nil {
		t.Fatal(err)
	}
	reader := flate.NewReader(bytes.NewReader(compressed))
	defer reader.Close()
	var auth saml.AuthnRequest
	if err := xml.NewDecoder(reader).Decode(&auth); err != nil {
		t.Fatal(err)
	}
	if auth.ID == "" || auth.Issuer == nil || auth.Issuer.Value != "urn:amazon:cognito:sp:"+id ||
		auth.AssertionConsumerServiceURL != b.root(id)+"/"+hostedACS || auth.NameIDPolicy == nil ||
		auth.NameIDPolicy.Format == nil || *auth.NameIDPolicy.Format != string(saml.PersistentNameIDFormat) {
		t.Fatal("AuthnRequest did not ask for a persistent subject at the exact ACS")
	}
	return hostedLogin{auth, cookies[0], u.Query().Get("RelayState"), verifier, client, id}
}

// Synthetic IdP, genuine issuer-pinned RSA-SHA256 signature and actual auth
// redirect/cookie/ACS/PKCE/JWT handlers. The assertion-only envelope matches
// Entra Emulator; the Keycloak case also signs the response. No injected grant.
func (f hostedSignedIDP) post(t *testing.T, b *hostedBrowser, l hostedLogin, provider string, name saml.NameID, attrs ...saml.Attribute) *httptest.ResponseRecorder {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	end := now.Add(2 * time.Minute)
	issuer := saml.Issuer{Value: fixtureEntity}
	a := saml.Assertion{ID: "assertion-" + l.auth.ID, IssueInstant: now, Issuer: issuer,
		Subject: &saml.Subject{NameID: &name, SubjectConfirmations: []saml.SubjectConfirmation{{
			Method: "urn:oasis:names:tc:SAML:2.0:cm:bearer", SubjectConfirmationData: &saml.SubjectConfirmationData{
				Recipient: l.auth.AssertionConsumerServiceURL, InResponseTo: l.auth.ID, NotOnOrAfter: end},
		}}},
		Conditions: &saml.Conditions{NotBefore: now.Add(-time.Minute), NotOnOrAfter: end,
			AudienceRestrictions: []saml.AudienceRestriction{{Audience: saml.Audience{Value: l.auth.Issuer.Value}}}},
		AuthnStatements: []saml.AuthnStatement{{AuthnInstant: now, AuthnContext: saml.AuthnContext{
			AuthnContextClassRef: &saml.AuthnContextClassRef{Value: "urn:oasis:names:tc:SAML:2.0:ac:classes:Password"},
		}}},
	}
	if len(attrs) > 0 {
		a.AttributeStatements = []saml.AttributeStatement{{Attributes: attrs}}
	}
	signed, err := f.sign.SignEnveloped(a.Element())
	if err != nil {
		t.Fatal(err)
	}
	response := (&saml.Response{ID: "response-" + l.auth.ID, Version: "2.0", InResponseTo: l.auth.ID,
		IssueInstant: now, Destination: l.auth.AssertionConsumerServiceURL, Issuer: &issuer,
		Status: saml.Status{StatusCode: saml.StatusCode{Value: saml.StatusSuccess}}}).Element()
	response.AddChild(signed)
	if provider == "Keycloak" {
		response, err = f.sign.SignEnveloped(response)
		if err != nil {
			t.Fatal(err)
		}
	}
	doc := etree.NewDocument()
	doc.SetRoot(response)
	raw, err := doc.WriteToBytes()
	if err != nil {
		t.Fatal(err)
	}
	r := hostedFixtureRequest(http.MethodPost, l.auth.AssertionConsumerServiceURL,
		url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString(raw)}, "RelayState": {l.relay}})
	// The wire request has an origin-form target and same host, not a proxy URL.
	r.URL.Scheme, r.URL.Host = "", ""
	r.Header.Set("Origin", fixtureHostedIDP)
	r.AddCookie(l.cookie)
	w := httptest.NewRecorder()
	b.ServeHTTP(w, r)
	return w
}

func redeemHostedSignedLogin(t *testing.T, b *hostedBrowser, l hostedLogin, assertion *httptest.ResponseRecorder) string {
	t.Helper()
	callback, err := url.Parse(assertion.Header().Get("Location"))
	if assertion.Code != http.StatusFound || err != nil || callback.Host != "127.0.0.1:18447" || callback.Path != "/callback" ||
		callback.Query().Get("state") != fixtureHostedState || !hostedCanonicalNonce(callback.Query().Get("code")) {
		t.Fatalf("valid signed assertion did not reach the registered callback: %d", assertion.Code)
	}
	f := url.Values{"client_id": {l.client}, "grant_type": {"authorization_code"}, "redirect_uri": {hostedFixtureCallback},
		"code": {callback.Query().Get("code")}, "code_verifier": {l.verifier}}
	w := httptest.NewRecorder()
	r := hostedFixtureRequest(http.MethodPost, hostedPrefix+l.id+"/"+hostedToken, f)
	r.Header.Set("Origin", "https://127.0.0.1:18447")
	b.ServeHTTP(w, r)
	var output struct {
		Identity string `json:"id_token"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &output) != nil {
		t.Fatalf("valid signed ACS code could not be redeemed: %d", w.Code)
	}
	m := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(output.Identity, m, func(*jwt.Token) (any, error) { return &b.idKey.PublicKey, nil },
		jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(b.root(l.id)), jwt.WithAudience(l.client), jwt.WithExpirationRequired())
	sub, _ := m["sub"].(string)
	if err != nil || !token.Valid || m["token_use"] != "id" || m["nonce"] != fixtureHostedNonce || !hostedCanonicalNonce(sub) ||
		m[entraObjectIdentifier] != nil || m["name"] != nil || m["email"] != nil {
		t.Fatal("signed token lost issuer, subject or nonce binding, or disclosed raw identity")
	}
	return sub
}

func hostedObjectIDs(values ...string) saml.Attribute {
	a := saml.Attribute{Name: entraObjectIdentifier, NameFormat: samlURIAttributeFormat}
	for _, v := range values {
		a.Values = append(a.Values, saml.AttributeValue{Value: v})
	}
	return a
}

func TestHostedSignedACSStableIdentity(t *testing.T) {
	f := newHostedSignedIDP(t)
	b, p, scope, id, client := newHostedFixture(t, f.provider("Entra"), f.provider("Keycloak"))
	metadata := httptest.NewRecorder()
	b.ServeHTTP(metadata, hostedFixtureRequest(http.MethodGet, hostedPrefix+id+"/"+hostedMetadata, nil))
	var md saml.EntityDescriptor
	if metadata.Code != http.StatusOK || xml.Unmarshal(metadata.Body.Bytes(), &md) != nil || len(md.SPSSODescriptors) != 1 {
		t.Fatal("SP metadata unavailable")
	}
	formats := md.SPSSODescriptors[0].NameIDFormats
	if len(formats) != 1 || formats[0] != saml.PersistentNameIDFormat {
		t.Fatal("SP metadata did not advertise the supported persistent format")
	}
	login := func(provider, name string, qualifiers bool, attrs ...saml.Attribute) string {
		l := beginHostedSignedLogin(t, b, id, client, provider)
		subject := saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: name}
		if qualifiers {
			subject.NameQualifier, subject.SPNameQualifier = fixtureEntity, l.auth.Issuer.Value
		}
		return redeemHostedSignedLogin(t, b, l, f.post(t, b, l, provider, subject, attrs...))
	}
	alice := login("Entra", "original-upn@example.invalid", false, hostedObjectIDs(fixtureEntraAlice))
	if _, err := p.addClient(t.Context(), id, federationClientInput{Name: "unrelated client", Callbacks: []string{hostedFixtureCallback},
		Providers: []string{"Keycloak"}, Scopes: []string{"openid"}}); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewHostedBrowser(b.store, scope.Account, hostedFixtureOrigin)
	if err != nil {
		t.Fatal(err)
	}
	b = restarted.(*hostedBrowser)
	if got := login("Entra", "renamed-upn@example.invalid", true, hostedObjectIDs(fixtureEntraAlice)); got != alice {
		t.Fatal("Entra sub changed after UPN, client configuration, or browser process change")
	}
	if got := login("Entra", "original-upn@example.invalid", false, hostedObjectIDs(fixtureEntraBob)); got == alice {
		t.Fatal("different directory object inherited an identity for the same UPN")
	}
	// Both registrations deliberately share issuer, pin, endpoint and opaque
	// subject bytes. The chosen registration, not the issuer alone, scopes sub.
	keycloak := login("Keycloak", fixtureEntraAlice, true)
	if keycloak == alice || login("Keycloak", fixtureEntraAlice, false) != keycloak {
		t.Fatal("a separate provider collapsed into Entra, or Keycloak sub was unstable")
	}
	if err := p.removeClient(t.Context(), id, client); err != nil {
		t.Fatal(err)
	}
	if err := p.removeProvider(t.Context(), id, "Entra"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.addProvider(t.Context(), id, f.provider("Entra")); err != nil {
		t.Fatal(err)
	}
	newClient, err := p.addClient(t.Context(), id, federationClientInput{Name: "fresh provider client", Callbacks: []string{hostedFixtureCallback},
		Providers: []string{"Entra"}, Scopes: []string{"openid"}})
	if err != nil {
		t.Fatal(err)
	}
	client = newClient.ID
	if got := login("Entra", "original-upn@example.invalid", false, hostedObjectIDs(fixtureEntraAlice)); got == alice {
		t.Fatal("deleted and recreated registration silently inherited a previous sub")
	}
}

func TestHostedSignedACSRejectsUnsupportedIdentity(t *testing.T) {
	f := newHostedSignedIDP(t)
	b, _, scope, id, client := newHostedFixture(t, f.provider("Entra"), f.provider("Keycloak"))
	const persistent = string(saml.PersistentNameIDFormat)
	withName := func(format, value string) saml.NameID { return saml.NameID{Format: format, Value: value} }
	name := withName(persistent, "fixture@example.invalid")
	alice := hostedObjectIDs(fixtureEntraAlice)
	for _, tc := range []struct {
		caseName, provider string
		name               saml.NameID
		attrs              []saml.Attribute
	}{
		{"missing Entra signed object ID", "Entra", name, nil},
		{"invalid GUID", "Entra", name, []saml.Attribute{hostedObjectIDs("not-a-directory-object")}},
		{"empty directory object", "Entra", name, []saml.Attribute{hostedObjectIDs("00000000-0000-0000-0000-000000000000")}},
		{"repeated object claim", "Entra", name, []saml.Attribute{alice, alice}},
		{"multi-valued object claim", "Entra", name, []saml.Attribute{hostedObjectIDs(fixtureEntraAlice, fixtureEntraBob)}},
		{"transient Keycloak ID", "Keycloak", withName(string(saml.TransientNameIDFormat), "once"), nil},
		{"missing persistent format", "Keycloak", withName("", "opaque"), nil},
		{"non-opaque email format", "Keycloak", withName("urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress", "fixture@example.invalid"), nil},
		{"overlong persistent ID", "Keycloak", withName(persistent, strings.Repeat("k", 257)), nil},
		{"unregistered NameQualifier", "Keycloak", saml.NameID{Format: persistent, Value: "pairwise", NameQualifier: "urn:another-idp"}, nil},
		{"unregistered SPNameQualifier", "Keycloak", saml.NameID{Format: persistent, Value: "pairwise", SPNameQualifier: "urn:another-sp"}, nil},
		{"alternate SP identifier unsupported", "Keycloak", saml.NameID{Format: persistent, Value: "pairwise", SPProvidedID: "another-identifier"}, nil},
	} {
		t.Run(tc.caseName, func(t *testing.T) {
			l := beginHostedSignedLogin(t, b, id, client, tc.provider)
			w := f.post(t, b, l, tc.provider, tc.name, tc.attrs...)
			if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unsupported signed identity produced a callback or changed browser state: %d", w.Code)
			}
		})
	}
	if err := b.transition(t.Context(), scope, id, nil, func(_ federationConfig, _ federationRevision, s *hostedState) (bool, error) {
		if len(s.Codes) != 0 || len(s.Replay) != 0 {
			t.Fatal("refused signed identities wrote issuance state")
		}
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHostedLegacyRegistrationRemainsManagementOnly(t *testing.T) {
	b, p, _, id, client := newHostedFixture(t)
	// A previous metadata-only database has the same canonical projection
	// without a registration ID. No browser action may upgrade it implicitly.
	if err := p.change(t.Context(), createFederationProvider, id, func(c *federationConfig) error {
		c.Providers[0].RegistrationID = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c, _, err := p.configuration(t.Context(), id); err != nil || len(c.Providers) != 1 || c.Providers[0].RegistrationID != "" {
		t.Fatal("old metadata projection became unavailable to its management caller", err)
	}
	q := url.Values{"response_type": {"code"}, "client_id": {client}, "redirect_uri": {hostedFixtureCallback},
		"scope": {"openid"}, "state": {fixtureHostedState}, "code_challenge": {hostedDigest(strings.Repeat("v", 43))},
		"code_challenge_method": {"S256"}, "identity_provider": {"Entra"}}
	w := httptest.NewRecorder()
	b.ServeHTTP(w, hostedFixtureRequest(http.MethodGet, hostedPrefix+id+"/"+hostedAuthorize+"?"+q.Encode(), nil))
	if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("metadata-only registration silently gained browser authority", w.Code)
	}
}
