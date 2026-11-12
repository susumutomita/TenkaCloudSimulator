package guestnet

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"time"
)

const (
	MaxProbeSessions               = 1024
	MaxActiveProbeSessions         = 8
	ProbeSourcePort         uint16 = 40000
	MaxProbeSessionLifetime        = 10 * time.Second
	MaxNICSessionLifetime          = 24 * time.Hour
	probeQueueSize                 = 16
	maxNICFrames                   = 65536
	maxNICBytes                    = 64 << 20
)

var (
	ErrProbeSession      = errors.New("invalid private TCP session")
	ErrProbeClosed       = errors.New("private TCP session admission revoked")
	ErrProbeCapacity     = errors.New("private TCP session capacity exhausted")
	ErrProbeBusy         = errors.New("private NIC delivery already pending")
	ErrProbeDrainPending = errors.New("private TCP session work has not drained")
	probeRegistries      = make(chan struct{}, 8)
)

// ProbeNIC is immutable trusted tuple configuration, not physical authority.
// ServicePorts is copied. No destination is learned from a received packet.
type ProbeNIC struct {
	LocalIP, PeerIP   netip.Addr
	LocalMAC, PeerMAC [6]byte
	ServicePorts      []uint16
}

// TCPProbeSessions owns bounded in-memory queues and logical exchange lifetimes
// for ONE physical NIC incarnation. It opens/owns no stream, socket or guest and
// is not a FrameCommitter. A qualified owner must retain this same registry for
// the entire incarnation; creating a replacement registry cannot safely reset
// its source-port tombstones while old packets or work may exist.
//
// Each accepted Open consumes one of 1024 lifetime tuples, never reused, even
// after cancellation. Up to eight sessions (including unfinished cleanup) may
// coexist. Every incoming/outgoing attempt consumes cumulative frame/byte budget,
// including retired traffic. Malformed, unowned or overflowing input revokes the
// registry. Retired exact tuples are discarded, never delivered to a new session.
// There is no OS attachment, ARP/IMDS routing, policy decision or physical drain
// evidence here. A future persistent NIC owner must compose these independently.
type TCPProbeSessions struct {
	mu                                sync.Mutex
	nic                               ProbeNIC
	allowed                           map[uint16]bool
	ctx                               context.Context
	cancel                            context.CancelFunc
	expires                           time.Time
	records                           [MaxProbeSessions]*TCPProbeSession
	allocated, live, frames, bytes    int
	sending, closed, watched, drained bool
	done                              chan struct{}
}

// TCPProbeSession is one immutable, single-use logical probe tuple. Incoming
// frames are copied into its bounded queue. It does not implement TCP sequence
// state: a real TCP engine must still reject invalid handshakes and segments.
type TCPProbeSession struct {
	registry                 *TCPProbeSessions
	route                    TCPRoute
	classifier               *TCPRoutes
	ctx                      context.Context
	cancel                   context.CancelFunc
	expires                  time.Time
	incoming                 chan []byte
	done                     chan struct{}
	closed, watched, drained bool // guarded by registry.mu
	reading                  bool
	inflight                 int
}

