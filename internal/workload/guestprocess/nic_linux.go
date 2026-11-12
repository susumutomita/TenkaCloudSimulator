//go:build linux

package guestprocess

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// NICLaunch owns a newly created socket pair and the exact child inheriting its
// peer at FD3. It is never constructed from a caller supplied descriptor or PID.
// This is local process/stream ownership, not durable VM containment evidence.
type NICLaunch struct {
	childDebt  *os.File
	attempt    *LaunchAttempt
	stream     io.ReadWriteCloser
	once       sync.Once
	closeErr   error
	done       chan struct{}
	cleanupErr error
}

var nicOwners = struct {
	sync.Mutex
	retained map[*NICLaunch]struct{}
}{retained: map[*NICLaunch]struct{}{}}
var nicSlots = make(chan struct{}, 8)

// BeginNICLaunch performs real socket creation and process launch. The Spec must
// be a trusted fixed QEMU invocation using socket netdev FD3, never user argv.
// No tests in restricted environments may invoke this real OS entry point.
func BeginNICLaunch(ctx context.Context, spec Spec) (*NICLaunch, error) {
	return beginNICLaunch(ctx, spec, createNICPair, beginOwnedNICAttempt)
}

type nicPair struct {
	parent io.ReadWriteCloser
	child  *os.File
}

func createNICPair() (nicPair, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nicPair{}, err
	}
	return nicPair{os.NewFile(uintptr(fds[0]), "guest-nic-owner"), os.NewFile(uintptr(fds[1]), "guest-nic-child")}, nil
}

// The child descriptor is privately created, not duplicated/adopted from input.
// Existing anonymous-pipe-only BeginSupervisorBoundLaunch remains unchanged.
func beginOwnedNICAttempt(ctx context.Context, spec Spec, child *os.File) (*LaunchAttempt, error) {
	select {
	case supervisorSlots <- struct{}{}:
	default:
		return nil, ErrSupervisorBudget
	}
	deadline, ok := ctx.Deadline()
	if !ok || !time.Now().Before(deadline) {
		<-supervisorSlots
		return nil, ErrInvalid
	}
	timed, stop := context.WithDeadline(ctx, deadline)
	lifetime, cancel := context.WithCancelCause(timed)
	context.AfterFunc(lifetime, stop)
	a := &LaunchAttempt{ctx: lifetime, cancel: cancel, ready: make(chan struct{}), done: make(chan struct{})}
	spec.Args = append([]string(nil), spec.Args...)
	spec.Env = append([]string{}, spec.Env...)
	attempts.Lock()
	attempts.retained[a] = struct{}{}
	attempts.Unlock()
	go a.run(spec, child, os.StartProcess, (*os.File).Close)
	return a, nil
}

func beginNICLaunch(ctx context.Context, spec Spec, pair func() (nicPair, error), launch func(context.Context, Spec, *os.File) (*LaunchAttempt, error)) (*NICLaunch, error) {
	if err := attemptContextError(ctx); err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); !ok || !time.Now().Before(deadline) {
		return nil, ErrInvalid
	}
	if err := validate(spec); err != nil {
		return nil, err
	}
	select {
	case nicSlots <- struct{}{}:
	default:
		return nil, ErrSupervisorBudget
	}
	endpoints, err := pair()
	if err != nil && endpoints.parent == nil && endpoints.child == nil {
		<-nicSlots
		return nil, err
	}
	if err != nil || endpoints.parent == nil || endpoints.child == nil {
		n := &NICLaunch{stream: endpoints.parent, childDebt: endpoints.child, done: make(chan struct{})}
		nicOwners.Lock()
		nicOwners.retained[n] = struct{}{}
		nicOwners.Unlock()
		if endpoints.parent != nil {
			n.closeErr = endpoints.parent.Close()
		}
		if endpoints.child != nil {
			n.closeErr = errors.Join(n.closeErr, endpoints.child.Close())
		}
		n.once.Do(func() {})
		n.cleanupErr = errors.Join(err, ErrOwnershipUncertain, n.closeErr)
		if n.closeErr == nil {
			n.release()
		}
		close(n.done)
		return n, n.cleanupErr
	}
	n := &NICLaunch{stream: endpoints.parent, childDebt: endpoints.child, done: make(chan struct{})}
	nicOwners.Lock()
	nicOwners.retained[n] = struct{}{}
	nicOwners.Unlock()
	a, err := launch(ctx, spec, endpoints.child)
	n.attempt = a
	if err != nil && a == nil {
		// Rejection before attempt ownership leaves both endpoints owned here.
		n.closeErr = errors.Join(endpoints.child.Close(), n.stream.Close())
		n.once.Do(func() {})
		n.cleanupErr = errors.Join(err, n.closeErr)
		if n.closeErr == nil {
			n.release()
		}
		close(n.done)
		return n, err
	}
	if a == nil {
		n.cleanupErr = ErrOwnershipUncertain
		_ = n.Close()
		close(n.done)
		return n, n.cleanupErr
	}
	if err != nil {
		_ = n.Close()
	}
	go func() {
		<-a.ctx.Done()
		_ = n.Close()
		exit, e := a.WaitCleanup(context.Background())
		if !exit.Reaped {
			e = errors.Join(e, ErrOwnershipUncertain)
		}
		_ = n.Close()
		n.cleanupErr = errors.Join(e, n.closeErr)
		if n.cleanupErr == nil {
			n.release()
		}
		close(n.done)
	}()
	return n, err
}
func (n *NICLaunch) release() {
	nicOwners.Lock()
	delete(nicOwners.retained, n)
	nicOwners.Unlock()
	<-nicSlots
}
func (n *NICLaunch) Read(p []byte) (int, error) {
	if n == nil || n.stream == nil {
		return 0, ErrInvalid
	}
	return n.stream.Read(p)
}
func (n *NICLaunch) Write(p []byte) (int, error) {
	if n == nil || n.stream == nil {
		return 0, ErrInvalid
	}
	return n.stream.Write(p)
}

// Close seals IO and requests termination without waiting for the child. Join
// must be observed separately; uncertain errors retain the resource admission.
func (n *NICLaunch) Close() error {
	if n == nil {
		return ErrInvalid
	}
	n.once.Do(func() {
		if n.stream != nil {
			n.closeErr = n.stream.Close()
		}
		if n.attempt != nil {
			n.closeErr = errors.Join(n.closeErr, n.attempt.Cancel())
		}
	})
	return n.closeErr
}
func (n *NICLaunch) WaitReady(ctx context.Context) error {
	if n == nil || n.attempt == nil {
		return ErrInvalid
	}
	_, err := n.attempt.Wait(ctx)
	return err
}
func (n *NICLaunch) WaitCleanup(ctx context.Context) error {
	if err := attemptContextError(ctx); err != nil {
		return err
	}
	if n == nil {
		return ErrInvalid
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-n.done:
		if err := attemptContextError(ctx); err != nil {
			return err
		}
		return n.cleanupErr
	}
}
