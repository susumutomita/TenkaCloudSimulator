package guesttcp

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// IssuedProbeResource is an ALREADY exclusively acquired provider capability,
// never a resource name, database snapshot, Boolean authorization or raw stream.
// Transact must hold current physical lease/policy authority throughout apply,
// invoking it synchronously once or rejecting without invoking it. It must not
// retain/retry apply. Provider revocation must enter the session's Revoke before
// acknowledging revocation. No production issuer is supplied by this package.
// A caller implementing this interface does not thereby qualify physical ownership.
type IssuedProbeResource interface {
	Stream() io.ReadWriteCloser
	Transact(context.Context, OwnedProbeAcceptance, func() error) error
}

// OwnedProbeAcceptance is copied inspection data, not an authority credential.
// Exactly one of Frame or Observation is populated by the session.
type OwnedProbeAcceptance struct {
	NIC         guestnet.ProbeNIC
	Direction   FrameDirection
	Frame       []byte
	Observation *ProbeHTTPObservation
	Descriptor  *OwnedProbeDescriptor
}

// OwnedProbeConfig is immutable probe TCP policy. The base constructor rejects
// ARP; only a separately captured persistent descriptor adds fixed passive/ARP policy.
type OwnedProbeConfig struct {
	NIC           guestnet.ProbeNIC
	Paths         []string
	EvidenceLimit int
}

var ownedProbeSlots = make(chan struct{}, 8)
var ErrOwnedProbeSession = errors.New("owned probe session unavailable or authority uncertain")

// OwnedProbeSession fences this transferred stream, NOT all aliases of a VM.
// It implements both committers; Stream returns its same stable close facade.
// That facade is for the trusted transport only, not a bypass-resistant capability:
// its Read/Write track ownership but must be called through the frame authority gate.
// Failed/blocked Close retains a global slot; neither deadline nor Wait releases
// unresolved ownership. No rebind, renewal or resume operation exists.
type OwnedProbeSession struct {
	config     OwnedProbeConfig
	stream     io.ReadWriteCloser
	transact   func(context.Context, OwnedProbeAcceptance, func() error) error
	ctx        context.Context
	cancel     context.CancelFunc
	gate       chan struct{}
	terminal   atomic.Bool
	revokeOnce sync.Once
	closed     chan struct{}
	closeErr   error
	ioMu       sync.Mutex
	ioWG       sync.WaitGroup
	evidence   []ProbeHTTPObservation
	facade     *ownedProbeStream
	descriptor *OwnedProbeDescriptor
	passive    *guestnet.TCPRoutes
	arp        *guestnet.ARPResponder
	attached   bool
}
type ownedProbeStream struct{ owner *OwnedProbeSession }

