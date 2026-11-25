//go:build linux && amd64

package rdsruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBundleDigestPinsContentOwnershipAndLinks(t *testing.T) {
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("asset", []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("asset", "local"); err != nil {
		t.Fatal(err)
	}
	first, err := bundleDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("asset", []byte("after"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := bundleDigest(root)
	if err != nil || bytes.Equal(first, next) {
		t.Fatal("changed bytes preserved bundle identity")
	}
	if err := root.Chmod("asset", 0666); err != nil {
		t.Fatal(err)
	}
	if _, err := bundleDigest(root); !errors.Is(err, ErrBundle) {
		t.Fatal("other-writeable asset was accepted")
	}
	if err := root.Chmod("asset", 0600); err != nil {
		t.Fatal(err)
	}
	if err := root.Symlink("../other", "external"); err != nil {
		t.Fatal(err)
	}
	if _, err := bundleDigest(root); !errors.Is(err, ErrBundle) {
		t.Fatal("out-of-bundle link was accepted")
	}
}

func TestStartRequiresExplicitApprovedBundle(t *testing.T) {
	path := t.TempDir()
	if _, err := OpenBundle(path, ""); !errors.Is(err, ErrBundle) {
		t.Fatal("unapproved bundle was accepted")
	}
	if _, err := OpenBundle(path, strings.Repeat("0", 64)); !errors.Is(err, ErrBundle) {
		t.Fatal("incorrect bundle digest was accepted")
	}
	if _, err := Start(context.Background(), nil, path); !errors.Is(err, ErrBundle) {
		t.Fatal("missing optional bundle started a workload")
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatal("unapproved workload modified its workspace")
	}
}

func TestCleanupCannotAdoptReplacementDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "owned")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := ownedPath(path, true)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runtime{home: path, homeID: info}
	if err := os.Rename(path, filepath.Join(root, "previous")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(path, "do-not-remove")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(r.removeWorkspace(), ErrCleanupDebt) {
		t.Fatal("cleanup accepted a different workspace identity")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("cleanup changed a replacement workspace")
	}
}

func TestOutputBoundAndRevokedProbe(t *testing.T) {
	var out boundedOutput
	if _, err := out.Write(make([]byte, 512)); err != nil {
		t.Fatal(err)
	}
	if n, err := out.Write([]byte("extra")); n != 0 || err == nil || out.buf.Len() != 512 {
		t.Fatal("probe output bound was exceeded")
	}
	r := &Runtime{}
	r.revoked.Store(true)
	if err := r.Probe(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatal("revoked handle could start an authenticated probe")
	}
}

func TestActualOwnedPostgreSQL(t *testing.T) {
	dir, pin := os.Getenv("TCSIM_PG_TEST_BUNDLE"), os.Getenv("TCSIM_PG_TEST_PIN")
	if dir == "" && pin == "" {
		t.Skip("explicit approved PostgreSQL 15.20 bundle and independent pin not supplied")
	}
	b, err := OpenBundle(dir, pin)
	if err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp("", "native-rds-owned-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Retain uncertain workspace, including the smallest repro of a failed
		// lifecycle. Never recursively remove a live child directory in tests.
		if err := os.Remove(work); err != nil {
			t.Error("work root not confirmed empty")
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	r, err := Start(ctx, b, work)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := r.Probe(ctx); err != nil {
		t.Fatal("fresh authenticated read-only probe failed", err)
	}
	if duplicate, err := Start(ctx, b, work); !errors.Is(err, ErrWork) || duplicate != nil {
		if duplicate != nil {
			_ = duplicate.Close()
		}
		t.Fatal("active slot allowed a second workload")
	}
	secretMode, err := os.Stat(r.passPath())
	if err != nil || secretMode.Mode().Perm() != 0600 {
		t.Fatal("synthetic passfile not private")
	}
	testRejectsWrongSyntheticPassword(t, ctx, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !r.childExited() {
		t.Fatal("owned child not reaped")
	}
	if _, err := os.Lstat(r.home); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("confirmed shutdown did not remove disposable data")
	}
	if err := r.Close(); err != nil {
		t.Fatal("confirmed close was not idempotent", err)
	}
	if err := r.Probe(ctx); !errors.Is(err, ErrNotReady) {
		t.Fatal("stale handle can still probe")
	}
	t.Log("PostgreSQL 15.20 SCRAM accepted the synthetic credential and refused a wrong password; fixed probe matched owned cluster; duplicate refused; reaped and removed")
}

func testRejectsWrongSyntheticPassword(t *testing.T, ctx context.Context, r *Runtime) {
	t.Helper()
	wrong := filepath.Join(r.home, "bad-pgpass")
	if err := os.WriteFile(wrong, []byte(loopback+":"+r.port+":postgres:"+r.role+":test-wrong-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := r.bundle.executable(2)
	if err != nil {
		t.Fatal(err)
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, client, "-XqwAt", "-h", loopback, "-p", r.port,
		"-U", r.role, "-d", "postgres", "-c", "SELECT 1")
	cmd.Env = append(r.env(), "PGPASSFILE="+wrong, "PGSSLMODE=disable", "PGGSSENCMODE=disable", "PGCONNECT_TIMEOUT=2")
	var stderr boundedOutput
	cmd.Stderr = &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 2 ||
		!strings.Contains(stderr.buf.String(), "password authentication failed") {
		t.Fatal("wrong synthetic PostgreSQL password did not produce authentication rejection")
	}
}
