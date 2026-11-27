// Package entraidp provides a first-party, disposable local SAML IdP for
// synthetic Azure fixtures. No dependency on an AWS provider or host login.
package entraidp

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/crewjam/saml"
)

const (
	runLifetime     = 12 * time.Minute
	assertionLife   = time.Minute
	maxPending      = 12
	maxCodeAttempts = 6
	queryLimit      = 8 << 10
)

// AuthnRequestLifetime is the maximum age of the enrolled SP's signed
// request when its one-shot browser sign-in may complete.
const AuthnRequestLifetime = 2 * time.Minute

var boundedURLPath = regexp.MustCompile(`^(/[A-Za-z0-9_.~-]+)+/?$`)

// ServiceProvider is an operator-selected exact trust registration. Only a
// public RSA signing certificate, not a private key, crosses this boundary.
type ServiceProvider struct {
	EntityID              string
	ACS                   string
	SigningCertificateDER []byte
}

// Config has no ambient environment, network discovery, or HTTP mutation.
type Config struct {
	Origin          string
	ServiceProvider ServiceProvider
}

// Publication may be shared with the selected local relying party. No
// credential or key appears here. Every New creates a fresh tenant and pin.
type Publication struct {
	Directory
	Issuer                string
	MetadataURL           string
	SSOURL                string
	SigningCertificateDER []byte
}

type pending struct {
	request *saml.IdpAuthnRequest
	browser [sha256.Size]byte
	expires time.Time
}

// Fixture is safe for concurrent loopback use; the launcher owns the HTTPS
// listener, temporary TLS trust files/credentials, and calls Close on teardown.
type Fixture struct {
	origin    string
	acsOrigin string
	acs       string
	cookie    string
	public    Publication
	idp       *saml.IdentityProvider
	metadata  []byte
	expires   time.Time

	mu              sync.Mutex
	closed, used    bool
	attempts        int
	bootstrapDigest [sha256.Size]byte
	pending         map[string]pending
}

// New provisions one account and returns the one-time login code to the
// trusted caller exactly once. The caller must deliver it via a private
// per-run file/channel; it must never be placed in argv, URLs, or logs.
func New(config Config) (*Fixture, string, error) {
	origin, err := exactLoopbackURL(config.Origin, false)
	if err != nil {
		return nil, "", err
	}
	acs, err := exactLoopbackURL(config.ServiceProvider.ACS, true)
	if err != nil || acs.Host == origin.Host {
		return nil, "", errors.New("a separate exact local HTTPS ACS is required")
	}
	if !validEntityID(config.ServiceProvider.EntityID) {
		return nil, "", errors.New("an exact local or opaque URN service-provider entity ID is required")
	}
	dir := newDirectory()
	now := time.Now().UTC()
	code := randomOpaque()
	f := &Fixture{origin: config.Origin, acs: config.ServiceProvider.ACS, acsOrigin: "https://" + acs.Host,
		expires: now.Add(runLifetime), cookie: "__Host-tcsim-entra-" + dir.TenantID,
		pending: make(map[string]pending), bootstrapDigest: sha256.Sum256([]byte(code))}
	f.public = Publication{Directory: dir, SSOURL: f.origin + "/" + dir.TenantID + "/saml2",
		MetadataURL: f.origin + "/" + dir.TenantID + "/federationmetadata/2007-06/federationmetadata.xml"}
	f.public.Issuer = f.public.MetadataURL
	if err := f.configureSAML(config.ServiceProvider, now); err != nil {
		return nil, "", err
	}
	return f, code, nil
}

// Public returns an immutable copy of the registration handoff.
func (f *Fixture) Public() Publication {
	p := f.public
	p.SigningCertificateDER = slices.Clone(p.SigningCertificateDER)
	return p
}

// Close stops issuance and discards the one-time credential and pending state.
// Assertions issued before shutdown have their own short validity interval.
func (f *Fixture) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.bootstrapDigest = [sha256.Size]byte{}
	clear(f.pending)
}

