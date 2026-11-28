package cognitoidp

import (
	"bytes"
	"encoding/xml"
	"io"
	"net/mail"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/crewjam/saml"
)

// This preflight is a size/structure guard, never a signature validator. The
// maintained SAML library alone selects and verifies the consumed assertion.
func xmlGuard(raw []byte, unsafeAttributes map[string]bool) error {
	if len(raw) == 0 || len(raw) > 192<<10 {
		return errInvalid
	}
	d := xml.NewDecoder(bytes.NewReader(raw))
	depth, n, assertions, responses, roots := 0, 0, 0, 0, 0
	subjects, nameIDs := 0, 0
	ids := map[string]bool{}
	var path [32]xml.StartElement
	var attribute string
	inSubjectNameID := false
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return errInvalid
		}
		switch x := t.(type) {
		case xml.Directive:
			return errInvalid
		case xml.StartElement:
			if inSubjectNameID {
				return errInvalid // The consumed subject needs one plain NameID.
			}
			depth++
			if depth == 1 {
				roots++
			}
			n++
			if depth > 32 || n > 4096 {
				return errInvalid
			}
			path[depth-1] = x
			if depth == 3 && assertionElement(path[1], "Assertion") && assertionElement(x, "Subject") {
				subjects++
			}
			if depth == 4 && assertionElement(path[1], "Assertion") && assertionElement(path[2], "Subject") && assertionElement(x, "NameID") {
				if !plainNameID(x) {
					return errInvalid
				}
				nameIDs++
				inSubjectNameID = true
			}
			if depth == 4 && assertionElement(path[2], "AttributeStatement") && assertionElement(x, "Attribute") {
				for _, a := range x.Attr {
					if a.Name.Local == "Name" {
						if a.Name.Space != "" || attribute != "" {
							unsafeAttributes[attribute], unsafeAttributes[a.Value] = true, true
						}
						attribute = a.Value
					}
				}
			}
			if attribute != "" && (depth > 5 || depth == 5 && !plainValue(path[:depth])) {
				unsafeAttributes[attribute] = true
			}
			if depth == 1 && x.Name != (xml.Name{Space: federationSAMLProtocol, Local: "Response"}) {
				return errInvalid
			}
			if x.Name.Local == "Assertion" {
				assertions++
				if !assertionElement(x, "Assertion") || depth != 2 {
					return errInvalid
				}
			}
			if x.Name.Local == "Response" {
				responses++
			}
			if x.Name.Local == "EncryptedAssertion" || x.Name.Local == "EncryptedID" || x.Name.Local == "EncryptedAttribute" {
				return errInvalid
			}
			for _, a := range x.Attr {
				if strings.EqualFold(a.Name.Local, "id") {
					if a.Value == "" || ids[a.Value] {
						return errInvalid
					}
					ids[a.Value] = true
				}
				if (a.Name.Local == "Algorithm") && strings.Contains(strings.ToLower(a.Value), "sha1") {
					return errInvalid
				}
			}
		case xml.EndElement:
			if depth == 4 {
				attribute = ""
				inSubjectNameID = false
			}
			depth--
		case xml.Comment, xml.ProcInst:
			if inSubjectNameID {
				return errInvalid
			}
			if attribute != "" && depth >= 5 {
				unsafeAttributes[attribute] = true
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(x)) != "" {
				return errInvalid
			}
		}
	}
	if depth != 0 || roots != 1 || assertions != 1 || responses != 1 || subjects != 1 || nameIDs != 1 {
		return errInvalid
	}
	return nil
}

func attributeScope(dest string) string {
	switch dest {
	case "email", "email_verified":
		return "email"
	case "name", "given_name", "family_name":
		return "profile"
	}
	return ""
}

func validAttribute(dest, value string) bool {
	if value == "" || len(value) > 256 || value != strings.TrimSpace(value) ||
		strings.ContainsFunc(value, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		}) {
		return false
	}
	switch dest {
	case "email":
		address, err := mail.ParseAddress(value)
		return len(value) <= 254 && err == nil && address.Name == "" && address.Address == value
	case "email_verified":
		return value == "true" || value == "false"
	default:
		return attributeScope(dest) == "profile"
	}
}

