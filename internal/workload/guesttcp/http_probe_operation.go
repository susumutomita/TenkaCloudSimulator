package guesttcp

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// HTTPProbeOperationKey is a process-local correlation key, never authentication
// or a durable ownership token. The all-zero value is invalid.
type HTTPProbeOperationKey [16]byte

var ErrHTTPProbeOperationBinding = errors.New("HTTP probe operation key already binds a different target")

// HTTPProbeOperationRetention is how long a settled scoped handle stays
// discoverable after its original caller deadline. Only then may a full NIC
// forget it. A replay of the same command carries that same past deadline, so
// scoped Start rejects it before retention and cannot dispatch it twice.
const HTTPProbeOperationRetention = time.Minute

// HTTPProbeOperationSnapshot separates a bounded observation result from actual
// worker settlement. ResultError is immutable once ResultReady is true. Settled
// implies ResultReady, not success or whole-NIC cleanup.
type HTTPProbeOperationSnapshot struct {
	Key         HTTPProbeOperationKey
	Port        uint16
	Path        string
	StartedAt   time.Time
	Deadline    time.Time
	Route       guestnet.TCPRoute
	RouteReady  bool
	ResultReady bool
	Observation ProbeHTTPObservation
	ResultError error
	Settled     bool
}

// HTTPProbeOperation owns exactly one concrete observer invocation. It cannot be
// canceled, renewed or retried through discovery or waiting. Its original start
// context still cancels execution. NIC-local tombstones retain failed operations
// as well as successes. Raw handles stay until the NIC is discarded; a settled
// scoped handle may be forgotten HTTPProbeOperationRetention after its caller
// deadline, and only when the NIC's handle map is full.
type HTTPProbeOperation struct {
	mu      sync.Mutex
	state   HTTPProbeOperationSnapshot
	ready   chan struct{}
	settled chan struct{}
	scope   *sealedHTTPProbeScope
	service string
	// forgetAfter is zero (never) for raw handles.
	forgetAfter time.Time
}

// StartHTTPObservation records an operation before dispatching its single
// observer. A duplicate key and target returns the existing handle, even if ctx
// has changed or expired. A different target is rejected. Start errors mean no
// new operation was recorded; execution/admission errors belong to the handle.
//
// Only trusted callers may select the port/path. The concrete observer enforces
// the NIC allowlist and same-owner publication contract. Keys provide no authority.
// Each NIC retains at most MaxProbeSessions handles. When full, it forgets only
// settled scoped handles whose retention has passed; raw handles are never evicted.
func (t *PersistentTransport) StartHTTPObservation(ctx context.Context, key HTTPProbeOperationKey, port uint16, path string) (*HTTPProbeOperation, error) {
	return t.startHTTPObservation(ctx, key, port, path, nil, "")
}

