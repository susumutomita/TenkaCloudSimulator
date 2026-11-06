package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestLandingExactPublicAssetsAndMethods(t *testing.T) {
	a, err := NewAccess(http.HandlerFunc(serveLanding), strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	for requestPath, asset := range landingPaths {
		for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
			t.Run(method+requestPath, func(t *testing.T) {
				request := httptest.NewRequest(method, "http://127.0.0.1:4566"+requestPath, strings.NewReader("dummy body"))
				response := httptest.NewRecorder()
				a.ServeHTTP(response, request)
				if response.Header().Get("Set-Cookie") != "" || len(a.sessions) != 0 || len(a.browsers) != 0 {
					t.Fatal("product information created authentication state")
				}
				if method != http.MethodGet && method != http.MethodHead {
					if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
						t.Fatalf("non-read method: status %d", response.Code)
					}
					return
				}
				if response.Code != http.StatusOK || response.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("public asset: status %d", response.Code)
				}
				if response.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("missing response cache boundary")
				}
				if method == http.MethodHead {
					if response.Body.Len() != 0 || response.Header().Get("Content-Length") == "" {
						t.Fatal("HEAD did not preserve metadata without a body")
					}
					return
				}
				want, err := landingAssets.ReadFile(asset)
				if err != nil || response.Body.String() != string(want) {
					t.Fatal("response differs from the exact embedded asset")
				}
				if strings.HasSuffix(asset, ".mjs") && response.Header().Get("Content-Type") != "text/javascript; charset=utf-8" {
					t.Fatal("module MIME differs")
				}
			})
		}
	}
}

func TestLandingPublicAccessRetainsOriginAndHostBoundary(t *testing.T) {
	called := false
	a, err := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		serveLanding(w, r)
	}), strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, requestPath := range []string{"/_simulator/", "/_simulator/landing/landing.mjs"} {
		for _, variant := range []string{"host", "origin-host", "origin-scheme", "origin-user", "cross-site"} {
			t.Run(requestPath+variant, func(t *testing.T) {
				called = false
				r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566"+requestPath, nil)
				switch variant {
				case "host":
					r.Host = "untrusted.example"
				case "origin-host":
					r.Header.Set("Origin", "http://untrusted.example")
				case "origin-scheme":
					r.Header.Set("Origin", "https://127.0.0.1:4566")
				case "origin-user":
					r.Header.Set("Origin", "http://dummy@127.0.0.1:4566")
				case "cross-site":
					r.Header.Set("Sec-Fetch-Site", "cross-site")
				}
				w := httptest.NewRecorder()
				a.ServeHTTP(w, r)
				if w.Code != http.StatusForbidden || called {
					t.Fatal("public information bypassed host/origin validation")
				}
			})
		}
	}
}

func TestLandingAllowlistDoesNotExposeAPIOrUnknownPaths(t *testing.T) {
	called := false
	a, err := NewAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}), strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, requestPath := range []string{"/", "/bucket/key", "/_simulator/v1/iam/identities", "/_simulator/v1/access", "/_simulator/landing/unknown.mjs", "/_simulator/landing/index.html", "/_simulator/landing/../v1/access", "/_simulator/landing//landing.mjs", "/_simulator/landing/%2e%2e/v1/access", "/_simulator/landing/landing.mjs/extra"} {
		t.Run(requestPath, func(t *testing.T) {
			called = false
			w := httptest.NewRecorder()
			a.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566"+requestPath, nil))
			if isLandingPath(requestPath) || w.Code != http.StatusUnauthorized || called {
				t.Fatal("unknown or API path became public")
			}
		})
	}
}

func TestLandingCompositionPreservesSDKRootAndConsoleLogin(t *testing.T) {
	next, closeServer, err := New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeServer() })
	token := strings.Repeat("a", 64)
	a, err := NewAccess(next, token, false)
	if err != nil {
		t.Fatal(err)
	}
	request := func(requestPath, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4566"+requestPath, nil)
		if credential != "" {
			r.Header.Set("X-Simulator-Token", credential)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	if w := request("/_simulator/", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<!doctype html>") {
		t.Fatal("embedded product page is not available")
	}
	if w := request("/", ""); w.Code != http.StatusUnauthorized {
		t.Fatal("S3 endpoint became public")
	}
	if w := request("/", token); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ListAllMyBucketsResult") {
		t.Fatal("S3 root was replaced with a product page")
	}
	if w := request("/_simulator/console/", ""); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/_simulator/login" {
		t.Fatal("console login requirement changed")
	}
	if w := request("/_simulator/landing/unknown.mjs", token); w.Code != http.StatusNotFound {
		t.Fatal("unknown landing asset was served")
	}
}
