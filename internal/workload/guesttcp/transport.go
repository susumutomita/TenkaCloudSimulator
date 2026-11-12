package guesttcp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// FrameDirection describes the physical delivery boundary, not an AWS action.
type FrameDirection uint8

const (
	FromGuest FrameDirection = iota + 1
	ToGuest
)

var (
	ErrCommit = errors.New("private frame commit contract violated")
	// ErrDrainPending means transport cleanup has not been observed to finish.
	// It never authorizes replay, replacement, resource reuse or physical cleanup.
	ErrDrainPending = errors.New("private transport drain pending")
	// ErrDrainUncertain means all owned calls returned, but an owned Close failed.
	// The transport slot stays occupied permanently; Close is never retried.
	ErrDrainUncertain = errors.New("private transport stream closure unconfirmed")
	transportSlots    = make(chan struct{}, 8)
)

const transportWaitTimeout = 5 * time.Second

// FrameCommitter is supplied by the trusted physical owner, never the guest.
// CommitFrame must synchronously call commit exactly once, or reject without
// calling it. It must serialize current lease/policy revocation with the entire
// commit operation, not merely check metadata and release a lock before calling
// commit. A nil result means commit completed successfully. The frame is a copy
// for inspection, not an authority token; mutating it cannot change delivery.
//
// The owner must not retain commit, call it asynchronously, retry it, or block
// indefinitely. Cancellation must end pending admission. Revocation must close
// the transport before waiting for a fence held by blocked I/O. A partial write
// is uncertain, terminal delivery, never a reason to replay a frame.
// The callback must not synchronously call Transport.Close or Wait (which wait
// for this call); signal cancellation or close the stream independently instead.
//
// This package has NO production implementation of this contract. In particular,
// a database snapshot followed by a write does not implement atomic AWS packet
// authorization. Memory-test committers do not qualify physical revocation.
type FrameCommitter interface {
	CommitFrame(ctx context.Context, direction FrameDirection, frame []byte, commit func() error) error
}

// Transport owns an optional Engine, an optional passive service and one
// explicitly supplied private framed stream. At least one endpoint is required.
// It creates no socket or VM, and is not wired into runtime provisioning. The
// stream must support concurrent Read/Write/Close; Close must promptly interrupt
// blocked reads AND writes without waiting for their locks. Both stream and
// committer are trusted, bounded, cancellation-aware dependencies. An arbitrary
// io.ReadWriteCloser is not sufficient merely because it implements that type.
//
// All decoded frames, including rejected input and ARP, share an additional
// maxFrames/maxBytes budget; Engine retains its independent TCP-only budget.
// Endpoints retain their own bounds. The default lifetime is ten seconds;
// NewPassiveTransportUntil alone admits an explicit fixed boot-service window
// bounded by MaxPassiveLifetime. No frame is replayed on failure.
// Eight additional transport slots bound retained cleanup when a dependency
// violates its contract. A slot remains occupied until stream/service Close, all
// pumps and engine teardown actually return, and every Close succeeds. A failed
// close retains the slot permanently. Timing out a wait cannot release it.
type Transport struct {
	persistent   *persistentNIC
	engine       *Engine
	service      PassiveService
	serviceRoute int
	routes       *guestnet.TCPRoutes
	stream       io.ReadWriteCloser
	owner        FrameCommitter
	arp          *guestnet.ARPResponder
	decoder      *guestnet.Decoder
	encoder      *guestnet.Encoder
	ctx          context.Context
	// Keep the external lifetime distinct from our own intentional shutdown.
	// HTTP observation must still reject parent cancellation after I/O drains.
	lifetime context.Context
	// Stable service admission context, distinct from the transport's own
	// cancellation. It must be checked synchronously at the delivery boundary.
	serviceLifetime context.Context
	serviceExpires  time.Time
	cancel          context.CancelFunc
	done            chan struct{}
	results         chan error
	workers         sync.WaitGroup
	shutdown        sync.Once
	drainBy         time.Time
	budgetMu        sync.Mutex
	packets         int
	bytes           int
	err             error
	closeErr        error
}

