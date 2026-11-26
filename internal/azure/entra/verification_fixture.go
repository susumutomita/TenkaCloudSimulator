// Package entra provides a deliberately local, synthetic OIDC verification fixture.
package entra

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"regexp"
	"time"
)

const (
	issuerPath   = "/_simulator/azure/entra/v2.0"
	MetadataPath = issuerPath + "/verification-metadata.json"
	KeysPath     = "/_simulator/azure/entra/discovery/v2.0/keys"
	testAudience = "urn:tenkacloud:simulator:test:entra"
	testSubject  = "synthetic-fixture-user"
	profile      = "tenkacloud-simulator-entra-verification-fixture-v1"
)

var (
	fixtureHost  = netip.MustParseAddr("127.0.0.1")
	noncePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,96}$`)
)

// VerificationMetadata identifies the fixed test trust. It is not OpenID
// Provider discovery and has no authorization or token endpoints.
type VerificationMetadata struct {
	Profile           string   `json:"fixture_profile"`
	Issuer            string   `json:"issuer"`
	JWKSURI           string   `json:"jwks_uri"`
	Audience          string   `json:"expected_audience"`
	SigningAlgorithms []string `json:"id_token_signing_alg_values_supported"`
}

type VerificationFixture struct {
	address netip.AddrPort
	key     *rsa.PrivateKey
	keyID   string
}

// ValidListen confines the fixture to an explicit IPv4 numeric loopback.
// Port zero is for the isolated command to allocate an ephemeral local port.
func ValidListen(address netip.AddrPort) bool { return address.Addr() == fixtureHost }

func NewVerificationFixture(address netip.AddrPort) (*VerificationFixture, error) {
	if !ValidListen(address) || address.Port() == 0 {
		return nil, errors.New("Entra verification fixture needs an assigned 127.0.0.1 port")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(x509.MarshalPKCS1PublicKey(&key.PublicKey))
	return &VerificationFixture{address: address, key: key, keyID: base64.RawURLEncoding.EncodeToString(digest[:])}, nil
}

func (f *VerificationFixture) Metadata() VerificationMetadata {
	origin := "http://" + f.address.String()
	return VerificationMetadata{
		Profile: profile, Issuer: origin + issuerPath, JWKSURI: origin + KeysPath,
		Audience: testAudience, SigningAlgorithms: []string{"RS256"},
	}
}

// TestIDToken signs only the fixed synthetic principal and audience. It is
// reachable through explicit local startup output, never an HTTP operation.
func (f *VerificationFixture) TestIDToken(nonce string) (string, error) {
	if !noncePattern.MatchString(nonce) {
		return "", errors.New("test ID-token nonce must be 16–96 URL-safe characters")
	}
	now := time.Now().Unix()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": f.keyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iss": f.Metadata().Issuer, "aud": testAudience, "sub": testSubject,
		"nonce": nonce, "iat": now, "exp": now + 60,
	})
	if err != nil {
		return "", err
	}
	encode := base64.RawURLEncoding.EncodeToString
	input := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return input + "." + encode(signature), nil
}

func (f *VerificationFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !ValidListen(peer) || r.Host != f.address.String() ||
		r.URL.IsAbs() || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		http.NotFound(w, r)
		return
	}
	for _, h := range []string{"Authorization", "Cookie", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		if len(r.Header.Values(h)) != 0 {
			http.NotFound(w, r)
			return
		}
	}
	var body any
	switch r.URL.Path {
	case MetadataPath:
		body = f.Metadata()
	case KeysPath:
		body = map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": f.keyID,
			"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
			"e": "AQAB", // crypto/rsa.GenerateKey sets the public exponent to 65537.
		}}}
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		http.Error(w, "request body not supported", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
