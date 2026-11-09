// Package guestcommand drives noninteractive guest commands through an already
// authorized QGA client. It tracks observations and a cleanup obligation; it
// neither owns a VM nor turns control-channel closure into process cancellation.
package guestcommand

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qga"
)

// MaxExecutionTimeout caps the command observation budget, not guest process
// runtime. A lifecycle owner must service CleanupRequired with actual containment.
const MaxExecutionTimeout = time.Hour

const (
	MinPollInterval = 100 * time.Millisecond
	MaxPolls        = 1024
)

var (
	ErrClosed          = errors.New("guest command driver is closed or quarantined")
	ErrBusy            = errors.New("guest command driver already has an active operation")
	ErrInvalidHandle   = errors.New("guest command handle belongs to another driver")
	ErrInvalidTimeout  = errors.New("guest command timeout must be positive and at most one hour")
	ErrPollTooSoon     = errors.New("guest command status polling is rate limited")
	ErrPollLimit       = errors.New("guest command status poll budget exhausted")
	ErrCleanupRequired = errors.New("guest command outcome is uncertain; lifecycle cleanup is required")
)

type State string

const (
	Starting          State = "starting"
	Accepted          State = "accepted"
	Exited            State = "exited"
	Rejected          State = "rejected"
	OutputUnavailable State = "output-unavailable"
	Uncertain         State = "uncertain"
)

type Reason string

const (
	NoReason              Reason = ""
	DeadlineExpired       Reason = "deadline-expired"
	CancellationRequested Reason = "cancellation-requested"
	ControlLost           Reason = "control-lost"
	DriverClosed          Reason = "driver-closed"
	PollBudgetExhausted   Reason = "poll-budget-exhausted"
)

// Snapshot is an observation, not a provider status projection. Accepted means
// QGA returned a handle, not that the process is still running. Exited preserves
// signed QAPI status. OutputUnavailable means QGA reported exit with truncated
// output; the exit code is unavailable. Uncertain never means stopped/cancelled.
// Returned Result pointers and bytes are independent copies.
type Snapshot struct {
	State           State
	Deadline        time.Time
	Result          qga.Status
	CleanupRequired bool
	Reason          Reason
	Polls           int
}

// Handle is bound to a single driver and command. It is neither serializable nor
// a recovery token. Its observations remain readable after the command finishes.
type Handle struct {
	owner *Driver
	job   *job
}

type job struct {
	snapshot    Snapshot
	qgaHandle   qga.Handle
	timer       *time.Timer
	polling     bool
	nextPoll    time.Time
	abortReason Reason
}

// Driver owns its supplied QGA client exclusively. It admits one active command
// at a time so abandoning that command cannot disrupt another driver-owned job.
// A transport failure, timeout or cancellation request quarantines the driver
// permanently. The trusted owner must reconcile the associated immutable guest
// lease before constructing a replacement. Close may run concurrently with calls.
// This object contains no provider ID, IAM, target discovery, disk or VM control.
type Driver struct {
	client *qga.Client
	mu     sync.Mutex
	active *job
	closed bool
}

func New(client *qga.Client) (*Driver, error) {
	if client == nil {
		return nil, errors.New("an authorized QGA client is required")
	}
	return &Driver{client: client}, nil
}

