package cognitoidp

import (
	"bytes"
	"encoding/json"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	federationConfigVersion     = 1
	federationRegistrationBytes = 16
	maxFederationProviders      = 2
	maxFederationClients        = 10
	maxFederationCallbacks      = 5
	maxFederationMapping        = 5
	maxFederationScopes         = 3
	maxFederationURL            = 1024
	federationPKCE              = "S256"
)

type federationOwner struct {
	Scope Scope
	ID    string
}

type federationConfig struct {
	Version   int
	Owner     federationOwner
	Providers []federationProvider
	Clients   []federationClient
}

type federationProvider struct {
	Name            string
	EntityID        string
	RedirectURL     string
	SigningCertDER  []byte
	MetadataExpires time.Time
	Mapping         map[string]string
	// A new signed CreateIdentityProvider establishes a new trust registration.
	// Older metadata-only rows omit this and remain readable for management.
	RegistrationID string `json:",omitempty"`
}

// The only representable client is public, authorization code, S256. It
// cannot contain a secret, grant a role or select a different OAuth flow.
type federationClientInput struct {
	Name      string
	Callbacks []string
	Providers []string
	Scopes    []string
}

type federationClient struct {
	ID string
	federationClientInput
}

func readFederationConfig(raw []byte, owner federationOwner) (federationConfig, error) {
	if len(raw) == 0 {
		return federationConfig{Version: federationConfigVersion, Owner: owner}, nil
	}
	var cfg federationConfig
	if len(raw) > maxFederationMetadata || json.Unmarshal(raw, &cfg) != nil ||
		cfg.Version != federationConfigVersion || cfg.Owner != owner ||
		len(cfg.Providers) > maxFederationProviders || len(cfg.Clients) > maxFederationClients {
		return federationConfig{}, errFederationState
	}
	// The dedicated slot contains only our versioned canonical encoding.
	// This rejects unknown or duplicate fields, lossy decoding and extra data.
	exact, err := json.Marshal(cfg)
	if err != nil || !bytes.Equal(raw, exact) {
		return federationConfig{}, errFederationState
	}
	providers := make(map[string]bool, len(cfg.Providers))
	registrations := make(map[string]bool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if providers[p.Name] || !p.validStructure() || (p.RegistrationID != "" && registrations[p.RegistrationID]) {
			return federationConfig{}, errFederationState
		}
		providers[p.Name] = true
		if p.RegistrationID != "" {
			registrations[p.RegistrationID] = true
		}
	}
	clients := make(map[string]bool, len(cfg.Clients))
	for _, c := range cfg.Clients {
		if clients[c.ID] || !validFederationClientID(c.ID) || !c.federationClientInput.valid() {
			return federationConfig{}, errFederationState
		}
		for _, provider := range c.Providers {
			if !providers[provider] {
				return federationConfig{}, errFederationState
			}
		}
		clients[c.ID] = true
	}
	return cfg, nil
}

func providerName(name string) bool { return name == "Keycloak" || name == "Entra" }

func (p federationProvider) validStructure() bool {
	if !providerName(p.Name) || !printableNonSpace(p.EntityID, maxFederationURL) || !validFederationMapping(p.Mapping) ||
		(p.RegistrationID != "" && !validFederationRegistrationID(p.RegistrationID)) {
		return false
	}
	if _, err := localFederationURL(p.RedirectURL, false); err != nil {
		return false
	}
	_, err := parseFederationPin(p.SigningCertDER)
	return err == nil
}

func (p federationProvider) currentPin(now time.Time) bool {
	cert, err := parseFederationPin(p.SigningCertDER)
	return err == nil && !now.Before(cert.NotBefore) && now.Before(cert.NotAfter) &&
		(p.MetadataExpires.IsZero() || now.Before(p.MetadataExpires))
}

