package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCognitoFederationCLIRejectsNonLocalAndUnsafeBeforeEffects(t *testing.T) {
	for _, flags := range [][]string{
		{"--allow-remote", "--listen", "127.0.0.1:0"},
		{"--unsafe-no-auth", "--listen", "127.0.0.1:0"},
		{"--listen", "0.0.0.0:0"},
		{"--listen", "localhost:0"},
	} {
		root := t.TempDir()
		state, token := filepath.Join(root, "test-fixture.sqlite"), filepath.Join(root, "fixture-unused.token")
		args := append([]string{"--cognito-local-federation-config", "--state", state, "--token-file", token}, flags...)
		if err := run(t.Context(), args, io.Discard); err == nil || !strings.Contains(err.Error(), "requires authenticated numeric loopback") {
			t.Fatalf("bad opt-in flag accepted: %v", err)
		}
		for _, path := range []string{state, token} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("rejected opt-in touched state or token path: %v", err)
			}
		}
	}
}
