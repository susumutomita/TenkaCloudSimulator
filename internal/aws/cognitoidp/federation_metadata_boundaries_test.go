package cognitoidp

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestFederationMetadataRequiresUnambiguousLocalSigningSelection(t *testing.T) {
	at := time.Now().UTC().Truncate(time.Second)
	raw := federationXMLFixture(at, federationCertFixture(t, at, 45))
	lines := strings.Split(raw, "\n")
	if len(lines) < 7 || !strings.Contains(lines[3], "<md:KeyDescriptor") || !strings.Contains(lines[4], federationSAMLRedirect) || !strings.Contains(lines[5], federationSAMLPost) {
		t.Fatal("expected synthetic IdP role and two bindings")
	}
	signing, redirect, post := lines[3], lines[4], lines[5]
	withEntityID := strings.Replace(raw, "<md:EntityDescriptor ", "<md:EntityDescriptor ID=\"fixture\" ", 1)
	compile := func(input string) (federationProvider, error) {
		return compileFederationProvider(federationProviderInput{Name: "Keycloak", MetadataFile: input}, at)
	}
	if got, err := compile(withEntityID); err != nil || got.EntityID != fixtureEntity || got.RedirectURL != fixtureIdPURL {
		t.Fatal("valid instance identifier altered the signing selection", err)
	}
	for _, tc := range []struct{ name, metadata string }{
		{"multiple signing descriptors", strings.Replace(raw, signing, signing+"\n"+signing, 1)},
		{"missing SSO endpoints", strings.Replace(strings.Replace(raw, redirect, "", 1), post, "", 1)},
		{"multiple redirect bindings", strings.Replace(raw, post, redirect, 1)},
		{"POST without redirect", strings.Replace(raw, redirect, "", 1)},
		{"malformed closing tag", strings.Replace(raw, "</md:EntityDescriptor>", "</md:Wrong>", 1)},
		{"document directive", strings.Replace(raw, "<md:EntityDescriptor", "<!DOCTYPE EntityDescriptor>\n<md:EntityDescriptor", 1)},
		{"unexpected processing instruction", strings.Replace(raw, "<md:IDPSSODescriptor", "<?fixture note?>\n<md:IDPSSODescriptor", 1)},
		{"namespaced entity selector", strings.Replace(raw, " entityID=", " md:entityID=", 1)},
		{"duplicate document identifier", strings.Replace(withEntityID, "<md:IDPSSODescriptor ", "<md:IDPSSODescriptor ID=\"fixture\" ", 1)},
		{"unsupported hash declaration", strings.Replace(raw, "<md:Extensions>", "<md:Extensions Algorithm=\"urn:test:sha1\">", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := compile(tc.metadata); !errors.Is(err, errInvalid) || got.Name != "" {
				t.Fatal("ambiguous or unsupported metadata exposed a provider", err)
			}
		})
	}

	issuer, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	edPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		pub  any
		use  x509.KeyUsage
	}{
		{"encryption-only certificate", &issuer.PublicKey, x509.KeyUsageKeyEncipherment},
		{"unsupported public key type", edPublic, x509.KeyUsageDigitalSignature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := &x509.Certificate{SerialNumber: big.NewInt(46), NotBefore: at.Add(-time.Hour), NotAfter: at.Add(time.Hour), KeyUsage: tc.use}
			der, err := x509.CreateCertificate(rand.Reader, meta, meta, tc.pub, issuer)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := compile(federationXMLFixture(at, der)); !errors.Is(err, errInvalid) || got.Name != "" {
				t.Fatal("unsupported signing certificate exposed a provider", err)
			}
		})
	}
	bad := federationProviderInput{Name: "Keycloak", MetadataFile: raw, Mapping: map[string]string{
		"email": "mail", "name": "name", "given_name": "given", "family_name": "family", "email_verified": "email_verified", "unsupported": "fixture",
	}}
	if _, err := compileFederationProvider(bad, at); !errors.Is(err, errInvalid) {
		t.Fatal("oversize mapping accepted", err)
	}
}