func randomOpaque() string {
	var b [32]byte
	rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func canonicalOpaque(s string) bool {
	if len(s) != 43 {
		return false
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == s
}

func exactLoopbackURL(raw string, requirePath bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() != "127.0.0.1" || u.Opaque != "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" ||
		u.Port() == "" || len(raw) > 384 || u.String() != raw {
		return nil, errors.New("an exact numeric-loopback HTTPS URL is required")
	}
	address, err := netip.ParseAddrPort(u.Host)
	if err != nil || address.String() != u.Host || address.Port() == 0 {
		return nil, errors.New("an exact numeric-loopback HTTPS port is required")
	}
	if requirePath {
		if !boundedURLPath.MatchString(u.Path) {
			return nil, errors.New("a bounded exact local HTTPS path is required")
		}
		for _, segment := range strings.Split(u.Path, "/") {
			if segment == "." || segment == ".." {
				return nil, errors.New("dot-segments are not permitted")
			}
		}
	} else if u.Path != "" {
		return nil, errors.New("origin must not contain a path")
	}
	return u, nil
}

func unsafeHeader(name string) bool {
	name = strings.ToLower(name)
	return name == "authorization" || name == "forwarded" || strings.HasPrefix(name, "x-forwarded-")
}

func (f *Fixture) admitted(r *http.Request) bool {
	if r.TLS == nil || "https://"+r.Host != f.origin || r.URL.IsAbs() || r.URL.RawPath != "" ||
		r.URL.ForceQuery || r.URL.Fragment != "" || len(r.Header.Values("Origin")) > 1 {
		return false
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || peer.Addr().String() != "127.0.0.1" || peer.Port() == 0 {
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != f.origin {
		return false
	}
	for k := range r.Header {
		if unsafeHeader(k) {
			return false
		}
	}
	return true
}

func (f *Fixture) cookieFor(value string, age int) *http.Cookie {
	return &http.Cookie{Name: f.cookie, Value: value, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: age}
}

func securityHeaders(h http.Header, action string) {
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action "+action)
}

func refused(w http.ResponseWriter, status int) {
	http.Error(w, "Local synthetic SAML request refused.", status)
}

func (f *Fixture) current(now time.Time) bool { return !f.closed && now.Before(f.expires) }

func (f *Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w.Header(), "'none'")
	if !f.admitted(r) {
		refused(w, http.StatusForbidden)
		return
	}
	f.mu.Lock()
	active := f.current(time.Now())
	f.mu.Unlock()
	if !active {
		refused(w, http.StatusGone)
		return
	}
	switch "https://" + r.Host + r.URL.Path {
	case f.public.MetadataURL:
		if r.Method != http.MethodGet || r.ContentLength != 0 || r.URL.RawQuery != "" || len(r.TransferEncoding) != 0 {
			refused(w, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/samlmetadata+xml")
		_, _ = w.Write(f.metadata)
	case f.public.SSOURL:
		switch r.Method {
		case http.MethodGet:
			f.begin(w, r)
		case http.MethodPost:
			f.finish(w, r)
		default:
			refused(w, http.StatusMethodNotAllowed)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *Fixture) begin(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) > queryLimit {
		refused(w, http.StatusBadRequest)
		return
	}
	req, err := f.signedRequest(r)
	if err != nil {
		refused(w, http.StatusBadRequest)
		return
	}
	browser, flow := randomOpaque(), randomOpaque()
	f.mu.Lock()
	now := time.Now().UTC()
	for id, v := range f.pending {
		if !now.Before(v.expires) {
			delete(f.pending, id)
		}
	}
	expires := earlier(req.Request.IssueInstant.Add(AuthnRequestLifetime), f.expires)
	ok := f.current(now) && now.Before(expires) && !f.used && len(f.pending) < maxPending
	for _, known := range f.pending {
		if known.request.Request.ID == req.Request.ID {
			ok = false
		}
	}
	if ok {
		f.pending[flow] = pending{request: req, browser: sha256.Sum256([]byte(browser)), expires: expires}
	}
	f.mu.Unlock()
	if !ok {
		refused(w, http.StatusBadRequest)
		return
	}
	http.SetCookie(w, f.cookieFor(browser, int(AuthnRequestLifetime.Seconds())))
	renderForm(w, loginPage, struct{ Action, Flow string }{f.public.SSOURL, flow}, f.origin, "")
}

func (f *Fixture) finish(w http.ResponseWriter, r *http.Request) {
	ct, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || ct != "application/x-www-form-urlencoded" || len(r.Header.Values("Content-Type")) != 1 ||
		r.Header.Get("Origin") != f.origin || r.URL.RawQuery != "" {
		refused(w, http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if r.ParseForm() != nil || len(r.PostForm) != 2 || len(r.PostForm["flow"]) != 1 || len(r.PostForm["code"]) != 1 {
		refused(w, http.StatusBadRequest)
		return
	}
	flow, code := r.PostForm.Get("flow"), r.PostForm.Get("code")
	cookies := r.CookiesNamed(f.cookie)
	if len(cookies) != 1 || !canonicalOpaque(flow) || !canonicalOpaque(cookies[0].Value) {
		refused(w, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	item, known := f.pending[flow]
	now := time.Now().UTC()
	browser := sha256.Sum256([]byte(cookies[0].Value))
	if !known || !f.current(now) || f.used || !now.Before(item.expires) ||
		subtle.ConstantTimeCompare(browser[:], item.browser[:]) != 1 || f.attempts >= maxCodeAttempts {
		f.mu.Unlock()
		refused(w, http.StatusBadRequest)
		return
	}
	f.attempts++
	digest := sha256.Sum256([]byte(code))
	if !canonicalOpaque(code) || subtle.ConstantTimeCompare(digest[:], f.bootstrapDigest[:]) != 1 {
		f.mu.Unlock()
		refused(w, http.StatusBadRequest)
		return
	}
	// Consume before signing, including on internal errors. A concurrent
	// request cannot sign a second assertion using the same credential.
	f.used = true
	f.bootstrapDigest = [sha256.Size]byte{}
	clear(f.pending)
	p, err := f.response(item.request, now)
	f.mu.Unlock()
	if err != nil {
		refused(w, http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, f.cookieFor("", -1))
	nonce := randomOpaque()
	renderForm(w, postPage, struct {
		saml.IdpAuthnRequestForm
		Nonce string
	}{p, nonce}, f.acsOrigin, nonce)
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func renderForm(w http.ResponseWriter, tmpl *template.Template, value any, action, scriptNonce string) {
	var page bytes.Buffer
	if err := tmpl.Execute(&page, value); err != nil {
		refused(w, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	securityHeaders(h, action)
	// Native non-CORS form POSTs with no-referrer serialize Origin as null.
	// Share only the origin, never the path/query; both receivers can still
	// enforce their exact Origin allowlist. Metadata/errors use no-referrer.
	h.Set("Referrer-Policy", "origin")
	if scriptNonce != "" {
		h.Set("Content-Security-Policy", h.Get("Content-Security-Policy")+"; script-src 'nonce-"+scriptNonce+"'")
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	_, _ = page.WriteTo(w)
}

var loginPage = template.Must(template.New("synthetic-login").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Local synthetic sign-in</title></head>
<body><h1>Local synthetic sign-in</h1><p>Use only the one-time code from this disposable fixture.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="flow" value="{{.Flow}}">
<label for="code">One-time synthetic login code</label>
<input id="code" name="code" type="password" autocomplete="off" required>
<button type="submit">Sign in synthetic identity</button></form></body></html>`))

var postPage = template.Must(template.New("local-return").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Return to local application</title></head>
<body><form id="return" method="post" action="{{.URL}}"><input type="hidden" name="SAMLResponse" value="{{.SAMLResponse}}">
{{if .RelayState}}<input type="hidden" name="RelayState" value="{{.RelayState}}">{{end}}
<button type="submit">Continue to local application</button></form>
<script nonce="{{.Nonce}}">document.getElementById('return').submit();</script></body></html>`))
