//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestExplicitTokenDisplayUsesActualTerminalAndPreservesFile(t *testing.T) {
	t.Setenv("CI", "restore-at-cleanup")
	if err := os.Unsetenv("CI"); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal("test runner needs a pseudoterminal")
	}
	master := os.NewFile(uintptr(fd), "temporary-pty-master")
	defer master.Close()
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal("could not unlock pseudoterminal")
	}
	index, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal("could not locate pseudoterminal")
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", index), os.O_RDWR, 0)
	if err != nil {
		t.Fatal("could not open pseudoterminal")
	}
	defer slave.Close()
	if !interactiveTokenOutput(slave) {
		t.Fatal("real terminal was not recognized")
	}
	for _, ci := range []string{"", "true", "false"} {
		t.Setenv("CI", ci)
		if interactiveTokenOutput(slave) {
			t.Fatal("CI terminal could display a secret")
		}
	}
	if err = os.Unsetenv("CI"); err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/operator"
	// A known test-only value is never included in assertion messages or reports.
	dummy := "TemporaryDummyOperatorTokenForUnitTerminalOnly0123456789"
	if err = os.WriteFile(path, []byte(dummy+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 16)
	go func() {
		scanner := bufio.NewScanner(master)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen=127.0.0.1:0", "--token-file=" + path, "--show-token"}, slave)
	}()
	seen := false
	ready := false
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	for !ready {
		select {
		case line := <-lines:
			seen = seen || strings.Contains(line, dummy)
			ready = strings.Contains(line, "AWS simulation endpoint ")
		case <-deadline.C:
			t.Fatal("CLI did not become ready")
		case <-done:
			t.Fatal("CLI stopped before readiness")
		}
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal("CLI shutdown failed")
	}
	if !seen {
		t.Fatal("explicit interactive startup did not display its saved token")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != dummy+"\n" {
		t.Fatal("interactive startup changed the existing token")
	}
}
