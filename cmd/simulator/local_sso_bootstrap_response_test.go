package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalSSOBootstrapOnlyAcceptsUsableEndpointResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"successful metadata", http.StatusOK, "<EntityDescriptor/>", ""},
		{"service unavailable", http.StatusServiceUnavailable, "starting", "local SSO endpoint refused the operation"},
		{"oversized response", http.StatusOK, strings.Repeat("x", localSSOLimit+1), "local SSO response exceeds its limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := localSSOGet(t.Context(), srv.Client(), srv.URL)
			if tc.want != "" {
				if err == nil || err.Error() != tc.want || got != nil {
					t.Fatalf("unusable endpoint was not rejected: error=%v", err)
				}
				return
			}
			if err != nil || string(got) != tc.body {
				t.Fatalf("usable metadata was not returned: error=%v", err)
			}
		})
	}
	t.Run("wrong shape cannot seed local Cognito", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.Header.Get("X-Amz-Target") == "" || r.Header.Get("Authorization") == "" {
				t.Error("bootstrap request did not use its ordinary signed protocol")
			}
			_, _ = w.Write([]byte(`{"UserPool":"not the response shape"}`))
		}))
		defer srv.Close()
		var pool struct{ UserPool struct{ ID string } }
		err := localSSOAWS(t.Context(), srv.Client(), srv.URL, "synthetic-test-secret", "CreateUserPool", struct{}{}, &pool)
		if err == nil || err.Error() != "invalid local Cognito CreateUserPool response" || pool.UserPool.ID != "" {
			t.Fatalf("incompatible response did not fail closed: error=%v", err)
		}
	})
	t.Run("closed endpoint is not ready", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := "http://" + listener.Addr().String()
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport}
		got, err := localSSOGet(t.Context(), client, address)
		if err == nil || err.Error() != "local SSO endpoint not ready or transport refused" || got != nil {
			t.Fatalf("closed endpoint was reported ready: error=%v", err)
		}
	})
}
