package guesttcp

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// ProbeHTTPObservation identifies the immutable TCP tuple that produced this
// response. HTTPObservation is a received status/header timestamp, not a health
// promise, score, body or authorization token. Route is captured from the actual
// probe, not supplied by Observe or learned from response bytes. Path is the
// constructor-fixed / or /healthz, so accepting owners can bind the HTTP target
// as well as the service port without parsing guest-selected response data.
type ProbeHTTPObservation struct {
	HTTPObservation
	Route guestnet.TCPRoute
	Path  string
}

// ProbeHTTPObservationCommitter must be the SAME immutable physical owner as
// the PersistentTransport's FrameCommitter. After that probe has fully drained,
// it must atomically verify the current owner/incarnation/policy and exact tuple
// before accepting the observation. The shared NIC remains live; whole-NIC
// cleanup is NOT claimed. The tuple and status alone cannot prove ownership.
//
// Commit must honor the captured context and never publish after cancellation,
// deadline, owner revocation or uncertain cleanup. It may not retain/retry the
// observation. A blocked call retains observation capacity until it returns.
// This package supplies no production implementation, scorer or provider store.
type ProbeHTTPObservationCommitter interface {
	CommitProbeHTTPObservation(context.Context, ProbeHTTPObservation) error
}

// ProbeHTTPObserver performs one fixed-service HTTP request through a fresh
// child of an existing persistent NIC. Construction does not allocate a probe or
// consume the NIC. The trusted caller fixes an approved port and / or /healthz;
// Observe accepts no URL, command, credentials, headers, body or score metadata.
// After observation admission it is single-use, including on failure. It closes
// only its child, never disguises a whole-NIC Close, and never retries.
type ProbeHTTPObserver struct {
	transport *PersistentTransport
	owner     ProbeHTTPObservationCommitter
	port      uint16
	path      string
	mu        sync.Mutex
	started   bool
	done      chan struct{}
	onRoute   func(guestnet.TCPRoute)
}

func NewProbeHTTPObserver(t *PersistentTransport, port uint16, path string) (*ProbeHTTPObserver, error) {
	if t == nil || t.nic == nil || (path != "/" && path != "/healthz") {
		return nil, ErrConfig
	}
	owner, ok := t.nic.base.owner.(ProbeHTTPObservationCommitter)
	if !ok || nilInterface(owner) {
		return nil, ErrConfig
	}
	allowed := false
	for _, p := range t.nic.config.ServicePorts {
		if p == port {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, ErrConfig
	}
	if err := t.nic.base.admissionError(); err != nil {
		return nil, err
	}
	return &ProbeHTTPObserver{transport: t, owner: owner, port: port, path: path, done: make(chan struct{})}, nil
}

// Observe shares the eight global observation slots with HTTPObserver. The
// original caller/NIC deadline and eight seconds bound its return. A pending
// child close or owner publication retains that slot until it actually settles,
// even after this caller returns. Failure returns the zero observation.
func (o *ProbeHTTPObserver) Observe(ctx context.Context) (ProbeHTTPObservation, error) {
	if o == nil || o.transport == nil || nilInterface(ctx) {
		return ProbeHTTPObservation{}, ErrConfig
	}
	if err := observationContextError(ctx); err != nil {
		return ProbeHTTPObservation{}, err
	}
	n := o.transport.nic
	if err := n.base.admissionError(); err != nil {
		return ProbeHTTPObservation{}, err
	}
	o.mu.Lock()
	if o.started {
		o.mu.Unlock()
		return ProbeHTTPObservation{}, ErrCapacity
	}
	select {
	case observationSlots <- struct{}{}:
	default:
		o.mu.Unlock()
		return ProbeHTTPObservation{}, ErrCapacity
	}
	o.started = true
	o.mu.Unlock()
	started := time.Now()
	deadline := started.Add(HTTPObservationTimeout)
	for _, parent := range []context.Context{ctx, n.base.ctx} {
		if earlier, ok := parent.Deadline(); ok && earlier.Before(deadline) {
			deadline = earlier
		}
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	call := &probeObservationContext{Context: bounded, nic: n, cancel: cancel}
	stop := watchProbeObservationLifetimes(cancel, n.base.ctx, n.lifetime, n.base.serviceLifetime)
	type result struct {
		observation ProbeHTTPObservation
		err         error
	}
	done := make(chan result, 1)
	go func() {
		observation, err := o.observe(call, started)
		stop()
		<-observationSlots
		close(o.done)
		done <- result{observation, err}
	}()
	select {
	case r := <-done:
		if err := observationContextError(call); err != nil {
			return ProbeHTTPObservation{}, err
		}
		return r.observation, r.err
	case <-call.Done():
		return ProbeHTTPObservation{}, call.Err()
	}
}

// WaitContext observes actual observation-worker settlement, not just Observe's
// bounded reply. A host transport that retains admission until its backend call
// settles must join this work before settling that call, including after timeout.
// A wait never retries or republishes an observation. The caller/five-second
// deadline bounds this wait; pending work and its capacity remain owned.
// A nil result means settlement only, even when Observe failed. Preserve that
// original operation error; never convert a completed wait into probe success.
// This does not join or certify the independently owned whole NIC's cleanup.
func (o *ProbeHTTPObserver) WaitContext(ctx context.Context) error {
	if o == nil || nilInterface(ctx) {
		return ErrConfig
	}
	o.mu.Lock()
	started := o.started
	o.mu.Unlock()
	if !started {
		return ErrConfig
	}
	select {
	case <-o.done:
		return nil
	default:
	}
	deadline := time.Now().Add(transportWaitTimeout)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	call, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-o.done:
		return nil
	case <-call.Done():
		return errors.Join(ErrDrainPending, call.Err())
	}
}

type probeObservationContext struct {
	context.Context
	nic    *persistentNIC
	cancel context.CancelFunc
}

func (c *probeObservationContext) Err() error {
	if c.nic.base.admissionError() != nil {
		c.cancel()
	}
	// Preserve context.Context's Canceled/DeadlineExceeded error contract.
	return c.Context.Err()
}

func watchProbeObservationLifetimes(cancel context.CancelFunc, contexts ...context.Context) func() {
	type hook struct {
		stop func() bool
		done chan struct{}
	}
	hooks := make([]hook, 0, len(contexts))
	for _, ctx := range contexts {
		if ctx == nil {
			continue
		}
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { cancel(); close(done) })
		hooks = append(hooks, hook{stop, done})
	}
	return func() {
		for _, h := range hooks {
			if !h.stop() {
				<-h.done
			}
		}
	}
}

