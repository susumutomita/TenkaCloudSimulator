package guesttcp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

const (
	persistentQueueSize = 16
	// The attachment budget is a rate, not a lifetime total: at most this many
	// frames/bytes in each fixed persistentBudgetWindow. A day-long attachment
	// keeps its probes; a flood inside one window still ends the attachment.
	maxPersistentFrames    = 65536
	maxPersistentBytes     = 64 << 20
	persistentBudgetWindow = time.Minute
)

// PersistentTransport owns one explicitly supplied framed stream, one retained
// passive service, gateway ARP and bounded short probe exchanges. It opens no
// socket or VM. A trusted FrameCommitter must still supply atomic physical and
// policy fencing; this package supplies no such production owner.
//
// Probe completion does not close the stream or passive service. Whole-transport
// failure/expiry closes both and joins all child work before releasing capacity.
// Existing HTTPObserver is intentionally incompatible with this lifetime.
type PersistentTransport struct{ nic *persistentNIC }

type persistentNIC struct {
	operationsMu   sync.Mutex
	operations     map[HTTPProbeOperationKey]*HTTPProbeOperation
	operationScope *HTTPProbeOperationScope
	base           *Transport
	registry       *guestnet.TCPProbeSessions
	lifetime       context.Context
	config         guestnet.ProbeNIC
	queue          chan *persistentWrite
	queueMu        sync.Mutex
	queueClosed    bool
	mu             sync.Mutex
	sealed         bool
	probes         map[*ProbeExchange]struct{}
	children       sync.WaitGroup
	// now is read under base.budgetMu; tests replace it before any traffic.
	now func() time.Time
}

// clock is the NIC's budget and handle-retention clock.
func (n *persistentNIC) clock() time.Time {
	if n.now == nil {
		return time.Now()
	}
	return n.now()
}

type persistentWrite struct {
	ctx   context.Context
	probe *ProbeExchange
	frame []byte
	done  chan error
}

type capturedPassiveService struct {
	RevocablePassiveService
	lifetime context.Context
}

func (s *capturedPassiveService) Lifetime() context.Context { return s.lifetime }

// NewPersistentTransportUntil captures a fixed boot deadline of at most
// MaxPassiveLifetime. It owns stream/service ONLY on success. The service must
// advertise a finite immutable admission lifetime. The entire attachment ends
// at the earliest parent, service or requested deadline; traffic never renews it.
//
// The passive route and optional gateway ARP must identify the same guest/MAC as
// nic. The service cannot overlap ANY future probe tuple. Aggregate traffic is
// capped at 65536 frames/64 MiB per one-minute window; endpoint and registry caps remain.
// No capability or public runtime is enabled by this opt-in constructor.
func NewPersistentTransportUntil(ctx context.Context, nic guestnet.ProbeNIC, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig, service RevocablePassiveService, route guestnet.TCPRoute, deadline time.Time) (*PersistentTransport, error) {
	expires, err := passiveDeadline(ctx, deadline)
	if err != nil {
		return nil, err
	}
	if nilInterface(service) || nilInterface(stream) || nilInterface(owner) || !route.AnyPeerPort || route.PeerIP != nic.PeerIP || route.PeerMAC != nic.PeerMAC || route.LocalMAC != nic.LocalMAC {
		return nil, ErrConfig
	}
	lifetime := service.Lifetime()
	if nilInterface(lifetime) {
		return nil, ErrConfig
	}
	serviceExpiry, finite := lifetime.Deadline()
	if !finite || serviceExpiry.IsZero() {
		return nil, ErrConfig
	}
	if err := serviceLifetimeError(lifetime, serviceExpiry); err != nil {
		return nil, err
	}
	if serviceExpiry.Before(expires) {
		expires = serviceExpiry
	}
	if route.LocalIP == nic.LocalIP && route.LocalPort >= guestnet.ProbeSourcePort && int(route.LocalPort) < int(guestnet.ProbeSourcePort)+guestnet.MaxProbeSessions {
		return nil, guestnet.ErrTCPRouteConfig
	}
	routes, err := guestnet.NewTCPRoutes([]guestnet.TCPRoute{route})
	if err != nil {
		return nil, err
	}
	registry, err := guestnet.NewTCPProbeSessions(ctx, nic, expires)
	if err != nil {
		return nil, err
	}
	nic.ServicePorts = append([]uint16(nil), nic.ServicePorts...)
	n := &persistentNIC{registry: registry, lifetime: registry.Lifetime(), config: nic, queue: make(chan *persistentWrite, persistentQueueSize), probes: make(map[*ProbeExchange]struct{}), now: time.Now}
	c := Config{LocalIP: nic.LocalIP, PeerIP: nic.PeerIP, LocalMAC: nic.LocalMAC, PeerMAC: nic.PeerMAC}
	_, err = newTransportMode(ctx, c, stream, owner, arpConfig, &capturedPassiveService{service, lifetime}, routes, expires, true, n)
	if err != nil {
		registry.Revoke()
		_ = registry.Wait(context.Background()) // No sessions or caller resources were admitted.
		return nil, err
	}
	return &PersistentTransport{nic: n}, nil
}

