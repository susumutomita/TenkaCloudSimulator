package server

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccessIdentityAndTeamBoundary(t *testing.T) {
	operator := strings.Repeat("a", 64)
	var seen string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Simulator-Principal")
		w.WriteHeader(204)
	})
	a, err := NewAccess(handler, operator, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewAccess(handler, strings.Repeat("b", 64), false)
	request := func(h http.Handler, method, path, body, token string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:4566"+path, strings.NewReader(body))
		r.Header.Set("X-Simulator-Token", token)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	issue := request(a, "POST", "/_simulator/v1/access", `{"Principal":"reader"}`, operator, nil)
	var issued struct{ AccessToken string }
	if issue.Code != 200 || json.Unmarshal(issue.Body.Bytes(), &issued) != nil {
		t.Fatal(issue)
	}
	for _, token := range []string{operator, issued.AccessToken} {
		if got := request(b, "GET", "/", "", token, nil); got.Code != 401 {
			t.Fatal("cross-team token accepted", got)
		}
	}
	got := request(a, "GET", "/", "", issued.AccessToken, nil)
	if got.Code != 204 || seen != "reader" {
		t.Fatal(got, seen)
	}
	if got = request(a, "GET", "/", "", issued.AccessToken, map[string]string{"X-Simulator-Principal": "simulator-admin"}); got.Code != 403 {
		t.Fatal(got)
	}
	if got = request(a, "GET", "/_simulator/v1/capabilities", "", issued.AccessToken, map[string]string{"X-Simulator-Principal": "simulator-admin"}); got.Code != 204 || seen != "reader" {
		t.Fatal(got, seen)
	}
	if got = request(a, "POST", "/_simulator/v1/access", `{"Principal":"other"}`, issued.AccessToken, nil); got.Code != 403 {
		t.Fatal(got)
	}
	for _, body := range []string{`{`, `{"Principal":"simulator-admin"}`, `{"Principal":"bad/name"}`, `{"Principal":"x","Extra":1}`, `{"Principal":"reader"}{}`} {
		if got = request(a, "POST", "/_simulator/v1/access", body, operator, nil); got.Code != 400 {
			t.Fatal(got)
		}
	}
	if got = request(a, "GET", "/_simulator/v1/access", "", operator, nil); got.Code != 405 {
		t.Fatal(got)
	}
	for _, headers := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		if got = request(a, "GET", "/", "", operator, headers); got.Code != 403 {
			t.Fatal(got)
		}
	}
	r := httptest.NewRequest("GET", "http://evil.example/", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w)
	}
	for _, path := range []string{"/_simulator/console/", "/_simulator/docs/"} {
		if got = request(a, "GET", path, "", "", nil); got.Code != 303 {
			t.Fatal(got)
		}
	}
	for _, path := range []string{"/_simulator/login", "/_simulator/console/app.js", "/_simulator/console/app.css"} {
		if got = request(a, "GET", path, "", "", nil); got.Code != 204 {
			t.Fatal(got)
		}
	}
	if got = request(a, "PUT", "/_simulator/login", "", "", nil); got.Code != 405 {
		t.Fatal(got)
	}
	if got = request(a, "POST", "/_simulator/login", "token=invalid", "", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}); got.Code != 401 {
		t.Fatal(got)
	}
	login := request(a, "POST", "/_simulator/login", "token="+issued.AccessToken, "", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if login.Code != 303 {
		t.Fatal(login)
	}
	cookies := login.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Name == b.cookie {
		t.Fatal(cookies)
	}
	if got = request(a, "GET", "/", "", "", map[string]string{"Cookie": cookies[0].String()}); got.Code != 204 {
		t.Fatal(got)
	}
	a.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if got = request(a, "GET", "/", "", issued.AccessToken, nil); got.Code != 401 {
		t.Fatal(got)
	}
	// Bound session allocation and cleanup operate without retaining plaintext tokens.
	a.now = time.Now
	for i := 0; i < 1000; i++ {
		sum := sha256.Sum256([]byte(string(rune(i))))
		a.sessions[sum] = accessSession{"reader", time.Now().Add(time.Minute)}
	}
	if got = request(a, "POST", "/_simulator/v1/access", `{"Principal":"reader"}`, operator, nil); got.Code != 429 {
		t.Fatal(got)
	}
	a.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if got = request(a, "POST", "/_simulator/v1/access", `{"Principal":"reader"}`, operator, nil); got.Code != 200 {
		t.Fatal(got)
	}
	for _, token := range []string{"short", strings.Repeat("x", 257), strings.Repeat("!", 40)} {
		if _, err = NewAccess(handler, token, false); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
}

func TestAccessLoginBoundsAndRemoteHost(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	a, _ := NewAccess(next, strings.Repeat("x", 64), true)
	for _, host := range []string{"127.0.0.1:4566", "[::1]:4566", "localhost:4566", "192.0.2.1:4566"} {
		r := httptest.NewRequest("GET", "http://"+host+"/", nil)
		r.Header.Set("X-Simulator-Token", strings.Repeat("x", 64))
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatal(host, w)
		}
	}
	for _, body := range []string{"%zz", strings.Repeat("x", 5000)} {
		r := httptest.NewRequest("POST", "http://127.0.0.1/_simulator/login", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w)
		}
	}
	// Valid same-origin traffic, including a secure session cookie, stays usable.
	r := httptest.NewRequest("POST", "https://127.0.0.1/_simulator/login", strings.NewReader("token="+strings.Repeat("x", 64)))
	r.Header.Set("Origin", "https://127.0.0.1")
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 303 || !w.Result().Cookies()[0].Secure {
		t.Fatal(w)
	}
}