// NewTransport explicitly opts into framed I/O. Ownership transfers only on a
// successful return: on every constructor error the caller still owns stream.
// After success, cancellation, any invalid input, any failed commit, or engine
// termination requests stream closure and engine teardown. Close and Wait have
// bounded waits; pending or uncertain cleanup retains its transport slot. Frames
// delivered before closure cannot be recalled.
//
// Optional ARP serves only the fixed peer asking for its configured gateway.
// GatewayIP may differ from LocalIP (the remote TCP source), but GatewayMAC must
// equal LocalMAC. No ARP discovery, unsolicited replies or forwarding is added.
func NewTransport(ctx context.Context, c Config, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig) (*Transport, error) {
	return newTransport(ctx, c, stream, owner, arpConfig, nil, nil, time.Time{}, false)
}

func newTransport(ctx context.Context, c Config, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig, service PassiveService, routes *guestnet.TCPRoutes, deadline time.Time, requireServiceLifetime bool) (*Transport, error) {
	return newTransportMode(ctx, c, stream, owner, arpConfig, service, routes, deadline, requireServiceLifetime, nil)
}

func newTransportMode(ctx context.Context, c Config, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig, service PassiveService, routes *guestnet.TCPRoutes, deadline time.Time, requireServiceLifetime bool, persistent *persistentNIC) (*Transport, error) {
	passiveOnly := !deadline.IsZero()
	if nilInterface(ctx) || nilInterface(stream) || nilInterface(owner) {
		return nil, ErrConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var serviceLifetime context.Context
	var serviceExpires time.Time
	if revocable, ok := service.(RevocablePassiveService); ok {
		if nilInterface(revocable) {
			return nil, ErrConfig
		}
		serviceLifetime = revocable.Lifetime()
		if nilInterface(serviceLifetime) {
			return nil, ErrConfig
		}
		if expires, ok := serviceLifetime.Deadline(); ok {
			if expires.IsZero() {
				return nil, ErrConfig
			}
			serviceExpires = expires
		}
		if err := serviceLifetimeError(serviceLifetime, serviceExpires); err != nil {
			return nil, err
		}
	} else if requireServiceLifetime {
		return nil, ErrConfig
	}
	if requireServiceLifetime && serviceExpires.IsZero() {
		return nil, ErrConfig
	}
	if passiveOnly {
		if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
			deadline = parentDeadline
		}
		if !time.Now().Before(deadline) {
			return nil, context.DeadlineExceeded
		}
	}
	var responder *guestnet.ARPResponder
	if arpConfig != nil {
		ac := *arpConfig
		if ac.GuestIP != c.PeerIP || ac.GuestMAC != c.PeerMAC || ac.GatewayMAC != c.LocalMAC {
			return nil, ErrConfig
		}
		var err error
		responder, err = guestnet.NewARPResponder(ac)
		if err != nil {
			return nil, err
		}
	}
	select {
	case transportSlots <- struct{}{}:
	default:
		return nil, ErrCapacity
	}
	var e *Engine
	if !passiveOnly {
		var err error
		e, err = New(c)
		if err != nil {
			<-transportSlots
			return nil, err
		}
		// Preserve the Engine's original deadline, not a fresh lifetime.
		deadline, _ = e.ctx.Deadline()
	}
	if expires, ok := ctx.Deadline(); ok && expires.Before(deadline) {
		deadline = expires
	}
	if !serviceExpires.IsZero() && serviceExpires.Before(deadline) {
		deadline = serviceExpires
	}
	// Passive-only mode uses the deadline captured before admission/setup;
	// the callback guard also checks this absolute bound.
	call, cancel := context.WithDeadline(ctx, deadline)
	setupErr := call.Err()
	if setupErr == nil {
		setupErr = serviceLifetimeError(serviceLifetime, serviceExpires)
	}
	if setupErr != nil {
		cancel()
		if e != nil {
			e.Close()
		}
		<-transportSlots
		return nil, setupErr
	}
	workerCount, serviceRoute := 1, 0
	if e != nil {
		workerCount++
		serviceRoute = 1
	}
	if service != nil {
		workerCount++
	}
	t := &Transport{persistent: persistent, engine: e, service: service, serviceRoute: serviceRoute, routes: routes, stream: stream, owner: owner, arp: responder,
		decoder: guestnet.NewDecoder(stream), encoder: guestnet.NewEncoder(stream),
		ctx: call, lifetime: ctx, serviceLifetime: serviceLifetime, serviceExpires: serviceExpires, cancel: cancel, done: make(chan struct{}), results: make(chan error, workerCount)}
	if persistent != nil {
		persistent.base = t
		workerCount++
		// One extra result is reserved before any pump can publish failure.
		t.results = make(chan error, workerCount)
	}
	t.workers.Add(workerCount)
	if persistent != nil {
		go t.pump(persistent.write)
	}
	go t.pump(t.receive)
	if e != nil {
		go t.pump(t.send)
	}
	if service != nil {
		go t.pump(t.sendService)
	}
	go t.supervise()
	return t, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	switch v := reflect.ValueOf(value); v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (t *Transport) pump(run func() error) {
	defer t.workers.Done()
	t.results <- run()
}

func (t *Transport) supervise() {
	var engineDone, serviceDone, registryDone <-chan struct{}
	if t.engine != nil {
		engineDone = t.engine.ctx.Done()
	}
	if t.serviceLifetime != nil {
		serviceDone = t.serviceLifetime.Done()
	}
	if t.persistent != nil {
		registryDone = t.persistent.lifetime.Done()
	}
	select {
	case <-registryDone:
		t.err = t.persistent.lifetime.Err()
	case t.err = <-t.results:
	case <-t.ctx.Done():
		t.err = t.ctx.Err()
	case <-engineDone:
		t.err = t.engine.ctx.Err()
	case <-serviceDone:
		t.err = serviceLifetimeError(t.serviceLifetime, t.serviceExpires)
		if t.err == nil {
			t.err = ErrClosed
		}
	}
	t.beginClose()
	// No commit, encoder or budget lock may be held while initiating Close.
	// Closing before joining is essential: a committer may be inside Write.
	// One owned close call may itself violate the trusted stream contract. Do
	// not let that prevent engine revocation, and never release this transport's
	// admission slot while any close call or pump remains outstanding. Close
	// both dependencies independently: one blocked Close must not prevent the
	// other resource or the engine from being revoked.
	closeCount := 1
	if t.service != nil {
		closeCount++
	}
	closed := make(chan error, closeCount)
	go func() { closed <- t.stream.Close() }()
	if t.service != nil {
		go func() { closed <- t.service.Close() }()
	}
	if t.engine != nil {
		t.engine.Close()
	}
	if t.persistent != nil {
		t.persistent.closeAndJoin()
	}
	t.workers.Wait()
	t.closeErr = <-closed
	if closeCount == 2 {
		t.closeErr = errors.Join(t.closeErr, <-closed)
	}
	if t.closeErr == nil {
		<-transportSlots
	} else {
		// A returned error does not establish that the owned dependencies released
		// their resources. Retain cleanup debt rather than admit unlimited leaks.
		t.closeErr = errors.Join(ErrDrainUncertain, t.closeErr)
	}
	close(t.done)
}

// Dial starts the Engine's single fixed-tuple connection. Closing the returned
// connection requests cleanup of the whole transport with the bounded Close
// semantics, not just a TCP endpoint. ErrDrainPending means it has not joined.
// Passive-only transports reject Dial with ErrConfig without closing the service.
func (t *Transport) Dial(ctx context.Context) (net.Conn, error) {
	if t == nil || t.engine == nil {
		return nil, ErrConfig
	}
	conn, err := t.engine.Dial(ctx)
	if err == nil {
		err = t.admissionError()
	}
	if err != nil {
		// The transport retains the engine/connection and all cleanup ownership.
		// Returning to an expired caller is not evidence that cleanup finished.
		if ctx == nil {
			ctx = context.Background()
		}
		return nil, errors.Join(err, t.CloseContext(ctx))
	}
	return &transportConnection{Conn: conn, owner: t}, nil
}

type transportConnection struct {
	net.Conn
	owner *Transport
}

func (c *transportConnection) Close() error { return c.owner.Close() }

// Close is CloseContext with a background context. Its bounded return applies
// also to the net.Conn returned by Dial; ErrDrainPending is not successful drain.
func (t *Transport) Close() error {
	return t.CloseContext(context.Background())
}

// Wait is WaitContext with a background context: at most five seconds of
// observation, without requesting cancellation or abandoning cleanup ownership.
func (t *Transport) Wait() error {
	return t.WaitContext(context.Background())
}

// CloseContext requests revocation even with an already-canceled context, then
// waits until the first shutdown request's five-second cleanup deadline or the
// caller's earlier deadline. Repeated calls never extend that cleanup deadline.
// ErrDrainPending means cleanup has NOT been observed to finish; ownership and
// capacity remain retained. ErrDrainUncertain means the owned calls joined but
// an owned Close failed. A failed close retains the slot permanently and is never
// retried. Call again to observe the same cleanup, never to restart it. A nil
// result means only the owned calls returned and every Close reported success.
// It does not recall bytes already delivered or establish physical revocation.
func (t *Transport) CloseContext(ctx context.Context) error {
	if nilInterface(ctx) {
		return ErrConfig
	}
	t.beginClose()
	call, cancel := context.WithDeadline(ctx, t.drainBy)
	defer cancel()
	if err := t.waitContext(call); err != nil {
		return err
	}
	return t.closeErr
}

// WaitContext observes cleanup without requesting cancellation, for at most five
// seconds or the caller's earlier deadline. A pending result is not the terminal
// transport result; a later call can observe actual completion. It does not reset
// CloseContext's cleanup deadline or extend packet admission.
func (t *Transport) WaitContext(ctx context.Context) error {
	if nilInterface(ctx) {
		return ErrConfig
	}
	if err := t.waitContext(ctx); err != nil {
		return err
	}
	return errors.Join(t.err, t.closeErr)
}

func (t *Transport) beginClose() {
	t.shutdown.Do(func() {
		t.drainBy = time.Now().Add(transportWaitTimeout)
		t.cancel()
	})
}

func (t *Transport) waitContext(ctx context.Context) error {
	// Prefer already-observed completion to caller expiry: this is read-only
	// cleanup observation, not admission to a new effect.
	select {
	case <-t.done:
		return nil
	default:
	}
	deadline := time.Now().Add(transportWaitTimeout)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	// Equal to the earlier parent's original deadline installs our own timer.
	// A delayed parent notification must not extend bounded cleanup observation.
	call, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-t.done:
		return nil
	case <-call.Done():
		return errors.Join(ErrDrainPending, call.Err())
	}
}

