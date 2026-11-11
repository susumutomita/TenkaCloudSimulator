//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// This is negative qualification, not a production watchdog implementation.
// Every process is our non-daemonizing test executable with a finite watchdog.
func TestLocalExpiryHelper(t *testing.T) {
	mode := os.Getenv("GUESTPROCESS_EXPIRY_HELPER")
	if mode == "" {
		return
	}
	watchdog := time.AfterFunc(30*time.Second, func() { os.Exit(70) })
	defer watchdog.Stop()
	dir := os.Getenv("GUESTPROCESS_EXPIRY_DIR")
	publish := func(name, value string) {
		if os.WriteFile(filepath.Join(dir, name+".tmp"), []byte(value), 0600) != nil {
			os.Exit(71)
		}
		if os.Rename(filepath.Join(dir, name+".tmp"), filepath.Join(dir, name)) != nil {
			os.Exit(74)
		}
	}
	if mode == "child" {
		publish("child-ready", strconv.Itoa(os.Getpid()))
		for {
			if _, err := os.Stat(filepath.Join(dir, "probe")); err == nil {
				publish("child-progress", "")
				for {
					time.Sleep(time.Second)
				}
			}
			time.Sleep(time.Millisecond)
		}
	}
	exe, _ := os.Executable()
	child, err := LaunchSupervisorBound(context.Background(), Spec{Executable: exe, Args: []string{"-test.run=^TestLocalExpiryHelper$"}, Env: []string{"GUESTPROCESS_EXPIRY_HELPER=child", "GUESTPROCESS_EXPIRY_DIR=" + dir}})
	if err != nil {
		os.Exit(72)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "arm")); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// Leave time for the observer to acknowledge the actual stop, including
	// race-instrumented startup. A late stop fails rather than weakening evidence.
	deadline := time.Now().Add(2 * time.Second)
	timer := time.NewTimer(time.Until(deadline))
	publish("armed", strconv.FormatInt(deadline.UnixNano(), 10))
	<-timer.C
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exit, err := child.Kill(ctx)
	if err != nil || !exit.Reaped {
		os.Exit(73)
	}
	publish("reaped", "")
}

// Arbitrary signals are deliberately test-only and use the original atomic
// ownership handle, never a PID discovered in files or process listings.
func signalOwnedTestProcess(t *testing.T, p *Process, signal unix.Signal) {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fd < 0 {
		t.Fatal("owned helper already exited")
	}
	if err := unix.PidfdSendSignal(p.fd, signal, nil, 0); err != nil {
		t.Fatal(err)
	}
}

func waitOwnedStop(t *testing.T, p *Process) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var info unix.Siginfo
		p.mu.Lock()
		err := unix.Waitid(unix.P_PIDFD, p.fd, &info, unix.WSTOPPED|unix.WNOHANG|unix.WNOWAIT, nil)
		p.mu.Unlock()
		if err != nil && !errors.Is(err, unix.EINTR) {
			t.Fatal(err)
		}
		if err == nil && info.Signo != 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("owned helper did not enter stopped state")
}

func TestStoppedSupervisorDoesNotEnforceLocalExpiry(t *testing.T) {
	dir := t.TempDir()
	exe, _ := os.Executable()
	// Bind the outer helper too: its watchdog cannot run while SIGSTOPped,
	// so death of this test owner must independently request kernel SIGKILL.
	supervisor, err := LaunchSupervisorBound(context.Background(), Spec{Executable: exe, Args: []string{"-test.run=^TestLocalExpiryHelper$"}, Env: []string{"GUESTPROCESS_EXPIRY_HELPER=supervisor", "GUESTPROCESS_EXPIRY_DIR=" + dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := supervisor.Kill(ctx); err != nil {
			t.Error(err)
		}
	})
	rawPID := waitFile(t, filepath.Join(dir, "child-ready"))
	pid, err := strconv.Atoi(string(rawPID))
	if err != nil {
		t.Fatal(err)
	}
	// Observation only, matching the existing supervisor-death tests. This is
	// our explicit benign descendant; never signal using a PID-derived handle.
	childFD, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer unix.Close(childFD)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := supervisor.Kill(ctx); err != nil {
			t.Error(err)
		}
		if !pollExit(t, childFD, 5000) {
			t.Error("benign child did not exit during cleanup")
		}
	})
	if err := os.WriteFile(filepath.Join(dir, "arm"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	raw := waitFile(t, filepath.Join(dir, "armed"))
	nanos, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Unix(0, nanos)
	signalOwnedTestProcess(t, supervisor, unix.SIGSTOP)
	waitOwnedStop(t, supervisor)
	// The stop must precede expiry, or this run cannot establish the premise.
	if !time.Now().Before(deadline) {
		t.Fatal("test supervisor stopped too late")
	}
	time.Sleep(time.Until(deadline.Add(100 * time.Millisecond)))
	if _, err := os.Stat(filepath.Join(dir, "reaped")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("local expiry cleanup unexpectedly completed while stopped: %v", err)
	}
	// A fresh challenge after expiry establishes child progress, not just a PID
	// or a cached liveness observation. PDEATHSIG does not fire on supervisor stop.
	if err := os.WriteFile(filepath.Join(dir, "probe"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFile(t, filepath.Join(dir, "child-progress"))
	signalOwnedTestProcess(t, supervisor, unix.SIGCONT)
	waitFile(t, filepath.Join(dir, "reaped"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exit, err := supervisor.Wait(ctx); err != nil || !exit.Reaped || exit.Code != 0 {
		t.Fatalf("supervisor cleanup after resume: %+v %v", exit, err)
	}
}
