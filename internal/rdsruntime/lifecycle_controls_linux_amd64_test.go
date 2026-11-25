//go:build linux && amd64

package rdsruntime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A lock file is only a precondition for the authenticated SQL probe. Exercise
// its format and ownership separately, without issuing SQL or starting a server.
func TestLocalPostmasterLockValidation(t *testing.T) {
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.FindProcess(os.Getpid()) // used only as a PID for parsing, never signalled
	if err != nil {
		t.Fatal(err)
	}
	defer self.Release()
	r := &Runtime{home: home, homeID: id, port: "5432", child: self, done: make(chan struct{})}
	if err := os.Mkdir(r.dataPath(), 0700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(r.dataPath(), "postmaster.pid")
	base := []string{strconv.Itoa(self.Pid), r.dataPath(), "1245", r.port, "", loopback, "", "ready", ""}
	line := func() []byte { return []byte(strings.Join(base, "\n")) }
	if _, err := r.readyLock(); !errors.Is(err, ErrNotReady) {
		t.Fatal("missing lock accepted", err)
	}
	for _, tc := range []struct {
		index int
		value string
	}{
		{0, "0"}, {1, home}, {2, "0"}, {2, "tomorrow"}, {3, "1234"}, {4, home}, {5, "localhost"}, {7, "starting"},
	} {
		bad := slices.Clone(base)
		bad[tc.index] = tc.value
		writeFixture(t, p, []byte(strings.Join(bad, "\n")), 0600)
		if _, err := r.readyLock(); !errors.Is(err, ErrNotReady) {
			t.Fatalf("accepted bad lock field %d/%q: %v", tc.index, tc.value, err)
		}
	}
	writeFixture(t, p, []byte(strings.Repeat("x", 4097)), 0600)
	if _, err := r.readyLock(); !errors.Is(err, ErrNotReady) {
		t.Fatal("oversized lock accepted", err)
	}
	writeFixture(t, p, line(), 0600)
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.readyLock(); !errors.Is(err, ErrNotReady) {
		t.Fatal("nonprivate lock accepted", err)
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if witness, err := r.readyLock(); err != nil || witness != "1245" {
		t.Fatal("well-formed owned lock rejected", witness, err)
	}
	if err := r.await(t.Context()); err != nil || r.witness != "1245" {
		t.Fatal("start witness not captured", err)
	}
	r.homeID, _ = os.Stat(r.dataPath())
	if _, err := r.readyLock(); !errors.Is(err, ErrNotReady) {
		t.Fatal("workspace replacement accepted", err)
	}
	c, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.await(c); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrNotReady) {
		t.Fatal("wait ignored cancellation", err)
	}
	close(r.done)
	if err := r.await(t.Context()); !errors.Is(err, ErrNotReady) {
		t.Fatal("wait ignored exited process", err)
	}
}

func TestConfirmedExitAndUncertainCleanupRemainDistinct(t *testing.T) {
	for _, mode := range []string{"clean", "exit-error", "retained-lock"} {
		t.Run(mode, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), ownedSlot)
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			id, err := os.Stat(home)
			if err != nil {
				t.Fatal(err)
			}
			// Reap an actual child. This is the Go test runner with no tests
			// selected, not postgres, and does not stand in for its protocol.
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$")
			if mode == "exit-error" {
				cmd.Args = append(cmd.Args, "-test.nonexistent.flag-for-status")
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waitErr := cmd.Wait()
			if (waitErr != nil) != (mode == "exit-error") {
				t.Fatal("wrong child result", waitErr)
			}
			r := &Runtime{home: home, homeID: id, child: cmd.Process, done: make(chan struct{}), waitErr: waitErr}
			if r.wait(0) {
				t.Fatal("unpublished child status was accepted")
			}
			close(r.done)
			if mode == "retained-lock" {
				if err := os.Mkdir(r.dataPath(), 0700); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, filepath.Join(r.dataPath(), "postmaster.pid"), []byte("not removed"), 0600)
			}
			for i := 0; i < 2; i++ {
				err := r.Close()
				if mode == "clean" && err != nil || mode != "clean" && !errors.Is(err, ErrCleanupDebt) {
					t.Fatal("unexpected cleanup decision", mode, err)
				}
				if err := r.Probe(t.Context()); !errors.Is(err, ErrNotReady) {
					t.Fatal("handle not revoked", err)
				}
			}
			_, err = os.Stat(home)
			if mode == "clean" && !errors.Is(err, os.ErrNotExist) || mode != "clean" && err != nil {
				t.Fatal("incorrect cleanup disposition", mode, err)
			}
		})
	}
}

func TestPrelaunchSecretsAndUnreadyHandle(t *testing.T) {
	home := t.TempDir()
	r := &Runtime{home: home, port: "5432", role: "test_role", bundle: &Bundle{dir: filepath.Join(home, "unconfigured")}}
	if err := r.writePassfile("synthetic_password"); err != nil {
		t.Fatal(err)
	}
	p := r.passPath()
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("passfile not private", err)
	}
	data, err := os.ReadFile(p)
	if err != nil || string(data) != "127.0.0.1:5432:postgres:test_role:synthetic_password\n" {
		t.Fatal("passfile was not scoped")
	}
	t.Setenv("TENKACLOUD_RDS_TEST_SENTINEL", "must-not-be-inherited")
	if got := r.env(); !slices.Contains(got, "HOME="+home) || !slices.Contains(got, "LD_LIBRARY_PATH="+filepath.Join(r.bundle.dir, "lib")) ||
		strings.Contains(strings.Join(got, "\n"), "SENTINEL") || strings.Contains(strings.Join(got, "\n"), "synthetic_password") {
		t.Fatal("unexpected child environment")
	}
	if _, err := r.prepare(t.Context()); !errors.Is(err, ErrBundle) {
		t.Fatal("unconfigured bundle admitted", err)
	}
	if r.identity == "" || r.role == "test_role" {
		t.Fatal("per-attempt identities were not generated")
	}
	if err := r.Probe(t.Context()); !errors.Is(err, ErrNotReady) {
		t.Fatal("missing witness accepted", err)
	}
	r.witness = "unproven"
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.Probe(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled probe admitted", err)
	}
	if err := r.Probe(t.Context()); !errors.Is(err, ErrBundle) {
		t.Fatal("missing bundle accepted", err)
	}
	if err := r.Close(); !errors.Is(err, ErrNotReady) {
		t.Fatal("unstarted child accepted as stopped", err)
	}
	r.home = filepath.Join(home, "missing")
	if err := r.writePassfile("unused"); !errors.Is(err, ErrStart) {
		t.Fatal("absent workspace accepted", err)
	}
	var absent *Runtime
	if err := absent.Probe(t.Context()); !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
	if err := absent.Close(); !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
	port, err := availablePort()
	n, conv := strconv.Atoi(port)
	if err != nil || conv != nil || n < 1 || n > 65535 {
		t.Fatal("no valid OS-assigned loopback port", port, err)
	}
}