func (t *Transport) admissionError() error {
	if t.persistent != nil {
		if err := observationContextError(t.persistent.lifetime); err != nil {
			return err
		}
	}
	if err := serviceLifetimeError(t.serviceLifetime, t.serviceExpires); err != nil {
		return err
	}
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if t.engine != nil {
		if err := t.engine.ctx.Err(); err != nil {
			return err
		}
	}
	if deadline, ok := t.ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func serviceLifetimeError(lifetime context.Context, expires time.Time) error {
	if lifetime == nil {
		return nil
	}
	if !expires.IsZero() && !time.Now().Before(expires) {
		return context.DeadlineExceeded
	}
	if err := lifetime.Err(); err != nil {
		return err
	}
	select {
	case <-lifetime.Done():
		return ErrClosed
	default:
		return nil
	}
}

func (t *Transport) account(n int) error {
	t.budgetMu.Lock()
	defer t.budgetMu.Unlock()
	frames, bytes := maxFrames, maxBytes
	if t.persistent != nil {
		frames, bytes = maxPersistentFrames, maxPersistentBytes
	}
	if n < guestnet.MinFrameBytes || n > guestnet.MaxFrameBytes || t.packets >= frames || t.bytes > bytes-n {
		return ErrCapacity
	}
	t.packets++
	t.bytes += n
	return nil
}

func (t *Transport) receive() error {
	for {
		frame, err := t.decoder.ReadFrame()
		if err != nil {
			return err
		}
		// A persistent VM NIC is a real guest link: stock guests emit IPv6, UDP,
		// gratuitous ARP and outbound TCP that no owned route handles. Those are
		// dropped like an unrouted network would, never accepted and never
		// charged to the lifetime budget, instead of revoking the whole NIC.
		if t.persistent != nil && t.persistent.unhandled(frame) {
			continue
		}
		if err := t.account(len(frame)); err != nil {
			return err
		}
		switch binary.BigEndian.Uint16(frame[12:14]) {
		case 0x0800:
			if t.persistent != nil {
				if err := t.persistent.receive(frame); err != nil {
					return err
				}
				continue
			}
			if t.routes != nil {
				if err := t.receiveRouted(frame); err != nil {
					return err
				}
				continue
			}
			p, err := guestnet.ParseTCPFrame(frame)
			if err != nil {
				return err
			}
			c := t.engine.config
			if p.SourceMAC != c.PeerMAC || p.DestinationMAC != c.LocalMAC || p.SourceIP != c.PeerIP || p.DestinationIP != c.LocalIP || p.SourcePort != c.Port || p.DestinationPort != c.SourcePort {
				return guestnet.ErrPacket
			}
			if err := t.commit(FromGuest, frame, func() error { return t.engine.Inject(frame) }); err != nil {
				return err
			}
		case 0x0806:
			if t.arp == nil {
				return guestnet.ErrPacket
			}
			reply, err := t.arp.Reply(frame)
			if err != nil {
				return err
			}
			if err := t.commit(FromGuest, frame, func() error { return nil }); err != nil {
				return err
			}
			if err := t.deliver(reply); err != nil {
				return err
			}
		default:
			return guestnet.ErrPacket
		}
	}
}

func (t *Transport) send() error {
	for {
		frame, err := t.engine.ReadFrame(t.ctx)
		if err != nil {
			return err
		}
		if err := t.deliverTCP(0, frame); err != nil {
			return err
		}
	}
}

func (t *Transport) deliver(frame []byte) error {
	if err := t.account(len(frame)); err != nil {
		return err
	}
	if t.persistent != nil {
		return t.persistent.submit(t.ctx, nil, frame)
	}
	return t.commit(ToGuest, frame, func() error { return t.encoder.WriteFrame(frame) })
}

func (t *Transport) commit(direction FrameDirection, frame []byte, apply func() error) error {
	return t.commitContext(t.ctx, direction, frame, apply)
}

// The fixed per-probe context must be checked inside the owner's callback too.
func (t *Transport) commitContext(ctx context.Context, direction FrameDirection, frame []byte, apply func() error) error {
	return t.commitContextMode(ctx, direction, frame, apply, false)
}

// Only ProbeExchange.receive uses this local synchronous injection mode.
func (t *Transport) commitLocalInjection(ctx context.Context, frame []byte, apply func() error) error {
	return t.commitContextMode(ctx, FromGuest, frame, apply, true)
}
func (t *Transport) commitContextMode(ctx context.Context, direction FrameDirection, frame []byte, apply func() error, local bool) error {
	var token *localInjectionToken
	if local {
		token = &localInjectionToken{}
		ctx = context.WithValue(ctx, localInjectionKey{}, token)
	}

	admission := func() error {
		return errors.Join(t.admissionError(), observationContextError(ctx))
	}
	if err := admission(); err != nil {
		return err
	}
	// Defensively reject retained/repeated callbacks. The gate joins a callback
	// racing the owner's return. It cannot prove same-goroutine execution or the
	// owner's policy atomicity; those remain trusted prerequisites.
	var mu sync.Mutex
	active, called, invalid := true, false, false
	var applied error
	do := func() error {
		mu.Lock()
		defer mu.Unlock()
		if !active || called {
			invalid = true
			return ErrCommit
		}
		called = true
		if applied = admission(); applied == nil {
			applied = apply()
			if token != nil && direction == FromGuest {
				raw := applied
				sharedErr, childErr := t.admissionError(), observationContextError(ctx)
				if raw == nil {
					token.completed.Store(true)
				}
				if raw == nil && sharedErr == nil {
					token.eligible.Store(true)
					if childErr != nil {
						applied = &localInjectionCompleted{token: token, cause: childErr}
					} else {
						applied = nil
					}
				} else {
					applied = errors.Join(raw, sharedErr, childErr)
				}
				return applied
			}
			// An already-entered write can have effects even if revocation races
			// its return. Do not report successful commit after admission expired.
			applied = errors.Join(applied, admission())
		}
		return applied
	}
	err := t.owner.CommitFrame(ctx, direction, append([]byte(nil), frame...), do)
	mu.Lock()
	defer mu.Unlock()
	active = false
	if err != nil {
		return err
	}
	if !called || invalid {
		return ErrCommit
	}
	return applied
}
