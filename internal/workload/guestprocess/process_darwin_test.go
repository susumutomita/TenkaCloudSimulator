//go:build darwin

package guestprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMain lets the test binary act as the private supervisor, exactly as the
// simulator executable does from main.
func TestMain(m *testing.M) {
	RunSupervisorIfRequested()
	os.Exit(m.Run())
}

func TestDarwinHelper(t *testing.T) {
	mode := os.Getenv("GUESTPROCESS_TEST_HELPER")
	if mode == "" {
		return
	}
	if os.Getenv("GUESTPROCESS_MUST_NOT_INHERIT") != "" {
		os.Exit(41)
	}
	if path := os.Getenv("GUESTPROCESS_PID_FILE"); path != "" {
		if os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
			os.Exit(45)
		}
	}
	switch mode {
	case "exit":
		os.Exit(0)
	case "exit7":
		os.Exit(7)
	case "sleep":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	case "validate":
		// The exact literal argument arrives without a shell.
		if len(os.Args) != 4 || os.Args[3] != "$(not-a-shell); *" {
			os.Exit(42)
		}
		var null unix.Stat_t
		if unix.Stat(os.DevNull, &null) != nil {
			os.Exit(43)
		}
		for _, fd := range []int{0, 1, 2} {
			var st unix.Stat_t
			if unix.Fstat(fd, &st) != nil || st.Rdev != null.Rdev || st.Mode&unix.S_IFMT != unix.S_IFCHR {
				os.Exit(43)
			}
		}
		// Neither the private slot nor the supervisor control socket is inherited.
		for _, fd := range []int{PrivateFD, supervisorControlFD} {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
				os.Exit(46)
			}
		}
		if len(os.Environ()) != 1 {
			os.Exit(47)
		}
		os.Exit(0)
	case "private":
		// FD3 is the private endpoint; echo one byte back through it.
		f := os.NewFile(PrivateFD, "private")
		var b [1]byte
		if _, err := f.Read(b[:]); err != nil {
			os.Exit(48)
		}
		if _, err := f.Write(b[:]); err != nil {
			os.Exit(49)
		}
		if _, err := unix.FcntlInt(supervisorControlFD, unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			os.Exit(46)
		}
		os.Exit(0)
	case "owner":
		// An owner process that launches a sleeping child and then waits to be
		// killed abruptly by the test.
		exe, err := os.Executable()
		if err != nil {
			os.Exit(50)
		}
		spec := Spec{Executable: exe, Args: []string{"-test.run=^TestDarwinHelper$"}, Env: []string{"GUESTPROCESS_TEST_HELPER=sleep", "GUESTPROCESS_PID_FILE=" + os.Getenv("GUESTPROCESS_CHILD_PID_FILE")}}
		if _, err := LaunchSupervisorBound(context.Background(), spec); err != nil {
			os.Exit(51)
		}
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(44)
}

func darwinSpec(t *testing.T, mode string, env ...string) Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Spec{Executable: exe, Args: []string{"-test.run=^TestDarwinHelper$", "--", "$(not-a-shell); *"}, Env: append([]string{"GUESTPROCESS_TEST_HELPER=" + mode}, env...)}
}

