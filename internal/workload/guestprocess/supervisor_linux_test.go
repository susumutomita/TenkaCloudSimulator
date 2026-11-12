//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Go runs package initialization on the exec/startup thread. Linux PDEATHSIG
// is per task, so inspecting it later from a testing goroutine may inspect a
// different newly created runtime thread whose value is correctly zero.
// Capture only; never set the signal, which would mask a broken launcher.
var boundChildStartup struct {
	signal int32
	errno  unix.Errno
	tid    int
}

func init() {
	if os.Getenv("GUESTPROCESS_BOUND_HELPER") != "child" {
		return
	}
	boundChildStartup.tid = unix.Gettid()
	_, _, boundChildStartup.errno = unix.Syscall6(unix.SYS_PRCTL, unix.PR_GET_PDEATHSIG, uintptr(unsafe.Pointer(&boundChildStartup.signal)), 0, 0, 0, 0)
}

// Files are readiness/termination barriers only. No sockets or network are used.
func TestBoundSupervisorHelper(t *testing.T) {
	mode := os.Getenv("GUESTPROCESS_BOUND_HELPER")
	if mode == "" {
		return
	}
	// Each helper has its own watchdog, independent of the outer test and
	// readiness barriers, so a crashed observer cannot leave benign helpers alive.
	watchdog := time.AfterFunc(30*time.Second, func() { os.Exit(64) })
	defer watchdog.Stop()
	dir := os.Getenv("GUESTPROCESS_BOUND_DIR")
	// Diagnostic-only fixed stage receipts in this test's private directory.
	// They contain no environment, arguments or arbitrary error strings.
	receipt := func(stage string, code int, errno unix.Errno) {
		_ = os.WriteFile(filepath.Join(dir, mode+"-diagnostic"), []byte(fmt.Sprintf("stage=%s code=%d errno=%d\n", stage, code, errno)), 0600)
	}
	receipt("entered", 0, 0)
	if mode == "child" {
		signal, errno := boundChildStartup.signal, boundChildStartup.errno
		if errno != 0 || signal != int32(unix.SIGKILL) || boundChildStartup.tid != os.Getpid() {
			receipt("pdeathsig", int(signal), errno)
			os.Exit(60)
		}
		if err := os.WriteFile(filepath.Join(dir, "child.tmp"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			var errno unix.Errno
			_ = errors.As(err, &errno)
			receipt("write-child", 61, errno)
			os.Exit(61)
		}
		// Publish complete PID bytes atomically; the observer must never see a
		// partially written numeric identity. Both names are in this private dir.
		if err := os.Rename(filepath.Join(dir, "child.tmp"), filepath.Join(dir, "child")); err != nil {
			var errno unix.Errno
			_ = errors.As(err, &errno)
			receipt("rename-child", 65, errno)
			os.Exit(65)
		}
		receipt("child-ready", 0, 0)
		for {
			time.Sleep(time.Second)
		}
	}
	exe, _ := os.Executable()
	ready := make(chan error, 1)
	go func() {
		_, err := LaunchSupervisorBound(context.Background(), Spec{Executable: exe, Args: []string{"-test.run=^TestBoundSupervisorHelper$"}, Env: []string{"GUESTPROCESS_BOUND_HELPER=child", "GUESTPROCESS_BOUND_DIR=" + dir}})
		ready <- err
	}() // The launching caller goroutine must be allowed to exit harmlessly.
	if err := <-ready; err != nil {
		var errno unix.Errno
		_ = errors.As(err, &errno)
		receipt("bound-launch", 62, errno)
		os.Exit(62)
	}
	if err := os.WriteFile(filepath.Join(dir, "caller-returned"), nil, 0600); err != nil {
		var errno unix.Errno
		_ = errors.As(err, &errno)
		receipt("write-caller", 63, errno)
		os.Exit(63)
	}
	receipt("caller-returned", 0, 0)
	for {
		if _, err := os.Stat(filepath.Join(dir, "exit")); err == nil {
			os.Exit(0)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitFile(t *testing.T, path string) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			return b
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return nil
}

func pollExit(t *testing.T, fd int, timeout int) bool {
	t.Helper()
	for {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, timeout)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return false
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			t.Fatalf("unexpected pidfd events: %x", fds[0].Revents)
		}
		return true
	}
}

func TestBoundSupervisorDeath(t *testing.T) {
	for _, mode := range []string{"normal", "sigkill"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			exe, _ := os.Executable()
			supervisor, err := Launch(context.Background(), Spec{Executable: exe, Args: []string{"-test.run=^TestBoundSupervisorHelper$"}, Env: []string{"GUESTPROCESS_BOUND_HELPER=supervisor", "GUESTPROCESS_BOUND_DIR=" + dir}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if t.Failed() {
					for _, name := range []string{"supervisor-diagnostic", "child-diagnostic", "caller-returned", "child"} {
						data, readErr := os.ReadFile(filepath.Join(dir, name))
						if len(data) > 256 {
							data = data[:256]
						}
						t.Logf("barrier %s present=%t notExist=%t bytes=%q", name, readErr == nil, errors.Is(readErr, os.ErrNotExist), data)
					}
					// Wait only observes the already owned supervisor; it never adopts/signals a PID.
					observe, cancelObserve := context.WithTimeout(context.Background(), time.Millisecond)
					exit, waitErr := supervisor.Wait(observe)
					cancelObserve()
					t.Logf("owned supervisor reaped=%t code=%d waitDeadline=%t waitError=%t", exit.Reaped, exit.Code, errors.Is(waitErr, context.DeadlineExceeded), waitErr != nil)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := supervisor.Kill(ctx); err != nil {
					t.Error(err)
				}
			})
			b := waitFile(t, filepath.Join(dir, "child"))
			pid, err := strconv.Atoi(string(b))
			if err != nil {
				t.Fatal(err)
			}
			// Test-only observation of our explicitly launched benign descendant. The
			// readiness barrier identifies its PID, but its watchdog can expire before
			// acquisition. Never signal through this observation-only descriptor:
			// PID reuse must not turn test cleanup into unrelated-process signaling.
			fd, err := unix.PidfdOpen(pid, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			waitFile(t, filepath.Join(dir, "caller-returned"))
			if pollExit(t, fd, 100) {
				t.Fatal("child died while supervisor alive after caller returned")
			}
			if mode == "normal" {
				if err := os.WriteFile(filepath.Join(dir, "exit"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if exit, err := supervisor.Kill(ctx); err != nil || !exit.Reaped {
					t.Fatalf("kill supervisor: %+v %v", exit, err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if exit, err := supervisor.Wait(ctx); err != nil || !exit.Reaped {
				t.Fatalf("wait supervisor: %+v %v", exit, err)
			}
			// POLLIN proves kernel exit of the orphan, NOT reaping by this observer.
			if !pollExit(t, fd, 5000) {
				t.Fatal("child survived supervisor death")
			}
		})
	}
}

func waitSlots(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(supervisorSlots) == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("creator admissions = %d, want %d", len(supervisorSlots), want)
}

func TestBoundLaunchCancellationErrorsAndBudget(t *testing.T) {
	waitSlots(t, 0)
	for _, tc := range []struct {
		ctx  context.Context
		spec Spec
		want error
	}{
		{nil, helperSpec(t, "exit"), ErrInvalid},
		{context.Background(), Spec{}, ErrInvalid},
		{context.Background(), Spec{Executable: "/nonexistent/bound-test", Env: []string{}}, os.ErrNotExist},
	} {
		p, err := LaunchSupervisorBound(tc.ctx, tc.spec)
		if p != nil || !errors.Is(err, tc.want) {
			t.Fatalf("launch = %v %v; want %v", p, err, tc.want)
		}
		waitSlots(t, 0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if p, err := LaunchSupervisorBound(ctx, helperSpec(t, "sleep")); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled launch: %v %v", p, err)
	}
	// Reserve the same finite admission slots without creating 16 test children.
	for range maxSupervisorBound {
		supervisorSlots <- struct{}{}
	}
	p, err := LaunchSupervisorBound(context.Background(), helperSpec(t, "sleep"))
	for range maxSupervisorBound {
		<-supervisorSlots
	}
	if p != nil || !errors.Is(err, ErrSupervisorBudget) {
		t.Fatalf("budget: %v %v", p, err)
	}
	p, err = LaunchSupervisorBound(context.Background(), helperSpec(t, "sleep"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := p.Kill(ctx)
		if err != nil {
			t.Error(err)
		}
	})
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if exit, err := p.Wait(short); !errors.Is(err, context.DeadlineExceeded) || exit.Reaped {
		t.Fatalf("canceled wait: %+v %v", exit, err)
	}
	if len(supervisorSlots) != 1 {
		t.Fatal("wait cancellation retired creator thread")
	}
	killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer killCancel()
	if exit, err := p.Kill(killCtx); err != nil || !exit.Reaped {
		t.Fatal(fmt.Sprint(exit, err))
	}
	waitSlots(t, 0)
}

// Cancel between the public precheck and the locked creator's precheck.
// This deterministic context has no child or blocked exec to abandon.
type cancelOnCreatorContext struct {
	context.Context
	calls atomic.Int32
}

func (c *cancelOnCreatorContext) Err() error {
	if c.calls.Add(1) > 1 {
		return context.Canceled
	}
	return nil
}

func TestBoundCreatorPrecheckAndNaturalReap(t *testing.T) {
	waitSlots(t, 0)
	ctx := &cancelOnCreatorContext{Context: context.Background()}
	if p, err := LaunchSupervisorBound(ctx, helperSpec(t, "exit")); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("creator canceled: %v %v", p, err)
	}
	waitSlots(t, 0)
	p, err := LaunchSupervisorBound(context.Background(), helperSpec(t, "exit"))
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exit, err := p.Wait(deadline); err != nil || !exit.Reaped || exit.Code != 0 {
		t.Fatalf("natural exit: %+v %v", exit, err)
	}
	waitSlots(t, 0)
}

func TestWaitFailureRemainsFailure(t *testing.T) {
	// Synthetic completion models an unexpected kernel wait failure without
	// inducing forbidden external reaping or invalidating a live child's pidfd.
	failure := errors.New("synthetic wait failure")
	p := &Process{fd: -1, done: make(chan struct{}), waitErr: failure}
	close(p.done)
	for _, wait := range []func(context.Context) (Exit, error){p.Wait, p.Kill} {
		exit, err := wait(context.Background())
		if !errors.Is(err, failure) || exit.Reaped {
			t.Fatalf("failure became success: %+v %v", exit, err)
		}
	}
}

// Negative control: the same helper must refuse an ordinary unbound launch.
// This catches fabricated startup success or a helper that sets PDEATHSIG itself.
func TestBoundSupervisorHelperRejectsUnbound(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Launch(context.Background(), Spec{Executable: exe, Args: []string{"-test.run=^TestBoundSupervisorHelper$"}, Env: []string{"GUESTPROCESS_BOUND_HELPER=child", "GUESTPROCESS_BOUND_DIR=" + dir}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := p.Kill(ctx); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exit, err := p.Wait(ctx)
	if err != nil || !exit.Reaped || exit.Code != 60 {
		t.Fatalf("unbound helper accepted or unknown: %+v %v", exit, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "child")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unbound child published readiness: %v", err)
	}
}
