package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type accessSession struct {
	principal string
	expires   time.Time
}

// Access binds opaque local credentials to exercise identities. A separate
// Access instance and database must be assigned to each team by the host.
type Access struct {
	next     http.Handler
	operator [32]byte
	cookie   string
	remote   bool
	mu       sync.Mutex
	sessions map[[32]byte]accessSession
	now      func() time.Time
}

func NewAccess(next http.Handler, operatorToken string, allowRemote bool) (*Access, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{32,256}$`).MatchString(operatorToken) {
		return nil, fmt.Errorf("operator token must contain 32–256 URL-safe characters")
	}
	sum := sha256.Sum256([]byte(operatorToken))
	return &Access{next: next, operator: sum, cookie: "tcsim_" + hex.EncodeToString(sum[:6]), remote: allowRemote, sessions: map[[32]byte]accessSession{}, now: time.Now}, nil
}
func (a *Access) identity(token string) (string, bool) {
	sum := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(sum[:], a.operator[:]) == 1 {
		return "simulator-admin", true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[sum]
	if !ok || !a.now().Before(session.expires) {
		delete(a.sessions, sum)
		return "", false
	}
	return session.principal, true
}
func (a *Access) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; form-action 'self'")
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !a.remote && !ip.IsLoopback()) {
		http.Error(w, "Unrecognized simulator host", 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "Cross-origin request rejected", 403)
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Cross-site request rejected", 403)
		return
	}
	if r.URL.Path == "/_simulator/login" {
		if r.Method == "GET" {
			clone := r.Clone(r.Context())
			u := *r.URL
			clone.URL = &u
			clone.URL.Path = "/_simulator/console/"
			a.next.ServeHTTP(w, clone)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if r.ParseForm() != nil {
			http.Error(w, "Invalid sign-in request", 400)
			return
		}
		token := r.PostForm.Get("token")
		if _, ok := a.identity(token); !ok {
			http.Error(w, "Invalid or expired access token", 401)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: a.cookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
		http.Redirect(w, r, "/_simulator/console/", 303)
		return
	}
	// Bootstrap assets contain no exercise values or credentials.
	if r.Method == "GET" && (strings.HasSuffix(r.URL.Path, ".js") || strings.HasSuffix(r.URL.Path, ".css")) && strings.HasPrefix(r.URL.Path, "/_simulator/console/") {
		a.next.ServeHTTP(w, r)
		return
	}
	token := r.Header.Get("X-Simulator-Token")
	if token == "" {
		if cookie, e := r.Cookie(a.cookie); e == nil {
			token = cookie.Value
		}
	}
	principal, ok := a.identity(token)
	if !ok {
		if r.Method == "GET" && (strings.HasPrefix(r.URL.Path, "/_simulator/console/") || strings.HasPrefix(r.URL.Path, "/_simulator/docs/")) {
			http.Redirect(w, r, "/_simulator/login", 303)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(401)
		_, _ = fmt.Fprint(w, `{"error":"A valid simulator access token is required"}`)
		return
	}
	r.Header.Set("X-Simulator-Access-Role", "operator")
	if principal != "simulator-admin" {
		r.Header.Set("X-Simulator-Access-Role", "participant")
		if selected := r.Header.Get("X-Simulator-Principal"); selected != "" && selected != principal && r.URL.Path != "/_simulator/v1/capabilities" {
			http.Error(w, "Cannot select another identity with this access token", 403)
			return
		}
		r.Header.Set("X-Simulator-Principal", principal)
	}
	if r.URL.Path == "/_simulator/v1/access" {
		if principal != "simulator-admin" {
			http.Error(w, "Only the host may issue participant access", 403)
			return
		}
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		var input struct{ Principal string }
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF || !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(input.Principal) || input.Principal == "simulator-admin" {
			http.Error(w, "Choose a non-reserved exercise identity", 400)
			return
		}
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			http.Error(w, "Access issuance failed", 500)
			return
		}
		newToken := hex.EncodeToString(random)
		a.mu.Lock()
		for key, session := range a.sessions {
			if !a.now().Before(session.expires) {
				delete(a.sessions, key)
			}
		}
		if len(a.sessions) >= 1000 {
			a.mu.Unlock()
			http.Error(w, "Access-session limit reached", 429)
			return
		}
		expires := a.now().Add(15 * time.Minute)
		a.sessions[sha256.Sum256([]byte(newToken))] = accessSession{input.Principal, expires}
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": newToken, "Principal": input.Principal, "ExpiresAt": expires.Format(time.RFC3339)})
		return
	}
	a.next.ServeHTTP(w, r)
}