func (p federationProvider) offers(scopes []string) bool {
	for _, scope := range scopes {
		switch scope {
		case "email":
			if p.Mapping["email"] == "" {
				return false
			}
		case "profile":
			if p.Mapping["name"] == "" && p.Mapping["given_name"] == "" && p.Mapping["family_name"] == "" {
				return false
			}
		}
	}
	return true
}

func validFederationMapping(mapping map[string]string) bool {
	if len(mapping) > maxFederationMapping {
		return false
	}
	seen := make(map[string]bool, len(mapping))
	for dest, src := range mapping {
		switch dest {
		case "email", "name", "given_name", "family_name":
		case "email_verified":
			if src != "email_verified" || mapping["email"] == "" {
				return false
			}
		default:
			return false
		}
		if !printableNonSpace(src, 256) || seen[src] {
			return false
		}
		seen[src] = true
	}
	return true
}

func printableNonSpace(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, c := range s {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}

func uniqueBoundedStrings(xs []string, max int) bool {
	if len(xs) == 0 || len(xs) > max {
		return false
	}
	for i, x := range xs {
		if x == "" || slices.Contains(xs[:i], x) {
			return false
		}
	}
	return true
}

func validFederationClientID(s string) bool {
	return federationLowerHex(s, 24)
}

func validFederationRegistrationID(s string) bool {
	return federationLowerHex(s, 2*federationRegistrationBytes)
}

func federationLowerHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validFederationScopes(scopes []string) bool {
	if !uniqueBoundedStrings(scopes, maxFederationScopes) || !slices.Contains(scopes, "openid") {
		return false
	}
	for _, scope := range scopes {
		if scope != "openid" && scope != "email" && scope != "profile" {
			return false
		}
	}
	return true
}

func (c federationClientInput) valid() bool {
	if !validPoolName(c.Name) || strings.TrimSpace(c.Name) != c.Name ||
		!uniqueBoundedStrings(c.Callbacks, maxFederationCallbacks) ||
		!uniqueBoundedStrings(c.Providers, maxFederationProviders) || !validFederationScopes(c.Scopes) {
		return false
	}
	for _, n := range c.Providers {
		if !providerName(n) {
			return false
		}
	}
	for _, raw := range c.Callbacks {
		if _, err := localFederationURL(raw, true); err != nil {
			return false
		}
	}
	return true
}

// Keep registered URLs as exact strings. Do not resolve hosts, re-serialize a
// callback, follow redirects, interpret remote metadata URLs or normalize path
// aliases. The same defensive restrictions apply to local IdP destinations.
func localFederationURL(raw string, callback bool) (*url.URL, error) {
	localScheme := strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://")
	if !localScheme || !printableNonSpace(raw, maxFederationURL) || strings.ContainsAny(raw, "#\\") {
		return nil, errInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Opaque != "" ||
		u.RawPath != "" || u.EscapedPath() != u.Path || u.ForceQuery || u.Path == "" {
		return nil, errInvalid
	}
	ip, addrErr := netip.ParseAddr(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	if addrErr != nil || !ip.IsLoopback() || ip.Is4In6() || ip.Zone() != "" ||
		portErr != nil || port <= 0 || port > 65535 ||
		u.Host != net.JoinHostPort(ip.String(), strconv.Itoa(port)) {
		return nil, errInvalid
	}
	if strings.Contains(u.Path, "//") || !strings.HasPrefix(u.Path, "/") {
		return nil, errInvalid
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, errInvalid
		}
	}
	if !callback && u.RawQuery != "" {
		return nil, errInvalid
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errInvalid
	}
	for key, vals := range query {
		if !printableNonSpace(key, maxFederationURL) || len(vals) != 1 || strings.ContainsFunc(vals[0], func(c rune) bool { return c < ' ' || c == 127 }) {
			return nil, errInvalid
		}
		switch strings.ToLower(key) {
		case "code", "state", "error", "error_description", "error_uri", "iss", "session_state":
			return nil, errInvalid
		}
	}
	return u, nil
}
