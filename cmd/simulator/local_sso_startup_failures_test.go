package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setLocalSSOStartupTemp(t *testing.T, dir string) {
	t.Helper()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, dir)
	}
}

func TestLocalSSOStartupNeedsPrivateStateBeforeBinding(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "not-created", "tmp")
	setLocalSSOStartupTemp(t, missing)

	binds := 0
	var output bytes.Buffer
	err := runLocalSSOWithListener(t.Context(), nil, &output, func(string, string) (net.Listener, error) {
		binds++
		return nil, errors.New("unexpected listener allocation")
	})
	if err == nil || err.Error() != "cannot allocate private SSO state" || binds != 0 || output.Len() != 0 {
		t.Fatalf("invalid scratch root did not fail before binding or advertising (binds %d)", binds)
	}
}

func TestLocalSSOStartupNeverAdvertisesOrLeaksAfterPhaseFailure(t *testing.T) {
	tests := []struct {
		name         string
		conflictPath string
		stopListener int // 1-based listener acquisition number; zero keeps all listeners open
		want         string
	}{
		{name: "SQLite path occupied", conflictPath: "state.sqlite", want: "could not initialize local Cognito"},
		{name: "private Cognito bootstrap listener stopped", stopListener: 1, want: "signed local Cognito CreateUserPool failed"},
		{name: "Entra metadata listener stopped", stopListener: 3, want: "local Entra metadata could not be confirmed"},
		{name: "Cognito metadata listener stopped", stopListener: 2, want: "signed Cognito SP metadata could not be confirmed"},
		{name: "sample application listener stopped", stopListener: 4, want: "local sample browser page unavailable"},
		{name: "browser CA file path occupied", conflictPath: "local-ca.pem", want: "cannot store public per-run browser CA"},
		{name: "private login-code path occupied", conflictPath: "login-code", want: "cannot store private one-shot Entra code"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			setLocalSSOStartupTemp(t, root)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()

			var listeners []net.Listener
			t.Cleanup(func() {
				for _, listener := range listeners {
					_ = listener.Close()
				}
			})
			var output bytes.Buffer
			err := runLocalSSOWithListener(ctx, nil, &output, func(network, address string) (net.Listener, error) {
				if network != "tcp4" || address != "127.0.0.1:0" {
					return nil, errors.New("fixture refused a non-isolated listener")
				}
				l, err := net.Listen(network, address)
				if err != nil {
					return nil, err
				}
				listeners = append(listeners, l)
				if len(listeners) == 4 {
					if tc.conflictPath != "" {
						// Model a real filesystem conflict with a directory in
						// an owned run; let ordinary SQLite/file I/O fail.
						dirs, readErr := os.ReadDir(root)
						if readErr != nil || len(dirs) != 1 || !dirs[0].IsDir() {
							_ = l.Close()
							return nil, errors.New("expected exactly one private fixture run directory")
						}
						if makeErr := os.Mkdir(filepath.Join(root, dirs[0].Name(), tc.conflictPath), 0700); makeErr != nil {
							_ = l.Close()
							return nil, errors.New("could not prepare fixture filesystem conflict")
						}
					}
					if tc.stopListener > 0 {
						// All ports are genuinely acquired first. Close only
						// this fixture's target and let the existing verified
						// HTTPS readiness request encounter the stopped peer.
						if closeErr := listeners[tc.stopListener-1].Close(); closeErr != nil {
							_ = l.Close()
							return nil, errors.New("could not stop fixture peer")
						}
					}
				}
				return l, nil
			})
			if err == nil || err.Error() != tc.want || len(listeners) != 4 || output.Len() != 0 {
				t.Fatalf("startup did not fail closed at intended phase (binds %d, error %v)", len(listeners), err)
			}
			dirs, err := os.ReadDir(root)
			if err != nil || len(dirs) != 0 {
				t.Fatal("failed startup left private state, including any partially written trust")
			}
			// SetDeadline on the exact listener objects is nonblocking
			// and distinguishes ownership cleanup from another process later
			// reusing the same operating-system port.
			for i, listener := range listeners {
				raw, ok := listener.(*net.TCPListener)
				if !ok || !errors.Is(raw.SetDeadline(time.Now()), net.ErrClosed) {
					t.Errorf("startup left its listener %d open", i)
				}
			}
		})
	}
}