func (t *PersistentTransport) Lifetime() context.Context {
	if t == nil || t.nic == nil {
		return nil
	}
	return t.nic.base.ctx
}
func (t *PersistentTransport) Close() error { return t.CloseContext(context.Background()) }
func (t *PersistentTransport) CloseContext(ctx context.Context) error {
	if t == nil || t.nic == nil {
		return ErrConfig
	}
	return t.nic.base.CloseContext(ctx)
}
func (t *PersistentTransport) WaitContext(ctx context.Context) error {
	if t == nil || t.nic == nil {
		return ErrConfig
	}
	return t.nic.base.WaitContext(ctx)
}

// OpenProbe creates one fixed-service Engine with a fresh, never-reused tuple.
// ctx covers the probe lifetime. Its deadline and ten seconds bound the probe;
// closing it normally retires only that exchange. The selected service port must
// have appeared in the constructor's trusted allowlist, not guest/request data.
// Construction and unresolved teardown count against eight live reservations,
// in addition to the shared Engine/Listener stack and worker limits.
func (t *PersistentTransport) OpenProbe(ctx context.Context, port uint16) (*ProbeExchange, error) {
	if t == nil || t.nic == nil || nilInterface(ctx) {
		return nil, ErrConfig
	}
	n := t.nic
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sealed {
		return nil, ErrClosed
	}
	if err := n.base.admissionError(); err != nil {
		return nil, err
	}
	if len(n.probes) >= guestnet.MaxActiveProbeSessions {
		return nil, ErrCapacity
	}
	session, err := n.registry.Open(ctx, port)
	if err != nil {
		return nil, err
	}
	c := Config{LocalIP: n.config.LocalIP, PeerIP: n.config.PeerIP, LocalMAC: n.config.LocalMAC, PeerMAC: n.config.PeerMAC, Port: port, SourcePort: session.Route().LocalPort}
	engine, err := New(c)
	if err != nil {
		session.Revoke()
		_ = session.Wait(context.Background())
		return nil, err
	}
	pumpCtx, cancel := context.WithCancel(session.Lifetime())
	p := &ProbeExchange{nic: n, session: session, engine: engine, ctx: pumpCtx, cancel: cancel, done: make(chan struct{})}
	if err := p.admission(); err != nil {
		cancel()
		session.Revoke()
		engine.Close()
		for errors.Is(session.Wait(context.Background()), guestnet.ErrProbeDrainPending) {
		}
		return nil, err
	}
	n.probes[p] = struct{}{}
	n.children.Add(1) // Admission and shutdown's Wait share n.mu.
	p.pumps.Add(2)
	go p.pump(p.send)
	go p.pump(p.receive)
	go p.supervise()
	return p, nil
}

// ProbeExchange owns one Engine and its two logical pumps, not the persistent
// stream/service. Close stops new output, joins already-selected writes, then
// revokes the tuple and closes the Engine. An expired/aborted entered write has
// an uncertain outcome and instead terminates shared transport admission.
// Dial is single-use. There is no observation publication or scorer authority.
type ProbeExchange struct {
	nic     *persistentNIC
	session *guestnet.TCPProbeSession
	engine  *Engine
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closing bool
	drainBy time.Time
	pumps   sync.WaitGroup
	calls   sync.WaitGroup
	done    chan struct{}
	err     error
}

func (p *ProbeExchange) Lifetime() context.Context {
	if p == nil {
		return nil
	}
	return p.session.Lifetime()
}
func (p *ProbeExchange) Dial(ctx context.Context) (net.Conn, error) {
	if p == nil || nilInterface(ctx) {
		return nil, ErrConfig
	}
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		return nil, ErrClosed
	}
	p.calls.Add(1)
	p.mu.Unlock()
	defer p.calls.Done()
	conn, err := p.engine.Dial(ctx)
	if err == nil {
		err = p.admission()
	}
	if err != nil {
		// A shorter Dial context can kill the Engine while its session still
		// has time left. That failure must revoke selected output immediately.
		p.abort(err, ctx.Err() != nil)
		return nil, err
	}
	return &probeConnection{Conn: conn, probe: p}, nil
}

