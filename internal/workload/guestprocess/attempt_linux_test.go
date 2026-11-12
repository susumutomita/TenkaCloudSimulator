//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const inheritedMarker = "private-fd3"

// Only this harmless, non-daemonizing executable is launched. The pipe carries
// a fixed test marker, not VM control data; there are no sockets or network I/O.
func TestPrivateFDHelper(t *testing.T) {
	mode := os.Getenv("GUESTPROCESS_PRIVATE_HELPER")
	if mode == "" {
		return
	}
	watchdog := time.AfterFunc(20*time.Second, func() { os.Exit(81) })
	defer watchdog.Stop()
	if os.Getenv("GUESTPROCESS_MUST_NOT_INHERIT") != "" {
		os.Exit(82)
	}
	for _, fd := range []int{0, 1, 2} {
		if target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd)); err != nil || target != "/dev/null" {
			os.Exit(83)
		}
	}
	flags, err := unix.FcntlInt(PrivateFD, unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_WRONLY {
		os.Exit(84)
	}
	target, err := os.Readlink("/proc/self/fd/3")
	if err != nil || target != os.Getenv("GUESTPROCESS_PIPE_TARGET") {
		os.Exit(85)
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		os.Exit(86)
	}
	for _, entry := range entries {
		value, _ := os.Readlink("/proc/self/fd/" + entry.Name())
		if value == os.Getenv("GUESTPROCESS_FORBIDDEN_FILE") || (value == target && entry.Name() != "3") {
			os.Exit(87)
		}
	}
	if _, err := os.NewFile(PrivateFD, "test-private").Write([]byte(inheritedMarker)); err != nil {
		os.Exit(88)
	}
	if mode == "sleep" {
		time.Sleep(15 * time.Second)
	}
	os.Exit(0)
}

func privateSpec(t *testing.T, mode string, writer *os.File) Spec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(int(writer.Fd())))
	if err != nil {
		t.Fatal(err)
	}
	sentinel, err := os.CreateTemp(t.TempDir(), "not-inherited-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sentinel.Close() })
	return Spec{Executable: exe, Args: []string{"-test.run=^TestPrivateFDHelper$"}, Env: []string{
		"GUESTPROCESS_PRIVATE_HELPER=" + mode,
		"GUESTPROCESS_PIPE_TARGET=" + target,
		"GUESTPROCESS_FORBIDDEN_FILE=" + sentinel.Name(),
	}}
}

func anonymousPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	if err := r.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return r, w
}

func attemptContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func cleanAttempt(t *testing.T, a *LaunchAttempt) {
	t.Helper()
	t.Cleanup(func() {
		_ = a.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		exit, err := a.WaitCleanup(ctx)
		if err != nil || !exit.Reaped {
			t.Errorf("attempt cleanup = %+v, %v", exit, err)
		}
	})
}

func readMarker(t *testing.T, reader *os.File) {
	t.Helper()
	b := make([]byte, len(inheritedMarker))
	if _, err := io.ReadFull(reader, b); err != nil || string(b) != inheritedMarker {
		t.Fatalf("marker = %q, %v", b, err)
	}
}

func requireEOF(t *testing.T, reader *os.File) {
	t.Helper()
	var b [1]byte
	if n, err := reader.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("parent or child descriptor retained: %d %v", n, err)
	}
}

func TestPrivateFDInheritanceAndNaturalCleanup(t *testing.T) {
	waitSlots(t, 0)
	t.Setenv("GUESTPROCESS_MUST_NOT_INHERIT", "not-inherited")
	r, w := anonymousPipe(t)
	a, err := BeginSupervisorBoundLaunch(context.Background(), privateSpec(t, "exit", w), w)
	if err != nil {
		t.Fatal(err)
	}
	cleanAttempt(t, a)
	// The accepted attempt owns its duplicate, independent of caller closure.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	readMarker(t, r)
	ctx := attemptContext(t)
	exit, err := a.WaitCleanup(ctx)
	if err != nil || !exit.Reaped || exit.Code != 0 {
		t.Fatalf("cleanup = %+v, %v", exit, err)
	}
	// Natural completion must not turn successful readiness into cancellation.
	for range 3 {
		p, err := a.Wait(ctx)
		if err != nil || p == nil {
			t.Fatalf("repeated readiness = %v, %v", p, err)
		}
	}
	requireEOF(t, r)
	waitSlots(t, 0)
}