// Start sends one command with no automatic retry. The deadline begins before
// guest-exec, and remains active without polling. A nonzero handle allows the
// caller to observe even rejected or uncertain attempts. Native providers must
// durably record intent before this call and persist the resulting observation;
// this in-memory adapter does not supply crash recovery or exactly-once dispatch.
func (d *Driver) Start(ctx context.Context, command qga.Command, timeout time.Duration) (Handle, Snapshot, error) {
	if timeout <= 0 || timeout > MaxExecutionTimeout {
		return Handle{}, Snapshot{}, ErrInvalidTimeout
	}
	if err := ctx.Err(); err != nil {
		return Handle{}, Snapshot{}, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return Handle{}, Snapshot{}, ErrClosed
	}
	if d.active != nil {
		d.mu.Unlock()
		return Handle{}, Snapshot{}, ErrBusy
	}
	j := &job{snapshot: Snapshot{State: Starting, Deadline: time.Now().Add(timeout)}}
	h := Handle{owner: d, job: j}
	d.active = j
	j.timer = time.AfterFunc(timeout, func() { d.quarantine(j, DeadlineExpired) })
	d.mu.Unlock()

	callCtx, cancel := context.WithDeadline(ctx, j.snapshot.Deadline)
	qh, err := d.client.StartCommand(callCtx, command)
	cancel()
	d.mu.Lock()
	if err == nil {
		j.qgaHandle = qh
		if j.abortReason != NoReason || !time.Now().Before(j.snapshot.Deadline) {
			reason := j.abortReason
			if reason == NoReason {
				reason = DeadlineExpired
			}
			d.markUncertainLocked(j, reason)
			out := copySnapshot(j.snapshot)
			d.mu.Unlock()
			_ = d.client.Close()
			return h, out, ErrCleanupRequired
		}
		j.snapshot.State = Accepted
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		return h, out, nil
	}
	var uncertain *qga.StartUncertainError
	if errors.As(err, &uncertain) {
		reason := j.abortReason
		if reason == NoReason {
			if !time.Now().Before(j.snapshot.Deadline) {
				reason = DeadlineExpired
			} else {
				reason = ControlLost
			}
		}
		d.markUncertainLocked(j, reason)
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		_ = d.client.Close()
		return h, out, cleanupError(err)
	}
	// A non-uncertain Start error means no command was accepted. Only explicit
	// remote rejection and local invalid-input admission are known reusable.
	j.snapshot.State = Rejected
	j.timer.Stop()
	d.active = nil
	var remote *qga.RemoteError
	reusable := !d.closed && (errors.As(err, &remote) || errors.Is(err, qga.ErrInvalidCommand))
	if !reusable {
		d.closed = true
		j.snapshot.Reason = j.abortReason
		if j.snapshot.Reason == NoReason {
			j.snapshot.Reason = ControlLost
		}
	}
	out := copySnapshot(j.snapshot)
	d.mu.Unlock()
	if !reusable {
		_ = d.client.Close()
	}
	return h, out, err
}

// Poll makes at most one status RPC. Concurrent polls fail with ErrBusy rather
// than queueing. Terminal observations are returned without a new RPC, so an
// exited QGA handle is never reused. Any ambiguous status failure quarantines
// the lease even if it might already have exited or been reaped in the guest.
func (d *Driver) Poll(ctx context.Context, h Handle) (Snapshot, error) {
	if h.owner != d || h.job == nil {
		return Snapshot{}, ErrInvalidHandle
	}
	if err := ctx.Err(); err != nil {
		return d.observe(h.job), err
	}
	d.mu.Lock()
	j := h.job
	if j.snapshot.State != Accepted {
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		if out.CleanupRequired {
			return out, ErrCleanupRequired
		}
		return out, nil
	}
	if j.polling {
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		return out, ErrBusy
	}
	if !time.Now().Before(j.snapshot.Deadline) {
		d.quarantineLocked(j, DeadlineExpired)
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		_ = d.client.Close()
		return out, ErrCleanupRequired
	}
	if j.snapshot.Polls == MaxPolls {
		d.quarantineLocked(j, PollBudgetExhausted)
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		_ = d.client.Close()
		return out, cleanupError(ErrPollLimit)
	}
	if time.Now().Before(j.nextPoll) {
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		return out, ErrPollTooSoon
	}
	j.snapshot.Polls++
	j.nextPoll = time.Now().Add(MinPollInterval)
	j.polling = true
	qh, deadline := j.qgaHandle, j.snapshot.Deadline
	d.mu.Unlock()

	callCtx, cancel := context.WithDeadline(ctx, deadline)
	result, err := d.client.CommandStatus(callCtx, qh)
	cancel()
	d.mu.Lock()
	j.polling = false
	if j.snapshot.State == Uncertain {
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		return out, cleanupError(err)
	}
	if !time.Now().Before(deadline) {
		d.quarantineLocked(j, DeadlineExpired)
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		_ = d.client.Close()
		return out, cleanupError(err)
	}
	if err == nil {
		if result.Exited {
			j.snapshot.State = Exited
			j.snapshot.Result = result
			j.timer.Stop()
			d.active = nil
		}
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		return out, nil
	}
	if errors.Is(err, qga.ErrOutputTruncated) {
		// QGA only emits this error after observing exit and retiring its handle.
		j.snapshot.State = OutputUnavailable
		j.timer.Stop()
		d.active = nil
		out := copySnapshot(j.snapshot)
		d.mu.Unlock()
		return out, err
	}
	d.quarantineLocked(j, ControlLost)
	out := copySnapshot(j.snapshot)
	d.mu.Unlock()
	_ = d.client.Close()
	return out, cleanupError(err)
}

