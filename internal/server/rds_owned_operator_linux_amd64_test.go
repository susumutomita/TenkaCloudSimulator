//go:build linux && amd64

package server

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guesttcp"
)

func operatorFixture(t *testing.T) (string, OwnedPostgresOperatorConfig) {
	t.Helper()
	root, work := t.TempDir(), t.TempDir()
	// Go creates the child directory using mode 0777 before the test user's
	// umask. These positive fixtures must satisfy the real private-mode gate.
	for _, dir := range []string{root, work} {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(root, "state.sqlite")
	if err := os.WriteFile(state, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return state, OwnedPostgresOperatorConfig{
		BundleDirectory: filepath.Join(root, "missing-approved-bundle"),
		BundleSHA256:    strings.Repeat("0", 64),
		WorkRoot:        work, Region: "us-east-1", Identifier: "example-db",
	}
}

func TestOwnedPostgresOperatorRefusesUnprovenInputs(t *testing.T) {
	state, fixture := operatorFixture(t)
	opts := DefaultProviderConfig()
	if got, err := WithOwnedPostgresOperator(opts, state, fixture); !errors.Is(err, errOwnedPostgresTopology) || got.ownedPostgres != nil {
		t.Fatalf("missing required topology: %#v / %v", got.ownedPostgres, err)
	}
	opts.RDSPrivatePostgresTopology = true
	copyOptions := opts
	copyOptions.ownedPostgres = &ownedPostgresOperator{} // unavailable marker must not be overwritten
	if _, err := WithOwnedPostgresOperator(copyOptions, state, fixture); !errors.Is(err, errOwnedPostgresAlreadySelected) {
		t.Fatalf("should reject duplicate selection: %v", err)
	}
	noPin := fixture
	noPin.BundleSHA256 = ""
	if _, err := WithOwnedPostgresOperator(opts, state, noPin); !errors.Is(err, errOwnedPostgresConfig) {
		t.Fatalf("should reject missing independent pin: %v", err)
	}
	if os.Geteuid() == 0 {
		if _, err := WithOwnedPostgresOperator(opts, state, fixture); !errors.Is(err, errOwnedPostgresPlatform) {
			t.Fatalf("root unexpectedly passed the operator boundary: %v", err)
		}
		return
	}
	for _, candidate := range []struct {
		label, path string
		edit        func(*OwnedPostgresOperatorConfig)
		expected    error
	}{
		{"ephemeral state", ":memory:", nil, errOwnedPostgresState},
		{"missing state", state + "-missing", nil, errOwnedPostgresState},
		{"noncanonical db identifier", state, func(s *OwnedPostgresOperatorConfig) { s.Identifier = "EXAMPLE" }, errOwnedPostgresConfig},
		{"unsupported region", state, func(s *OwnedPostgresOperatorConfig) { s.Region = "unknown" }, errOwnedPostgresConfig},
		{"bundle and work overlap", state, func(s *OwnedPostgresOperatorConfig) { s.BundleDirectory = s.WorkRoot }, errOwnedPostgresConfig},
		{"state nested in bundle", state, func(s *OwnedPostgresOperatorConfig) { s.BundleDirectory = filepath.Dir(state) }, errOwnedPostgresConfig},
		{"missing work root", state, func(s *OwnedPostgresOperatorConfig) { s.WorkRoot += "-missing" }, rdsruntime.ErrWork},
		{"invalid pin", state, func(s *OwnedPostgresOperatorConfig) { s.BundleSHA256 = "not-a-digest" }, rdsruntime.ErrBundle},
		{"missing bundle", state, nil, rdsruntime.ErrBundle},
	} {
		t.Run(candidate.label, func(t *testing.T) {
			settings := fixture
			if candidate.edit != nil {
				candidate.edit(&settings)
			}
			got, err := WithOwnedPostgresOperator(opts, candidate.path, settings)
			if !errors.Is(err, candidate.expected) || got.ownedPostgres != nil || opts.ownedPostgres != nil {
				t.Fatalf("operator refusal must not fall back to permitted startup: %v", err)
			}
			entries, err := os.ReadDir(fixture.WorkRoot)
			if err != nil || len(entries) != 0 {
				t.Fatalf("refused input touched the work directory: %v / %v", len(entries), err)
			}
			info, err := os.Stat(state)
			if err != nil || info.Size() != 0 {
				t.Fatal("refused input wrote SQLite state")
			}
		})
	}
}

func TestOwnedPostgresOperatorPinsPrivateStateBeforeProviderCreation(t *testing.T) {
	path, _ := operatorFixture(t)
	info, err := localOwnedState(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := &ownedPostgresOperator{path: path, stateFile: info}
	if err := owner.checkState(path); err != nil {
		t.Fatal(err)
	}
	path2 := filepath.Join(filepath.Dir(path), "alias.sqlite")
	if err := os.Symlink(path, path2); err != nil {
		t.Fatal(err)
	}
	if _, err := localOwnedState(path2); !errors.Is(err, errOwnedPostgresState) {
		t.Fatalf("state symlink accepted: %v", err)
	}
	if err := os.Remove(path2); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(path, path2); err != nil {
		t.Fatal(err)
	}
	if _, err := localOwnedState(path); !errors.Is(err, errOwnedPostgresState) {
		t.Fatalf("shared inode accepted: %v", err)
	}
	if err := os.Remove(path2); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := localOwnedState(path); !errors.Is(err, errOwnedPostgresState) {
		t.Fatalf("nonprivate file accepted: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	opts := DefaultProviderConfig()
	opts.ownedPostgres = owner
	if h, close, e := NewWithProviders(path, opts); h != nil || close != nil || !errors.Is(e, errOwnedPostgresTopology) {
		t.Fatalf("lost topology should refuse the actual server constructor: %v", e)
	}
	opts.RDSPrivatePostgresTopology = true
	if h, close, e := NewWithProviders(path2, opts); h != nil || close != nil || !errors.Is(e, errOwnedPostgresState) {
		t.Fatalf("different state path should fail before opening an operator store: %v", e)
	}
	if _, err := os.Lstat(path2); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused constructor created a SQLite path: %v", err)
	}
	if err := os.Rename(path, path2); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if h, close, e := NewWithProviders(path, opts); h != nil || close != nil || !errors.Is(e, errOwnedPostgresState) {
		t.Fatalf("replacement SQLite inode should fail before creating the provider: %v", e)
	}
	info, err = localOwnedState(path)
	if err != nil {
		t.Fatal(err)
	}
	// Intentional invalid private binding, not a successful runtime fixture.
	// It reaches the real catalog constructor, which must not fall back.
	opts.ownedPostgres = &ownedPostgresOperator{path: path, stateFile: info}
	if h, close, e := NewWithProviders(path, opts); h != nil || close != nil || e == nil ||
		!strings.Contains(e.Error(), "requires one canonical private RDS target and native topology") {
		t.Fatalf("unproven owned catalog silently fell back or failed elsewhere: %v", e)
	}
}

func TestOwnedPostgresAndBattleCompositionAreMutuallyExclusive(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "not-created.sqlite")
	opts := DefaultProviderConfig()
	opts.RDSPrivatePostgresTopology = true
	opts.ownedPostgres = &ownedPostgresOperator{} // unadmitted marker, only conflict is exercised
	if h, close, err := NewWithBattleRuntime(path, opts, "", guesttcp.HTTPProbeCommandClock{}, aws.EC2InstanceRuntimeConfig{}); h != nil || close != nil || !errors.Is(err, errOwnedPostgresBattleConflict) {
		t.Fatalf("Battle constructor should reject the conflict before creating a registry: %v", err)
	}
	opts.configureInstances = func(*sql.DB, *aws.StackStore) error {
		t.Error("conflicting provider reached SQLite initialization")
		return nil
	}
	if h, close, err := NewWithProviders(path, opts); h != nil || close != nil || !errors.Is(err, errOwnedPostgresBattleConflict) {
		t.Fatalf("shared composer should reject a mixed owner before checking SQLite: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("conflicting provider created startup state: %v / %v", entries, err)
	}
}
