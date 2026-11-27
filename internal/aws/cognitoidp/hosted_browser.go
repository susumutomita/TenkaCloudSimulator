//go:build cognito_hosted

package cognitoidp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/crewjam/saml"
	"github.com/golang-jwt/jwt/v5"
	dsig "github.com/russellhaering/goxmldsig"
)

const (
	hostedPrefix    = "/_simulator/cognito/"
	hostedAuthorize = "oauth2/authorize"
	hostedACS       = "saml2/idpresponse"
	hostedMetadata  = "saml2/metadata"
	hostedToken     = "oauth2/token"
	hostedJWKS      = ".well-known/jwks.json"
	hostedTokenTTL  = 5 * time.Minute
)

type hostedBrowser struct {
	store            *Store
	account          string
	origin           *url.URL
	epoch            string
	spKey            *rsa.PrivateKey
	spCertificate    *x509.Certificate
	idKey, accessKey *rsa.PrivateKey
}

func NewHostedBrowser(store *Store, account, origin string) (HostedBrowser, error) {
	u, err := localFederationURL(origin+"/", false)
	if store == nil || !store.nativeFederation || !accountRE.MatchString(account) ||
		err != nil || u.Scheme != "https" || u.Scheme+"://"+u.Host != origin {
		return nil, errors.New("Cognito hosted login requires signed configuration and a canonical numeric-loopback HTTPS origin")
	}
	if err := store.openHostedState(context.Background()); err != nil {
		return nil, err
	}
	spKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	cert := &x509.Certificate{
		SerialNumber: serial.Add(serial, big.NewInt(1)), Subject: pkix.Name{CommonName: "Local simulator SAML signing"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &spKey.PublicKey, spKey)
	if err != nil {
		return nil, err
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	idKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	accessKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	epoch, err := hostedRandom()
	if err != nil {
		return nil, err
	}
	return &hostedBrowser{store, account, u, epoch, spKey, cert, idKey, accessKey}, nil
}

func hostedRoute(r *http.Request) (id, operation string, ok bool) {
	if r.URL.RawPath != "" || r.URL.IsAbs() || !strings.HasPrefix(r.URL.Path, hostedPrefix) {
		return "", "", false
	}
	id, operation, ok = strings.Cut(strings.TrimPrefix(r.URL.Path, hostedPrefix), "/")
	region, _, found := strings.Cut(id, "_")
	if !ok || !found || !regionRE.MatchString(region) || !(Scope{Region: region}).validPoolID(id) {
		return "", "", false
	}
	switch operation {
	case hostedAuthorize, hostedMetadata, hostedJWKS:
		ok = r.Method == http.MethodGet
	case hostedACS, hostedToken:
		ok = r.Method == http.MethodPost
	default:
		ok = false
	}
	return id, operation, ok
}

func (b *hostedBrowser) Owns(r *http.Request) bool { _, _, ok := hostedRoute(r); return ok }

func (b *hostedBrowser) root(id string) string {
	return b.origin.Scheme + "://" + b.origin.Host + hostedPrefix + id
}

func (b *hostedBrowser) accepts(r *http.Request) bool {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !peer.Addr().IsLoopback() || peer.Addr().Is4In6() || peer.Addr().Zone() != "" || peer.Port() == 0 ||
		r.TLS == nil || r.Host != b.origin.Host || len(r.Header.Values("Origin")) > 1 ||
		r.Header.Get("Authorization") != "" || r.Header.Get("X-Amz-Target") != "" {
		return false
	}
	for key := range r.Header {
		k := strings.ToLower(key)
		if k == "forwarded" || strings.HasPrefix(k, "x-forwarded-") {
			return false
		}
	}
	return true
}

func (b *hostedBrowser) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-TenkaCloud-Simulator", "true")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	id, op, ok := hostedRoute(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !b.accepts(r) {
		http.Error(w, "Invalid local Cognito endpoint", http.StatusForbidden)
		return
	}
	region, _, _ := strings.Cut(id, "_")
	scope := Scope{Account: b.account, Region: region}
	var err error
	switch op {
	case hostedAuthorize:
		err = b.authorize(w, r, scope, id)
	case hostedACS:
		err = b.acs(w, r, scope, id)
	case hostedToken:
		err = b.token(w, r, scope, id)
	case hostedJWKS:
		err = b.jwks(w, r, scope, id)
	case hostedMetadata:
		err = b.metadata(w, r, scope, id)
	}
	if err != nil {
		// No SAML details, credentials, attributes, state or callback on errors.
		http.Error(w, "Invalid local Cognito authorization", http.StatusBadRequest)
	}
}

