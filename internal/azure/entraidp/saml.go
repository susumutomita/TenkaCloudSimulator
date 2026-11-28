package entraidp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/crewjam/saml"
	dsig "github.com/russellhaering/goxmldsig"
)

// EmailClaimURI is the SAML attribute a relying party maps to its email
// field. It is not NameID and does not attest to mailbox verification.
const EmailClaimURI = "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress"

const (
	protocolNamespace = "urn:oasis:names:tc:SAML:2.0:protocol"
	oidClaim          = "http://schemas.microsoft.com/identity/claims/objectidentifier"
	tenantClaim       = "http://schemas.microsoft.com/identity/claims/tenantid"
	bearerMethod      = "urn:oasis:names:tc:SAML:2.0:cm:bearer"
)

var (
	entityURN = regexp.MustCompile(`^urn:[A-Za-z][A-Za-z0-9_.-]*:[A-Za-z0-9_.:~-]+$`)
	requestID = regexp.MustCompile(`^[_A-Za-z][A-Za-z0-9_.:-]{0,127}$`)
)

func validEntityID(text string) bool {
	if len(text) == 0 || len(text) > 256 {
		return false
	}
	if entityURN.MatchString(text) {
		// An entity ID is a label and is never resolved.
		return true
	}
	_, err := exactLoopbackURL(text, true)
	return err == nil
}

type singleProvider struct{ metadata *saml.EntityDescriptor }

func (s singleProvider) GetServiceProvider(_ *http.Request, entityID string) (*saml.EntityDescriptor, error) {
	if entityID != s.metadata.EntityID {
		return nil, os.ErrNotExist
	}
	return s.metadata, nil
}

func (f *Fixture) configureSAML(provider ServiceProvider, now time.Time) error {
	der := provider.SigningCertificateDER
	if len(der) == 0 || len(der) > 4096 {
		return errors.New("one bounded SP signing certificate is required")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) ||
		cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return errors.New("a current public SP signing certificate is required")
	}
	key, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok || key.N.BitLen() < 2048 || key.N.BitLen() > 4096 || key.E != 65537 {
		return errors.New("the SP pin must be RSA 2048–4096 with exponent 65537")
	}
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	serial.Add(serial, big.NewInt(1))
	t := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "TenkaCloud synthetic local SAML signer"},
		NotBefore: now.Add(-time.Minute), NotAfter: f.expires.Add(assertionLife), KeyUsage: x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true}
	signed, err := x509.CreateCertificate(rand.Reader, t, t, &signingKey.PublicKey, signingKey)
	if err != nil {
		return err
	}
	signingCert, err := x509.ParseCertificate(signed)
	if err != nil {
		return err
	}
	f.public.SigningCertificateDER = signed
	meta, _ := url.Parse(f.public.MetadataURL)
	sso, _ := url.Parse(f.public.SSOURL)
	flag := true
	desc := &saml.EntityDescriptor{EntityID: provider.EntityID, SPSSODescriptors: []saml.SPSSODescriptor{{
		SSODescriptor: saml.SSODescriptor{RoleDescriptor: saml.RoleDescriptor{
			ProtocolSupportEnumeration: protocolNamespace,
			KeyDescriptors:             []saml.KeyDescriptor{signingDescriptor(cert.Raw)},
		}},
		AuthnRequestsSigned:  &flag,
		WantAssertionsSigned: &flag,
		AssertionConsumerServices: []saml.IndexedEndpoint{{
			Binding: saml.HTTPPostBinding, Location: provider.ACS, IsDefault: &flag,
		}},
	}}}
	f.idp = &saml.IdentityProvider{
		MetadataURL: *meta, SSOURL: *sso, Signer: signingKey, Certificate: signingCert,
		SignatureMethod: dsig.RSASHA256SignatureMethod, RejectSHA1Signatures: true,
		WantAuthnRequestsSigned: true, StrictACSBinding: true, ServiceProviderProvider: singleProvider{desc},
		NameIDFormats: []saml.NameIDFormat{saml.PersistentNameIDFormat},
	}
	// Publish only the implemented binding and the public signing pin, not the
	// library's other useful defaults such as encryption or POST initiation.
	published := f.idp.Metadata()
	published.ValidUntil = f.expires
	published.CacheDuration = 0
	role := &published.IDPSSODescriptors[0]
	role.KeyDescriptors = []saml.KeyDescriptor{signingDescriptor(signed)}
	role.SingleSignOnServices = []saml.Endpoint{{Binding: saml.HTTPRedirectBinding, Location: f.public.SSOURL}}
	role.SingleLogoutServices = nil
	f.metadata, err = xml.Marshal(published)
	return err
}

func signingDescriptor(der []byte) saml.KeyDescriptor {
	return saml.KeyDescriptor{Use: "signing", KeyInfo: saml.KeyInfo{
		X509Data: saml.X509Data{X509Certificates: []saml.X509Certificate{{Data: base64.StdEncoding.EncodeToString(der)}}},
	}}
}

