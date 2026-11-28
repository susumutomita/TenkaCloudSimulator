package entraidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
)

const (
	origin = "https://127.0.0.1:17441"
	acs    = "https://127.0.0.1:17442/fixture/sp/acs"
	entity = "urn:example:synthetic:test-sp"
)

func temporarySP(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	c := &x509.Certificate{
		SerialNumber: big.NewInt(2048), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
	}
	data, err := x509.CreateCertificate(rand.Reader, c, c, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(data)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

func incoming(t *testing.T, method, target string, body url.Values) *http.Request {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, u.RequestURI(), strings.NewReader(body.Encode()))
	r.Host, r.RemoteAddr = u.Host, "127.0.0.1:17444"
	r.TLS = &tls.ConnectionState{}
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
	}
	return r
}

func serve(f *Fixture, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	f.ServeHTTP(w, r)
	return w
}

func newSPFixture(t *testing.T) (*Fixture, string, *saml.ServiceProvider) {
	t.Helper()
	key, cert := temporarySP(t)
	f, code, err := New(Config{Origin: origin, ServiceProvider: ServiceProvider{EntityID: entity, ACS: acs, SigningCertificateDER: cert.Raw}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Close)
	response := serve(f, incoming(t, http.MethodGet, f.Public().MetadataURL, nil))
	if response.Code != 200 || response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("metadata status or referrer policy", response.Code)
	}
	var md saml.EntityDescriptor
	if err := xml.Unmarshal(response.Body.Bytes(), &md); err != nil {
		t.Fatal(err)
	}
	p := f.Public()
	if md.EntityID != p.Issuer || len(md.IDPSSODescriptors) != 1 {
		t.Fatal("metadata does not identify the fixture")
	}
	role := md.IDPSSODescriptors[0]
	if len(role.SingleSignOnServices) != 1 || role.SingleSignOnServices[0].Location != p.SSOURL ||
		role.SingleSignOnServices[0].Binding != saml.HTTPRedirectBinding ||
		len(role.KeyDescriptors) != 1 || role.KeyDescriptors[0].Use != "signing" ||
		role.KeyDescriptors[0].KeyInfo.X509Data.X509Certificates[0].Data != base64.StdEncoding.EncodeToString(p.SigningCertificateDER) ||
		strings.Contains(response.Body.String(), code) || strings.Contains(response.Body.String(), p.Email()) {
		t.Fatal("unavailable features or secret in registration metadata")
	}
	a, _ := url.Parse(acs)
	sp := &saml.ServiceProvider{Key: key, Certificate: cert, EntityID: entity, AcsURL: *a, IDPMetadata: &md,
		SignatureMethod: dsig.RSASHA256SignatureMethod, AuthnNameIDFormat: saml.PersistentNameIDFormat}
	return f, code, sp
}

func signed(t *testing.T, f *Fixture, sp *saml.ServiceProvider, relay string) (*saml.AuthnRequest, *url.URL) {
	t.Helper()
	a, err := sp.MakeAuthenticationRequest(f.Public().SSOURL, saml.HTTPRedirectBinding, saml.HTTPPostBinding)
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.Redirect(relay, sp)
	if err != nil {
		t.Fatal(err)
	}
	return a, target
}

func hidden(t *testing.T, response string, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(response)
	if m == nil {
		t.Fatal("missing hidden field", name)
	}
	return html.UnescapeString(m[1])
}

type login struct {
	flow   string
	cookie *http.Cookie
}

func beginLogin(t *testing.T, f *Fixture, target *url.URL) login {
	t.Helper()
	out := serve(f, incoming(t, http.MethodGet, target.String(), nil))
	if out.Code != 200 || !strings.Contains(out.Body.String(), "One-time synthetic login code") {
		t.Fatalf("signed fresh login refused: %d %s", out.Code, out.Body.String())
	}
	if out.Header().Get("Referrer-Policy") != "origin" {
		t.Fatal("login form must preserve same-origin POST Origin without exposing its query")
	}
	cookies := out.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].Path != "/" ||
		cookies[0].SameSite != http.SameSiteStrictMode || !strings.HasPrefix(cookies[0].Name, "__Host-") {
		t.Fatal("missing private browser binding")
	}
	return login{flow: hidden(t, out.Body.String(), "flow"), cookie: cookies[0]}
}

func complete(t *testing.T, f *Fixture, ticket login, code string) *http.Request {
	t.Helper()
	r := incoming(t, http.MethodPost, f.Public().SSOURL, url.Values{"flow": {ticket.flow}, "code": {code}})
	r.AddCookie(ticket.cookie)
	return r
}

