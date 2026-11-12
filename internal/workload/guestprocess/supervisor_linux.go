//go:build linux

package guestprocess

import (
	"context"
	"runtime"
	"syscall"
)

// Bound the extra OS threads independently from the scheduler's GOMAXPROCS.
// This budget bounds creator-thread retention, not the number of live children.
const maxSupervisorBound = 16

var supervisorSlots = make(chan struct{}, maxSupervisorBound)

// LaunchSupervisorBound opts into Linux creator-thread-death SIGKILL for this
// single trusted child. It is not durable ownership, descendant containment or
// a guarantee against a stopped/hung supervisor. The executable must preserve
// PDEATHSIG (in particular, no privilege-changing exec or clearing the signal).
//
// A dedicated goroutine pins the creating OS thread until the reaper finishes.
// On wait failure, retiring that thread requests SIGKILL without claiming exit.
// Caller goroutine exit and Wait cancellation therefore cannot kill the child.
// Admission is nonblocking and bounded; ErrSupervisorBudget starts no child.
// The exec handshake, as with Launch, is synchronous and not cancellable.
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
	type result struct {
		p   *Process
		err error
	}
	ready := make(chan result, 1)
	// Own slices before handing execution to the dedicated creator goroutine.
	spec.Args = append([]string(nil), spec.Args...)
	spec.Env = append([]string{}, spec.Env...)
	go func() {
		runtime.LockOSThread()
		// Never unlock: exiting this goroutine retires its OS thread. This happens
		// on launch failure or after the sole owner finishes its wait. On uncertainty
		// retirement requests kernel SIGKILL, but supplies no exit/reaping evidence.
		defer func() { <-supervisorSlots }()
		p, err := launch(ctx, spec, syscall.SIGKILL)
		ready <- result{p, err}
		if err != nil {
			return
		}
		// The original wait result (including failure) remains on p. Retiring
		// the thread never changes it or closes a retained ownership descriptor.
		_, _ = p.Wait(context.Background())
	}()
	// Never abandon an in-flight launch on ctx cancellation: it may own a child.
	r := <-ready
	return r.p, r.err
}