// Validation failures leave ownership with the caller and invoke no stream I/O.
// On success the raw stream is exclusively transferred: do not use/close it
// elsewhere. A later transport-construction failure requires Revoke and Wait;
// it never makes this resource reusable. parent must have a fixed deadline.
func NewOwnedProbeSession(parent context.Context, resource IssuedProbeResource, config OwnedProbeConfig) (*OwnedProbeSession, error) {
	if nilInterface(parent) || nilInterface(resource) || observationContextError(parent) != nil {
		return nil, ErrOwnedProbeSession
	}
	if d, ok := parent.Deadline(); !ok || !d.After(time.Now()) {
		return nil, ErrOwnedProbeSession
	}
	c := config
	c.NIC.ServicePorts = append([]uint16(nil), config.NIC.ServicePorts...)
	c.Paths = append([]string(nil), config.Paths...)
	if c.EvidenceLimit < 1 || c.EvidenceLimit > 1024 || len(c.Paths) < 1 || len(c.Paths) > 2 || len(c.NIC.ServicePorts) < 1 || len(c.NIC.ServicePorts) > 8 {
		return nil, ErrOwnedProbeSession
	}
	seen := map[uint16]bool{}
	for _, p := range c.NIC.ServicePorts {
		if p == 0 || seen[p] {
			return nil, ErrOwnedProbeSession
		}
		seen[p] = true
		_, err := guestnet.NewTCPRoutes([]guestnet.TCPRoute{{LocalIP: c.NIC.LocalIP, PeerIP: c.NIC.PeerIP, LocalMAC: c.NIC.LocalMAC, PeerMAC: c.NIC.PeerMAC, LocalPort: guestnet.ProbeSourcePort, PeerPort: p}})
		if err != nil {
			return nil, ErrOwnedProbeSession
		}
	}
	for i, p := range c.Paths {
		if (p != "/" && p != "/healthz") || (i > 0 && p == c.Paths[0]) {
			return nil, ErrOwnedProbeSession
		}
	}
	stream := resource.Stream()
	if nilInterface(stream) {
		return nil, ErrOwnedProbeSession
	}
	select {
	case ownedProbeSlots <- struct{}{}:
	default:
		return nil, ErrCapacity
	}
	deadline, _ := parent.Deadline()
	ctx, cancel := context.WithDeadline(parent, deadline)
	s := &OwnedProbeSession{config: c, stream: stream, transact: resource.Transact, ctx: ctx, cancel: cancel, gate: make(chan struct{}, 1), closed: make(chan struct{})}
	s.gate <- struct{}{}
	s.facade = &ownedProbeStream{s}
	go func() { <-ctx.Done(); s.Revoke() }()
	return s, nil
}
func (s *OwnedProbeSession) Stream() io.ReadWriteCloser {
	if s == nil {
		return nil
	}
	return s.facade
}
func (s *OwnedProbeSession) current(ctx context.Context) error {
	if s == nil || s.terminal.Load() {
		return ErrOwnedProbeSession
	}
	return errors.Join(observationContextError(ctx), observationContextError(s.ctx))
}
func (s *OwnedProbeSession) enter(ctx context.Context) error {
	if nilInterface(ctx) || s.current(ctx) != nil {
		return ErrOwnedProbeSession
	}
	wait, cancel := ownedWaitContext(ctx)
	defer cancel()
	select {
	case <-wait.Done():
		return wait.Err()
	case <-s.ctx.Done():
		return ErrOwnedProbeSession
	case <-s.gate:
	}
	if err := s.current(ctx); err != nil {
		s.gate <- struct{}{}
		return err
	}
	return nil
}

