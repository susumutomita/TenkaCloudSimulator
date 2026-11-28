package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalSSOSampleUsesOnlyItsDirectRoutes(t *testing.T) {
	const origin = "https://127.0.0.1:18443"
	p := newLocalSSORelyingParty(nil, "https://127.0.0.1:19443", "test-local-browser", origin)
	for _, scenario := range []struct {
		name, page, proxyHeader, bodyContains, notContains string
		status                                             int
	}{
		{"reverse proxy unsupported", "/", "Forwarded", "Local sample request refused.", "Sign in with local Entra", http.StatusForbidden},
		{"outdated bookmark", "/old-sample-page", "", "404 page not found", "Sign in with local Entra", http.StatusNotFound},
		{"direct sample still available", "/", "", "Sign in with local Entra", "Local sample request refused.", http.StatusOK},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, scenario.page, nil)
			req.Host = strings.TrimPrefix(origin, "https://")
			req.RemoteAddr = "127.0.0.1:23111"
			req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13}
			if scenario.proxyHeader != "" {
				req.Header.Set(scenario.proxyHeader, "proto=https;host=127.0.0.1:18443")
			}
			out := httptest.NewRecorder()
			p.ServeHTTP(out, req)
			if out.Code != scenario.status || !strings.Contains(out.Body.String(), scenario.bodyContains) ||
				strings.Contains(out.Body.String(), scenario.notContains) || len(out.Header().Values("Set-Cookie")) != 0 ||
				out.Header().Get("Cache-Control") != "no-store" || len(p.pending) != 0 {
				t.Fatal("sample routed an unsupported request or created sign-in state before a supported start")
			}
		})
	}
}

type localSSODrainWitness struct {
	*net.TCPListener
	once   sync.Once
	closed chan struct{}
}

func (l *localSSODrainWitness) Close() error {
	err := l.TCPListener.Close()
	l.once.Do(func() { close(l.closed) })
	return err
}

func TestLocalSSOShutdownClosesAdmissionBeforeDrainingOwnedHandlers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	tcp, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tcp.Close() })
	raw := &localSSODrainWitness{TCPListener: tcp, closed: make(chan struct{})}
	listener := &localSSOListener{Listener: raw}
	_, _, leaf, err := localSSOTLS(1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseNow := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseNow)
	listener.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-entered:
			w.WriteHeader(http.StatusConflict)
			return
		default:
			close(entered)
		}
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-ctx.Done():
			w.WriteHeader(http.StatusRequestTimeout)
		}
	}), leaf[0])
	handler := listener.server.Handler
	request := func() *http.Request {
		return httptest.NewRequestWithContext(ctx, http.MethodGet, listener.origin()+"/", nil)
	}
	before := make(chan int, 1)
	go func() {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, request())
		before <- out.Code
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first request did not cross the owned admission boundary")
	}
	done := make(chan error, 1)
	go func() { done <- listener.close() }()
	select {
	case <-raw.closed:
	case <-ctx.Done():
		t.Fatal("shutdown did not close its real bound listener")
	}
	if !errors.Is(tcp.SetDeadline(time.Now()), net.ErrClosed) {
		t.Fatal("listener was not closed")
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown finished while an admitted handler still owned work: %v", err)
	default:
	}
	// An already-dispatched request may still reach the handler boundary.
	// No new network connection is attempted after the listener closes.
	late := httptest.NewRecorder()
	handler.ServeHTTP(late, request())
	if late.Code != http.StatusServiceUnavailable || !strings.Contains(late.Body.String(), "Local SSO stopping.") {
		t.Fatal("the held shutdown boundary admitted another sample request")
	}
	releaseNow()
	select {
	case status := <-before:
		if status != http.StatusOK {
			t.Fatalf("already admitted handler did not finish: %d", status)
		}
	case <-ctx.Done():
		t.Fatal("already admitted handler did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("owned shutdown did not complete after drain")
	}
}