func (t *PersistentTransport) startHTTPObservation(ctx context.Context, key HTTPProbeOperationKey, port uint16, path string, scope *sealedHTTPProbeScope, service string) (*HTTPProbeOperation, error) {
	started := time.Now()
	if t == nil || t.nic == nil || key == (HTTPProbeOperationKey{}) {
		return nil, ErrConfig
	}
	n := t.nic
	n.operationsMu.Lock()
	if h := n.operations[key]; h != nil {
		if h.state.Port != port || h.state.Path != path || h.scope != scope || h.service != service {
			n.operationsMu.Unlock()
			return nil, ErrHTTPProbeOperationBinding
		}
		n.operationsMu.Unlock()
		return h, nil
	}
	// Reject unbounded caller strings before retaining them. Targets admitted to
	// retention have fixed-size keys/ports and one of two constant paths.
	if nilInterface(ctx) || (path != "/" && path != "/healthz") {
		n.operationsMu.Unlock()
		return nil, ErrConfig
	}
	if !n.roomLocked() {
		n.operationsMu.Unlock()
		return nil, ErrCapacity
	}
	n.operationsMu.Unlock()
	callerDeadline, finite := ctx.Deadline()
	if scope != nil {
		if !finite || callerDeadline.IsZero() {
			return nil, ErrConfig
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !time.Now().Before(callerDeadline) {
			return nil, context.DeadlineExceeded
		}
	}
	deadline := started.Add(HTTPObservationTimeout)
	if finite && callerDeadline.Before(deadline) {
		deadline = callerDeadline
	}
	if d, ok := n.base.ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	h := &HTTPProbeOperation{
		state: HTTPProbeOperationSnapshot{Key: key, Port: port, Path: path, StartedAt: started, Deadline: deadline},
		ready: make(chan struct{}), settled: make(chan struct{}), scope: scope, service: service,
	}
	if scope != nil {
		h.forgetAfter = callerDeadline.Add(HTTPProbeOperationRetention)
	}
	// Deadline methods belong to the supplied contexts, so run them outside
	// the map lock. A racing start may have won while we captured the deadline.
	// Recheck identity and quota before recording or constructing any observer.
	n.operationsMu.Lock()
	if existing := n.operations[key]; existing != nil {
		n.operationsMu.Unlock()
		if existing.state.Port != port || existing.state.Path != path || existing.scope != scope || existing.service != service {
			return nil, ErrHTTPProbeOperationBinding
		}
		return existing, nil
	}
	if !n.roomLocked() {
		n.operationsMu.Unlock()
		return nil, ErrCapacity
	}
	if n.operations == nil {
		n.operations = make(map[HTTPProbeOperationKey]*HTTPProbeOperation)
	}
	n.operations[key] = h
	n.operationsMu.Unlock()
	o, err := NewProbeHTTPObserver(t, port, path)
	if err != nil {
		h.publishResult(ProbeHTTPObservation{}, err)
		h.publishSettlement()
		return h, nil
	}
	o.onRoute = h.recordRoute
	// Install the original absolute deadline before scheduling. Observe may impose
	// a shorter deadline, but cannot renew this one after a scheduling delay.
	call, cancel := context.WithDeadline(ctx, deadline)
	go h.run(o, call, cancel)
	return h, nil
}

// roomLocked reports whether another handle fits, first forgetting settled
// scoped handles whose retention has passed if the map is full.
func (n *persistentNIC) roomLocked() bool {
	if len(n.operations) < guestnet.MaxProbeSessions {
		return true
	}
	now := n.clock()
	for key, h := range n.operations {
		if h.forgettable(now) {
			delete(n.operations, key)
		}
	}
	return len(n.operations) < guestnet.MaxProbeSessions
}

func (h *HTTPProbeOperation) forgettable(now time.Time) bool {
	if h.scope == nil || h.forgetAfter.IsZero() || now.Before(h.forgetAfter) {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state.Settled
}

// LookupHTTPObservation never starts work or changes its lifetime. An unknown
// key (including after process recreation) is not evidence of settlement.
func (t *PersistentTransport) LookupHTTPObservation(key HTTPProbeOperationKey) (*HTTPProbeOperation, bool) {
	if t == nil || t.nic == nil {
		return nil, false
	}
	t.nic.operationsMu.Lock()
	defer t.nic.operationsMu.Unlock()
	h, ok := t.nic.operations[key]
	if ok && h.scope != nil {
		return nil, false
	}
	return h, ok
}

func (h *HTTPProbeOperation) run(o *ProbeHTTPObserver, ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	observation, err := o.Observe(ctx)
	h.publishResult(observation, err)
	// Observe can reject before admission, in which case done will never close.
	// This unique observer is private to this invocation, so no later caller can
	// change started. Otherwise join the real worker without a cleanup timeout.
	o.mu.Lock()
	started := o.started
	o.mu.Unlock()
	if started {
		<-o.done
	}
	h.publishSettlement()
}

// recordRoute runs only from this operation's observer, after real allocation
// and before Dial. It is independent of the eventual success/error result.
func (h *HTTPProbeOperation) recordRoute(route guestnet.TCPRoute) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.state.RouteReady {
		h.state.Route, h.state.RouteReady = route, true
	}
}

func (h *HTTPProbeOperation) publishResult(v ProbeHTTPObservation, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.Observation, h.state.ResultError, h.state.ResultReady = v, err, true
	close(h.ready)
}
func (h *HTTPProbeOperation) publishSettlement() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state.Settled = true
	close(h.settled)
}

func (h *HTTPProbeOperation) Snapshot() (HTTPProbeOperationSnapshot, error) {
	if h == nil {
		return HTTPProbeOperationSnapshot{}, ErrConfig
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, nil
}

// WaitResult returns the original result in the snapshot, separately from a
// wait error. Canceling this wait cannot cancel execution or imply settlement.
func (h *HTTPProbeOperation) WaitResult(ctx context.Context) (HTTPProbeOperationSnapshot, error) {
	if h == nil || nilInterface(ctx) {
		return HTTPProbeOperationSnapshot{}, ErrConfig
	}
	if err := waitHTTPProbeSignal(ctx, h.ready); err != nil {
		return HTTPProbeOperationSnapshot{}, err
	}
	return h.Snapshot()
}

// WaitSettlement joins actual work, not just the bounded result. It returns nil
// only after settlement and does not replace ResultError. A bounded wait that
// expires returns ErrDrainPending and leaves the operation and its capacity live.
// A backend adapter must keep its underlying promise unsettled in that case.
func (h *HTTPProbeOperation) WaitSettlement(ctx context.Context) error {
	if h == nil || nilInterface(ctx) {
		return ErrConfig
	}
	if err := waitHTTPProbeSignal(ctx, h.settled); err != nil {
		return errors.Join(ErrDrainPending, err)
	}
	return nil
}

func waitHTTPProbeSignal(ctx context.Context, signal <-chan struct{}) error {
	select {
	case <-signal:
		return nil
	default:
	}
	if err := observationContextError(ctx); err != nil {
		return err
	}
	// Install a local timer even when a custom parent delays Done/Err delivery.
	if deadline, ok := ctx.Deadline(); ok {
		bounded, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		ctx = bounded
	}
	select {
	case <-signal:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