func TestPrivateFDBorrowedOriginalAndLifetimeCancellation(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := BeginSupervisorBoundLaunch(ctx, privateSpec(t, "sleep", w), w)
	if err != nil {
		t.Fatal(err)
	}
	cleanAttempt(t, a)
	p, err := a.Wait(attemptContext(t))
	if err != nil || p == nil {
		t.Fatalf("ready = %v, %v", p, err)
	}
	readMarker(t, r)
	if len(supervisorSlots) != 1 {
		t.Fatal("live child released its creator admission")
	}
	cancel() // Still owns and kills the child after exposing its Process.
	exit, err := a.WaitCleanup(attemptContext(t))
	if err != nil || !exit.Reaped || exit.Code != -1 {
		t.Fatalf("canceled cleanup = %+v, %v", exit, err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("attempt closed caller's original: %v", err)
	}
	var b [1]byte
	if n, err := r.Read(b[:]); n != 1 || err != nil || b[0] != 'x' {
		t.Fatalf("original pipe = %q, %d, %v", b, n, err)
	}
	_ = w.Close()
	requireEOF(t, r)
	waitSlots(t, 0)
}

// Delay only the return from this exact harmless child's StartProcess call.
// This models unavailable exec completion, not a cancellable kernel handshake.
func TestPrivateFDCanceledLateLaunchRemainsOwned(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	start := func(name string, args []string, attr *os.ProcAttr) (*os.Process, error) {
		if len(attr.Files) != PrivateFD+1 || attr.Sys.Pdeathsig != unix.SIGKILL {
			return nil, errors.New("unexpected launch attributes")
		}
		child, err := os.StartProcess(name, args, attr)
		close(entered)
		<-release
		return child, err
	}
	a, err := beginSupervisorBoundLaunch(ctx, privateSpec(t, "sleep", w), w, start)
	if err != nil {
		t.Fatal(err)
	}
	cleanAttempt(t, a)
	t.Cleanup(unblock)
	_ = w.Close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start not reached")
	}
	readMarker(t, r)
	cancel()
	before := time.Now()
	if p, err := a.Wait(attemptContext(t)); p != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled readiness = %v, %v", p, err)
	}
	if time.Since(before) > time.Second {
		t.Fatal("caller waited for blocked start")
	}
	short, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if exit, err := a.WaitCleanup(short); exit.Reaped || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("premature cleanup = %+v, %v", exit, err)
	}
	attempts.Lock()
	_, retained := attempts.retained[a]
	attempts.Unlock()
	if !retained || len(supervisorSlots) != 1 {
		t.Fatal("pending late child lost ownership/admission")
	}
	unblock()
	exit, err := a.WaitCleanup(attemptContext(t))
	if err != nil || !exit.Reaped || exit.Code != -1 {
		t.Fatalf("late child cleanup = %+v, %v", exit, err)
	}
	requireEOF(t, r)
	waitSlots(t, 0)
}

func TestPrivateFDObservationTimeoutDoesNotCancel(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	start := func(name string, args []string, attr *os.ProcAttr) (*os.Process, error) {
		close(entered)
		<-release
		return os.StartProcess(name, args, attr)
	}
	a, err := beginSupervisorBoundLaunch(context.Background(), privateSpec(t, "sleep", w), w, start)
	if err != nil {
		t.Fatal(err)
	}
	cleanAttempt(t, a)
	t.Cleanup(unblock)
	<-entered
	short, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if p, err := a.Wait(short); p != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("observation = %v, %v", p, err)
	}
	if a.ctx.Err() != nil || len(supervisorSlots) != 1 {
		t.Fatal("observer canceled attempt")
	}
	unblock()
	p, err := a.Wait(attemptContext(t))
	if err != nil || p == nil {
		t.Fatalf("later readiness = %v, %v", p, err)
	}
	readMarker(t, r)
	// Creator still owns a live child after a short-lived observer returns.
	if len(supervisorSlots) != 1 {
		t.Fatal("live creator admission lost")
	}
}

