//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// PrivateFD is the only inherited non-stdio descriptor supported by an attempt.
// This slot does not qualify a socket, QEMU backend or physical VM transport.
const PrivateFD = 3

// LaunchAttempt owns one supervisor-bound launch and its child lifetime. Do not
// copy it. The lifetime context and Cancel apply even after Wait returns a
// Process. Wait is observation, not an ownership transfer. There is no retry,
// adoption, restart, detach or permission to replay an admitted attempt.
//
// A retained process-local registry keeps uncertain ownership reachable even if
// the caller drops the attempt. Neither it nor the pidfd survives supervisor
// loss. Kernel exec, kill and wait operations are not given a hard deadline.
// Admission bounds pending work, live pinned creators and retained uncertainty
// together to maxSupervisorBound, shared with LaunchSupervisorBound.
type LaunchAttempt struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	ready  chan struct{}
	done   chan struct{}

	// The creator writes these before closing their corresponding channel.
	process    *Process
	launchErr  error
	exit       Exit
	cleanupErr error
}

var attempts = struct {
	sync.Mutex
	retained map[*LaunchAttempt]struct{}
}{retained: make(map[*LaunchAttempt]struct{})}

var errAttemptFinished = errors.New("guestprocess: attempt work finished")

// BeginSupervisorBoundLaunch accepts exactly one trusted anonymous pipe endpoint
// for inheritance at PrivateFD. Other endpoint kinds, including sockets, are
// unsupported until separately qualified. It never creates a pipe or socket.
// This API is opt-in; no VM/default runtime calls it.
//
// The caller always owns privateFile. Before returning an accepted attempt, this
// function duplicates it atomically with close-on-exec under SyscallConn.Control.
// The attempt owns only that duplicate; callers may close their original after
// return. Its parent duplicate remains owned through the synchronous exec
// handshake and is then closed once. Only the child copy occupies FD3.
//
// Immediate errors start no child. Once accepted, caller cancellation can return
// promptly from Wait but cannot interrupt exec. The creator retains ownership,
// kills any late exact child through its pidfd and waits for reaping. Context
// cancellation after a successful Wait also requests child termination. Use a
// context covering the intended child lifetime, not just a startup deadline.
func BeginSupervisorBoundLaunch(ctx context.Context, spec Spec, privateFile *os.File) (*LaunchAttempt, error) {
	return beginSupervisorBoundLaunch(ctx, spec, privateFile, os.StartProcess)
}

func beginSupervisorBoundLaunch(ctx context.Context, spec Spec, privateFile *os.File, start func(string, []string, *os.ProcAttr) (*os.Process, error)) (*LaunchAttempt, error) {
	if err := attemptContextError(ctx); err != nil {
		return nil, err
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	if privateFile == nil {
		return nil, ErrInvalid
	}
	select {
	case supervisorSlots <- struct{}{}:
	default:
		return nil, ErrSupervisorBudget
	}
	private, err := duplicateAnonymousPipe(privateFile)
	if err != nil {
		<-supervisorSlots
		return nil, err
	}
	// Slices belong to the worker after admission, not to the returning caller.
	spec.Args = append([]string(nil), spec.Args...)
	spec.Env = append([]string{}, spec.Env...)
	lifetime, cancel := context.WithCancelCause(ctx)
	a := &LaunchAttempt{ctx: lifetime, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	attempts.Lock()
	attempts.retained[a] = struct{}{}
	attempts.Unlock()
	go a.run(spec, private, start, (*os.File).Close)
	return a, nil
}

func duplicateAnonymousPipe(file *os.File) (*os.File, error) {
	raw, err := file.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("%w: private descriptor: %w", ErrInvalid, err)
	}
	fd := -1
	var dupErr error
	if err = raw.Control(func(original uintptr) {
		fd, dupErr = unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, PrivateFD)
	}); err != nil {
		return nil, fmt.Errorf("%w: private descriptor: %w", ErrInvalid, err)
	}
	if dupErr != nil {
		return nil, fmt.Errorf("guestprocess: duplicate private descriptor: %w", dupErr)
	}
	private := os.NewFile(uintptr(fd), "guestprocess-private")
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err == nil && stat.Mode&unix.S_IFMT != unix.S_IFIFO {
		err = ErrUnsupported
	}
	if err == nil {
		// fstat alone also accepts named FIFOs. Inspect only our private duplicate,
		// never a caller descriptor that could close/reuse between operations.
		var target string
		target, err = os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		if err == nil && (!strings.HasPrefix(target, "pipe:[") || !strings.HasSuffix(target, "]")) {
			err = ErrUnsupported
		}
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("guestprocess: unsupported private endpoint: %w", err), private.Close())
	}
	return private, nil
}