type probeConnection struct {
	net.Conn
	probe *ProbeExchange
}

func (c *probeConnection) Close() error { return c.probe.Close() }
func (p *ProbeExchange) Close() error   { return p.CloseContext(context.Background()) }
func (p *ProbeExchange) CloseContext(ctx context.Context) error {
	if p == nil || nilInterface(ctx) {
		return ErrConfig
	}
	p.beginClose()
	p.mu.Lock()
	deadline := p.drainBy
	p.mu.Unlock()
	call, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := p.wait(call); err != nil {
		return err
	}
	return p.err
}
func (p *ProbeExchange) WaitContext(ctx context.Context) error {
	if p == nil || nilInterface(ctx) {
		return ErrConfig
	}
	if err := p.wait(ctx); err != nil {
		return err
	}
	return p.err
}
func (p *ProbeExchange) wait(ctx context.Context) error {
	select {
	case <-p.done:
		return nil
	default:
	}
	deadline := time.Now().Add(transportWaitTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	call, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-p.done:
		return nil
	case <-call.Done():
		return errors.Join(ErrDrainPending, call.Err())
	}
}
func (p *ProbeExchange) beginClose() {
	p.mu.Lock()
	if !p.closing {
		p.closing = true
		p.drainBy = time.Now().Add(transportWaitTimeout)
		p.cancel()
	}
	p.mu.Unlock()
}

// abort is distinct from normal Close: failed Engine/Dial work cannot leave a
// selected frame live merely to preserve siblings. force applies when the
// caller's Dial context expires during a previously requested graceful close.
func (p *ProbeExchange) abort(err error, force bool) {
	p.mu.Lock()
	revoke := force || !p.closing
	if revoke {
		p.err = errors.Join(p.err, err)
		if !p.closing {
			p.closing = true
			p.drainBy = time.Now().Add(transportWaitTimeout)
		}
		p.cancel()
	}
	p.mu.Unlock()
	if revoke {
		p.session.Revoke()
	}
}

func (p *ProbeExchange) admission() error {
	return errors.Join(p.nic.base.admissionError(), observationContextError(p.ctx))
}
func (p *ProbeExchange) pump(run func() error) {
	defer p.pumps.Done()
	err := run()
	if err != nil {
		p.abort(err, false)
	} else {
		p.beginClose()
	}
}
func (p *ProbeExchange) supervise() {
	select {
	case <-p.ctx.Done():
	case <-p.nic.base.ctx.Done():
	case <-p.engine.ctx.Done():
	}
	p.abort(errors.Join(p.ctx.Err(), p.engine.ctx.Err(), p.nic.base.ctx.Err()), false)
	p.beginClose()
	// Do not revoke the session while its queued/selected output is unsettled.
	// Cancellation of the external session itself is intentionally not graceful.
	p.pumps.Wait()
	p.session.Revoke()
	p.engine.Close()
	p.calls.Wait()
	for errors.Is(p.session.Wait(context.Background()), guestnet.ErrProbeDrainPending) {
	}
	p.nic.mu.Lock()
	delete(p.nic.probes, p)
	close(p.done)
	p.nic.children.Done()
	p.nic.mu.Unlock()
}
func (p *ProbeExchange) send() error {
	for {
		frame, err := p.engine.ReadFrame(p.ctx)
		if err != nil {
			return err
		}
		if err = p.nic.base.account(len(frame)); err != nil {
			p.nic.base.beginClose()
			return err
		}
		if err = p.nic.submit(p.session.Lifetime(), p, frame); err != nil {
			return err
		}
	}
}
func (p *ProbeExchange) receive() error {
	for {
		frame, err := p.session.ReadFrame(p.ctx)
		if err != nil {
			return err
		}
		if err = p.nic.base.commitLocalInjection(p.ctx, frame, func() error { return p.engine.Inject(frame) }); err != nil {
			if p.ctx.Err() == nil {
				p.nic.base.beginClose()
			}
			return err
		}
	}
}

// Engine failure can precede the Dial continuation or probe supervisor. Fence
// it synchronously inside owner admission, not only via asynchronous abort.
// Normal Close leaves the Engine live until selected writes have settled.
func (p *ProbeExchange) writeFrame(ctx context.Context, owned []byte) error {
	return p.nic.base.commitContext(ctx, ToGuest, owned, func() error {
		if err := observationContextError(p.engine.ctx); err != nil {
			return err
		}
		err := p.nic.base.encoder.WriteFrame(owned)
		return errors.Join(err, observationContextError(p.engine.ctx))
	})
}