func TestPrivateFDRejectBeforeAdmissionOrStart(t *testing.T) {
	waitSlots(t, 0)
	_, w := anonymousPipe(t)
	valid := privateSpec(t, "exit", w)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	regular, err := os.CreateTemp(t.TempDir(), "regular-")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	var called atomic.Bool
	start := func(string, []string, *os.ProcAttr) (*os.Process, error) {
		called.Store(true)
		return nil, errors.New("must not start")
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		spec Spec
		file *os.File
		want error
	}{
		{"nil-context", nil, valid, w, ErrInvalid},
		{"canceled", canceled, valid, w, context.Canceled},
		{"invalid-spec", context.Background(), Spec{}, w, ErrInvalid},
		{"nil-file", context.Background(), valid, nil, ErrInvalid},
		{"closed-file", context.Background(), valid, closed, ErrInvalid},
		{"regular-file", context.Background(), valid, regular, ErrUnsupported},
		{"directory", context.Background(), valid, directory, ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, err := beginSupervisorBoundLaunch(tc.ctx, tc.spec, tc.file, start)
			if a != nil || !errors.Is(err, tc.want) {
				t.Fatalf("rejected = %v %v", a, err)
			}
			waitSlots(t, 0)
		})
	}
	for range maxSupervisorBound {
		supervisorSlots <- struct{}{}
	}
	a, err := beginSupervisorBoundLaunch(context.Background(), valid, w, start)
	for range maxSupervisorBound {
		<-supervisorSlots
	}
	if a != nil || !errors.Is(err, ErrSupervisorBudget) || called.Load() {
		t.Fatalf("budget/rejection started work = %v %v %v", a, err, called.Load())
	}
	// Every rejection leaves the original file usable.
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateFDExecFailure(t *testing.T) {
	waitSlots(t, 0)
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		executable string
		want       error
	}{
		{"missing", context.Background(), "/nonexistent/guestprocess-private-test", os.ErrNotExist},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := anonymousPipe(t)
			spec := privateSpec(t, "exit", w)
			if tc.executable != "" {
				spec.Executable = tc.executable
			}
			a, err := BeginSupervisorBoundLaunch(tc.ctx, spec, w)
			if err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			if p, err := a.Wait(attemptContext(t)); p != nil || !errors.Is(err, tc.want) {
				t.Fatalf("ready = %v %v", p, err)
			}
			if exit, err := a.WaitCleanup(attemptContext(t)); exit.Reaped || !errors.Is(err, tc.want) {
				t.Fatalf("cleanup = %+v %v", exit, err)
			}
			requireEOF(t, r)
			waitSlots(t, 0)
		})
	}
}

func TestPrivateFDAttemptInvalidObservers(t *testing.T) {
	for _, a := range []*LaunchAttempt{nil, {}} {
		for _, ctx := range []context.Context{nil, context.Background()} {
			if _, err := a.Wait(ctx); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			if _, err := a.WaitCleanup(ctx); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		}
		if err := a.Cancel(); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}

func TestPrivateFDConcurrentObservationAndCancel(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	a, err := BeginSupervisorBoundLaunch(context.Background(), privateSpec(t, "sleep", w), w)
	if err != nil {
		t.Fatal(err)
	}
	cleanAttempt(t, a)
	readMarker(t, r)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if err := a.Cancel(); err != nil {
				t.Error(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if exit, err := a.WaitCleanup(ctx); err != nil || !exit.Reaped {
				t.Errorf("cleanup = %+v %v", exit, err)
			}
			if p, err := a.Wait(ctx); p != nil || !errors.Is(err, context.Canceled) {
				t.Errorf("canceled = %v %v", p, err)
			}
		})
	}
	wg.Wait()
	waitSlots(t, 0)
}

