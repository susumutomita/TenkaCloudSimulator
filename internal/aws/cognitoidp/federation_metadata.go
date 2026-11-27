package cognitoidp

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	federationSAMLProtocol = "urn:oasis:names:tc:SAML:2.0:protocol"
	federationSAMLRedirect = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
	federationSAMLPost     = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST"
	maxFederationXML       = 32 << 10
	maxFederationCert      = 8 << 10
)

type samlMetadataEntity struct {
	XMLName xml.Name      `xml:"urn:oasis:names:tc:SAML:2.0:metadata EntityDescriptor"`
	ID      string        `xml:"entityID,attr"`
	Expires *time.Time    `xml:"validUntil,attr"`
	Roles   []samlIdPRole `xml:"urn:oasis:names:tc:SAML:2.0:metadata IDPSSODescriptor"`
}

type samlIdPRole struct {
	Expires   *time.Time        `xml:"validUntil,attr"`
	Protocols string            `xml:"protocolSupportEnumeration,attr"`
	Keys      []samlMetadataKey `xml:"urn:oasis:names:tc:SAML:2.0:metadata KeyDescriptor"`
	Endpoints []samlEndpoint    `xml:"urn:oasis:names:tc:SAML:2.0:metadata SingleSignOnService"`
}

type samlMetadataKey struct {
	Use      string        `xml:"use,attr"`
	KeyInfos []samlKeyInfo `xml:"http://www.w3.org/2000/09/xmldsig# KeyInfo"`
}

type samlKeyInfo struct {
	CertificateData []samlCertData `xml:"http://www.w3.org/2000/09/xmldsig# X509Data"`
}

type samlCertData struct {
	Certificates []string `xml:"http://www.w3.org/2000/09/xmldsig# X509Certificate"`
}

type samlEndpoint struct {
	Binding  string `xml:"Binding,attr"`
	Location string `xml:"Location,attr"`
	Response string `xml:"ResponseLocation,attr"`
}

type federationProviderInput struct {
	Name, MetadataFile string
	Mapping            map[string]string
}

// The source is inline, operator-managed identity-provider metadata. Keep
// only the pinned public trust inputs, not the original document. This is
// neither verification of metadata signatures nor a SAML assertion parser.
func compileFederationProvider(in federationProviderInput, now time.Time) (federationProvider, error) {
	if !providerName(in.Name) || len(in.Mapping) > maxFederationMapping {
		return federationProvider{}, errInvalid
	}
	// Bound the copy first, then validate the immutable snapshot that the
	// publisher will commit even if its policy callback changes the input.
	mapping := maps.Clone(in.Mapping)
	if !validFederationMapping(mapping) || !boundedMetadataXML(in.MetadataFile) {
		return federationProvider{}, errInvalid
	}
	var entity samlMetadataEntity
	if xml.Unmarshal([]byte(in.MetadataFile), &entity) != nil || len(entity.Roles) != 1 {
		return federationProvider{}, errInvalid
	}
	role := entity.Roles[0]
	if !slices.Contains(strings.Fields(role.Protocols), federationSAMLProtocol) {
		return federationProvider{}, errInvalid
	}
	redirect, err := federationRedirect(role.Endpoints)
	if err != nil {
		return federationProvider{}, err
	}
	p := federationProvider{Name: in.Name, EntityID: entity.ID, RedirectURL: redirect, Mapping: mapping}
	for _, expires := range []*time.Time{entity.Expires, role.Expires} {
		if expires == nil {
			continue
		}
		if !now.Before(*expires) {
			return federationProvider{}, errInvalid
		}
		if p.MetadataExpires.IsZero() || expires.Before(p.MetadataExpires) {
			p.MetadataExpires = *expires
		}
	}
	for _, key := range role.Keys {
		if key.Use == "encryption" {
			continue // Neither stored nor an alternative to the signing pin.
		}
		if (key.Use != "" && key.Use != "signing") || p.SigningCertDER != nil ||
			len(key.KeyInfos) != 1 || len(key.KeyInfos[0].CertificateData) != 1 || len(key.KeyInfos[0].CertificateData[0].Certificates) != 1 {
			return federationProvider{}, errInvalid
		}
		source := strings.Join(strings.Fields(key.KeyInfos[0].CertificateData[0].Certificates[0]), "")
		p.SigningCertDER, err = base64.StdEncoding.Strict().DecodeString(source)
		if err != nil {
			return federationProvider{}, errInvalid
		}
	}
	if !p.validStructure() || !p.currentPin(now) {
		return federationProvider{}, errInvalid
	}
	return p, nil
}