func launchDarwin(t *testing.T, mode string, env ...string) *Process {
	t.Helper()
	p, err := Launch(context.Background(), darwinSpec(t, mode, env...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := p.Kill(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return p
}

func waitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && len(raw) > 0 {
			pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child never reported its PID")
	return 0
}

func processGone(pid int) bool {
	err := unix.Kill(pid, 0)
	return errors.Is(err, unix.ESRCH)
}

func TestDarwinNaturalExitExactLaunchAndRepeatedObservation(t *testing.T) {
	t.Setenv("GUESTPROCESS_MUST_NOT_INHERIT", "secret-test-sentinel")
	p := launchDarwin(t, "validate")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := p.Wait(ctx)
	if err != nil || !result.Reaped || result.Code != 0 {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
	for range 3 {
		if result, err = p.Kill(ctx); err != nil || !result.Reaped {
			t.Fatalf("Kill after exit = %+v, %v", result, err)
		}
	}
}

func TestDarwinExitCodeComesFromTheChildReceipt(t *testing.T) {
	p := launchDarwin(t, "exit7")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result, err := p.Wait(ctx); err != nil || !result.Reaped || result.Code != 7 {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
}

func TestDarwinKillOwnedChildConcurrentReportsSignal(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	p := launchDarwin(t, "sleep", "GUESTPROCESS_PID_FILE="+pidFile)
	pid := waitPIDFile(t, pidFile)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			result, err := p.Kill(ctx)
			if err != nil || !result.Reaped || result.Code != -1 {
				t.Errorf("Kill = %+v, %v", result, err)
			}
		})
	}
	wg.Wait()
	if !processGone(pid) {
		t.Fatalf("child %d survived Kill", pid)
	}
}

func TestDarwinTimeoutDoesNotMeanExit(t *testing.T) {
	p := launchDarwin(t, "sleep")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v", err)
	}
}

func TestDarwinStartFailureStartsNoChild(t *testing.T) {
	spec := Spec{Executable: filepath.Join(t.TempDir(), "missing"), Env: []string{}}
	p, err := Launch(context.Background(), spec)
	if p != nil || err == nil || errors.Is(err, ErrOwnershipUncertain) {
		t.Fatalf("Launch = %v, %v", p, err)
	}
}

func TestDarwinRejectInvalidBeforeLaunch(t *testing.T) {
	for _, spec := range []Spec{{Executable: "relative", Env: []string{}}, {Executable: "/bin/true"}, {Executable: "/bin/true", Env: []string{"A=1", "A=2"}}} {
		if p, err := Launch(context.Background(), spec); p != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("Launch(%+v) = %v, %v", spec, p, err)
		}
	}
	saved := supervisorExecutable
	t.Cleanup(func() { supervisorExecutable = saved })
	supervisorExecutable = func() (string, error) { return "", errors.New("no executable") }
	if p, err := Launch(context.Background(), darwinSpec(t, "exit")); p != nil || err == nil {
		t.Fatalf("Launch without supervisor = %v, %v", p, err)
	}
}

func TestDarwinPrivateDescriptorReachesOnlyTheChild(t *testing.T) {
	pair, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	ours := os.NewFile(uintptr(pair[0]), "ours")
	theirs := os.NewFile(uintptr(pair[1]), "theirs")
	defer ours.Close()
	p, err := launchWithFile(context.Background(), darwinSpec(t, "private"), theirs, os.StartProcess)
	_ = theirs.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ours.Write([]byte{'x'}); err != nil {
		t.Fatal(err)
	}
	_ = ours.SetReadDeadline(time.Now().Add(10 * time.Second))
	var b [1]byte
	if _, err := ours.Read(b[:]); err != nil || b[0] != 'x' {
		t.Fatalf("echo = %q, %v", b, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if result, err := p.Wait(ctx); err != nil || !result.Reaped || result.Code != 0 {
		t.Fatalf("Wait = %+v, %v", result, err)
	}
}

func TestDarwinOwnerLossKillsSupervisedChild(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(exe, "-test.run=^TestDarwinHelper$")
	owner.Env = []string{"GUESTPROCESS_TEST_HELPER=owner", "GUESTPROCESS_CHILD_PID_FILE=" + pidFile}
	owner.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _ = owner.Wait() })
	pid := waitPIDFile(t, pidFile)
	if processGone(pid) {
		t.Fatal("child was not running")
	}
	// Abrupt owner loss: no Kill, no deferred cleanup in the owner.
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the loss of its owner", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDarwinPrivatePipeMustBeAnonymous(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if err := requireAnonymousPipe(int(r.Fd())); err != nil {
		t.Fatalf("anonymous pipe rejected: %v", err)
	}
	fifo := filepath.Join(t.TempDir(), "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	named, err := os.OpenFile(fifo, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer named.Close()
	if err := requireAnonymousPipe(int(named.Fd())); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("named FIFO = %v", err)
	}
}

func TestDarwinSupervisorRejectsUnknownMode(t *testing.T) {
	if code := supervise("unknown", "/bin/true", nil); code != supervisorProtocolCode {
		t.Fatalf("supervise = %d", code)
	}
}