func TestPrivateFDConcurrentOriginalClose(t *testing.T) {
	// Atomic duplicate versus Close yields either a valid owned pipe or a
	// rejection. No test launches, opens sockets, or intentionally reuses an FD.
	for range 20 {
		_, w := anonymousPipe(t)
		done := make(chan struct{})
		go func() { _ = w.Close(); close(done) }()
		duplicate, err := duplicateAnonymousPipe(w)
		<-done
		if err != nil {
			if !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
			continue
		}
		if _, err := duplicate.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		_ = duplicate.Close()
	}
}

// Fixture admission is used only for pre-start and synthetic failure timing;
// normal API paths are covered above. It never adopts an outside process.
func admittedAttempt(t *testing.T, parent context.Context, original *os.File) (*LaunchAttempt, *os.File) {
	t.Helper()
	private, err := duplicateAnonymousPipe(original)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(parent)
	a := &LaunchAttempt{ctx: ctx, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	supervisorSlots <- struct{}{}
	attempts.Lock()
	attempts.retained[a] = struct{}{}
	attempts.Unlock()
	return a, private
}

func TestPrivateFDPreStartCancellation(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	spec := privateSpec(t, "exit", w)
	a, private := admittedAttempt(t, context.Background(), w)
	_ = a.Cancel()
	_ = w.Close()
	var called atomic.Bool
	go a.run(spec, private, func(string, []string, *os.ProcAttr) (*os.Process, error) {
		called.Store(true)
		return nil, errors.New("must not start")
	}, (*os.File).Close)
	if exit, err := a.WaitCleanup(attemptContext(t)); exit.Reaped || !errors.Is(err, context.Canceled) || called.Load() {
		t.Fatalf("pre-start cancellation = %+v %v, called %v", exit, err, called.Load())
	}
	requireEOF(t, r)
	waitSlots(t, 0)
}

func TestPrivateFDCloseUncertaintyKillsChildRetainsDebt(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	spec := privateSpec(t, "sleep", w)
	a, private := admittedAttempt(t, context.Background(), w)
	// Actually close the duplicate successfully, then report synthetic error.
	// No live descriptor is invalidated and no uncertain real debt is discarded.
	go a.run(spec, private, os.StartProcess, func(f *os.File) error {
		if err := f.Close(); err != nil {
			return err
		}
		return errors.New("synthetic duplicate close uncertainty")
	})
	t.Cleanup(func() {
		_ = a.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if exit, _ := a.WaitCleanup(ctx); !exit.Reaped {
			t.Error("synthetic-close child not reaped; preserving debt")
			return
		}
		attempts.Lock()
		delete(attempts.retained, a)
		attempts.Unlock()
		<-supervisorSlots
	})
	_ = w.Close()
	if p, err := a.Wait(attemptContext(t)); p == nil || !errors.Is(err, ErrOwnershipUncertain) {
		t.Fatalf("close uncertainty readiness = %v %v", p, err)
	}
	exit, err := a.WaitCleanup(attemptContext(t))
	if !exit.Reaped || exit.Code != -1 || !errors.Is(err, ErrOwnershipUncertain) {
		t.Fatalf("close uncertainty cleanup = %+v %v", exit, err)
	}
	attempts.Lock()
	_, retained := attempts.retained[a]
	attempts.Unlock()
	if !retained || len(supervisorSlots) != 1 {
		t.Fatal("close uncertainty incorrectly released admission")
	}
	// Child may be killed before writing its marker. EOF still proves no pipe
	// copy remains, and this synthetic test never claims that of real errors.
	if _, err := io.ReadAll(r); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateFDWaitFailureRemainsOwned(t *testing.T) {
	waitSlots(t, 0)
	_, w := anonymousPipe(t)
	a, private := admittedAttempt(t, context.Background(), w)
	_ = private.Close()
	failure := errors.New("synthetic kernel wait failure")
	p := &Process{fd: -1, done: make(chan struct{}), waitErr: failure}
	close(p.done)
	a.process = p
	close(a.ready)
	a.supervise(p, nil)
	exit, err := a.WaitCleanup(context.Background())
	attempts.Lock()
	_, retained := attempts.retained[a]
	delete(attempts.retained, a) // synthetic process only, not a runtime reset
	attempts.Unlock()
	<-supervisorSlots
	if exit.Reaped || !errors.Is(err, failure) || !retained {
		t.Fatalf("failed wait released ownership = %+v %v %v", exit, err, retained)
	}
}

func TestPrivateFDUncertainLaunchRetainsDebt(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	a, err := beginSupervisorBoundLaunch(context.Background(), privateSpec(t, "exit", w), w,
		func(string, []string, *os.ProcAttr) (*os.Process, error) {
			// No actual start: model an unresolvable launch outcome.
			return nil, ErrOwnershipUncertain
		})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	if exit, err := a.WaitCleanup(attemptContext(t)); exit.Reaped || !errors.Is(err, ErrOwnershipUncertain) {
		t.Fatalf("uncertain launch = %+v %v", exit, err)
	}
	attempts.Lock()
	_, retained := attempts.retained[a]
	delete(attempts.retained, a) // synthetic outcome; no actual child exists
	attempts.Unlock()
	if len(supervisorSlots) != 1 {
		t.Fatal("uncertainty admission lost")
	}
	<-supervisorSlots
	if !retained {
		t.Fatal("uncertainty registry lost")
	}
	requireEOF(t, r)
}

type delayedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestPrivateFDAbsoluteObservationDeadline(t *testing.T) {
	waitSlots(t, 0)
	_, w := anonymousPipe(t)
	ctx := delayedDeadlineContext{Context: context.Background(), deadline: time.Now().Add(-time.Second)}
	if a, err := BeginSupervisorBoundLaunch(ctx, privateSpec(t, "exit", w), w); a != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired admission = %v %v", a, err)
	}
	// A synthetic completed attempt checks observation without running a child.
	lifetime, cancel := context.WithCancelCause(context.Background())
	a := &LaunchAttempt{ctx: lifetime, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	close(a.ready)
	close(a.done)
	if p, err := a.Wait(ctx); p != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ready = %v %v", p, err)
	}
	if exit, err := a.WaitCleanup(ctx); exit.Reaped || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup = %+v %v", exit, err)
	}
	cancel(errAttemptFinished)
	if p, err := a.Wait(ctx); p != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finished = %v %v", p, err)
	}
	waitSlots(t, 0)
}

