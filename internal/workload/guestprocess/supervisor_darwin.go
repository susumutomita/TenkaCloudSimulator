//go:build darwin

package guestprocess

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

// LaunchSupervisorBound starts a child that is killed and reaped by its private
// supervisor when this process is lost or Kill is requested. It is not durable
// ownership, descendant containment or a guarantee against a stopped/hung
// supervisor. Admission is nonblocking and bounded; ErrSupervisorBudget starts
// no child. The start handshake is synchronous and not cancellable.
func LaunchSupervisorBound(ctx context.Context, spec Spec) (*Process, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	select {
	case supervisorSlots <- struct{}{}:
	default:
		return nil, ErrSupervisorBudget
	}
	spec.Args = append([]string(nil), spec.Args...)
	spec.Env = append([]string{}, spec.Env...)
	p, err := launchWithFile(ctx, spec, nil, os.StartProcess)
	if p == nil {
		if !errors.Is(err, ErrOwnershipUncertain) {
			<-supervisorSlots
		}
		return nil, err
	}
	go func() {
		// The slot is held until the sole owner has observed the outcome. An
		// uncertain outcome keeps its slot, as retained debt does on Linux.
		if exit, _ := p.Wait(context.Background()); exit.Reaped {
			<-supervisorSlots
		}
	}()
	return p, err
}

// RunSupervisorIfRequested turns this process into a private child supervisor
// when, and only when, guestprocess launched it as one. Executables that launch
// Darwin children call it first in main, before any other work.
func RunSupervisorIfRequested() {
	if len(os.Args) < 4 || os.Args[1] != supervisorArg {
		return
	}
	os.Exit(supervise(os.Args[2], os.Args[3], os.Args[4:]))
}

// supervise starts exactly one child and owns it until it is reaped. The owner
// is reached only through FD4; FD3 is passed to the child only in fd3 mode.
func supervise(mode, executable string, args []string) int {
	var stat unix.Stat_t
	if unix.Fstat(supervisorControlFD, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return supervisorProtocolCode
	}
	// Neither private descriptor may reach the child unless explicitly selected.
	unix.CloseOnExec(supervisorControlFD)
	control := os.NewFile(supervisorControlFD, "guest-supervisor-control")
	files := []*os.File{os.Stdin, os.Stdout, os.Stderr}
	var private *os.File
	switch mode {
	case "fd3":
		private = os.NewFile(PrivateFD, "guest-private")
		files = append(files, private)
	case "none":
		if err := unix.Close(PrivateFD); err != nil {
			return supervisorProtocolCode
		}
	default:
		return supervisorProtocolCode
	}
	if private != nil {
		unix.CloseOnExec(PrivateFD)
	}
	// A direct termination signal to this supervisor also kills and reaps the
	// child, so a supervisor never exits leaving its child behind.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	child, err := os.StartProcess(executable, append([]string{executable}, args...), &os.ProcAttr{
		Dir: "/", Env: os.Environ(), Files: files,
	})
	if private != nil {
		// Only the child keeps the private endpoint.
		_ = private.Close()
	}
	if err != nil {
		_, _ = control.Write([]byte{receiptStartFailed})
		return supervisorStartFailedCode
	}
	var mu sync.Mutex
	reaped := false
	kill := func() {
		mu.Lock()
		defer mu.Unlock()
		// The child is unreaped, so its PID cannot have been reused.
		if !reaped {
			_ = unix.Kill(child.Pid, unix.SIGKILL)
		}
	}
	go func() {
		<-signals
		kill()
	}()
	if _, err := control.Write([]byte{receiptStarted}); err != nil {
		kill()
	}
	waitForExitOrTermination(child.Pid, kill)
	mu.Lock()
	state, err := child.Wait()
	reaped = true
	mu.Unlock()
	if err != nil {
		return supervisorProtocolCode
	}
	var receipt [5]byte
	receipt[0] = receiptReaped
	binary.BigEndian.PutUint32(receipt[1:], uint32(int32(state.ExitCode())))
	_, _ = control.Write(receipt[:])
	return 0
}

// waitForExitOrTermination returns once the child has exited (without reaping
// it). Control EOF, unexpected control input or any kqueue failure requests a
// kill first; the function still returns only after the child's exit.
func waitForExitOrTermination(pid int, kill func()) {
	kq, err := unix.Kqueue()
	if err != nil {
		kill()
		return
	}
	defer unix.Close(kq)
	unix.CloseOnExec(kq)
	var change [1]unix.Kevent_t
	// One notification suffices: EOF and unexpected input both mean terminate.
	unix.SetKevent(&change[0], supervisorControlFD, unix.EVFILT_READ, unix.EV_ADD|unix.EV_ONESHOT)
	if _, err := unix.Kevent(kq, change[:], nil, nil); err != nil {
		kill()
	}
	unix.SetKevent(&change[0], pid, unix.EVFILT_PROC, unix.EV_ADD)
	change[0].Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, change[:], nil, nil); err != nil {
		// ESRCH: our unreaped child has already exited. Otherwise its exit can
		// no longer be observed here, so kill it; the caller's wait reaps it.
		if !errors.Is(err, unix.ESRCH) {
			kill()
		}
		return
	}
	events := make([]unix.Kevent_t, 2)
	for {
		n, err := unix.Kevent(kq, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			kill()
			return
		}
		for _, event := range events[:n] {
			switch event.Filter {
			case unix.EVFILT_PROC:
				return
			case unix.EVFILT_READ:
				kill()
			}
		}
	}
}