// Revoke seals admission before asynchronous Close, never waits for an I/O-held
// fence first. Close happens once. Wait's cancellation does not cancel cleanup.
func (s *OwnedProbeSession) Revoke() {
	if s == nil {
		return
	}
	s.revokeOnce.Do(func() {
		s.ioMu.Lock()
		s.terminal.Store(true)
		s.cancel()
		s.ioMu.Unlock()
		go func() {
			err := s.stream.Close()
			s.ioWG.Wait()
			<-s.gate
			s.closeErr = err
			s.gate <- struct{}{}
			if err == nil {
				<-ownedProbeSlots
			}
			close(s.closed)
		}()
	})
}
func (s *OwnedProbeSession) Wait(ctx context.Context) error {
	if s == nil || nilInterface(ctx) {
		return ErrOwnedProbeSession
	}
	wait, cancel := ownedWaitContext(ctx)
	defer cancel()
	select {
	case <-wait.Done():
		return wait.Err()
	case <-s.closed:
		if s.closeErr != nil {
			return errors.Join(ErrDrainUncertain, s.closeErr)
		}
		return nil
	}
}
func (f *ownedProbeStream) call(run func() (int, error)) (int, error) {
	s := f.owner
	s.ioMu.Lock()
	if s.current(s.ctx) != nil {
		s.ioMu.Unlock()
		return 0, ErrOwnedProbeSession
	}
	s.ioWG.Add(1)
	s.ioMu.Unlock()
	defer s.ioWG.Done()
	return run()
}
func (f *ownedProbeStream) Read(p []byte) (int, error) {
	return f.call(func() (int, error) { return f.owner.stream.Read(p) })
}
func (f *ownedProbeStream) Write(p []byte) (int, error) {
	return f.call(func() (int, error) { return f.owner.stream.Write(p) })
}
func (f *ownedProbeStream) Close() error { f.owner.Revoke(); return f.owner.Wait(context.Background()) }
func (s *OwnedProbeSession) nic() guestnet.ProbeNIC {
	n := s.config.NIC
	n.ServicePorts = append([]uint16(nil), n.ServicePorts...)
	return n
}
func (s *OwnedProbeSession) route(r guestnet.TCPRoute) bool {
	n := s.config.NIC
	if r.AnyPeerPort || r.LocalIP != n.LocalIP || r.PeerIP != n.PeerIP || r.LocalMAC != n.LocalMAC || r.PeerMAC != n.PeerMAC || r.LocalPort < guestnet.ProbeSourcePort || r.LocalPort >= guestnet.ProbeSourcePort+guestnet.MaxProbeSessions {
		return false
	}
	for _, p := range n.ServicePorts {
		if p == r.PeerPort {
			return true
		}
	}
	return false
}
func (s *OwnedProbeSession) frame(d FrameDirection, b []byte) bool {
	if s.arp != nil && len(b) >= 14 && b[12] == 8 && b[13] == 6 {
		if d == FromGuest {
			_, err := s.arp.Reply(b)
			return err == nil
		}
		return d == ToGuest && s.arp.MatchesReply(b)
	}
	if s.passive != nil {
		direction := guestnet.TCPFromPeer
		if d == ToGuest {
			direction = guestnet.TCPToPeer
		}
		if d == FromGuest || d == ToGuest {
			if _, err := s.passive.Classify(direction, b); err == nil {
				return true
			}
		}
	}
	p, err := guestnet.ParseTCPFrame(b)
	if err != nil {
		return false
	}
	r := guestnet.TCPRoute{}
	switch d {
	case ToGuest:
		r = guestnet.TCPRoute{LocalIP: p.SourceIP, PeerIP: p.DestinationIP, LocalMAC: p.SourceMAC, PeerMAC: p.DestinationMAC, LocalPort: p.SourcePort, PeerPort: p.DestinationPort}
	case FromGuest:
		r = guestnet.TCPRoute{LocalIP: p.DestinationIP, PeerIP: p.SourceIP, LocalMAC: p.DestinationMAC, PeerMAC: p.SourceMAC, LocalPort: p.DestinationPort, PeerPort: p.SourcePort}
	default:
		return false
	}
	return s.route(r)
}