func sessionContextError(ctx context.Context) error {
	if ctx == nil {
		return ErrProbeSession
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d, ok := ctx.Deadline(); ok && !time.Now().Before(d) {
		return context.DeadlineExceeded
	}
	return nil
}

// NewTCPProbeSessions captures a fixed requested lifetime, at most 24 hours,
// clamped to the earlier parent deadline. Ownership is memory-only. Invalid or
// exhausted admission neither consumes a slot nor changes caller resources.
func NewTCPProbeSessions(ctx context.Context, nic ProbeNIC, deadline time.Time) (*TCPProbeSessions, error) {
	if err := sessionContextError(ctx); err != nil {
		return nil, err
	}
	now := time.Now()
	if deadline.IsZero() || deadline.After(now.Add(MaxNICSessionLifetime)) || len(nic.ServicePorts) == 0 || len(nic.ServicePorts) > MaxTCPRoutes {
		return nil, ErrProbeSession
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if !now.Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	allowed := make(map[uint16]bool, len(nic.ServicePorts))
	routes := make([]TCPRoute, 0, len(nic.ServicePorts))
	for _, port := range nic.ServicePorts {
		if allowed[port] {
			return nil, ErrProbeSession
		}
		allowed[port] = true
		routes = append(routes, TCPRoute{LocalIP: nic.LocalIP, PeerIP: nic.PeerIP, LocalMAC: nic.LocalMAC, PeerMAC: nic.PeerMAC, LocalPort: ProbeSourcePort, PeerPort: port})
	}
	if _, err := NewTCPRoutes(routes); err != nil {
		return nil, err
	}
	// Active probes do not use link-local source addresses.
	if !nic.LocalIP.IsGlobalUnicast() {
		return nil, ErrProbeSession
	}
	select {
	case probeRegistries <- struct{}{}:
	default:
		return nil, ErrProbeCapacity
	}
	lifetime, cancel := context.WithDeadline(ctx, deadline)
	if err := sessionContextError(lifetime); err != nil {
		cancel()
		<-probeRegistries
		return nil, err
	}
	nic.ServicePorts = append([]uint16(nil), nic.ServicePorts...)
	r := &TCPProbeSessions{nic: nic, allowed: allowed, ctx: lifetime, cancel: cancel, expires: deadline, done: make(chan struct{})}
	go func() {
		<-lifetime.Done()
		r.mu.Lock()
		r.closeLocked()
		r.watched = true
		r.finishLocked()
		r.mu.Unlock()
	}()
	return r, nil
}

func (r *TCPProbeSessions) admissionLocked() error {
	if r.closed {
		return ErrProbeClosed
	}
	if err := sessionContextError(r.ctx); err != nil {
		r.closeLocked()
		return errors.Join(ErrProbeClosed, err)
	}
	return nil
}

// Open allocates a fresh exact tuple for one approved service port. The caller
// context covers the exchange lifetime, not just construction. Its original
// deadline, the registry deadline and ten seconds all bound the session.
func (r *TCPProbeSessions) Open(ctx context.Context, port uint16) (*TCPProbeSession, error) {
	if r == nil {
		return nil, ErrProbeSession
	}
	if err := sessionContextError(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.admissionLocked(); err != nil {
		return nil, err
	}
	if !r.allowed[port] {
		return nil, ErrProbeSession
	}
	if r.live >= MaxActiveProbeSessions || r.allocated >= MaxProbeSessions {
		return nil, ErrProbeCapacity
	}
	deadline := time.Now().Add(MaxProbeSessionLifetime)
	if r.expires.Before(deadline) {
		deadline = r.expires
	}
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	lifetime, cancel := context.WithDeadline(ctx, deadline)
	if err := sessionContextError(lifetime); err != nil {
		cancel()
		return nil, err
	}
	route := TCPRoute{LocalIP: r.nic.LocalIP, PeerIP: r.nic.PeerIP, LocalMAC: r.nic.LocalMAC, PeerMAC: r.nic.PeerMAC, LocalPort: ProbeSourcePort + uint16(r.allocated), PeerPort: port}
	classifier, err := NewTCPRoutes([]TCPRoute{route})
	if err != nil {
		cancel()
		return nil, err
	}
	s := &TCPProbeSession{registry: r, route: route, classifier: classifier, ctx: lifetime, cancel: cancel, expires: deadline, incoming: make(chan []byte, probeQueueSize), done: make(chan struct{})}
	r.records[r.allocated] = s
	r.allocated++
	r.live++
	go func() {
		<-lifetime.Done()
		r.mu.Lock()
		s.closeLocked()
		s.watched = true
		s.finishLocked()
		r.mu.Unlock()
	}()
	return s, nil
}

// Route returns immutable metadata only, never a delivery or ownership permit.
func (s *TCPProbeSession) Route() TCPRoute {
	if s == nil {
		return TCPRoute{}
	}
	return s.route
}
func (s *TCPProbeSession) Lifetime() context.Context {
	if s == nil {
		return nil
	}
	return s.ctx
}

// Lifetime is logical admission only. A future physical owner must observe its
// cancellation and revoke the actual NIC independently, including on failures.
func (r *TCPProbeSessions) Lifetime() context.Context {
	if r == nil {
		return nil
	}
	return r.ctx
}

func (s *TCPProbeSession) admissionLocked() error {
	if err := s.registry.admissionLocked(); err != nil {
		return err
	}
	if s.closed {
		return ErrProbeClosed
	}
	if err := sessionContextError(s.ctx); err != nil {
		s.closeLocked()
		return errors.Join(ErrProbeClosed, err)
	}
	return nil
}
func (r *TCPProbeSessions) accountLocked(n int) error {
	if n < MinFrameBytes || n > MaxFrameBytes {
		r.closeLocked()
		return ErrFrameSize
	}
	if r.frames >= maxNICFrames || r.bytes > maxNICBytes-n {
		r.closeLocked()
		return ErrProbeCapacity
	}
	r.frames++
	r.bytes += n
	return nil
}

// RouteIncoming copies one parsed guest reply into its sole active session.
// false,nil means a valid exact retired tuple was discarded. Unknown tuples,
// malformed packets and queue overflow terminate admission for the registry.
// The physical owner must authorize delivery separately; matching bytes are not
// a permit. Callers must not mutate frame concurrently with this method.
func (r *TCPProbeSessions) RouteIncoming(frame []byte) (bool, error) {
	if r == nil {
		return false, ErrProbeSession
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.admissionLocked(); err != nil {
		return false, err
	}
	if err := r.accountLocked(len(frame)); err != nil {
		return false, err
	}
	packet, err := ParseTCPFrame(frame)
	if err != nil {
		r.closeLocked()
		return false, err
	}
	index := int(packet.DestinationPort) - int(ProbeSourcePort)
	if index < 0 || index >= r.allocated {
		r.closeLocked()
		return false, ErrTCPRoute
	}
	s := r.records[index]
	if _, err = s.classifier.Classify(TCPFromPeer, frame); err != nil {
		r.closeLocked()
		return false, err
	}
	if err = s.admissionLocked(); err != nil {
		if registryErr := r.admissionLocked(); registryErr != nil {
			return false, registryErr
		}
		return false, nil
	}
	owned := append([]byte(nil), frame...)
	select {
	case s.incoming <- owned:
		return true, nil
	default:
		r.closeLocked()
		return false, ErrProbeCapacity
	}
}

// ReadFrame rejects buffered data once revocation is observed. Only one read may
// be pending per session. Closing wakes it without closing a physical NIC or
// another session's queue. A frame already handed off is not recalled; its
// consumer must still fence actual delivery. Wait joins this method's work.
func (s *TCPProbeSession) ReadFrame(ctx context.Context) ([]byte, error) {
	if s == nil {
		return nil, ErrProbeSession
	}
	if err := sessionContextError(ctx); err != nil {
		return nil, err
	}
	r := s.registry
	r.mu.Lock()
	err := s.admissionLocked()
	if err != nil {
		r.mu.Unlock()
		return nil, err
	}
	if s.reading {
		r.mu.Unlock()
		return nil, ErrProbeBusy
	}
	s.reading = true
	s.inflight++
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		s.reading = false
		s.inflight--
		s.finishLocked()
		r.mu.Unlock()
	}()
	deadline := s.expires
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	call, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-call.Done():
		return nil, call.Err()
	case <-s.ctx.Done():
		return nil, errors.Join(ErrProbeClosed, s.ctx.Err())
	case frame := <-s.incoming:
		r.mu.Lock()
		err = s.admissionLocked()
		r.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if err = sessionContextError(call); err != nil {
			return nil, err
		}
		return frame, nil
	}
}

// Transmit validates the session's exact outbound tuple and synchronously calls
// deliver once. Only one delivery may be pending per registry: contention fails,
// never queues. The callback receives owned bytes and a bounded context. It must
// finish all work before returning, and independently fence physical ownership
// and policy at the actual write. This primitive supplies NO physical permit.
//
// A blocked callback can block Transmit, but not Revoke or a bounded Wait. It
// retains session and registry admission until it returns. An admitted callback
// may already have effects when revoked; late success is rejected and never
// authorizes retry. Do not call this session's Wait from inside deliver.
func (s *TCPProbeSession) Transmit(ctx context.Context, frame []byte, deliver func(context.Context, []byte) error) (err error) {
	if s == nil || deliver == nil {
		return ErrProbeSession
	}
	if err := sessionContextError(ctx); err != nil {
		return err
	}
	r := s.registry
	r.mu.Lock()
	if err := s.admissionLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	if err := r.accountLocked(len(frame)); err != nil {
		r.mu.Unlock()
		return err
	}
	if _, err := s.classifier.Classify(TCPToPeer, frame); err != nil {
		r.closeLocked()
		r.mu.Unlock()
		return err
	}
	if r.sending {
		r.mu.Unlock()
		return ErrProbeBusy
	}
	owned := append([]byte(nil), frame...)
	r.sending = true
	s.inflight++
	r.mu.Unlock()
	deadline := s.expires
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	call, cancel := context.WithDeadline(ctx, deadline)
	stopped := make(chan struct{})
	stop := context.AfterFunc(s.ctx, func() { cancel(); close(stopped) })
	expired := make(chan struct{})
	stopExpiry := context.AfterFunc(call, func() { r.Revoke(); close(expired) })
	called, returned := false, false
	defer func() {
		// Stop the operation-expiry hook before intentional local cancellation.
		// If expiry won, join its admission revocation before publishing drain.
		if !stopExpiry() {
			<-expired
		}
		cancel()
		if !stop() {
			<-stopped
		}
		r.mu.Lock()
		if !returned {
			// A panicking callback may have partially written. Preserve the panic
			// while revoking admission and joining its now-unwound local work.
			r.closeLocked()
		}
		if late := sessionContextError(ctx); late != nil {
			err = errors.Join(err, late)
		}
		if late := s.admissionLocked(); late != nil {
			err = errors.Join(err, late)
		}
		if err != nil {
			if called {
				r.closeLocked()
			} else {
				s.closeLocked()
			}
		}
		r.sending = false
		s.inflight--
		s.finishLocked()
		r.mu.Unlock()
	}()
	err = sessionContextError(call)
	if err == nil {
		r.mu.Lock()
		err = s.admissionLocked()
		r.mu.Unlock()
		if err == nil {
			called = true
			err = deliver(call, owned)
		}
	}
	if late := sessionContextError(call); late != nil {
		err = errors.Join(err, late)
	}
	returned = true
	return err
}

func (s *TCPProbeSession) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	s.cancel()
	for {
		select {
		case <-s.incoming:
		default:
			s.finishLocked()
			return
		}
	}
}
func (s *TCPProbeSession) finishLocked() {
	if !s.closed || !s.watched || s.inflight != 0 || s.drained {
		return
	}
	s.drained = true
	close(s.done)
	s.registry.live--
	s.registry.finishLocked()
}
func (r *TCPProbeSessions) closeLocked() {
	if r.closed {
		return
	}
	r.closed = true
	r.cancel()
	for _, s := range r.records[:r.allocated] {
		s.closeLocked()
	}
	r.finishLocked()
}
func (r *TCPProbeSessions) finishLocked() {
	if !r.closed || !r.watched || r.live != 0 || r.drained {
		return
	}
	r.drained = true
	<-probeRegistries
	close(r.done)
}

// Revoke closes logical admission immediately; it neither closes nor proves
// cleanup of a physical stream. Wait observes only the work owned here.
func (r *TCPProbeSessions) Revoke() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closeLocked()
	r.mu.Unlock()
}
func (s *TCPProbeSession) Revoke() {
	if s == nil {
		return
	}
	r := s.registry
	r.mu.Lock()
	s.closeLocked()
	r.mu.Unlock()
}
func waitProbeDrain(ctx context.Context, done <-chan struct{}) error {
	if ctx == nil {
		return ErrProbeSession
	}
	select {
	case <-done:
		return nil
	default:
	}
	if err := sessionContextError(ctx); err != nil {
		return errors.Join(ErrProbeDrainPending, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	call, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	select {
	case <-done:
		return nil
	case <-call.Done():
		return errors.Join(ErrProbeDrainPending, call.Err())
	}
}

// Wait observes logical drain only, bounded by the earlier caller/five-second
// wait deadline. It does not extend admission, retry work or prove NIC cleanup.
func (r *TCPProbeSessions) Wait(ctx context.Context) error {
	if r == nil {
		return ErrProbeSession
	}
	return waitProbeDrain(ctx, r.done)
}
func (s *TCPProbeSession) Wait(ctx context.Context) error {
	if s == nil {
		return ErrProbeSession
	}
	return waitProbeDrain(ctx, s.done)
}