// Inspect returns the latest observation without transport IO. CleanupRequired
// is sticky. There is deliberately no API to clear quarantine or assert a fake
// cancellation: lease teardown/reconciliation belongs to the lifecycle owner.
func (d *Driver) Inspect(h Handle) (Snapshot, error) {
	if h.owner != d || h.job == nil {
		return Snapshot{}, ErrInvalidHandle
	}
	return d.observe(h.job), nil
}

// RequestCancellation abandons observation, closes the control channel and
// records a cleanup obligation. It does NOT send a signal or stop any process.
// A completed command is left unchanged. ErrCleanupRequired is intentional:
// callers must not project this operation as successful process cancellation.
func (d *Driver) RequestCancellation(h Handle) (Snapshot, error) {
	if h.owner != d || h.job == nil {
		return Snapshot{}, ErrInvalidHandle
	}
	d.quarantine(h.job, CancellationRequested)
	out := d.observe(h.job)
	if out.CleanupRequired {
		return out, ErrCleanupRequired
	}
	return out, nil
}

// Close closes only the supplied control channel. Any outstanding command
// becomes uncertain and needs real lifecycle cleanup; completed records survive.
func (d *Driver) Close() error {
	d.mu.Lock()
	d.closed = true
	if d.active != nil {
		d.quarantineLocked(d.active, DriverClosed)
	}
	d.mu.Unlock()
	return d.client.Close()
}

func (d *Driver) observe(j *job) Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return copySnapshot(j.snapshot)
}

func (d *Driver) quarantine(j *job, reason Reason) {
	d.mu.Lock()
	changed := d.quarantineLocked(j, reason)
	d.mu.Unlock()
	if changed {
		_ = d.client.Close()
	}
}

// The active-job identity fence makes delayed timer callbacks harmless after
// completion or when a later command has reused a guest PID.
func (d *Driver) quarantineLocked(j *job, reason Reason) bool {
	if d.active != j || j.snapshot.State == Uncertain {
		return false
	}
	if j.snapshot.State == Starting {
		// Closing an in-flight Start does not establish that anything was sent.
		// Its final QGA classification decides whether guest cleanup is needed.
		if j.abortReason != NoReason {
			return false
		}
		j.abortReason = reason
		j.timer.Stop()
		d.closed = true
		return true
	}
	d.markUncertainLocked(j, reason)
	return true
}

func (d *Driver) markUncertainLocked(j *job, reason Reason) {
	j.snapshot.State = Uncertain
	j.snapshot.Reason = reason
	j.snapshot.CleanupRequired = true
	j.timer.Stop()
	d.closed = true
}

func copySnapshot(s Snapshot) Snapshot {
	if s.Result.ExitCode != nil {
		v := *s.Result.ExitCode
		s.Result.ExitCode = &v
	}
	if s.Result.Signal != nil {
		v := *s.Result.Signal
		s.Result.Signal = &v
	}
	s.Result.Stdout = append([]byte(nil), s.Result.Stdout...)
	s.Result.Stderr = append([]byte(nil), s.Result.Stderr...)
	return s
}

func cleanupError(cause error) error {
	if cause == nil {
		return ErrCleanupRequired
	}
	return fmt.Errorf("%w: %w", ErrCleanupRequired, cause)
}