func TestLocalSAMLResponseAndClaimScope(t *testing.T) {
	f, code, sp := newSPFixture(t)
	const relay = "isolated-test-request"
	request, target := signed(t, f, sp, relay)
	ticket := beginLogin(t, f, target)
	if got := serve(f, incoming(t, http.MethodGet, target.String(), nil)).Code; got != 400 {
		t.Fatal("same request admitted twice", got)
	}
	wrongBrowser := *ticket.cookie
	wrongBrowser.Value = randomOpaque()
	wrong := login{flow: ticket.flow, cookie: &wrongBrowser}
	if got := serve(f, complete(t, f, wrong, code)).Code; got != 400 {
		t.Fatal("foreign browser consumed login", got)
	}
	badOrigin := complete(t, f, ticket, code)
	badOrigin.Header.Set("Origin", "https://127.0.0.1:17442")
	if got := serve(f, badOrigin); got.Code != 403 || got.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("foreign origin was accepted or refusal leaked a referrer", got.Code)
	}
	badOrigin.Header.Set("Origin", "null")
	if got := serve(f, badOrigin).Code; got != 403 {
		t.Fatal("opaque origin was accepted", got)
	}
	if got := serve(f, complete(t, f, ticket, randomOpaque())).Code; got != 400 {
		t.Fatal("unbound code accepted", got)
	}
	// The sign-in form is not an account or claims provisioning endpoint.
	extra := incoming(t, http.MethodPost, f.Public().SSOURL, url.Values{
		"flow": {ticket.flow}, "code": {code}, "email": {"other@fixture.invalid"},
	})
	extra.AddCookie(ticket.cookie)
	if out := serve(f, extra); out.Code != 400 || strings.Contains(out.Body.String(), "SAMLResponse") {
		t.Fatal("login form with an unknown field must not issue an assertion", out.Code)
	}
	p := f.Public()
	descriptorCopy := p
	descriptorCopy.Directory.email = "changed@fixture.invalid"
	if descriptorCopy.Email() == p.Email() || f.Public().Email() != p.Email() {
		t.Fatal("mutating a descriptor copy changed the provisioned identity")
	}
	w := serve(f, complete(t, f, ticket, code))
	if w.Code != 200 || strings.Contains(w.Body.String(), code) || w.Header().Get("Referrer-Policy") != "origin" {
		t.Fatal("approved login not accepted or code echoed", w.Code)
	}
	payload := w.Body.String()
	if !strings.Contains(payload, `action="`+acs+`"`) || hidden(t, payload, "RelayState") != relay ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "form-action https://127.0.0.1:17442") {
		t.Fatal("wrong fixed destination or pending metadata")
	}
	posted := incoming(t, http.MethodPost, acs, url.Values{"SAMLResponse": {hidden(t, payload, "SAMLResponse")}, "RelayState": {relay}})
	// The generic SP parses an already-decoded form and uses the actual
	// public HTTPS URL, as an embedding HTTP handler would supply.
	if err := posted.ParseForm(); err != nil {
		t.Fatal(err)
	}
	posted.URL, _ = url.Parse(acs)
	parsed, err := sp.ParseResponse(posted, []string{request.ID})
	if err != nil {
		var detail *saml.InvalidResponseError
		if errors.As(err, &detail) {
			t.Fatal("the synthetic SP refused the response", detail.PrivateErr)
		}
		t.Fatal("generic SAML SP did not verify the actual response", err)
	}
	if parsed.Issuer.Value != p.Issuer || parsed.Subject.NameID.Value != p.ObjectID ||
		parsed.Subject.NameID.NameQualifier != p.Issuer || parsed.Subject.NameID.SPNameQualifier != entity ||
		parsed.Subject.NameID.Format != string(saml.PersistentNameIDFormat) ||
		len(parsed.Conditions.AudienceRestrictions) != 1 || parsed.Conditions.AudienceRestrictions[0].Audience.Value != entity ||
		parsed.Conditions.NotOnOrAfter.Sub(parsed.AuthnStatements[0].AuthnInstant) > assertionLife ||
		len(parsed.AttributeStatements) != 1 {
		t.Fatal("verified assertion differs from its limited scope")
	}
	attrs := parsed.AttributeStatements[0].Attributes
	if len(attrs) != 3 || attrs[0].Name != oidClaim || len(attrs[0].Values) != 1 || attrs[0].Values[0].Value != p.ObjectID ||
		attrs[1].Name != tenantClaim || len(attrs[1].Values) != 1 || attrs[1].Values[0].Value != p.TenantID ||
		attrs[2].Name != EmailClaimURI || attrs[2].NameFormat != "urn:oasis:names:tc:SAML:2.0:attrname-format:uri" ||
		len(attrs[2].Values) != 1 || attrs[2].Values[0].Value != p.Email() {
		t.Fatal("expected only signed object ID, tenant ID and synthetic email")
	}
	if got := serve(f, complete(t, f, ticket, code)).Code; got != 400 {
		t.Fatal("one-time login allowed replay", got)
	}
	_, next := signed(t, f, sp, relay)
	if got := serve(f, incoming(t, http.MethodGet, next.String(), nil)).Code; got != 400 {
		t.Fatal("used synthetic account could start another login", got)
	}
}

