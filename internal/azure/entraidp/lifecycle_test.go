package entraidp

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewjam/saml"
)

func assertRefused(t *testing.T, f *Fixture, r *http.Request, status int) {
	t.Helper()
	w := serve(f, r)
	if w.Code != status || strings.Contains(w.Body.String(), "SAMLResponse") ||
		strings.Contains(w.Body.String(), "One-time synthetic login code") ||
		w.Header().Get("Cache-Control") != "no-store" ||
		w.Header().Get("Referrer-Policy") != "no-referrer" ||
		w.Header().Get("Location") != "" || len(w.Result().Cookies()) != 0 {
		t.Fatalf("expected credential-free local refusal %d, got %d", status, w.Code)
	}
}

func fixtureTestCertificate(t *testing.T, signer crypto.Signer, start, end time.Time, usage x509.KeyUsage) []byte {
	t.Helper()
	template := x509.Certificate{SerialNumber: big.NewInt(34), NotBefore: start, NotAfter: end, KeyUsage: usage}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, signer.Public(), signer)
	if err != nil {
		t.Fatal("could not create synthetic SP certificate", err)
	}
	return der
}

func TestLocalRegistrationValidatesCurrentSigningAuthority(t *testing.T) {
	rsaKey, rsaCert := temporarySP(t)
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"origin has a path", func(c *Config) { c.Origin += "/tenant" }},
		{"ACS path empty", func(c *Config) { c.ServiceProvider.ACS = "https://127.0.0.1:17442" }},
		{"blank entity", func(c *Config) { c.ServiceProvider.EntityID = "" }},
		{"nonlocal entity", func(c *Config) { c.ServiceProvider.EntityID = "https://service.example.invalid/sp" }},
		{"bad DER", func(c *Config) { c.ServiceProvider.SigningCertificateDER = []byte("not a certificate") }},
		{"expired RSA signing key", func(c *Config) {
			c.ServiceProvider.SigningCertificateDER = fixtureTestCertificate(t, rsaKey, now.Add(-2*time.Hour), now.Add(-time.Hour), x509.KeyUsageDigitalSignature)
		}},
		{"certificate unsuitable for signing", func(c *Config) {
			c.ServiceProvider.SigningCertificateDER = fixtureTestCertificate(t, rsaKey, now.Add(-time.Hour), now.Add(time.Hour), x509.KeyUsageKeyEncipherment)
		}},
		{"unsupported valid signing-key type", func(c *Config) {
			c.ServiceProvider.SigningCertificateDER = fixtureTestCertificate(t, edKey, now.Add(-time.Hour), now.Add(time.Hour), x509.KeyUsageDigitalSignature)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Origin: origin, ServiceProvider: ServiceProvider{EntityID: entity, ACS: acs, SigningCertificateDER: rsaCert.Raw}}
			tc.change(&cfg)
			f, code, err := New(cfg)
			if err == nil || f != nil || code != "" {
				if f != nil {
					f.Close()
				}
				t.Fatal("refused registration produced fixture or credential")
			}
		})
	}

	cfg := Config{Origin: origin, ServiceProvider: ServiceProvider{EntityID: acs + "/local-sp", ACS: acs, SigningCertificateDER: rsaCert.Raw}}
	clean, code, err := New(cfg)
	if err != nil || clean == nil || !canonicalOpaque(code) {
		t.Fatal("exact local HTTPS entity and valid enrolled key should start one fixture", err)
	}
	t.Cleanup(clean.Close)
	first := clean.Public()
	pubPin := bytes.Clone(first.SigningCertificateDER)
	first.SigningCertificateDER[0] ^= 255
	if after := clean.Public(); !bytes.Equal(after.SigningCertificateDER, pubPin) {
		t.Fatal("mutating returned public metadata changed the live trust pin")
	}
	metadata := serve(clean, incoming(t, http.MethodGet, first.MetadataURL, nil))
	var md saml.EntityDescriptor
	if metadata.Code != 200 || xml.Unmarshal(metadata.Body.Bytes(), &md) != nil ||
		!strings.Contains(metadata.Body.String(), base64.StdEncoding.EncodeToString(pubPin)) {
		t.Fatal("initial registered signing metadata did not survive caller mutation")
	}
	another, anotherCode, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(another.Close)
	second := another.Public()
	guid := regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
	if code == anotherCode || first.Issuer == second.Issuer || bytes.Equal(pubPin, second.SigningCertificateDER) ||
		first.TenantID == second.TenantID || first.ObjectID == second.ObjectID ||
		!guid.MatchString(second.TenantID) || !guid.MatchString(second.ObjectID) {
		t.Fatal("a new run carried old synthetic tenant, subject, credentials or signing trust")
	}
}

