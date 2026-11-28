package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalSSOCliDispatchIsolatesTrustAcrossRestarts(t *testing.T) {
	scratch := t.TempDir()
	setLocalSSOStartupTemp(t, scratch)
	type launch struct {
		address string
		state   string
		roots   *x509.CertPool
		stop    func()
	}
	start := func() launch {
		t.Helper()
		ctx, cancel := context.WithCancel(t.Context())
		ready := make(chan string, 2)
		done := make(chan error, 1)
		go func() {
			done <- run(ctx, []string{"local-sso"}, localSSOReadyWriter{ready})
		}()
		finished := false
		stop := func() {
			t.Helper()
			cancel()
			if finished {
				return
			}
			select {
			case err := <-done:
				finished = true
				if err != nil {
					t.Errorf("normal CLI did not stop cleanly after cancellation: %v", err)
				}
			case <-time.After(8 * time.Second):
				t.Error("normal CLI did not stop within the cleanup bound")
			}
		}
		t.Cleanup(stop)
		var notice string
		select {
		case notice = <-ready:
		case err := <-done:
			finished = true
			t.Fatalf("local-sso CLI dispatch failed before readiness: %v", err)
		case <-time.After(30 * time.Second):
			t.Fatal("local-sso command did not reach readiness")
		}

		var address, caPath string
		for line := range strings.SplitSeq(notice, "\n") {
			if v, ok := strings.CutPrefix(line, "Open: "); ok {
				address = v
			}
			if v, ok := strings.CutPrefix(line, "Trust this per-run CA only in an isolated browser profile: "); ok {
				caPath = v
			}
		}
		origin, err := url.Parse(address)
		if err != nil || origin.Scheme != "https" || origin.Hostname() != "127.0.0.1" || origin.Port() == "" ||
			origin.User != nil || origin.Path != "/" || origin.RawQuery != "" || origin.Fragment != "" {
			t.Fatal("command did not publish its canonical HTTPS loopback sample")
		}
		state := filepath.Dir(caPath)
		if filepath.Base(caPath) != "local-ca.pem" || filepath.Dir(state) != scratch {
			t.Fatal("command did not publish an isolated per-run trust root")
		}
		data, err := os.ReadFile(caPath)
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(data) {
			t.Fatal("command did not provision a valid local trust root")
		}
		return launch{address, state, roots, stop}
	}
	request := func(address string, roots *x509.CertPool) error {
		t.Helper()
		parsed, err := url.Parse(address)
		if err != nil {
			t.Fatal("invalid isolated sample URL")
		}
		transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13},
			DisableKeepAlives: true, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
				if network != "tcp" || target != parsed.Host {
					return nil, errors.New("fixture refused unexpected destination")
				}
				return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp4", target)
			}}
		defer transport.CloseIdleConnections()
		client := &http.Client{Timeout: 4 * time.Second, Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
		if err != nil {
			t.Fatal("could not create fixture request")
		}
		res, err := client.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
		if err != nil || res.StatusCode != http.StatusOK || !strings.Contains(string(body), "Sign in with local Entra") ||
			res.Header.Get("Cache-Control") != "no-store" {
			return errors.New("local sample did not return its expected browser entry")
		}
		return nil
	}
	assertRemoved := func(state string) {
		t.Helper()
		if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("run's private state was not removed")
		}
	}

	first := start()
	if err := request(first.address, first.roots); err != nil {
		t.Fatal("first real CLI run did not serve its HTTPS sample")
	}
	first.stop()
	assertRemoved(first.state)

	second := start()
	if first.state == second.state {
		t.Fatal("restart reused private run state")
	}
	var wrongTrust x509.UnknownAuthorityError
	if err := request(second.address, first.roots); !errors.As(err, &wrongTrust) {
		t.Fatal("earlier browser trust did not reject the new run")
	}
	if err := request(second.address, second.roots); err != nil {
		t.Fatal("new browser trust did not accept the new run")
	}
	assertRemoved(first.state)
	second.stop()
	assertRemoved(second.state)
}
