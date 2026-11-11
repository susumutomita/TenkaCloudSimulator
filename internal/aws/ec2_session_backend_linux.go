//go:build linux

package aws

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestsession"
)

// CanonicalSSMSessionBackend connects only the private endpoint of a retained
// canonical VM. It never launches a guest, accepts a caller path or retries a
// possibly consumed channel. Failed attempts remain bounded tombstones; positively joined guest Exit allows a fresh generation.
type CanonicalSSMSessionBackend struct {
	closed   bool
	runtime  *EC2InstanceRuntime
	mu       sync.Mutex
	attempts map[*ec2InstanceRuntimeEntry]*canonicalSessionAttempt
	history  []*canonicalSessionAttempt
	dial     func(context.Context, string, string) (net.Conn, error)
}
type canonicalSessionAttempt struct {
	generation int
	done       chan struct{}
	conn       *canonicalSessionConn
	driver     *guestsession.Driver
	err        error
}

func NewCanonicalSSMSessionBackend(runtime *EC2InstanceRuntime) (*CanonicalSSMSessionBackend, error) {
	if runtime == nil {
		return nil, errGuestLease
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.shutting {
		return nil, errGuestLease
	}
	if runtime.sessionBackend == nil {
		runtime.sessionBackend = &CanonicalSSMSessionBackend{runtime: runtime, attempts: make(map[*ec2InstanceRuntimeEntry]*canonicalSessionAttempt), dial: (&net.Dialer{}).DialContext}
	}
	return runtime.sessionBackend, nil
}
func (b *CanonicalSSMSessionBackend) target(ctx context.Context, region, id string) (*ec2InstanceRuntimeEntry, SSMSessionTarget, error) {
	if b == nil || ctx == nil || ctx.Err() != nil {
		return nil, SSMSessionTarget{}, errGuestLease
	}
	b.runtime.mu.Lock()
	e := b.runtime.retained[id]
	if b.runtime.shutting || e == nil || e.sealed || e.plan.Region != region || e.backend == nil || e.receipt == nil || !e.receipt.Ready() {
		b.runtime.mu.Unlock()
		return nil, SSMSessionTarget{}, ErrEC2GuestAttachmentPending
	}
	p := e.backend.Prepared()
	if p == nil || p.SessionSocket() == "" || p.Record().SessionImageSHA256 != b.runtime.config.SessionImageSHA256 {
		b.runtime.mu.Unlock()
		return nil, SSMSessionTarget{}, ErrEC2GuestAttachmentPending
	}
	target := SSMSessionTarget{Region: region, StackID: e.plan.StackID, InstanceID: id}
	canonical := e.plan.canonical
	b.runtime.mu.Unlock()
	var state, plan string
	if err := b.runtime.db.QueryRowContext(ctx, `SELECT state,plan FROM aws_ec2_instance_runtime WHERE region=? AND stack_id=? AND instance=?`, region, target.StackID, id).Scan(&state, &plan); err != nil || state != "ready" || plan != canonical || !b.current(e, target) {
		return nil, SSMSessionTarget{}, ErrEC2GuestAttachmentPending
	}
	return e, target, nil
}

// current uses the retained actor fence; SQL is never held across stream IO.
func (b *CanonicalSSMSessionBackend) current(e *ec2InstanceRuntimeEntry, t SSMSessionTarget) bool {
	b.runtime.mu.Lock()
	defer b.runtime.mu.Unlock()
	return !b.runtime.shutting && b.runtime.retained[t.InstanceID] == e && !e.sealed && e.plan.Region == t.Region && e.plan.StackID == t.StackID && e.receipt != nil && e.receipt.Ready()
}
func (b *CanonicalSSMSessionBackend) ResolveSessionTarget(ctx context.Context, region, id string) (SSMSessionTarget, error) {
	_, t, err := b.target(ctx, region, id)
	return t, err
}
func (b *CanonicalSSMSessionBackend) OpenSessionChannel(ctx context.Context, target SSMSessionTarget) (driver *guestsession.Driver, err error) {
	e, actual, err := b.target(ctx, target.Region, target.InstanceID)
	if err != nil || actual != target {
		return nil, ErrEC2GuestAttachmentPending
	}
	// Capture only the same retained immutable preparation; no caller endpoint.
	p := e.backend.Prepared()
	e.receipt.mu.Lock()
	lifetime, resource := e.receipt.lifetime, e.receipt.resource
	e.receipt.mu.Unlock()
	if resource == nil || resource.closed == nil {
		return nil, ErrEC2GuestAttachmentPending
	}
	deadline, ok := ctx.Deadline()
	ownerDeadline, ownerOK := lifetime.Deadline()
	if !ok || !ownerOK || !deadline.After(time.Now()) {
		return nil, errGuestLease
	}
	if ownerDeadline.Before(deadline) {
		deadline = ownerDeadline
	}
	owned, cancel := context.WithDeadline(ctx, deadline)
	stopOwner := context.AfterFunc(lifetime, cancel)
	b.mu.Lock()
	prior := b.attempts[e]
	b.mu.Unlock()
	if prior != nil {
		select {
		case <-prior.done:
		default:
			stopOwner()
			cancel()
			return nil, ErrEC2GuestAttachmentPending
		}
		b.mu.Lock()
		oldDriver, oldConn, oldErr, generation := prior.driver, prior.conn, prior.err, prior.generation
		b.mu.Unlock()
		if oldErr != nil || oldDriver == nil || oldConn == nil || generation >= 8 || !oldDriver.ExitAcknowledged() {
			stopOwner()
			cancel()
			return nil, ErrEC2GuestAttachmentPending
		}
		select {
		case <-oldConn.done:
		default:
			stopOwner()
			cancel()
			return nil, ErrEC2GuestAttachmentPending
		}
		if oldConn.closeErr != nil || oldDriver.WaitCleanup(owned) != nil {
			stopOwner()
			cancel()
			return nil, ErrEC2GuestAttachmentPending
		}
	}
	b.mu.Lock()
	if b.closed || b.attempts[e] != prior || len(b.history) >= 8192 || (prior == nil && len(b.attempts) >= 1024) {
		b.mu.Unlock()
		stopOwner()
		cancel()
		return nil, ErrEC2GuestAttachmentPending
	}
	generation := 1
	if prior != nil {
		generation = prior.generation + 1
	}
	attempt := &canonicalSessionAttempt{done: make(chan struct{}), generation: generation}
	b.attempts[e] = attempt
	b.history = append(b.history, attempt)
	b.mu.Unlock()
	defer func() {
		defer close(attempt.done)
		if err != nil {
			stopOwner()
			cancel()
		}
		b.mu.Lock()
		attempt.err = err
		b.mu.Unlock()
	}()
	if current, _, check := b.target(owned, target.Region, target.InstanceID); check != nil || current != e {
		return nil, ErrEC2GuestAttachmentPending
	}
	conn, dialErr := b.dial(owned, "unix", p.SessionSocket())
	if conn != nil {
		wrapped := &canonicalSessionConn{Conn: conn, valid: func() bool {
			return owned.Err() == nil && b.current(e, target)
		}, cancel: cancel, stopOwner: stopOwner, done: make(chan struct{})}
		b.mu.Lock()
		attempt.conn = wrapped
		b.mu.Unlock()
		go func() {
			select {
			case <-resource.closed:
			case <-owned.Done():
			case <-wrapped.done:
				return
			}
			_ = wrapped.Close()
		}()
		conn = wrapped
	}
	if dialErr != nil {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, dialErr
	}
	if conn == nil {
		return nil, errors.New("private session dial returned no channel")
	}
	// Driver.New performs the mandatory exact guest UUID Hello. This never sends
	// Start; a failure is still retained, not a certificate of clean disposal.
	driver, err = guestsession.New(owned, conn, p.Record().UUID)
	b.mu.Lock()
	attempt.driver = driver // Retain even a failed constructor receipt.
	b.mu.Unlock()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if current, _, check := b.target(owned, target.Region, target.InstanceID); check != nil || current != e {
		_ = driver.Close()
		return nil, ErrEC2GuestAttachmentPending
	}
	return driver, nil
}

// Close errors remain attached to the exact retained attempt; no failed close is
// retried or converted into positive cleanup evidence. Reads discard output if
// invalidation raced completion. In-flight writes may have partial effects.
type canonicalSessionConn struct {
	net.Conn
	valid     func() bool
	once      sync.Once
	closeErr  error
	cancel    context.CancelFunc
	stopOwner func() bool
	done      chan struct{}
}

func (c *canonicalSessionConn) Read(p []byte) (int, error) {
	if !c.valid() {
		return 0, ErrEC2GuestAttachmentPending
	}
	n, err := c.Conn.Read(p)
	if !c.valid() {
		clear(p[:n])
		return 0, ErrEC2GuestAttachmentPending
	}
	return n, err
}
func (c *canonicalSessionConn) Write(p []byte) (int, error) {
	if !c.valid() {
		return 0, ErrEC2GuestAttachmentPending
	}
	return c.Conn.Write(p)
}
func (c *canonicalSessionConn) Close() error {
	c.once.Do(func() { c.cancel(); c.stopOwner(); c.closeErr = c.Conn.Close(); close(c.done) })
	return c.closeErr
}

// WaitCleanup observes retained local channel/reader joins after runtime seal.
// It does not prove a guest PTY exited and never retires/reuses the reservation.
func (b *CanonicalSSMSessionBackend) WaitCleanup(ctx context.Context) error {
	if ctx == nil {
		return errGuestLease
	}
	b.mu.Lock()
	b.closed = true
	list := append([]*canonicalSessionAttempt(nil), b.history...)
	if len(list) == 0 {
		for _, a := range b.attempts {
			list = append(list, a)
		}
	}
	b.mu.Unlock()
	var result error
	for _, a := range list {
		select {
		case <-a.done:
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		}
		b.mu.Lock()
		conn, driver, attemptErr := a.conn, a.driver, a.err
		b.mu.Unlock()
		if conn == nil {
			if attemptErr != nil {
				result = errors.Join(result, ErrEC2GuestAttachmentPending)
			}
			continue
		}
		select {
		case <-conn.done:
		case <-ctx.Done():
			return errors.Join(result, ctx.Err())
		}
		result = errors.Join(result, conn.closeErr)
		if driver != nil {
			joined, ok := any(driver).(interface{ WaitCleanup(context.Context) error })
			if !ok {
				result = errors.Join(result, ErrEC2GuestAttachmentPending)
			} else {
				result = errors.Join(result, joined.WaitCleanup(ctx))
			}
		}
	}
	return result
}