func TestExactAdmissionAndExpiredFlow(t *testing.T) {
	f, code, sp := newSPFixture(t)
	_, link := signed(t, f, sp, "test")
	for _, tt := range []struct {
		label  string
		edit   func(*http.Request)
		status int
	}{
		{"cleartext", func(r *http.Request) { r.TLS = nil }, 403},
		{"foreign host", func(r *http.Request) { r.Host = "127.0.0.1:8000" }, 403},
		{"forwarded", func(r *http.Request) { r.Header.Set("Forwarded", "for=127.0.0.1") }, 403},
		{"foreign peer", func(r *http.Request) { r.RemoteAddr = "192.0.2.10:9090" }, 403},
		{"unknown path", func(r *http.Request) { r.URL.Path = "/unknown" }, 404},
		{"duplicate relay", func(r *http.Request) { r.URL.RawQuery += "&RelayState=test" }, 400},
		{"unsigned", func(r *http.Request) { q := r.URL.Query(); q.Del("Signature"); r.URL.RawQuery = q.Encode() }, 400},
		{"no signing key", func(r *http.Request) { r.URL.RawQuery = "RelayState=test" }, 400},
	} {
		t.Run(tt.label, func(t *testing.T) {
			r := incoming(t, http.MethodGet, link.String(), nil)
			tt.edit(r)
			if w := serve(f, r); w.Code != tt.status || strings.Contains(w.Body.String(), "SAMLResponse") {
				t.Fatal("unexpected authorization or output", w.Code)
			}
		})
	}
	ticket := beginLogin(t, f, link)
	f.mu.Lock()
	item := f.pending[ticket.flow]
	item.expires = time.Now().Add(-time.Millisecond)
	f.pending[ticket.flow] = item
	f.mu.Unlock()
	if got := serve(f, complete(t, f, ticket, code)).Code; got != 400 {
		t.Fatal("expired code accepted", got)
	}
	f.Close()
	if got := serve(f, incoming(t, http.MethodGet, f.Public().MetadataURL, nil)).Code; got != 410 {
		t.Fatal("closed fixture still mounted", got)
	}
}

func TestConcurrentOneShot(t *testing.T) {
	f, code, sp := newSPFixture(t)
	_, a := signed(t, f, sp, "local-a")
	_, b := signed(t, f, sp, "local-b")
	first := complete(t, f, beginLogin(t, f, a), code)
	second := complete(t, f, beginLogin(t, f, b), code)
	status := make(chan int, 2)
	for _, r := range []*http.Request{first, second} {
		go func() { status <- serve(f, r).Code }()
	}
	x, y := <-status, <-status
	if !((x == 200 && y == 400) || (x == 400 && y == 200)) {
		t.Fatalf("one-time result (%d,%d)", x, y)
	}
}

func TestRegistrationUsesExactLocalBoundaries(t *testing.T) {
	_, cert := temporarySP(t)
	for _, change := range []func(*Config){
		func(c *Config) { c.Origin = "http://127.0.0.1:17441" },
		func(c *Config) { c.Origin = "https://localhost:17441" },
		func(c *Config) { c.Origin = "https://127.0.0.1:017441" },
		func(c *Config) { c.ServiceProvider.ACS = origin + "/callback" },
		func(c *Config) { c.ServiceProvider.ACS = "https://127.0.0.1:17442/a/../callback" },
		func(c *Config) { c.ServiceProvider.ACS = "https://127.0.0.1:17442/callback?registration=unstable" },
		func(c *Config) { c.ServiceProvider.SigningCertificateDER = nil },
	} {
		conf := Config{Origin: origin, ServiceProvider: ServiceProvider{EntityID: entity, ACS: acs, SigningCertificateDER: cert.Raw}}
		change(&conf)
		if f, code, err := New(conf); err == nil || f != nil || code != "" {
			t.Fatal("unapproved registration was provisioned")
		}
	}
}
