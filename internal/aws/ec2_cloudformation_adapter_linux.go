//go:build linux

package aws

import (
	"context"
	"errors"
	"sync"
)

type ec2StackAttempt struct {
	runtime         *EC2InstanceRuntime
	plan            *EC2InstancePlan
	mu              sync.Mutex
	started, sealed bool
	cancel          context.CancelFunc
	done            chan struct{}
}

func (r *EC2InstanceRuntime) PrepareStackInstance(ctx context.Context, stack StackRecord, resource StackResource) (StackInstanceAttempt, error) {
	plan, err := r.Prepare(ctx, stack, resource)
	if err != nil {
		return nil, err
	}
	return &ec2StackAttempt{runtime: r, plan: plan, done: make(chan struct{})}, nil
}
func (a *ec2StackAttempt) Launch(ctx context.Context) error {
	a.mu.Lock()
	if a.started || a.sealed {
		a.mu.Unlock()
		return errGuestLease
	}
	a.started = true
	startup, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.mu.Unlock()
	defer close(a.done)
	defer cancel() // Admission only, never the VM lifetime.
	receipt, err := a.runtime.Launch(startup, a.plan)
	a.mu.Lock()
	sealed := a.sealed
	a.mu.Unlock()
	if sealed {
		if receipt != nil {
			err = errors.Join(err, receipt.Seal(ctx))
		}
		return errors.Join(err, ErrEC2GuestAttachmentPending)
	}
	return err
}
func (a *ec2StackAttempt) Seal(ctx context.Context) error {
	a.mu.Lock()
	a.sealed = true
	started, cancel := a.started, a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !started {
		return nil
	}
	return a.runtime.Seal(ctx, a.plan.Region, a.plan.StackID, a.plan.InstanceID)
}
func (a *ec2StackAttempt) WaitCleanup(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrEC2GuestAttachmentPending
	}
	a.mu.Lock()
	started, sealed := a.started, a.sealed
	a.mu.Unlock()
	if !sealed {
		return ErrEC2GuestAttachmentPending
	}
	if !started {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
	}
	return a.runtime.WaitCleanup(ctx, a.plan.Region, a.plan.StackID, a.plan.InstanceID)
}