// transaction protects against observable duplicate/late callbacks. It cannot
// prove a provider's same-goroutine execution or external lease atomicity.
func (s *OwnedProbeSession) transaction(ctx context.Context, a OwnedProbeAcceptance, apply func() error) error {
	token, _ := ctx.Value(localInjectionKey{}).(*localInjectionToken)
	claimed := token != nil && !token.completed.Load() && token.used.CompareAndSwap(false, true)
	if token != nil && !claimed {
		s.Revoke()
		return ErrOwnedProbeSession
	}

	if s.descriptor != nil {
		d := copyOwnedDescriptor(*s.descriptor)
		a.Descriptor = &d
	}
	var mu sync.Mutex
	active, called, invalid := true, false, false
	var applied error
	var callbackError *ownedCallbackError
	producerOutcome := false
	returned := false
	defer func() {
		if !returned {
			mu.Lock()
			active = false
			mu.Unlock()
			s.Revoke()
		}
	}()
	callback := func() error {
		mu.Lock()
		defer mu.Unlock()
		if !active || called {
			invalid = true
			s.Revoke()
			return ErrOwnedProbeSession
		}
		called = true
		if applied = s.current(ctx); applied == nil {
			applied = apply()
			marker, marked := applied.(*localInjectionCompleted)
			producerOutcome = applied == nil || marked && marker.token == token
			applied = errors.Join(applied, s.current(ctx))
		}
		if applied != nil {
			callbackError = &ownedCallbackError{cause: applied}
			return callbackError
		}
		return applied
	}
	err := s.transact(ctx, a, callback)
	mu.Lock()
	active = false
	bad := invalid || (err == nil && !called)
	result := errors.Join(err, applied, s.current(ctx))
	mu.Unlock()
	returned = true
	if bad {
		s.Revoke()
		return ErrOwnedProbeSession
	}
	propagated, exact := err.(*ownedCallbackError)
	exactPropagation := callbackError == nil && err == nil || callbackError != nil && exact && propagated == callbackError
	safeLocal := called && claimed && token.completed.Load() && token.eligible.Load() && producerOutcome && a.Direction == FromGuest && a.Observation == nil && exactPropagation && observationContextError(ctx) != nil && s.current(s.ctx) == nil

	if called && result != nil && !safeLocal {
		s.Revoke()
	}
	return result
}
func (s *OwnedProbeSession) CommitFrame(ctx context.Context, d FrameDirection, b []byte, apply func() error) error {
	if apply == nil || s == nil || !s.frame(d, b) {
		return ErrOwnedProbeSession
	}
	if err := s.enter(ctx); err != nil {
		return err
	}
	defer func() { s.gate <- struct{}{} }()
	return s.transaction(ctx, OwnedProbeAcceptance{NIC: s.nic(), Direction: d, Frame: append([]byte(nil), b...)}, apply)
}
func (s *OwnedProbeSession) CommitProbeHTTPObservation(ctx context.Context, v ProbeHTTPObservation) error {
	if s == nil || !s.route(v.Route) || v.StatusCode < 100 || v.StatusCode > 599 || v.StartedAt.IsZero() || v.ReceivedAt.Before(v.StartedAt) || v.ReceivedAt.After(time.Now()) {
		return ErrOwnedProbeSession
	}
	allowed := false
	for _, p := range s.config.Paths {
		allowed = allowed || p == v.Path
	}
	if !allowed {
		return ErrOwnedProbeSession
	}
	if err := s.enter(ctx); err != nil {
		return err
	}
	defer func() { s.gate <- struct{}{} }()
	// Evidence is a rolling record of the newest EvidenceLimit observations, so a
	// long-lived session keeps probing instead of refusing once the record fills.
	before := s.evidence
	copyV := v
	completed := false
	defer func() {
		if !completed {
			s.evidence = before
			s.Revoke()
		}
	}()
	err := s.transaction(ctx, OwnedProbeAcceptance{NIC: s.nic(), Observation: &copyV}, func() error {
		s.evidence = appendEvidence(before, v, s.config.EvidenceLimit)
		return nil
	})
	if err != nil {
		s.evidence = before
	}
	completed = true
	return err
}

// appendEvidence never writes into the visible elements of evidence, so a failed
// transaction can restore the previous slice unchanged.
func appendEvidence(evidence []ProbeHTTPObservation, v ProbeHTTPObservation, limit int) []ProbeHTTPObservation {
	if len(evidence) < limit {
		return append(evidence, v)
	}
	return append(append(make([]ProbeHTTPObservation, 0, limit), evidence[len(evidence)-limit+1:]...), v)
}

// Evidence is a copied local record of at most EvidenceLimit newest observations,
// not scoring or whole-NIC cleanup proof.
func (s *OwnedProbeSession) Evidence(ctx context.Context) ([]ProbeHTTPObservation, error) {
	if s == nil || nilInterface(ctx) {
		return nil, ErrOwnedProbeSession
	}
	wait, cancel := ownedWaitContext(ctx)
	defer cancel()
	select {
	case <-wait.Done():
		return nil, wait.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	if err := observationContextError(ctx); err != nil {
		return nil, err
	}
	return append([]ProbeHTTPObservation(nil), s.evidence...), nil
}

func ownedWaitContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if d, ok := ctx.Deadline(); ok {
		return context.WithDeadline(ctx, d)
	}
	return context.WithCancel(ctx)
}
