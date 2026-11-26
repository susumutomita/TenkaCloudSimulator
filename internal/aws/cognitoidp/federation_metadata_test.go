package cognitoidp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFederationMetadataBoundedTypedProjection(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	der := federationCertFixture(t, at, 223)
	good := federationXMLFixture(at, der)
	accept := func(xml string) (federationProvider, error) {
		return compileFederationProvider(federationProviderInput{"Keycloak", xml, map[string]string{"email": "mail", "email_verified": "email_verified"}}, at)
	}
	p, err := accept(good)
	if err != nil || p.EntityID != fixtureEntity || p.RedirectURL != fixtureIdPURL || !bytes.Equal(p.SigningCertDER, der) ||
		!p.MetadataExpires.Equal(at.Add(40*time.Minute)) {
		t.Fatal("valid local projection failed", err)
	}
	if !p.currentPin(at.Add(40*time.Minute-time.Second)) || p.currentPin(at.Add(40*time.Minute)) {
		t.Fatal("metadata expiry was not the earliest exclusive bound")
	}
	roles := strings.Split(good, "  <md:IDPSSODescriptor")
	oneRole := "  <md:IDPSSODescriptor" + strings.Split(roles[1], "  <md:Extensions")[0]
	for name, metadata := range map[string]string{
		"body cap":                     strings.Repeat(" ", maxFederationXML) + good,
		"remote reference refused":     "https://idp.example.invalid/metadata",
		"metadata issuer blank":        strings.Replace(good, fixtureEntity, "", 1),
		"invalid XML namespace":        strings.Replace(good, "urn:oasis:names:tc:SAML:2.0:metadata", "urn:test:other", 1),
		"multiple roles":               strings.Replace(good, oneRole, oneRole+oneRole, 1),
		"multiple roots":               good + "<extra/>",
		"binary input":                 good + string([]byte{0xff}),
		"fixture not yet valid":        federationXMLFixture(at, federationCertFixture(t, at.Add(2*time.Hour), 221)),
		"expired pin":                  federationXMLFixture(at, federationCertFixture(t, at.Add(-2*time.Hour), 222)),
		"expired metadata":             strings.Replace(good, at.Add(40*time.Minute).Format(time.RFC3339), at.Format(time.RFC3339), 1),
		"explicit zero expiry":         strings.Replace(good, at.Add(40*time.Minute).Format(time.RFC3339), "0001-01-01T00:00:00Z", 1),
		"wrong declared protocol":      strings.Replace(good, federationSAMLProtocol, "urn:test:other", 1),
		"duplicate signing keys":       strings.Replace(good, "use=\"signing\"", "use=\"signing\" use=\"signing\"", 1),
		"encryption not pin":           strings.Replace(good, "use=\"signing\"", "use=\"encryption\"", 1),
		"bad certificate":              strings.Replace(good, "<ds:X509Certificate>", "<ds:X509Certificate>@!", 1),
		"nested certificate":           strings.Replace(good, "</ds:X509Certificate>", "<child/></ds:X509Certificate>", 1),
		"redirect to different broker": strings.Replace(good, "18081/post", "18082/post", 1),
		"unrecognized binding":         strings.Replace(good, federationSAMLPost, "urn:test:other", 1),
		"only POST":                    strings.Replace(good, federationSAMLRedirect, federationSAMLPost, 1),
		"dynamic redirect query":       strings.Replace(good, fixtureIdPURL, fixtureIdPURL+"?x=y", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := accept(metadata); !errors.Is(err, errInvalid) || got.Name != "" {
				t.Fatal("bad source produced a pin", err)
			}
		})
	}
	for _, invalid := range []map[string]string{
		{"groups": "test"},
		{"email_verified": "email_verified"},
		{"email_verified": "boolean", "email": "mail"},
		{"email": "identical", "name": "identical"},
		{"email": strings.Repeat("a", 257)},
	} {
		if _, err := compileFederationProvider(federationProviderInput{"Entra", good, invalid}, at); !errors.Is(err, errInvalid) {
			t.Fatal("invalid optional mapping was admitted")
		}
	}
}