func (f *Fixture) signedRequest(r *http.Request) (*saml.IdpAuthnRequest, error) {
	invalid := errors.New("unsupported local SAML request")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) < 3 || len(q) > 4 || q.Get("SigAlg") != dsig.RSASHA256SignatureMethod ||
		q.Get("SAMLRequest") == "" || q.Get("Signature") == "" {
		return nil, invalid
	}
	for name, values := range q {
		switch name {
		case "SAMLRequest", "SigAlg", "Signature":
			if len(values) != 1 || values[0] == "" {
				return nil, invalid
			}
		case "RelayState":
			if len(values) != 1 || !safeRelay(values[0]) {
				return nil, invalid
			}
		default:
			return nil, invalid
		}
	}
	req, err := saml.NewIdpAuthnRequest(f.idp, r)
	if err != nil || len(req.RequestBuffer) > 12<<10 || req.Validate() != nil {
		return nil, invalid
	}
	a := req.Request
	now := time.Now().UTC()
	if !requestID.MatchString(a.ID) || a.IssueInstant.After(now.Add(3*time.Second)) ||
		!now.Before(a.IssueInstant.Add(AuthnRequestLifetime)) || a.AssertionConsumerServiceURL != f.acs ||
		a.ProtocolBinding != saml.HTTPPostBinding || req.ACSEndpoint == nil || req.ACSEndpoint.Location != f.acs ||
		a.AssertionConsumerServiceIndex != "" || a.AttributeConsumingServiceIndex != "" || a.RequestedAuthnContext != nil ||
		a.Subject != nil || a.Conditions != nil || a.Scoping != nil || a.Signature != nil || a.IsPassive != nil && *a.IsPassive ||
		a.NameIDPolicy == nil || a.NameIDPolicy.Format == nil || *a.NameIDPolicy.Format != string(saml.PersistentNameIDFormat) ||
		a.NameIDPolicy.SPNameQualifier != nil && *a.NameIDPolicy.SPNameQualifier != req.ServiceProviderMetadata.EntityID {
		return nil, invalid
	}
	// Never retain inbound browser HTTP headers, an open body, or raw XML as
	// part of login state. PostBinding needs only the resolved trusted inputs.
	req.HTTPRequest, req.RequestBuffer = nil, nil
	return req, nil
}

func safeRelay(value string) bool {
	return len(value) > 0 && len(value) <= 80 && strings.IndexFunc(value, func(r rune) bool {
		return r < 0x21 || r > 0x7e
	}) == -1
}

func (f *Fixture) response(req *saml.IdpAuthnRequest, now time.Time) (saml.IdpAuthnRequestForm, error) {
	exp := earlier(now.Add(assertionLife), f.expires)
	issuer, audience := f.public.Issuer, req.ServiceProviderMetadata.EntityID
	req.Now = now
	req.Assertion = &saml.Assertion{
		ID: "_" + randomOpaque(), Version: "2.0", IssueInstant: now, Issuer: saml.Issuer{Value: issuer},
		Subject: &saml.Subject{
			NameID: &saml.NameID{Format: string(saml.PersistentNameIDFormat), Value: f.public.ObjectID,
				NameQualifier: issuer, SPNameQualifier: audience},
			SubjectConfirmations: []saml.SubjectConfirmation{{
				Method: bearerMethod, SubjectConfirmationData: &saml.SubjectConfirmationData{
					InResponseTo: req.Request.ID, Recipient: f.acs, NotOnOrAfter: exp,
				},
			}},
		},
		Conditions: &saml.Conditions{
			NotBefore: now.Add(-3 * time.Second), NotOnOrAfter: exp,
			AudienceRestrictions: []saml.AudienceRestriction{{Audience: saml.Audience{Value: audience}}},
		},
		AuthnStatements: []saml.AuthnStatement{{
			AuthnInstant: now, SessionIndex: "_" + randomOpaque(), SessionNotOnOrAfter: &exp,
			AuthnContext: saml.AuthnContext{AuthnContextClassRef: &saml.AuthnContextClassRef{
				Value: "urn:oasis:names:tc:SAML:2.0:ac:classes:unspecified",
			}},
		}},
		AttributeStatements: []saml.AttributeStatement{{Attributes: []saml.Attribute{
			{Name: oidClaim, Values: []saml.AttributeValue{{Value: f.public.ObjectID}}},
			{Name: tenantClaim, Values: []saml.AttributeValue{{Value: f.public.TenantID}}},
			{Name: EmailClaimURI, NameFormat: "urn:oasis:names:tc:SAML:2.0:attrname-format:uri",
				Values: []saml.AttributeValue{{Value: f.public.Email()}}},
		}}},
	}
	// Generic XMLDSig signs both the assertion and the response. No
	// hand-built signed XML or implicit redirect destination.
	return req.PostBinding()
}
