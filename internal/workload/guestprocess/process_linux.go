//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Process is an unforgeable in-memory owner of a kernel pidfd. Do not copy it.
// There is intentionally no API to adopt a PID, signal arbitrarily, resume, or
// restart. Losing this supervisor loses the handle; no recovery is claimed.
type Process struct {
	mu      sync.Mutex
	fd      int
	done    chan struct{}
	result  Exit
	waitErr error
	// Retain the original child on an unexpected missing-pidfd outcome from
	// the opt-in attempt path. It cannot be signaled by a numeric PID.
	uncertainChild *os.Process
}

// Launch creates a child with an atomic CLONE_PIDFD handle, fixed argv, explicit
// environment, working directory / and all stdio connected to /dev/null. It does
// not invoke a shell. Context cancellation is checked before launch; Launch's
// synchronous exec handshake is not cancellable. After success the caller owns
// cleanup even if its context is canceled. Wait cancellation does not kill.
func Launch(ctx context.Context, spec Spec) (*Process, error) {
	return launch(ctx, spec, 0)
}

func launch(ctx context.Context, spec Spec, deathSignal syscall.Signal) (*Process, error) {
	return launchWithFile(ctx, spec, deathSignal, nil, os.StartProcess)
}

// start is scoped to one attempt, allowing tests to delay only the return from
// their own harmless child start. Production always supplies os.StartProcess.
func launchWithFile(ctx context.Context, spec Spec, deathSignal syscall.Signal, private *os.File, start func(string, []string, *os.ProcAttr) (*os.Process, error)) (*Process, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	// Fail before launching on kernels without pidfd support. Never adopt the
	// child using a numeric PID. Opening our own pidfd is a capability check only.
	probe, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return nil, fmt.Errorf("guestprocess: pidfd unavailable: %w", err)
	}
	// Verify waitid(P_PIDFD) support before starting a child. Self is not a child,
	// so a supported wait must return ECHILD, not success or EINVAL/ENOSYS/EPERM.
	var info unix.Siginfo
	err = unix.Waitid(unix.P_PIDFD, probe, &info, unix.WEXITED|unix.WNOHANG, nil)
	unix.Close(probe)
	if !errors.Is(err, unix.ECHILD) {
		return nil, fmt.Errorf("guestprocess: pidfd wait unavailable: %v", err)
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer null.Close()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd := -1
	files := []*os.File{null, null, null}
	if private != nil {
		files = append(files, private)
	}
	child, err := start(spec.Executable, append([]string{spec.Executable}, spec.Args...), &os.ProcAttr{
		Dir: "/", Env: append([]string{}, spec.Env...),
		Files: files,
		Sys:   &syscall.SysProcAttr{PidFD: &fd, Setpgid: true, Pdeathsig: deathSignal},
	})
	if err != nil {
		return nil, fmt.Errorf("guestprocess: launch: %w", err)
	}
	// Linux kernels supporting pidfd_open support CLONE_PIDFD. Fail closed if a
	// nonconforming kernel violates that contract; never fall back to PID kill.
	if fd < 0 {
		if private != nil {
			p := &Process{fd: -1, done: make(chan struct{}), uncertainChild: child, waitErr: ErrOwnershipUncertain}
			close(p.done)
			return p, fmt.Errorf("%w: child started without required pidfd", ErrOwnershipUncertain)
		}
		child.Release()
		return nil, fmt.Errorf("%w: child started without required pidfd", ErrOwnershipUncertain)
	}
	p := &Process{fd: fd, done: make(chan struct{})}
	go p.reap(child)
	return p, nil
}

func (p *Process) reap(child *os.Process) {
	var info unix.Siginfo
	var err error
	for {
		err = unix.Waitid(unix.P_PIDFD, p.fd, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	// The pidfd first establishes exit without reaping. The child remains a
	// zombie with its PID reserved until this sole owner calls Wait. Go normally
	// uses its duplicate pidfd; even its wait-only PID fallback cannot alias a
	// reused PID while our child remains unreaped. No PID signal is ever sent.
	var state *os.ProcessState
	if err == nil {
		state, err = child.Wait()
	} else {
		child.Release()
	}
	p.mu.Lock()
	p.result = Exit{Reaped: err == nil}
	if state != nil {
		p.result.Code = state.ExitCode()
	}
	p.waitErr = err
	// On a failed wait keep our handle so Kill may still terminate this exact
	// process. A failed wait never establishes exit or successful cleanup.
	if err == nil {
		unix.Close(p.fd)
		p.fd = -1
	}
	close(p.done)
	p.mu.Unlock()
}

// Wait observes completion and reaping of this exact child. Cancellation bounds
// the caller's wait, not the child lifetime. Concurrent/repeated waits are safe.
func (p *Process) Wait(ctx context.Context) (Exit, error) {
	if err := checkContext(ctx); err != nil {
		return Exit{}, err
	}
	if p == nil || p.done == nil {
		return Exit{}, ErrInvalid
	}
	select {
	case <-ctx.Done():
		return Exit{}, ctx.Err()
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.result, p.waitErr
	}
}

// Kill requests SIGKILL through the owned pidfd, then waits for actual kernel
// exit/reaping. A timeout or signal/wait error does not establish termination.
// It kills only the owned process, not a process group or escaped descendants.
func (p *Process) Kill(ctx context.Context) (Exit, error) {
	if err := checkContext(ctx); err != nil {
		return Exit{}, err
	}
	if p == nil || p.done == nil {
		return Exit{}, ErrInvalid
	}
	p.mu.Lock()
	var err error
	if p.fd >= 0 {
		err = unix.PidfdSendSignal(p.fd, unix.SIGKILL, nil, 0)
	}
	p.mu.Unlock()
	if err != nil && !errors.Is(err, unix.ESRCH) {
		return Exit{}, fmt.Errorf("guestprocess: kill: %w", err)
	}
	return p.Wait(ctx)
}

// Contain is deliberately unsupported. Neither successful SIGSTOP delivery nor
// stopping one PID proves that all guest work/descendants have stopped.
func (p *Process) Contain(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	return ErrUnsupported
}
