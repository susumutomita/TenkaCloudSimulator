package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLocalSSOCommandUsageDoesNotStartServices(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
	for _, tc := range []struct {
		name string
		args []string
		want string
		help bool
	}{
		{"help", []string{"--help"}, "Usage: tenkacloud-simulator local-sso", true},
		{"unexpected argument", []string{"unexpected"}, "local-sso takes no arguments", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := runLocalSSO(t.Context(), tc.args, &out)
			if tc.help {
				if !errors.Is(err, flag.ErrHelp) || !strings.Contains(out.String(), tc.want) {
					t.Fatal("help did not report the local-only command usage")
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatal("extraneous argument was not rejected")
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 || strings.Contains(out.String(), "sample ready") {
				t.Fatal("invalid CLI invocation initialized or advertised the sample")
			}
		})
	}
}

func TestLocalSSOStopsWholeRunAfterAnyBrowserListenerCloses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		index int
	}{{"Cognito", 1}, {"Entra", 2}, {"sample app", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			t.Setenv("TMP", dir)
			t.Setenv("TEMP", dir)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			ready := make(chan string, 1)
			done := make(chan error, 1)
			var listeners []net.Listener
			go func() {
				done <- runLocalSSOWithListener(ctx, nil, localSSOReadyWriter{ready}, func(network, address string) (net.Listener, error) {
					l, err := net.Listen(network, address)
					if err == nil {
						listeners = append(listeners, l)
					}
					return l, err
				})
			}()
			var stopped bool
			t.Cleanup(func() {
				cancel()
				if stopped {
					return
				}
				select {
				case <-done:
				case <-time.After(9 * time.Second):
					t.Error("launcher did not stop after fallback cancellation")
				}
			})
			select {
			case <-ready:
			case err := <-done:
				stopped = true
				t.Fatalf("launcher stopped before readiness: %v", err)
			case <-ctx.Done():
				t.Fatal("launcher did not become ready")
			}
			if len(listeners) != 4 {
				t.Fatal("unexpected number of isolated listeners")
			}
			addrs := make([]string, 0, len(listeners))
			for _, l := range listeners {
				addrs = append(addrs, l.Addr().String())
			}
			if err := listeners[tc.index].Close(); err != nil {
				t.Fatal("could not stop the fixture's selected listener")
			}
			select {
			case err := <-done:
				stopped = true
				if err == nil || err.Error() != "an isolated local SSO listener stopped" {
					t.Fatalf("unexpected listener exit was not reported: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Fatal("launcher did not shut down its peers after listener loss")
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatal("listener loss left private launch state")
			}
			for i, addr := range addrs {
				if conn, err := net.DialTimeout("tcp4", addr, time.Second); err == nil {
					_ = conn.Close()
					t.Errorf("listener loss left listener %d open", i)
				}
			}
		})
	}
}

func TestLocalSSOSampleBusyThenExpiredAttemptFreesSlot(t *testing.T) {
	const origin = "https://127.0.0.1:18443"
	p := newLocalSSORelyingParty(nil, "https://127.0.0.1:19443", "test-local-browser", origin)
	start := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/start", nil)
		req.Host = strings.TrimPrefix(origin, "https://")
		req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
		req.RemoteAddr = "127.0.0.1:23111"
		out := httptest.NewRecorder()
		p.ServeHTTP(out, req)
		return out
	}
	states := make(map[string]bool)
	var first string
	for i := range 4 {
		out := start()
		if out.Code != http.StatusSeeOther || len(out.Header().Values("Set-Cookie")) != 1 {
			t.Fatalf("login %d did not establish its own browser flow (status %d)", i, out.Code)
		}
		location, err := out.Result().Location()
		if err != nil {
			t.Fatal("missing normal authorize URL")
		}
		state := location.Query().Get("state")
		if !localSSOOpaque(state) || states[state] {
			t.Fatal("attempts did not get distinct state")
		}
		if i == 0 {
			first = state
		}
		states[state] = true
	}
	if out := start(); out.Code != http.StatusTooManyRequests || out.Header().Get("Location") != "" ||
		out.Header().Get("Set-Cookie") != "" || len(p.pending) != 4 {
		t.Fatal("busy page admitted another attempt or silently replaced a pending one")
	}
	// Represent the first attempt's normal expiry without waiting 100 seconds.
	// This fixture owns its RP and is not running any concurrent requests.
	expired := p.pending[first]
	expired.expires = time.Now().Add(-time.Second)
	p.pending[first] = expired
	out := start()
	if out.Code != http.StatusSeeOther || len(p.pending) != 4 {
		t.Fatal("retry after expiry did not reuse the released slot")
	}
	for state := range states {
		_, stillPending := p.pending[state]
		if stillPending != (state != first) {
			t.Fatal("expiry removed a wrong attempt or retained an expired one")
		}
	}
}
