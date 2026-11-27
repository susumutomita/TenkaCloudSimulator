package main

import (
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalSSOHTTPKeepsBootstrapInsideFixedTrustedEndpoints(t *testing.T) {
	open := func() *localSSOListener {
		t.Helper()
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal("could not bind own isolated fixture")
		}
		s := &localSSOListener{Listener: listener}
		t.Cleanup(func() {
			if err := s.close(); err != nil {
				t.Error("could not cleanly close own fixture listener")
			}
		})
		return s
	}
	_, roots, knownLeaves, err := localSSOTLS(2, time.Now())
	if err != nil {
		t.Fatal("could not create the synthetic run trust")
	}
	_, _, otherLeaves, err := localSSOTLS(1, time.Now())
	if err != nil {
		t.Fatal("could not create independent synthetic trust")
	}
	known, unrelated, foreignTrust, closed := open(), open(), open(), open()
	var followed, outside, foreign atomic.Int32

	known.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_, _ = io.WriteString(w, "fixture ready")
		case "/redirect":
			http.Redirect(w, r, "/followed", http.StatusFound)
		case "/followed":
			followed.Add(1)
			_, _ = io.WriteString(w, "redirect unexpectedly followed")
		default:
			http.NotFound(w, r)
		}
	}), knownLeaves[0])
	count := func(counter *atomic.Int32) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			_, _ = io.WriteString(w, "independent fixture")
		}
	}
	// The unrelated endpoint uses the trusted CA on purpose: the address
	// allowlist, not a TLS validation failure, must prevent the request.
	unrelated.serve(count(&outside), knownLeaves[1])
	foreignTrust.serve(count(&foreign), otherLeaves[0])
	closedOrigin := closed.origin()
	if err := closed.close(); err != nil {
		t.Fatal("could not close own allowed target")
	}

	client, stop := localSSOHTTP(roots, []*localSSOListener{known, foreignTrust, closed})
	defer stop()
	get := func(origin, path string) (*http.Response, error) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin+path, nil)
		if err != nil {
			t.Fatal("invalid fixture request")
		}
		return client.Do(req)
	}
	witness := func(origin, path string, status int, body string) http.Header {
		t.Helper()
		resp, err := get(origin, path)
		if err != nil {
			t.Fatal("fixed trusted endpoint did not respond")
		}
		defer resp.Body.Close()
		if resp.StatusCode != status {
			t.Fatalf("unexpected fixture status: %d", resp.StatusCode)
		}
		if body != "" {
			got, err := io.ReadAll(io.LimitReader(resp.Body, 256))
			if err != nil || string(got) != body {
				t.Fatal("trusted endpoint did not return the fixture result")
			}
		}
		return resp.Header
	}
	witness(known.origin(), "/health", http.StatusOK, "fixture ready")
	h := witness(known.origin(), "/redirect", http.StatusFound, "")
	if h.Get("Location") != "/followed" || followed.Load() != 0 {
		t.Fatal("bootstrap client followed a same-origin redirect")
	}
	for _, tc := range []struct {
		name, origin, wantError string
		unknownCA               bool
	}{
		{"unrelated trusted loopback", unrelated.origin(), "local SSO destination refused", false},
		{"allowed loopback with independent CA", foreignTrust.origin(), "", true},
		{"allowed loopback without listener", closedOrigin, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := get(tc.origin, "/health")
			if resp != nil {
				_ = resp.Body.Close()
			}
			if err == nil || resp != nil || tc.wantError != "" && !strings.Contains(err.Error(), tc.wantError) {
				t.Fatal("request did not fail at the expected transport boundary")
			}
			var untrusted x509.UnknownAuthorityError
			if tc.unknownCA && !errors.As(err, &untrusted) {
				t.Fatal("allowed endpoint did not fail certificate authority validation")
			}
		})
	}
	if outside.Load() != 0 || foreign.Load() != 0 || followed.Load() != 0 {
		t.Fatal("transport fallback or redirect reached a handler outside the intended request")
	}
}