func (o *ProbeHTTPObserver) observe(ctx context.Context, started time.Time) (observation ProbeHTTPObservation, err error) {
	p, err := o.transport.OpenProbe(ctx, o.port)
	if err != nil {
		return ProbeHTTPObservation{}, err
	}
	if o.onRoute != nil {
		o.onRoute(p.session.Route())
	}
	drained := false
	defer func() {
		if !drained {
			err = errors.Join(err, p.CloseContext(ctx))
			// The caller's wait is bounded by Observe. This worker retains its slot
			// while the persistent owner continues joining unfinished child work.
			<-p.done
		}
		if err != nil {
			observation = ProbeHTTPObservation{}
		}
	}()
	conn, err := p.Dial(ctx)
	if err != nil {
		return ProbeHTTPObservation{}, err
	}
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return ProbeHTTPObservation{}, err
	}
	address := net.JoinHostPort(p.engine.config.PeerIP.String(), strconv.Itoa(int(p.engine.config.Port)))
	request := "GET " + o.path + " HTTP/1.1\r\nHost: " + address + "\r\nConnection: close\r\n\r\n"
	count, err := io.WriteString(conn, request)
	if err != nil {
		return ProbeHTTPObservation{}, err
	}
	if count != len(request) {
		return ProbeHTTPObservation{}, io.ErrShortWrite
	}
	received, err := readHTTPObservationHeader(conn, started)
	if err != nil {
		return ProbeHTTPObservation{}, err
	}
	if err := errors.Join(observationContextError(ctx), p.admission(), observationContextError(p.engine.ctx)); err != nil {
		return ProbeHTTPObservation{}, err
	}
	observation = ProbeHTTPObservation{HTTPObservation: received, Route: p.session.Route(), Path: o.path}
	if err := p.CloseContext(ctx); err != nil {
		return ProbeHTTPObservation{}, err
	}
	drained = true
	// Normal child cleanup cancels its own session/Engine. Check the separate
	// original observation and shared-owner lifetime, not those intentional closes.
	if err := observationContextError(ctx); err != nil {
		return ProbeHTTPObservation{}, err
	}
	if err := o.owner.CommitProbeHTTPObservation(ctx, observation); err != nil {
		return ProbeHTTPObservation{}, err
	}
	if err := observationContextError(ctx); err != nil {
		return ProbeHTTPObservation{}, err
	}
	return observation, nil
}
