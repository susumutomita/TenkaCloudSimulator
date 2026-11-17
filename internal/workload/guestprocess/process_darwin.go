//go:build darwin

package guestprocess

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// Darwin has neither pidfd nor PDEATHSIG. Every Darwin child therefore runs
// under a private supervisor process: this same executable re-entered through
// RunSupervisorIfRequested. The supervisor starts the trusted child from the
// exact Spec, kills it when its control socket reaches EOF (an explicit Kill,
// or loss of this owner process for any reason), reaps it, and only then sends
// a reap receipt. This process never signals a numeric PID: Kill only shuts the
// write side of the control socket, so PID reuse cannot redirect a signal.
//
// Control protocol (supervisor to owner, in order):
//
//	'S'               the child was started
//	'E'               the child could not be started; the supervisor exits
//	'R' + int32 (BE)  the child was reaped with this exit code (-1 = signal)
//
// Owner to supervisor: EOF requests termination. Any byte also does.
const (
	supervisorArg       = "__tenkacloud-guest-supervisor"
	supervisorControlFD = 4
	receiptStarted      = 'S'
	receiptStartFailed  = 'E'
	receiptReaped       = 'R'
	// supervisorStartFailedCode is the supervisor's exit code after 'E'.
	supervisorStartFailedCode = 127
	// supervisorProtocolCode is the supervisor's exit code for an invalid launch.
	supervisorProtocolCode = 70
)

// Process is an in-memory owner of one supervisor-bound child. Do not copy it.
// As on Linux there is no API to adopt a PID, signal arbitrarily or restart.
type Process struct {
	mu      sync.Mutex
	control *os.File // owner end; nil once the supervisor has been reaped
	killed  bool
	done    chan struct{}
	result  Exit
	waitErr error
}

// Launch creates a supervisor-bound child with fixed argv, explicit environment,
// working directory / and all stdio connected to /dev/null. It does not invoke a
// shell. The start handshake, like a Linux exec, is synchronous and not
// cancellable. After success the caller owns cleanup.
func Launch(ctx context.Context, spec Spec) (*Process, error) {
	return launchWithFile(ctx, spec, nil, os.StartProcess)
}

// supervisorExecutable is the trusted running executable. Tests replace it only
// to observe a failure to locate it.
var supervisorExecutable = os.Executable

func launchWithFile(ctx context.Context, spec Spec, private *os.File, start startFunc) (*Process, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	self, err := supervisorExecutable()
	if err != nil || !filepath.IsAbs(self) {
		return nil, fmt.Errorf("guestprocess: supervisor executable unavailable: %v", err)
	}
	pair, err := socketPair()
	if err != nil {
		return nil, fmt.Errorf("guestprocess: supervisor control: %w", err)
	}
	owner := os.NewFile(uintptr(pair[0]), "guest-supervisor-owner")
	control := os.NewFile(uintptr(pair[1]), "guest-supervisor-control")
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, errors.Join(err, owner.Close(), control.Close())
	}
	defer null.Close()
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, owner.Close(), control.Close())
	}
	// FD3 is the private descriptor slot. Without a private file it holds
	// /dev/null, which the supervisor closes rather than passing to the child.
	mode, slot3 := "none", null
	if private != nil {
		mode, slot3 = "fd3", private
	}
	argv := append([]string{self, supervisorArg, mode, spec.Executable}, spec.Args...)
	supervisor, err := start(self, argv, &os.ProcAttr{
		Dir: "/", Env: append([]string{}, spec.Env...),
		Files: []*os.File{null, null, null, slot3, control},
		Sys:   &syscall.SysProcAttr{Setpgid: true},
	})
	closeErr := control.Close()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("guestprocess: launch: %w", err), owner.Close())
	}
	first, readErr := readByte(owner)
	if readErr == nil && first == receiptStartFailed {
		// The supervisor started no child. Observe its exit before saying so.
		state, waitErr := supervisor.Wait()
		if err := owner.Close(); waitErr == nil && err == nil && closeErr == nil && state.ExitCode() == supervisorStartFailedCode {
			return nil, errors.New("guestprocess: launch: child start failed")
		}
		return nil, fmt.Errorf("%w: supervisor did not confirm a failed child start", ErrOwnershipUncertain)
	}
	p := &Process{control: owner, done: make(chan struct{})}
	if readErr == nil && first == receiptStarted && closeErr == nil {
		go p.reap(supervisor)
		return p, nil
	}
	// Without a start receipt the supervisor may own a child. Request its
	// termination and keep observing it; never claim that no child exists.
	_ = p.shutdown()
	go p.reap(supervisor)
	err = fmt.Errorf("%w: supervisor start unconfirmed: %v", ErrOwnershipUncertain, errors.Join(readErr, closeErr))
	if private == nil {
		return nil, err
	}
	return p, err
}

func readByte(r io.Reader) (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(r, b[:])
	return b[0], err
}

func (p *Process) reap(supervisor *os.Process) {
	// A blocking wait is safe: this owner never signals the supervisor by PID.
	state, err := supervisor.Wait()
	var receipt [5]byte
	var receiptErr error
	if err == nil {
		// The supervisor has exited, so reads return buffered bytes then EOF.
		_, receiptErr = io.ReadFull(p.control, receipt[:])
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case err != nil:
		// Keep the control socket: a later Kill may still request termination.
		p.waitErr = fmt.Errorf("%w: supervisor wait: %v", ErrOwnershipUncertain, err)
	case receiptErr == nil && receipt[0] == receiptReaped && state.ExitCode() == 0:
		p.result = Exit{Reaped: true, Code: int(int32(binary.BigEndian.Uint32(receipt[1:])))}
	default:
		p.waitErr = fmt.Errorf("%w: supervisor exited without a child reap receipt", ErrOwnershipUncertain)
	}
	if err == nil {
		_ = p.control.Close()
		p.control = nil
	}
	close(p.done)
}

// shutdown requests termination through the control socket. p.mu must not be held.
func (p *Process) shutdown() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.control == nil || p.killed {
		return nil
	}
	raw, err := p.control.SyscallConn()
	if err != nil {
		return err
	}
	var shutErr error
	if err := raw.Control(func(fd uintptr) { shutErr = unix.Shutdown(int(fd), unix.SHUT_WR) }); err != nil {
		return err
	}
	if shutErr != nil && !errors.Is(shutErr, unix.ENOTCONN) {
		return shutErr
	}
	p.killed = true
	return nil
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

// Kill asks the supervisor to SIGKILL and reap the child, then waits for its
// receipt. A timeout or missing receipt does not establish termination. It
// kills only the owned child, not its descendants.
func (p *Process) Kill(ctx context.Context) (Exit, error) {
	if err := checkContext(ctx); err != nil {
		return Exit{}, err
	}
	if p == nil || p.done == nil {
		return Exit{}, ErrInvalid
	}
	if err := p.shutdown(); err != nil {
		return Exit{}, fmt.Errorf("guestprocess: kill: %w", err)
	}
	return p.Wait(ctx)
}

// Contain is deliberately unsupported, as on Linux.
func (p *Process) Contain(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	return ErrUnsupported
}
