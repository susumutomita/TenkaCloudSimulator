package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func browserAccess(t *testing.T) (*Access, string) {
	t.Helper()
	operator := strings.Repeat("o", 64)
	a, err := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Seen-Principal", r.Header.Get("X-Simulator-Principal"))
		w.Header().Set("Seen-Role", r.Header.Get("X-Simulator-Access-Role"))
		w.WriteHeader(204)
	}), operator, false)
	if err != nil {
		t.Fatal(err)
	}
	return a, operator
}
func browserRequest(a *Access, method, path, body, token string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("X-Simulator-Token", token)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for key, value := range headers {
		r.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func browserSignIn(t *testing.T, a *Access, token string, old *http.Cookie) *http.Cookie {
	t.Helper()
	w := browserRequest(a, "POST", "/_simulator/login", url.Values{"token": {token}}.Encode(), "", old, map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if w.Code != 303 {
		t.Fatalf("sign-in status: %d", w.Code)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("expected one browser cookie")
	}
	return cookies[0]
}
func participantCredential(t *testing.T, a *Access, operator string) string {
	t.Helper()
	w := browserRequest(a, "POST", "/_simulator/v1/access", `{"Principal":"reader"}`, operator, nil, nil)
	var result struct{ AccessToken string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.AccessToken == "" {
		t.Fatalf("participant issuance status: %d", w.Code)
	}
	return result.AccessToken
}
func TestBrowserSessionLogoutAndCredentialSeparation(t *testing.T) {
	a, operator := browserAccess(t)
	participant := participantCredential(t, a, operator)
	for _, credential := range []string{operator, participant} {
		cookie := browserSignIn(t, a, credential, nil)
		if cookie.Value == credential || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Expires.IsZero() {
			t.Fatal("browser credential/cookie attributes invalid")
		}
		if got := browserRequest(a, "GET", "/", "", "", cookie, nil); got.Code != 204 {
			t.Fatalf("browser authentication status: %d", got.Code)
		}
		if got := browserRequest(a, "GET", "/", "", cookie.Value, nil, nil); got.Code != 401 {
			t.Fatal("browser credential accepted as SDK credential")
		}
		if got := browserRequest(a, "GET", "/", "", "", &http.Cookie{Name: a.cookie, Value: credential}, nil); got.Code != 401 {
			t.Fatal("raw SDK credential accepted in browser cookie")
		}
		other := browserSignIn(t, a, credential, nil)
		logout := browserRequest(a, "POST", "/_simulator/logout", "", operator, cookie, nil)
		if logout.Code != 204 || logout.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("logout did not succeed without caching")
		}
		cleared := logout.Result().Cookies()
		if len(cleared) != 1 || cleared[0].Value != "" || cleared[0].MaxAge != -1 || !cleared[0].HttpOnly || cleared[0].SameSite != http.SameSiteStrictMode || !cleared[0].Expires.Before(time.Now()) {
			t.Fatal("logout did not expire the browser cookie")
		}
		if got := browserRequest(a, "GET", "/", "", "", cookie, nil); got.Code != 401 {
			t.Fatal("revoked browser cookie replay accepted")
		}
		for _, sdk := range []string{operator, participant} {
			if got := browserRequest(a, "GET", "/", "", sdk, nil, nil); got.Code != 204 {
				t.Fatal("logout revoked an SDK credential")
			}
		}
		if got := browserRequest(a, "GET", "/", "", "", other, nil); got.Code != 204 {
			t.Fatal("logout revoked another browser session")
		}
		if got := browserRequest(a, "POST", "/_simulator/logout", "", "", cookie, nil); got.Code != 204 {
			t.Fatal("repeated logout is not idempotent")
		}
		fresh := browserSignIn(t, a, credential, other)
		if got := browserRequest(a, "GET", "/", "", "", other, nil); got.Code != 401 {
			t.Fatal("re-login did not rotate the old browser session")
		}
		if got := browserRequest(a, "GET", "/", "", "", fresh, nil); got.Code != 204 {
			t.Fatal("re-login cookie unusable")
		}
	}
	if got := browserRequest(a, "POST", "/_simulator/logout", "", "", nil, nil); got.Code != 204 {
		t.Fatal("anonymous logout is not idempotent")
	}
	if got := browserRequest(a, "GET", "/_simulator/logout", "", operator, nil, nil); got.Code != 405 || got.Header().Get("Allow") != "POST" {
		t.Fatal("logout accepts a non-POST method")
	}
}
func TestBrowserParentBindingAndExpiry(t *testing.T) {
	for _, scenario := range []string{"parent-expired", "parent-deleted", "parent-changed", "browser-expired"} {
		t.Run(scenario, func(t *testing.T) {
			a, operator := browserAccess(t)
			start := time.Unix(1800000000, 0)
			a.now = func() time.Time { return start }
			token := participantCredential(t, a, operator)
			sum := sha256.Sum256([]byte(token))
			parent := a.sessions[sum]
			parent.expires = start.Add(2 * time.Minute)
			a.sessions[sum] = parent
			cookie := browserSignIn(t, a, token, nil)
			browserHash := sha256.Sum256([]byte(cookie.Value))
			session := a.browsers[browserHash]
			if !session.expires.Equal(parent.expires) || cookie.Expires.After(parent.expires) || session.parent != sum {
				t.Fatal("browser does not inherit parent expiry/hash")
			}
			switch scenario {
			case "parent-expired":
				// Parent validity must be checked even if a browser entry outlives it.
				session.expires = start.Add(time.Hour)
				a.browsers[browserHash] = session
				a.now = func() time.Time { return start.Add(3 * time.Minute) }
			case "parent-deleted":
				delete(a.sessions, sum)
			case "parent-changed":
				parent.principal = "other"
				a.sessions[sum] = parent
			case "browser-expired":
				a.now = func() time.Time { return start.Add(2 * time.Minute) }
			}
			if got := browserRequest(a, "GET", "/", "", "", cookie, nil); got.Code != 401 {
				t.Fatal("invalid parent/browser session accepted")
			}
			if _, exists := a.browsers[browserHash]; exists {
				t.Fatal("expired/revoked browser hash retained")
			}
			if got := browserRequest(a, "GET", "/", "", operator, nil, nil); got.Code != 204 {
				t.Fatal("operator SDK authority affected")
			}
		})
	}
	a, operator := browserAccess(t)
	start := time.Unix(1800000000, 0)
	a.now = func() time.Time { return start }
	cookie := browserSignIn(t, a, operator, nil)
	if !a.browsers[sha256.Sum256([]byte(cookie.Value))].expires.Equal(start.Add(15 * time.Minute)) {
		t.Fatal("ordinary browser TTL is not 15 minutes")
	}
	a.now = func() time.Time { return start.Add(15 * time.Minute) }
	if got := browserRequest(a, "GET", "/", "", "", cookie, nil); got.Code != 401 {
		t.Fatal("operator browser TTL not enforced")
	}
}
func TestBrowserSessionAuthorityAndOrigin(t *testing.T) {
	a, operator := browserAccess(t)
	token := participantCredential(t, a, operator)
	cookie := browserSignIn(t, a, token, nil)
	got := browserRequest(a, "GET", "/", "", "", cookie, map[string]string{"X-Simulator-Principal": "reader", "X-Simulator-Access-Role": "operator"})
	if got.Code != 204 || got.Header().Get("Seen-Principal") != "reader" || got.Header().Get("Seen-Role") != "participant" {
		t.Fatal("participant identity/role not bound")
	}
	if got := browserRequest(a, "GET", "/", "", "", cookie, map[string]string{"X-Simulator-Principal": "simulator-admin"}); got.Code != 403 {
		t.Fatal("participant selected admin")
	}
	if got := browserRequest(a, "POST", "/_simulator/v1/access", `{"Principal":"other"}`, "", cookie, nil); got.Code != 403 {
		t.Fatal("participant browser issued credentials")
	}
	if got := browserRequest(a, "GET", "/", "", "invalid", cookie, nil); got.Code != 401 {
		t.Fatal("invalid explicit SDK header fell back to cookie")
	}
	other, _ := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), operator, false)
	if got := browserRequest(other, "GET", "/", "", "", cookie, nil); got.Code != 401 {
		t.Fatal("browser cookie crossed Access instances sharing an operator token")
	}
	for _, origin := range []string{"null", "https://127.0.0.1:4566", "http://127.0.0.1", "http://127.0.0.1:4567", "http://evil.example", "http://user@127.0.0.1:4566", "http://127.0.0.1:4566/", "http://127.0.0.1:4566?", "http://127.0.0.1:4566?x=1"} {
		for _, path := range []string{"/_simulator/login", "/_simulator/logout"} {
			if got := browserRequest(a, "POST", path, "", "", cookie, map[string]string{"Origin": origin}); got.Code != 403 {
				t.Fatalf("invalid origin accepted on %s", path)
			}
		}
	}
	if got := browserRequest(a, "POST", "/_simulator/logout", "", "", cookie, map[string]string{"Sec-Fetch-Site": "cross-site"}); got.Code != 403 {
		t.Fatal("cross-site logout accepted")
	}
	if got := browserRequest(a, "GET", "/", "", "", cookie, nil); got.Code != 204 {
		t.Fatal("rejected cross-origin logout revoked the session")
	}
	if got := browserRequest(a, "POST", "/_simulator/logout", "", "", cookie, map[string]string{"Origin": "http://127.0.0.1:4566", "Sec-Fetch-Site": "same-origin"}); got.Code != 204 {
		t.Fatal("same-origin logout rejected")
	}
	if got := browserRequest(a, "GET", "/_simulator/v1/capabilities", "", operator, nil, nil); got.Header().Get("X-Simulator-Authentication") != "required" {
		t.Fatal("capabilities lacks authentication mode")
	}
	if got := browserRequest(a, "GET", "/", "", operator, nil, nil); got.Header().Get("X-Simulator-Authentication") != "" {
		t.Fatal("authentication metadata added to cloud wire response")
	}
}
func TestBrowserSessionQuotaCleanupAndConcurrentRevocation(t *testing.T) {
	a, operator := browserAccess(t)
	start := time.Unix(1800000000, 0)
	a.now = func() time.Time { return start }
	for i := 0; i < 999; i++ {
		a.browsers[sha256.Sum256([]byte(fmt.Sprint(i)))] = browserSession{accessSession{"simulator-admin", start.Add(time.Minute)}, a.operator}
	}
	cookie := browserSignIn(t, a, operator, nil)
	if len(a.browsers) != 1000 {
		t.Fatal("unexpected browser allocation")
	}
	if got := browserRequest(a, "POST", "/_simulator/login", url.Values{"token": {operator}}.Encode(), "", nil, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); got.Code != 429 {
		t.Fatal("browser quota not enforced")
	}
	cookie = browserSignIn(t, a, operator, cookie)
	if len(a.browsers) != 1000 {
		t.Fatal("replacement consumes extra browser slots")
	}
	a.now = func() time.Time { return start.Add(16 * time.Minute) }
	browserSignIn(t, a, operator, nil)
	if len(a.browsers) != 1 {
		t.Fatal("expired browser entries not pruned")
	}
	// Session lookup/logout share a lock; after logout completes every replay fails.
	cookie = browserSignIn(t, a, operator, nil)
	var group sync.WaitGroup
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func() { defer group.Done(); browserRequest(a, "GET", "/", "", "", cookie, nil) }()
	}
	browserRequest(a, "POST", "/_simulator/logout", "", "", cookie, nil)
	group.Wait()
	if got := browserRequest(a, "GET", "/", "", "", cookie, nil); got.Code != 401 {
		t.Fatal("concurrent logout replay accepted")
	}
}
func TestBrowserSecureCookieLogout(t *testing.T) {
	a, operator := browserAccess(t)
	r := httptest.NewRequest("POST", "https://127.0.0.1:4566/_simulator/login", strings.NewReader(url.Values{"token": {operator}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://127.0.0.1:4566")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 303 || !w.Result().Cookies()[0].Secure {
		t.Fatal("TLS browser cookie is not secure")
	}
	r = httptest.NewRequest("POST", "https://127.0.0.1:4566/_simulator/logout", nil)
	r.AddCookie(w.Result().Cookies()[0])
	r.Header.Set("Origin", "https://127.0.0.1:4566")
	w = httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 204 || !w.Result().Cookies()[0].Secure {
		t.Fatal("TLS logout cookie attributes changed")
	}
}