// Read values only from the assertion returned by the signature verifier.
// The XML preflight can reject an unsupported shape, never provide a claim.
func mappedAttributes(a *saml.Assertion, sources map[string]string, unsafe map[string]bool) (map[string]string, error) {
	if len(sources) == 0 {
		return nil, nil
	}
	claims := make(map[string]string, len(sources))
	for _, statement := range a.AttributeStatements {
		for _, attr := range statement.Attributes {
			dest, wanted := sources[attr.Name]
			if !wanted {
				continue
			}
			if unsafe[attr.Name] || claims[dest] != "" || len(attr.Values) != 1 {
				return nil, errInvalid
			}
			v := attr.Values[0]
			if !plainAttributeValue(v, dest == "email_verified") || !validAttribute(dest, v.Value) {
				return nil, errInvalid
			}
			claims[dest] = v.Value
		}
	}
	if len(claims) != len(sources) {
		return nil, errInvalid
	}
	return claims, nil
}

const (
	assertionNS            = "urn:oasis:names:tc:SAML:2.0:assertion"
	entraObjectIdentifier  = "http://schemas.microsoft.com/identity/claims/objectidentifier"
	samlURIAttributeFormat = "urn:oasis:names:tc:SAML:2.0:attrname-format:uri"
)

func assertionElement(e xml.StartElement, local string) bool {
	return e.Name == (xml.Name{Space: assertionNS, Local: local})
}

func namespaceDeclaration(a xml.Attr) bool {
	return a.Name.Space == "xmlns" || a.Name == (xml.Name{Local: "xmlns"})
}

// Empty, aliased, or alternate subject identifiers cannot collapse into a
// supported qualifier through the parser's optional string fields.
func plainNameID(e xml.StartElement) bool {
	seen := map[string]bool{}
	for _, a := range e.Attr {
		if namespaceDeclaration(a) {
			continue
		}
		switch a.Name.Local {
		case "Format", "NameQualifier", "SPNameQualifier":
			if a.Name.Space != "" || seen[a.Name.Local] || !printableNonSpace(a.Value, maxFederationURL) {
				return false
			}
			seen[a.Name.Local] = true
		default:
			return false // Includes SPProvidedID, which this SP does not manage.
		}
	}
	return true
}

func plainAttributeValue(v saml.AttributeValue, allowBoolean bool) bool {
	booleanType := v.Type == "boolean" || strings.HasSuffix(v.Type, ":boolean")
	return !v.Nil && v.NameID == nil && v.Encoding == "" && (!booleanType || allowBoolean)
}

// encoding/xml omits unknown children from saml.AttributeValue. Reject mixed
// content and non-scalar XML types before a selected mapping can use that value.
func plainValue(path []xml.StartElement) bool {
	value := path[len(path)-1]
	if !assertionElement(value, "AttributeValue") {
		return false
	}
	for _, a := range value.Attr {
		if namespaceDeclaration(a) {
			continue
		}
		if a.Name != (xml.Name{Space: "http://www.w3.org/2001/XMLSchema-instance", Local: "type"}) || !scalarType(path, a.Value) {
			return false
		}
	}
	return true
}

func scalarType(path []xml.StartElement, qname string) bool {
	prefix, name, qualified := strings.Cut(qname, ":")
	ns := xml.Name{Space: "xmlns", Local: prefix}
	if !qualified {
		name, ns = qname, xml.Name{Local: "xmlns"}
	}
	if name != "string" && name != "boolean" {
		return false
	}
	for i := len(path) - 1; i >= 0; i-- {
		for _, binding := range path[i].Attr {
			if binding.Name == ns {
				return binding.Value == "http://www.w3.org/2001/XMLSchema"
			}
		}
	}
	return false
}

var entraObjectGUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func hostedPrincipal(a *saml.Assertion, provider federationProvider, spEntityID string, unsafe map[string]bool) (string, error) {
	n := a.Subject.NameID
	if n.Format != string(saml.PersistentNameIDFormat) || !printableNonSpace(n.Value, 256) || n.SPProvidedID != "" ||
		(n.NameQualifier != "" && n.NameQualifier != provider.EntityID) ||
		(n.SPNameQualifier != "" && n.SPNameQualifier != spEntityID) {
		return "", errInvalid
	}
	switch provider.Name {
	case "Keycloak":
		// Only the pinned IdP's opaque pairwise persistent NameID is usable.
		return n.Value, nil
	case "Entra":
		// Entra Emulator v0.9.0 echoes the requested Format on mutable UPN.
		// Always use the signed Entra directory object ID, never the NameID.
		var identity string
		for _, statement := range a.AttributeStatements {
			for _, attr := range statement.Attributes {
				if attr.Name != entraObjectIdentifier {
					continue
				}
				if unsafe[attr.Name] || identity != "" || (attr.NameFormat != "" && attr.NameFormat != samlURIAttributeFormat) || len(attr.Values) != 1 {
					return "", errInvalid
				}
				v := attr.Values[0]
				if !plainAttributeValue(v, false) || len(v.Value) != 36 || !entraObjectGUID.MatchString(v.Value) || v.Value == "00000000-0000-0000-0000-000000000000" {
					return "", errInvalid
				}
				identity = v.Value
			}
		}
		if identity != "" {
			return identity, nil
		}
	}
	return "", errInvalid
}

type hostedAssertion struct {
	assertion  *saml.Assertion
	principal  string
	attributes map[string]string
}

// Keep the maintained parser on the consumed signed node; the local preflight
// limits shape only and never supplies claims. Record replay in the scoped
// issuance transaction after comparing the original revision and pending row.
func (b *hostedBrowser) verifyAssertion(id string, provider federationProvider, p hostedPending, raw []byte) (hostedAssertion, error) {
	unsafe := map[string]bool{}
	if xmlGuard(raw, unsafe) != nil {
		return hostedAssertion{}, errInvalid
	}
	sp := b.serviceProvider(id, &provider)
	a, err := sp.ParseXMLResponse(raw, []string{p.RequestID}, sp.AcsURL)
	if err != nil {
		return hostedAssertion{}, errInvalid
	}
	now := time.Now().UTC()
	if a.Issuer.Value != provider.EntityID || !printableNonSpace(a.ID, 1024) || a.Subject == nil ||
		a.Subject.NameID == nil ||
		a.Conditions == nil || len(a.AuthnStatements) != 1 || len(a.Subject.SubjectConfirmations) != 1 ||
		a.Conditions.NotBefore.IsZero() || a.Conditions.NotOnOrAfter.IsZero() || now.Before(a.Conditions.NotBefore) ||
		!now.Before(a.Conditions.NotOnOrAfter) || a.Conditions.NotOnOrAfter.After(now.Add(5*time.Minute)) ||
		a.IssueInstant.IsZero() || now.Before(a.IssueInstant) || now.Sub(a.IssueInstant) > 5*time.Minute ||
		a.Conditions.ProxyRestriction != nil {
		return hostedAssertion{}, errInvalid
	}
	sc := a.Subject.SubjectConfirmations[0]
	if sc.Method != "urn:oasis:names:tc:SAML:2.0:cm:bearer" || sc.NameID != nil || sc.SubjectConfirmationData == nil ||
		now.Before(sc.SubjectConfirmationData.NotBefore) || sc.SubjectConfirmationData.InResponseTo != p.RequestID ||
		sc.SubjectConfirmationData.Recipient != sp.AcsURL.String() || !now.Before(sc.SubjectConfirmationData.NotOnOrAfter) {
		return hostedAssertion{}, errInvalid
	}
	authTime := a.AuthnStatements[0].AuthnInstant
	if authTime.IsZero() || now.Before(authTime) {
		return hostedAssertion{}, errInvalid
	}
	principal, err := hostedPrincipal(a, provider, sp.EntityID, unsafe)
	if err != nil {
		return hostedAssertion{}, err
	}
	scopes := strings.Split(p.Scope, " ")
	sources := map[string]string{}
	for destination, source := range provider.Mapping {
		if slices.Contains(scopes, attributeScope(destination)) {
			sources[source] = destination
		}
	}
	attrs, err := mappedAttributes(a, sources, unsafe)
	if err != nil {
		return hostedAssertion{}, err
	}
	return hostedAssertion{assertion: a, principal: principal, attributes: attrs}, nil
}