// unhandled reports guest frames no owned route can consume: non-IPv4/ARP
// EtherTypes (IPv6, LLDP, ...), ARP other than the trusted gateway request, IPv4 that
// is not a well-formed TCP segment, and TCP not addressed to this NIC's local
// endpoint, passive service route or probe source-port range. Dropping them
// grants nothing. Frames that do target an owned tuple keep strict handling,
// so a spoofed or malformed exchange on an owned tuple still revokes.
func (n *persistentNIC) unhandled(frame []byte) bool {
	if len(frame) < 14 {
		return false
	}
	switch binary.BigEndian.Uint16(frame[12:14]) {
	case 0x0806:
		// Requests for other addresses, replies and gratuitous ARP are not ours
		// to answer; Reply is a pure validator of the one trusted request shape.
		_, err := n.base.arp.Reply(frame)
		return err != nil
	case 0x0800:
	default:
		return true
	}
	p, err := guestnet.ParseTCPFrame(frame)
	if err != nil {
		return true
	}
	if _, err = n.base.routes.Classify(guestnet.TCPFromPeer, frame); err == nil {
		return false
	}
	if p.DestinationIP != n.config.LocalIP || p.DestinationMAC != n.config.LocalMAC {
		return true
	}
	port := int(p.DestinationPort) - int(guestnet.ProbeSourcePort)
	return port < 0 || port >= guestnet.MaxProbeSessions
}

func (n *persistentNIC) receive(frame []byte) error {
	if _, err := n.base.routes.Classify(guestnet.TCPFromPeer, frame); err == nil {
		return n.base.commit(FromGuest, frame, func() error { return n.base.service.Inject(frame) })
	}
	// A queue handoff is not delivery authority. The receiving probe commits
	// immediately around Engine.Inject, with its own lifetime inside the callback.
	_, err := n.registry.RouteIncoming(frame)
	return err
}
func (n *persistentNIC) submit(ctx context.Context, p *ProbeExchange, frame []byte) error {
	req := &persistentWrite{ctx: ctx, probe: p, frame: append([]byte(nil), frame...), done: make(chan error, 1)}
	if err := observationContextError(ctx); err != nil {
		return err
	}
	// Seal enqueue atomically with the writer's final queue drain. Otherwise a
	// producer admitted just before cancellation could enqueue after it exited.
	n.queueMu.Lock()
	if n.queueClosed {
		n.queueMu.Unlock()
		return ErrClosed
	}
	if err := n.base.admissionError(); err != nil {
		n.queueMu.Unlock()
		return err
	}
	// Every source has at most one outstanding request. No unbounded producers.
	select {
	case n.queue <- req:
		n.queueMu.Unlock()
	default:
		n.queueMu.Unlock()
		n.base.beginClose()
		return ErrCapacity
	}
	// Even canceled work waits for real settlement; no abandoned queue ownership.
	return <-req.done
}
func (n *persistentNIC) write() error {
	defer func() {
		n.queueMu.Lock()
		defer n.queueMu.Unlock()
		n.queueClosed = true
		for {
			select {
			case req := <-n.queue:
				req.done <- ErrClosed
			default:
				return
			}
		}
	}()
	for {
		select {
		case <-n.base.ctx.Done():
			return n.base.ctx.Err()
		case req := <-n.queue:
			if err := n.base.admissionError(); err != nil {
				req.done <- err
				return err
			}
			if req.probe != nil {
				p := req.probe
				p.mu.Lock()
				closing := p.closing
				p.mu.Unlock()
				if closing {
					req.done <- ErrClosed
					continue
				}
				if err := observationContextError(req.ctx); err != nil {
					req.done <- err
					continue
				}
				err := p.session.Transmit(req.ctx, req.frame, p.writeFrame)
				req.done <- err
				if err != nil && n.base.admissionError() != nil {
					return err
				}
				// A session-only rejection before callback admission has no
				// uncertain write. The registry keeps shared admission live;
				// do not convert that queued expiry into a whole-NIC failure.
			} else {
				err := n.base.commitContext(req.ctx, ToGuest, req.frame, func() error { return n.base.encoder.WriteFrame(req.frame) })
				req.done <- err
				if err != nil {
					return err
				}
			}
		}
	}
}
func (n *persistentNIC) closeAndJoin() {
	n.mu.Lock()
	n.sealed = true
	for p := range n.probes {
		p.beginClose()
	}
	n.mu.Unlock()
	n.registry.Revoke()
	n.children.Wait()
	for errors.Is(n.registry.Wait(context.Background()), guestnet.ErrProbeDrainPending) {
	}
}