func hostedRandom() (string, error) {
	var raw [32]byte
	_, err := rand.Read(raw[:])
	return base64.RawURLEncoding.EncodeToString(raw[:]), err
}

func hostedDigest(s string) string {
	raw := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

// Separate the selected trust registration even when two labels share the
// issuer/key, or an operator removes and re-creates an identical registration.
func hostedIdentityNamespace(scope Scope, id string, v federationProvider) string {
	return hostedDigest(strings.Join([]string{"tcs-cognito-trust-v1", scope.Account, scope.Region, id,
		v.Name, v.RegistrationID, v.EntityID, v.RedirectURL, hostedDigest(string(v.SigningCertDER))}, "\x00"))
}

func hostedSameValue(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func hostedOrigin(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

func hostedLocalURL(raw string, callback bool, hostname string) (*url.URL, error) {
	u, err := localFederationURL(raw, callback)
	if err != nil || u.Scheme != "https" || u.Hostname() != hostname {
		return nil, errInvalid
	}
	return u, nil
}

var hostedVerifier = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

func hostedCanonicalNonce(s string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	return len(s) == 43 && err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == s
}

// The browser chooses an exact existing provider and public client, never
// authority, and all three numeric-loopback HTTPS hosts must be the same IP.
func (b *hostedBrowser) binding(cfg federationConfig, clientID, provider, callback string, scopes []string, now time.Time) (federationProvider, *url.URL, *url.URL, error) {
	i, j := cfg.clientIndex(clientID), cfg.providerIndex(provider)
	if i < 0 || j < 0 || !validFederationScopes(scopes) {
		return federationProvider{}, nil, nil, errInvalid
	}
	c, v := cfg.Clients[i], cfg.Providers[j]
	if !slices.Contains(c.Callbacks, callback) || !slices.Contains(c.Providers, provider) ||
		!validFederationRegistrationID(v.RegistrationID) || !v.currentPin(now) || !v.offers(scopes) {
		return federationProvider{}, nil, nil, errInvalid
	}
	for _, s := range scopes {
		if !slices.Contains(c.Scopes, s) {
			return federationProvider{}, nil, nil, errInvalid
		}
	}
	redirect, err := hostedLocalURL(callback, true, b.origin.Hostname())
	if err != nil {
		return federationProvider{}, nil, nil, err
	}
	destination, err := hostedLocalURL(v.RedirectURL, false, b.origin.Hostname())
	if err != nil {
		return federationProvider{}, nil, nil, err
	}
	return v, redirect, destination, nil
}

func hostedStrictFields(v url.Values, allowed string, max int) bool {
	for k, xs := range v {
		if !slices.Contains(strings.Fields(allowed), k) || len(xs) != 1 || len(xs[0]) > max {
			return false
		}
	}
	return true
}

func hostedPost(r *http.Request, allowed string, limit int) (url.Values, error) {
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.URL.RawQuery != "" || r.URL.ForceQuery || err != nil || media != "application/x-www-form-urlencoded" ||
		len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
		return nil, errInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, errInvalid
	}
	v, err := url.ParseQuery(string(raw))
	if err != nil || !hostedStrictFields(v, allowed, limit) {
		return nil, errInvalid
	}
	return v, nil
}

func hostedCookieName(id string) string { return "tcs_cognito_" + id }

func (b *hostedBrowser) authorize(w http.ResponseWriter, r *http.Request, scope Scope, id string) error {
	if len(r.URL.RawQuery) > 8192 || r.Header.Get("Sec-Fetch-Site") == "cross-site" ||
		r.Header.Get("Origin") != "" && r.Header.Get("Origin") != hostedOrigin(b.origin) {
		return errInvalid
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	const allowed = "response_type client_id redirect_uri scope state nonce code_challenge code_challenge_method identity_provider"
	requiredCount := 8
	hasNonce := q.Has("nonce")
	if hasNonce {
		requiredCount++
	}
	if err != nil || len(q) != requiredCount || !hostedStrictFields(q, allowed, 4096) ||
		q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" ||
		!hostedCanonicalNonce(q.Get("code_challenge")) || !providerName(q.Get("identity_provider")) ||
		!printableNonSpace(q.Get("state"), 1024) {
		return errInvalid
	}
	nonce := q.Get("nonce")
	if hasNonce {
		if !printableNonSpace(nonce, 1024) {
			return errInvalid
		}
	} else {
		// Cognito generates a nonce for third-party federation when the app
		// omits it. Bind it to the same pending row and grant, not the caller.
		nonce, err = hostedRandom()
		if err != nil {
			return err
		}
	}
	relay, err := hostedRandom()
	if err != nil {
		return err
	}
	secret, err := hostedRandom()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	p := hostedPending{Browser: hostedDigest(secret), Provider: q.Get("identity_provider"),
		Client: q.Get("client_id"), Redirect: q.Get("redirect_uri"), Challenge: q.Get("code_challenge"),
		Scope: q.Get("scope"), State: q.Get("state"), Nonce: nonce, Until: now.Add(hostedPendingTTL)}
	var target string
	err = b.transition(r.Context(), scope, id, nil, func(cfg federationConfig, _ federationRevision, s *hostedState) (bool, error) {
		v, _, endpoint, err := b.binding(cfg, p.Client, p.Provider, p.Redirect, strings.Split(p.Scope, " "), now)
		if err != nil || len(s.Pending) >= hostedPendingLimit {
			return false, errInvalid
		}
		sp := b.serviceProvider(id, &v)
		req, err := sp.MakeAuthenticationRequest(endpoint.String(), saml.HTTPRedirectBinding, saml.HTTPPostBinding)
		if err != nil {
			return false, errInvalid
		}
		u, err := req.Redirect(relay, sp)
		if err != nil || hostedOrigin(u) != hostedOrigin(endpoint) || u.Path != endpoint.Path {
			return false, errInvalid
		}
		p.RequestID, target = req.ID, u.String()
		s.Pending[hostedDigest(relay)] = p
		return true, nil
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: hostedCookieName(id), Value: secret, Path: hostedPrefix + id + "/",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: p.Until})
	http.Redirect(w, r, target, http.StatusFound)
	return nil
}

func (b *hostedBrowser) acs(w http.ResponseWriter, r *http.Request, scope Scope, id string) error {
	q, err := hostedPost(r, "SAMLResponse RelayState", 256<<10)
	if err != nil || len(q) != 2 || !hostedCanonicalNonce(q.Get("RelayState")) || len(r.Header.Values("Origin")) != 1 {
		return errInvalid
	}
	cookies := r.CookiesNamed(hostedCookieName(id))
	if len(cookies) != 1 || !hostedCanonicalNonce(cookies[0].Value) {
		return errInvalid
	}
	raw, err := base64.StdEncoding.DecodeString(q.Get("SAMLResponse"))
	if err != nil {
		return errInvalid
	}
	lookup := hostedDigest(q.Get("RelayState"))
	browser := hostedDigest(cookies[0].Value)
	var p hostedPending
	var provider federationProvider
	var revision federationRevision
	err = b.transition(r.Context(), scope, id, nil, func(cfg federationConfig, rev federationRevision, s *hostedState) (bool, error) {
		current, ok := s.Pending[lookup]
		if !ok || !hostedSameValue(current.Browser, browser) {
			return false, errInvalid
		}
		v, _, endpoint, err := b.binding(cfg, current.Client, current.Provider, current.Redirect, strings.Split(current.Scope, " "), time.Now().UTC())
		if err != nil || r.Header.Get("Origin") != hostedOrigin(endpoint) {
			return false, errInvalid
		}
		p, provider, revision = current, v, rev
		return false, nil
	})
	if err != nil {
		return err
	}
	verified, err := b.verifyAssertion(id, provider, p, raw)
	if err != nil {
		return err
	}
	assertion := verified.assertion
	replay := hostedDigest(provider.EntityID + "\x00" + assertion.ID)
	namespace := hostedIdentityNamespace(scope, id, provider)
	sub := hostedDigest("tcs-cognito-sub-v1\x00" + namespace + "\x00" + verified.principal)
	code, err := hostedRandom()
	if err != nil {
		return err
	}
	var target string
	err = b.transition(r.Context(), scope, id, &revision, func(cfg federationConfig, _ federationRevision, s *hostedState) (bool, error) {
		now := time.Now().UTC()
		_, consumed := s.Replay[replay]
		if current, ok := s.Pending[lookup]; !ok || current != p || consumed ||
			len(s.Codes) >= hostedCodeLimit || len(s.Replay) >= hostedReplayLimit {
			return false, errInvalid
		}
		v, redirect, endpoint, err := b.binding(cfg, p.Client, p.Provider, p.Redirect, strings.Split(p.Scope, " "), now)
		if err != nil || r.Header.Get("Origin") != hostedOrigin(endpoint) || hostedIdentityNamespace(scope, id, v) != namespace ||
			!now.Before(assertion.Conditions.NotOnOrAfter) || !now.Before(assertion.Subject.SubjectConfirmations[0].SubjectConfirmationData.NotOnOrAfter) {
			return false, errInvalid
		}
		s.Codes[hostedDigest(code)] = hostedGrant{Client: p.Client, Provider: p.Provider, Redirect: p.Redirect,
			Challenge: p.Challenge, Scope: p.Scope, Nonce: p.Nonce, Subject: sub, Attributes: verified.attributes,
			AuthTime: assertion.AuthnStatements[0].AuthnInstant, Until: now.Add(hostedCodeTTL)}
		s.Replay[replay] = assertion.Conditions.NotOnOrAfter
		delete(s.Pending, lookup)
		values := redirect.Query()
		values.Set("code", code)
		values.Set("state", p.State)
		redirect.RawQuery = values.Encode()
		target = redirect.String()
		return true, nil
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: hostedCookieName(id), Path: hostedPrefix + id + "/", Secure: true,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	http.Redirect(w, r, target, http.StatusFound)
	return nil
}

func (b *hostedBrowser) token(w http.ResponseWriter, r *http.Request, scope Scope, id string) error {
	q, err := hostedPost(r, "grant_type client_id code redirect_uri code_verifier", 4<<10)
	if err != nil || len(q) != 5 || q.Get("grant_type") != "authorization_code" ||
		!hostedCanonicalNonce(q.Get("code")) || !hostedVerifier.MatchString(q.Get("code_verifier")) {
		return errInvalid
	}
	lookup := hostedDigest(q.Get("code"))
	var response map[string]any
	var corsOrigin string
	err = b.transition(r.Context(), scope, id, nil, func(cfg federationConfig, _ federationRevision, s *hostedState) (bool, error) {
		g, ok := s.Codes[lookup]
		if !ok || g.Client != q.Get("client_id") || g.Redirect != q.Get("redirect_uri") ||
			!hostedSameValue(g.Challenge, hostedDigest(q.Get("code_verifier"))) {
			return false, errInvalid
		}
		now := time.Now().UTC()
		_, redirect, _, err := b.binding(cfg, g.Client, g.Provider, g.Redirect, strings.Split(g.Scope, " "), now)
		if err != nil {
			return false, err
		}
		corsOrigin = r.Header.Get("Origin")
		if corsOrigin != "" && corsOrigin != hostedOrigin(redirect) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			return false, errInvalid
		}
		identity, err := b.sign(id, g, "id", now)
		if err != nil {
			return false, err
		}
		access, err := b.sign(id, g, "access", now)
		if err != nil {
			return false, err
		}
		delete(s.Codes, lookup)
		response = map[string]any{"id_token": identity, "access_token": access, "token_type": "Bearer",
			"expires_in": int(hostedTokenTTL.Seconds()), "scope": g.Scope}
		return true, nil
	})
	if err != nil {
		return err
	}
	if corsOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", corsOrigin)
		w.Header().Set("Vary", "Origin")
	}
	hostedJSON(w, response)
	return nil
}

func (b *hostedBrowser) sign(id string, g hostedGrant, use string, now time.Time) (string, error) {
	jti, err := hostedRandom()
	if err != nil {
		return "", err
	}
	c := jwt.MapClaims{"iss": b.root(id), "sub": g.Subject, "iat": now.Unix(), "exp": now.Add(hostedTokenTTL).Unix(),
		"auth_time": g.AuthTime.Unix(), "token_use": use, "jti": jti}
	key := b.accessKey
	if use == "id" {
		key = b.idKey
		c["aud"], c["nonce"] = g.Client, g.Nonce
		for name, value := range g.Attributes {
			if !validAttribute(name, value) {
				return "", errInvalid
			}
			if name == "email_verified" {
				c[name] = value == "true"
			} else {
				c[name] = value
			}
		}
	} else {
		c["client_id"], c["scope"] = g.Client, g.Scope
	}
	t := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
	t.Header["kid"] = hostedKeyID(&key.PublicKey)
	return t.SignedString(key)
}

func hostedKeyID(key *rsa.PublicKey) string {
	return hostedDigest(string(x509.MarshalPKCS1PublicKey(key)))
}

func hostedJWK(key *rsa.PublicKey) map[string]string {
	return map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": hostedKeyID(key),
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}
}

func (b *hostedBrowser) publicRead(r *http.Request, scope Scope, id string) error {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Sec-Fetch-Site") == "cross-site" ||
		r.Header.Get("Origin") != "" && r.Header.Get("Origin") != hostedOrigin(b.origin) {
		return errInvalid
	}
	_, err := b.store.describe(r.Context(), scope, id)
	return err
}

func (b *hostedBrowser) jwks(w http.ResponseWriter, r *http.Request, scope Scope, id string) error {
	if err := b.publicRead(r, scope, id); err != nil {
		return err
	}
	hostedJSON(w, map[string]any{"keys": []map[string]string{hostedJWK(&b.idKey.PublicKey), hostedJWK(&b.accessKey.PublicKey)}})
	return nil
}

func (b *hostedBrowser) metadata(w http.ResponseWriter, r *http.Request, scope Scope, id string) error {
	if err := b.publicRead(r, scope, id); err != nil {
		return err
	}
	md := b.serviceProvider(id, nil).Metadata()
	role := &md.SPSSODescriptors[0]
	role.KeyDescriptors = slices.DeleteFunc(role.KeyDescriptors, func(k saml.KeyDescriptor) bool { return k.Use != "signing" })
	if len(role.KeyDescriptors) != 1 {
		return errInvalid
	}
	raw, err := xml.Marshal(md)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/samlmetadata+xml")
	_, _ = w.Write(raw)
	return nil
}

func hostedJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func (b *hostedBrowser) serviceProvider(id string, v *federationProvider) *saml.ServiceProvider {
	acs, _ := url.Parse(b.root(id) + "/" + hostedACS)
	metadata, _ := url.Parse(b.root(id) + "/" + hostedMetadata)
	sp := &saml.ServiceProvider{
		EntityID: "urn:amazon:cognito:sp:" + id, Key: b.spKey, Certificate: b.spCertificate, AcsURL: *acs,
		MetadataURL: *metadata, SignatureMethod: dsig.RSASHA256SignatureMethod, RejectSHA1Signatures: true,
		RejectUnknownConditions: true, RejectExpiredIDPMetadata: true, LimitRelayStateLength: true,
		AssertionConsumerServiceBindings: []string{saml.HTTPPostBinding},
		AuthnNameIDFormat:                saml.PersistentNameIDFormat,
	}
	if v != nil {
		sp.IDPMetadata = &saml.EntityDescriptor{EntityID: v.EntityID, ValidUntil: v.MetadataExpires, IDPSSODescriptors: []saml.IDPSSODescriptor{{
			SSODescriptor: saml.SSODescriptor{RoleDescriptor: saml.RoleDescriptor{
				ProtocolSupportEnumeration: "urn:oasis:names:tc:SAML:2.0:protocol",
				KeyDescriptors: []saml.KeyDescriptor{{Use: "signing", KeyInfo: saml.KeyInfo{X509Data: saml.X509Data{
					X509Certificates: []saml.X509Certificate{{Data: base64.StdEncoding.EncodeToString(v.SigningCertDER)}},
				}}}},
			}},
			SingleSignOnServices: []saml.Endpoint{{Binding: saml.HTTPRedirectBinding, Location: v.RedirectURL}},
		}}}
	}
	return sp
}