func (a *LaunchAttempt) run(spec Spec, private *os.File, start func(string, []string, *os.ProcAttr) (*os.Process, error), closePrivate func(*os.File) error) {
	runtime.LockOSThread()
	// Never unlock. Retirement on uncertainty requests PDEATHSIG without
	// manufacturing exit evidence or releasing the retained admission debt.
	// This check does not interrupt or bound an already-entered kernel exec.
	checkedStart := func(name string, args []string, attr *os.ProcAttr) (*os.Process, error) {
		if err := attemptContextError(a.ctx); err != nil {
			return nil, err
		}
		return start(name, args, attr)
	}
	p, launchErr := launchWithFile(a.ctx, spec, syscall.SIGKILL, private, checkedStart)
	// The closer is scoped to this attempt; production always uses File.Close.
	closeErr := closePrivate(private)
	if closeErr != nil {
		closeErr = fmt.Errorf("%w: close private parent duplicate: %v", ErrOwnershipUncertain, closeErr)
		// A readiness error must not leave a live child under an unbounded
		// Background lifetime. Reap it while retaining descriptor uncertainty.
		a.cancel(context.Canceled)
	}
	a.process, a.launchErr = p, errors.Join(launchErr, closeErr)
	close(a.ready)
	if p == nil {
		a.finish(Exit{}, a.launchErr, !errors.Is(a.launchErr, ErrOwnershipUncertain))
		return
	}
	if launchErr != nil {
		a.finish(Exit{}, a.launchErr, false)
		return
	}
	a.supervise(p, closeErr)
}

func (a *LaunchAttempt) supervise(p *Process, closeErr error) {
	var exit Exit
	var waitErr error
	select {
	case <-p.done:
		exit, waitErr = p.Wait(context.Background())
	case <-a.ctx.Done():
		// Never pass the canceled lifetime context to Kill: it would skip the
		// signal. This sole creator can wait indefinitely; observers cannot.
		exit, waitErr = p.Kill(context.Background())
		if !exit.Reaped && waitErr != nil {
			// A signal error is not permission to retire a still-live creator.
			// A failed wait returns immediately and retains uncertainty instead.
			var finalErr error
			exit, finalErr = p.Wait(context.Background())
			waitErr = errors.Join(waitErr, finalErr)
		}
	}
	a.finish(exit, errors.Join(closeErr, waitErr), exit.Reaped && closeErr == nil)
}

func (a *LaunchAttempt) finish(exit Exit, err error, released bool) {
	a.exit, a.cleanupErr = exit, err
	a.cancel(errAttemptFinished)
	if released {
		attempts.Lock()
		delete(attempts.retained, a)
		attempts.Unlock()
		<-supervisorSlots
	}
	close(a.done)
}

// Cancel requests termination without waiting for exec or child exit. A late
// child remains owned and is killed/reaped when its exec handshake returns.
func (a *LaunchAttempt) Cancel() error {
	if a == nil || a.cancel == nil {
		return ErrInvalid
	}
	a.cancel(context.Canceled)
	return nil
}

// Wait observes launch readiness. Cancellation bounds this caller only; it does
// not cancel the attempt unless ctx is also its lifetime context. A lifetime
// cancellation observed before returning readiness rejects success, though an
// already-returned Process remains valid and still belongs to the attempt.
// Neither a context error nor an exec error is reaping/cleanup evidence.
// A non-nil Process may accompany a readiness error; it is still owned, and
// callers must use WaitCleanup rather than interpreting the error as no child.
func (a *LaunchAttempt) Wait(ctx context.Context) (*Process, error) {
	if err := attemptContextError(ctx); err != nil {
		return nil, err
	}
	if a == nil || a.ready == nil {
		return nil, ErrInvalid
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.ctx.Done():
		// A definitive launch failure is more useful than internal cleanup's
		// cancellation, but cannot be read until the creator publishes ready.
		select {
		case <-a.ready:
			if err := attemptContextError(ctx); err != nil {
				return nil, err
			}
			if a.launchErr != nil {
				return a.process, a.launchErr
			}
			if context.Cause(a.ctx) == errAttemptFinished {
				return a.process, nil
			}
		default:
		}
		return nil, a.ctx.Err()
	case <-a.ready:
		if a.launchErr != nil {
			return a.process, a.launchErr
		}
		if err := attemptContextError(ctx); err != nil {
			return nil, err
		}
		if err := attemptContextError(a.ctx); err != nil && context.Cause(a.ctx) != errAttemptFinished {
			return nil, err
		}
		return a.process, nil
	}
}

// WaitCleanup observes completion of the sole owner's work. A nil error with
// Reaped=true proves only exact-child reaping. A failed exec can finish with an
// error and no child; a canceled observation proves neither. Uncertain outcomes
// keep their admission and handles even after this method returns an error.
func (a *LaunchAttempt) WaitCleanup(ctx context.Context) (Exit, error) {
	if err := attemptContextError(ctx); err != nil {
		return Exit{}, err
	}
	if a == nil || a.done == nil {
		return Exit{}, ErrInvalid
	}
	select {
	case <-ctx.Done():
		return Exit{}, ctx.Err()
	case <-a.done:
		if err := attemptContextError(ctx); err != nil {
			return Exit{}, err
		}
		return a.exit, a.cleanupErr
	}
}

// Readiness/observation must not turn delayed deadline notification into a
// fresh success. This is a caller-side check, not a physical kill deadline.
func attemptContextError(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}