func TestLocalRejectsUnenrolledAndUnsupportedSignedRequests(t *testing.T) {
	f, _, sp := newSPFixture(t)
	otherKey, otherCert := temporarySP(t)
	cases := []struct {
		name   string
		change func(*saml.ServiceProvider)
	}{
		{"another issuer", func(p *saml.ServiceProvider) { p.EntityID = "urn:example:synthetic:other-sp" }},
		{"unenrolled signing key", func(p *saml.ServiceProvider) { p.Key, p.Certificate = otherKey, otherCert }},
		{"another local callback", func(p *saml.ServiceProvider) { p.AcsURL.Path = "/other-registered-app" }},
		{"transient subject policy", func(p *saml.ServiceProvider) { p.AuthnNameIDFormat = saml.TransientNameIDFormat }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			other := *sp
			tc.change(&other)
			_, target := signed(t, f, &other, "other-legitimate-request")
			assertRefused(t, f, incoming(t, http.MethodGet, target.String(), nil), http.StatusBadRequest)
		})
	}
	_, good := signed(t, f, sp, "registered-request")
	assertRefused(t, f, incoming(t, http.MethodGet, good.String()+"&Signature=duplicate", nil), http.StatusBadRequest)
	assertRefused(t, f, incoming(t, http.MethodGet, good.String()+"&login_hint=unsupported", nil), http.StatusBadRequest)
	_ = beginLogin(t, f, good)
}

func TestLocalRequestCarriersFailClosedWithoutSpendingOneShotCode(t *testing.T) {
	f, code, sp := newSPFixture(t)
	meta := f.Public().MetadataURL
	sso := f.Public().SSOURL
	_, signedURL := signed(t, f, sp, "valid-local-flow")
	assertRefused(t, f, incoming(t, http.MethodHead, meta, nil), http.StatusBadRequest)
	assertRefused(t, f, incoming(t, http.MethodGet, meta+"?refresh=true", nil), http.StatusBadRequest)
	assertRefused(t, f, incoming(t, http.MethodPut, sso, nil), http.StatusMethodNotAllowed)
	requestWithBody := incoming(t, http.MethodGet, signedURL.String(), url.Values{"unexpected": {"body"}})
	assertRefused(t, f, requestWithBody, http.StatusBadRequest)
	ticket := beginLogin(t, f, signedURL)

	for _, tc := range []struct {
		name  string
		build func() *http.Request
	}{
		{"format must be a browser form", func() *http.Request {
			r := complete(t, f, ticket, code)
			r.Header.Set("Content-Type", "application/json")
			return r
		}},
		{"exact origin required", func() *http.Request {
			r := complete(t, f, ticket, code)
			r.Header.Del("Origin")
			return r
		}},
		{"malformed flow handle", func() *http.Request {
			r := incoming(t, http.MethodPost, sso, url.Values{"flow": {"short"}, "code": {code}})
			r.AddCookie(ticket.cookie)
			return r
		}},
		{"duplicated flow parameter", func() *http.Request {
			r := incoming(t, http.MethodPost, sso, url.Values{"flow": {ticket.flow, ticket.flow}, "code": {code}})
			r.AddCookie(ticket.cookie)
			return r
		}},
		{"bounded body", func() *http.Request {
			return complete(t, f, ticket, strings.Repeat("x", 1100))
		}},
		{"duplicate browser cookie", func() *http.Request {
			r := complete(t, f, ticket, code)
			r.AddCookie(ticket.cookie)
			return r
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertRefused(t, f, tc.build(), http.StatusBadRequest)
		})
	}
	w := serve(f, complete(t, f, ticket, code))
	cookies := w.Result().Cookies()
	if w.Code != 200 || len(cookies) != 1 || cookies[0].Name != ticket.cookie.Name ||
		cookies[0].MaxAge >= 0 || cookies[0].Value != "" {
		t.Fatal("bad carriers spent the valid code or success left the binding cookie live")
	}
}

func TestLocalPendingCapacityExpiryAndRetirement(t *testing.T) {
	f, code, sp := newSPFixture(t)
	// Model real passage beyond one recorded flow deadline deterministically.
	// The other live entries still consume the real service limit.
	var expired login
	for i := 0; i < maxPending; i++ {
		_, request := signed(t, f, sp, "local-capacity-witness")
		ticket := beginLogin(t, f, request)
		if i == 0 {
			expired = ticket
		}
	}
	_, waiting := signed(t, f, sp, "wait-for-expiry")
	assertRefused(t, f, incoming(t, http.MethodGet, waiting.String(), nil), http.StatusBadRequest)
	f.mu.Lock()
	p := f.pending[expired.flow]
	p.expires = time.Now().Add(-time.Second)
	f.pending[expired.flow] = p
	f.expires = time.Now().Add(45 * time.Second)
	f.mu.Unlock()
	active := beginLogin(t, f, waiting)
	assertRefused(t, f, complete(t, f, expired, code), http.StatusBadRequest)
	for i := 0; i < maxCodeAttempts; i++ {
		assertRefused(t, f, complete(t, f, active, randomOpaque()), http.StatusBadRequest)
	}
	assertRefused(t, f, complete(t, f, active, code), http.StatusBadRequest)
	f.Close()
	f.Close()
	f.mu.Lock()
	stopped := f.closed && len(f.pending) == 0 && bytes.Equal(f.bootstrapDigest[:], make([]byte, len(f.bootstrapDigest)))
	f.mu.Unlock()
	if !stopped {
		t.Fatal("retirement retained live pending flow or a usable code digest")
	}
	assertRefused(t, f, incoming(t, http.MethodGet, f.Public().MetadataURL, nil), http.StatusGone)
	assertRefused(t, f, complete(t, f, active, code), http.StatusGone)
}
