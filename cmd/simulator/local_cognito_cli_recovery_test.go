package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStandaloneCognitoConfigurationStartupRefusalsReleaseResources(t *testing.T) {
	for _, name := range []string{"occupied listen address", "unavailable database parent", "unavailable token parent", "invalid existing token"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state.sqlite")
			token := filepath.Join(root, "operator-token")
			held, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			address := held.Addr().String()
			occupied := name == "occupied listen address"
			if !occupied {
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "unavailable database parent":
				state = filepath.Join(root, "missing-db", "state.sqlite")
			case "unavailable token parent":
				token = filepath.Join(root, "missing-token", "operator-token")
			case "invalid existing token":
				// An interrupted provisioning leaves an incomplete local file.
				if err := os.WriteFile(token, []byte("unfinished\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}

			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			var output bytes.Buffer
			args := []string{"--listen", address, "--state", state, "--token-file", token, "--cognito-local-federation-config"}
			err = run(ctx, args, &output)
			if err == nil || output.Len() != 0 {
				t.Fatalf("failed startup advertised a ready operator endpoint or returned success (error: %v)", err)
			}
			switch name {
			case "occupied listen address":
				var op *net.OpError
				if !errors.As(err, &op) || op.Op != "listen" {
					t.Fatal("did not report the occupied listener", err)
				}
				if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("listener failure changed state", err)
				}
				if err := held.Close(); err != nil {
					t.Fatal(err)
				}
			case "unavailable token parent":
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("did not report that the operator token could not be written", err)
				}
			case "invalid existing token":
				saved, readErr := os.ReadFile(token)
				if readErr != nil || string(saved) != "unfinished\n" || !strings.Contains(err.Error(), "invalid operator token file length") {
					t.Fatal("startup did not preserve and report the incomplete operator token", err)
				}
			}
			if name != "invalid existing token" {
				if _, err := os.Stat(token); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("startup created an operator token before its own prerequisite succeeded", err)
				}
			}
			again, err := net.Listen("tcp4", address)
			if err != nil {
				t.Fatal("failed startup leaked its listener instead of allowing a retry", err)
			}
			again.Close()
		})
	}
}