func TestPrivateFDSnapshotOwnedBeforeReturn(t *testing.T) {
	waitSlots(t, 0)
	r, w := anonymousPipe(t)
	spec := privateSpec(t, "exit", w)
	a, err := BeginSupervisorBoundLaunch(context.Background(), spec, w)
	if err != nil {
		t.Fatal(err)
	}
	cleanAttempt(t, a)
	spec.Args[0] = "not-the-test-helper"
	for i := range spec.Env {
		spec.Env[i] = "NO_HELPER=1"
	}
	_ = w.Close()
	readMarker(t, r)
	if exit, err := a.WaitCleanup(attemptContext(t)); err != nil || !exit.Reaped || exit.Code != 0 {
		t.Fatalf("snapshot = %+v %v", exit, err)
	}
	requireEOF(t, r)
	waitSlots(t, 0)
}

// Model deadline notification lag only, not a physical process deadline.
type expiresAtObservationContext struct {
	context.Context
	calls atomic.Int32
}

func (c *expiresAtObservationContext) Deadline() (time.Time, bool) {
	if c.calls.Add(1) == 1 {
		return time.Now().Add(time.Minute), true
	}
	return time.Now().Add(-time.Second), true
}

func TestPrivateFDCompletedObservationRechecksDeadline(t *testing.T) {
	lifetime, cancel := context.WithCancelCause(context.Background())
	a := &LaunchAttempt{ctx: lifetime, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{}), exit: Exit{Reaped: true}}
	close(a.ready)
	close(a.done)
	if exit, err := a.WaitCleanup(&expiresAtObservationContext{Context: context.Background()}); exit.Reaped || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cleanup passed cutoff = %+v %v", exit, err)
	}
	if p, err := a.Wait(&expiresAtObservationContext{Context: context.Background()}); p != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ready passed cutoff = %v %v", p, err)
	}
	cancel(errAttemptFinished)
	if p, err := a.Wait(&expiresAtObservationContext{Context: context.Background()}); p != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finished ready passed cutoff = %v %v", p, err)
	}
}
