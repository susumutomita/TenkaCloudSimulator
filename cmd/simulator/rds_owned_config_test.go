package main

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/server"
)

func TestOwnedPostgresCLIRefusesBeforeStarting(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "operator.json")
	state := filepath.Join(root, "never-created.sqlite")
	opts := server.DefaultProviderConfig()
	opts.RDSPrivatePostgresTopology = true
	if got, err := configureOwnedPostgres(opts, "", state, nil, true, true); err != nil || !got.RDSPrivatePostgresTopology {
		t.Fatalf("unset option changed existing provider config: %+v / %v", got, err)
	}
	for _, tc := range []struct{ label, arg, want string }{
		{"unauthenticated", "--unsafe-no-auth", "requires authenticated numeric loopback"},
		{"remote opt-in", "--allow-remote", "requires authenticated numeric loopback"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			err := run(t.Context(), []string{"--rds-owned-postgres-config", cfg, "--state", state, tc.arg}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("operator boundary not enforced: %v", err)
			}
		})
	}
	err := run(t.Context(), []string{"--rds-owned-postgres-config", cfg, "--state", state}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "requires --rds-private-postgres-topology") {
		t.Fatalf("unsafe catalog fallback with omitted topology: %v", err)
	}
	if _, err := configureOwnedPostgres(opts, cfg, state, net.ParseIP("192.0.2.10"), false, false); err == nil ||
		!strings.Contains(err.Error(), "requires authenticated numeric loopback") {
		t.Fatalf("non-loopback admitted without authority: %v", err)
	}

	for _, tc := range []struct {
		label, document, want string
		mode                  os.FileMode
	}{
		{"duplicate", `{"region":"us-east-1","Region":"us-west-2"}`, "invalid owned PostgreSQL config", 0600},
		{"extra", `{"unknown":"input"}`, "invalid owned PostgreSQL config", 0600},
		{"trailing", `{"region":"us-east-1"}{}`, "invalid owned PostgreSQL config", 0600},
		{"empty", `{}`, "requires one explicit bundle", 0600},
		{"public-mode", `{}`, "absolute regular private file", 0644},
		{"over-limit", strings.Repeat(" ", 4097), "absolute regular private file", 0600},
	} {
		t.Run(tc.label, func(t *testing.T) {
			path := filepath.Join(root, tc.label)
			if err := os.WriteFile(path, []byte(tc.document), tc.mode); err != nil {
				t.Fatal(err)
			}
			// Verify public-mode denial even in a developer shell with a strict umask.
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			e := run(t.Context(), []string{"--listen", "127.0.0.1:0", "--rds-owned-postgres-config", path,
				"--rds-private-postgres-topology", "--state", state}, io.Discard)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("invalid local startup did not fail closed: %v", e)
			}
		})
	}
	if _, err := os.Lstat(state); !os.IsNotExist(err) {
		t.Fatalf("refused operator startup created SQLite state: %v", err)
	}
}

func TestOwnedPostgresCLIRejectsBattleBeforeAcquiringStartupFiles(t *testing.T) {
	root := t.TempDir()
	state, token := filepath.Join(root, "not-created.sqlite"), filepath.Join(root, "not-created-token")
	err := run(t.Context(), []string{
		"--rds-private-postgres-topology",
		"--rds-owned-postgres-config", filepath.Join(root, "missing-postgres-config"),
		"--battle-runtime-config", filepath.Join(root, "missing-battle-config"),
		"--state", state, "--token-file", token,
	}, io.Discard)
	if err == nil || err.Error() != "--rds-owned-postgres-config cannot be combined with --battle-runtime-config" {
		t.Fatalf("conflicting runtime selection did not fail before reading either config: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("conflicting runtime selection created startup state: %v / %v", entries, err)
	}
}