func parseFederationPin(der []byte) (*x509.Certificate, error) {
	if len(der) == 0 || len(der) > maxFederationCert {
		return nil, errInvalid
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || (cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageDigitalSignature == 0) {
		return nil, errInvalid
	}
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		// ParseCertificate checks positivity, not usability by Go 1.26's
		// RSA verifier. Keep the local size cap and its odd modulus plus
		// odd signed-32-bit exponent requirement (independent of int size).
		if n := pub.N.BitLen(); n < 2048 || n > 4096 || pub.N.Bit(0) == 0 ||
			pub.E < 3 || pub.E&1 == 0 || pub.E > 1<<31-1 {
			return nil, errInvalid
		}
	case *ecdsa.PublicKey:
		switch pub.Curve.Params().BitSize {
		case 256, 384, 521:
		default:
			return nil, errInvalid
		}
	default:
		return nil, errInvalid
	}
	return cert, nil
}

// Accept one HTTP-Redirect declaration and, optionally, one HTTP-POST
// declaration at the same broker origin. POST is never selected as a fallback.
func federationRedirect(endpoints []samlEndpoint) (string, error) {
	if len(endpoints) < 1 || len(endpoints) > 2 {
		return "", errInvalid
	}
	var anchor, redirect string
	post := false
	for _, e := range endpoints {
		u, err := localFederationURL(e.Location, false)
		if err != nil || e.Response != "" {
			return "", errInvalid
		}
		origin := u.Scheme + "://" + u.Host
		if anchor != "" && anchor != origin {
			return "", errInvalid
		}
		anchor = origin
		switch e.Binding {
		case federationSAMLRedirect:
			if redirect != "" {
				return "", errInvalid
			}
			redirect = e.Location
		case federationSAMLPost:
			if post {
				return "", errInvalid
			}
			post = true
		default:
			return "", errInvalid
		}
	}
	if redirect == "" {
		return "", errInvalid
	}
	return redirect, nil
}

// Bound structure before decoding a narrow projection. Refuse ambiguous
// attributes, multi-root documents, DTDs, processing instructions, and
// repeated XML identifiers. No network entity or key discovery is possible.
func boundedMetadataXML(raw string) bool {
	if len(raw) == 0 || len(raw) > maxFederationXML || !utf8.ValidString(raw) {
		return false
	}
	d := xml.NewDecoder(strings.NewReader(raw))
	depth, roots, elements, certDepth := 0, 0, 0, 0
	xmlDecl := false
	ids := map[string]bool{}
	for {
		token, err := d.Token()
		if err == io.EOF {
			return depth == 0 && roots == 1
		}
		if err != nil {
			return false
		}
		switch x := token.(type) {
		case xml.Directive:
			return false
		case xml.ProcInst:
			if x.Target != "xml" || xmlDecl || roots != 0 {
				return false
			}
			xmlDecl = true
		case xml.StartElement:
			if certDepth != 0 {
				return false
			}
			depth++
			if x.Name == (xml.Name{Space: "http://www.w3.org/2000/09/xmldsig#", Local: "X509Certificate"}) {
				certDepth = depth
			}
			elements++
			if depth == 1 {
				roots++
			}
			if depth > 32 || elements > 4096 || roots > 1 || len(x.Attr) > 32 {
				return false
			}
			seen := make(map[string]bool, len(x.Attr))
			for _, a := range x.Attr {
				local := a.Name.Local
				if seen[local] {
					return false
				}
				seen[local] = true
				switch local {
				case "entityID", "validUntil", "protocolSupportEnumeration", "use", "Binding", "Location", "ResponseLocation":
					if a.Name.Space != "" {
						return false
					}
				}
				if strings.EqualFold(local, "id") {
					if a.Value == "" || ids[a.Value] {
						return false
					}
					ids[a.Value] = true
				}
				if local == "Algorithm" && strings.Contains(strings.ToLower(a.Value), "sha1") {
					return false
				}
			}
		case xml.EndElement:
			if certDepth == depth {
				certDepth = 0
			}
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(x)) != "" {
				return false
			}
		}
	}
}