func TestFederationCallbackSourceBoundsAndLocalIdentity(t *testing.T) {
	for _, good := range []string{fixtureCallback, "http://[::1]:8080/oidc/callback", "http://127.0.0.2:15555/callback/"} {
		if _, err := localFederationURL(good, true); err != nil {
			t.Fatal("local callback refused", err)
		}
	}
	for _, unacceptable := range []string{
		"not a URL", "http://localhost:8080/callback", "http://192.0.2.8:8080/callback",
		"http://127.0.0.1/callback", "http://127.0.0.1:65536/callback", "http://127.0.0.1:0/callback",
		"http://127.0.0.1:08080/callback", "https://127.0.0.1:8080/callback",
		"http://127.0.0.1:8080/callback#section", "http://127.0.0.1:8080/callback?state=fixture",
		"http://127.0.0.1:8080/callback?error_description=fixture",
		"http://127.0.0.1:8080/callback?app=one&app=two",
		"http://127.0.0.1:8080/callback?app=%ZZ",
		"http://127.0.0.1:8080/a/../callback",
		"http://127.0.0.1:8080/%63allback", "http://127.0.0.1:8080/" + strings.Repeat("a", maxFederationURL),
	} {
		if _, err := localFederationURL(unacceptable, true); !errors.Is(err, errInvalid) {
			t.Fatal("invalid callback source not rejected", err)
		}
	}
	empty := federationInput()
	empty.Scopes = []string{"email"}
	unbounded := federationInput()
	unbounded.Callbacks = make([]string, maxFederationCallbacks+1)
	repeat := federationInput()
	repeat.Scopes = []string{"openid", "openid"}
	for _, value := range []federationClientInput{empty, unbounded, repeat} {
		if value.valid() {
			t.Fatal("invalid app client shape accepted")
		}
	}
}

func TestFederationMetadataRejectsUnusableRSAPublicPins(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	issuer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(41), NotBefore: at.Add(-time.Hour),
		NotAfter: at.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature}
	type shape struct {
		name  string
		n     *big.Int
		e     int
		valid bool
	}
	probes := []shape{
		{"ordinary pin", ordinary.PublicKey.N, ordinary.PublicKey.E, true},
		{"one is not an exponent", ordinary.PublicKey.N, 1, false},
		{"even exponent", ordinary.PublicKey.N, 2, false},
		{"even modulus", new(big.Int).SetBit(ordinary.PublicKey.N, 0, 0), ordinary.PublicKey.E, false},
	}
	if strconv.IntSize > 32 {
		tooLarge := int64(1<<31 + 1)
		probes = append(probes, shape{"exponent exceeds signed 32-bit range", ordinary.PublicKey.N, int(tooLarge), false})
	}
	for _, fixture := range probes {
		t.Run(fixture.name, func(t *testing.T) {
			// Configure a synthetic certificate only. No private key associated
			// with the malformed public shape or signed SAML assertion is used.
			der, err := x509.CreateCertificate(rand.Reader, cert, cert, &rsa.PublicKey{N: fixture.n, E: fixture.e}, issuer)
			if err != nil {
				t.Fatal("fixture encoding", err)
			}
			if _, err := x509.ParseCertificate(der); err != nil {
				t.Fatal("fixture did not reach public-key shape validation", err)
			}
			p, err := compileFederationProvider(federationProviderInput{
				Name: "Keycloak", MetadataFile: federationXMLFixture(at, der)}, at)
			if fixture.valid {
				if err != nil || !p.currentPin(at) {
					t.Fatal("valid RSA signing pin rejected", err)
				}
			} else if !errors.Is(err, errInvalid) || p.Name != "" || (federationProvider{SigningCertDER: der}).currentPin(at) {
				t.Fatal("unusable RSA public pin admitted", err)
			}
		})
	}
}
