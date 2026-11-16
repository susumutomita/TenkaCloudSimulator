package guesttcp

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

type issuedMemoryResource struct {
	stream      io.ReadWriteCloser
	transaction func(context.Context, OwnedProbeAcceptance, func() error) error
}

func (r *issuedMemoryResource) Stream() io.ReadWriteCloser { return r.stream }
func (r *issuedMemoryResource) Transact(c context.Context, a OwnedProbeAcceptance, f func() error) error {
	if r.transaction != nil {
		return r.transaction(c, a, f)
	}
	return f()
}

type ownedMemoryStream struct {
	closeCount atomic.Int32
	closed     chan struct{}
	once       sync.Once
	release    chan struct{}
	err        error
}

func (m *ownedMemoryStream) Read([]byte) (int, error)    { <-m.closed; return 0, io.EOF }
func (m *ownedMemoryStream) Write(p []byte) (int, error) { <-m.closed; return 0, io.ErrClosedPipe }
func (m *ownedMemoryStream) Close() error {
	m.closeCount.Add(1)
	m.once.Do(func() { close(m.closed) })
	if m.release != nil {
		<-m.release
	}
	return m.err
}
func ownedConfig() OwnedProbeConfig {
	c := config()
	return OwnedProbeConfig{NIC: guestnet.ProbeNIC{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, ServicePorts: []uint16{c.Port}}, Paths: []string{"/healthz"}, EvidenceLimit: 2}
}
func ownedFixture(t *testing.T, r *issuedMemoryResource) *OwnedProbeSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	s, e := NewOwnedProbeSession(ctx, r, ownedConfig())
	if e != nil {
		cancel()
		t.Fatal(e)
	}
	t.Cleanup(func() {
		s.Revoke()
		ctx, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if e := s.Wait(ctx); e != nil {
			t.Error(e)
		}
		cancel()
	})
	return s
}
func ownedObservation() ProbeHTTPObservation {
	c := config()
	now := time.Now()
	return ProbeHTTPObservation{HTTPObservation: HTTPObservation{StatusCode: 200, StartedAt: now.Add(-time.Millisecond), ReceivedAt: now}, Route: guestnet.TCPRoute{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, LocalPort: 40000, PeerPort: c.Port}, Path: "/healthz"}
}
func TestOwnedProbeSessionTransferAndPolicy(t *testing.T) {
	m := &ownedMemoryStream{closed: make(chan struct{})}
	r := &issuedMemoryResource{stream: m}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bad := ownedConfig()
	bad.Paths = []string{"/bad"}
	if _, e := NewOwnedProbeSession(ctx, r, bad); e == nil {
		t.Fatal("invalid accepted")
	}
	if m.closeCount.Load() != 0 {
		t.Fatal("consumed invalid")
	}
	s := ownedFixture(t, r)
	var _ FrameCommitter = s
	var _ ProbeHTTPObservationCommitter = s
	if s.Stream() != s.Stream() {
		t.Fatal("unstable facade")
	}
	if e := s.CommitFrame(ctx, FromGuest, reverseTCPFrame(config()), func() error { return nil }); e != nil {
		t.Fatal(e)
	}
	for _, frame := range [][]byte{nil, requestARP(arpBinding(config())), {1, 2, 3}} {
		if e := s.CommitFrame(ctx, FromGuest, frame, func() error { t.Fatal("invalid callback"); return nil }); e == nil {
			t.Fatal("bad frame accepted")
		}
	}
	if e := s.CommitFrame(ctx, ToGuest, reverseTCPFrame(config()), func() error { t.Fatal("direction"); return nil }); e == nil {
		t.Fatal("wrong direction")
	}
	if e := s.CommitProbeHTTPObservation(ctx, ownedObservation()); e != nil {
		t.Fatal(e)
	}
	v, e := s.Evidence(ctx)
	if e != nil || len(v) != 1 {
		t.Fatal(v, e)
	}
	v[0].StatusCode = 500
	v, _ = s.Evidence(ctx)
	if v[0].StatusCode != 200 {
		t.Fatal("mutable evidence")
	}
	wrong := ownedObservation()
	wrong.Path = "/"
	if s.CommitProbeHTTPObservation(ctx, wrong) == nil {
		t.Fatal("wrong path")
	}
}
func TestOwnedProbeSessionRevokeUnblocksAndJoins(t *testing.T) {
	m := &ownedMemoryStream{closed: make(chan struct{})}
	s := ownedFixture(t, &issuedMemoryResource{stream: m})
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.CommitFrame(context.Background(), FromGuest, reverseTCPFrame(config()), func() error { close(entered); _, e := s.Stream().Write([]byte{1}); return e })
	}()
	<-entered
	s.Revoke()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("revoked accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("close waited fence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := s.Wait(ctx); e != nil {
		t.Fatal(e)
	}
	s.Revoke()
	if m.closeCount.Load() != 1 {
		t.Fatal("close repeated")
	}
	if s.CommitProbeHTTPObservation(ctx, ownedObservation()) == nil {
		t.Fatal("late publication")
	}
}

type ownedSilentDeadline struct {
	context.Context
	deadline time.Time
}

func (c ownedSilentDeadline) Deadline() (time.Time, bool) { return c.deadline, true }
func TestOwnedProbeSessionSilentWaiterDeadline(t *testing.T) {
	m := &ownedMemoryStream{closed: make(chan struct{})}
	s := ownedFixture(t, &issuedMemoryResource{stream: m})
	<-s.gate
	ctx := ownedSilentDeadline{context.Background(), time.Now().Add(25 * time.Millisecond)}
	done := make(chan error, 1)
	go func() {
		done <- s.CommitFrame(ctx, FromGuest, reverseTCPFrame(config()), func() error { t.Error("expired callback"); return nil })
	}()
	select {
	case e := <-done:
		if e == nil {
			t.Error("expired success")
		}
	case <-time.After(time.Second):
		t.Error("unbounded waiter")
	}
	s.gate <- struct{}{}
}
func TestOwnedProbeSessionProviderMisuseAndRollback(t *testing.T) {
	for _, mode := range []string{"double", "missing", "error", "late"} {
		t.Run(mode, func(t *testing.T) {
			m := &ownedMemoryStream{closed: make(chan struct{})}
			var late func() error
			r := &issuedMemoryResource{stream: m, transaction: func(_ context.Context, _ OwnedProbeAcceptance, f func() error) error {
				switch mode {
				case "double":
					_ = f()
					return f()
				case "missing":
					return nil
				case "error":
					_ = f()
					return errors.New("provider failed")
				case "late":
					late = f
					return errors.New("not admitted")
				}
				return nil
			}}
			s := ownedFixture(t, r)
			if s.CommitProbeHTTPObservation(context.Background(), ownedObservation()) == nil {
				t.Fatal("misuse accepted")
			}
			if late != nil && late() == nil {
				t.Fatal("late callback accepted")
			}
			v, e := s.Evidence(context.Background())
			if e != nil || len(v) != 0 {
				t.Fatal("tentative evidence leaked", v, e)
			}
		})
	}
}
func TestOwnedProbeSessionBlockedCloseRetainsSlot(t *testing.T) {
	before := len(ownedProbeSlots)
	m := &ownedMemoryStream{closed: make(chan struct{}), release: make(chan struct{})}
	s := ownedFixture(t, &issuedMemoryResource{stream: m})
	s.Revoke()
	<-m.closed
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if s.Wait(ctx) == nil {
		t.Fatal("blocked close settled")
	}
	if len(ownedProbeSlots) != before+1 {
		t.Fatal("debt released")
	}
	close(m.release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if e := s.Wait(ctx2); e != nil {
		t.Fatal(e)
	}
	if len(ownedProbeSlots) != before {
		t.Fatal("successful join retained")
	}
}

func TestOwnedProbeSessionAppliedFailureIsTerminal(t *testing.T) {
	m := &ownedMemoryStream{closed: make(chan struct{})}
	s := ownedFixture(t, &issuedMemoryResource{stream: m})
	if s.CommitFrame(context.Background(), FromGuest, reverseTCPFrame(config()), func() error { return io.ErrShortWrite }) == nil {
		t.Fatal("failed write accepted")
	}
	if s.CommitFrame(context.Background(), FromGuest, reverseTCPFrame(config()), func() error { t.Fatal("replayed after uncertain write"); return nil }) == nil {
		t.Fatal("reused")
	}
}
func TestOwnedProbeSessionPanicCannotExposeTentativeEvidence(t *testing.T) {
	m := &ownedMemoryStream{closed: make(chan struct{})}
	r := &issuedMemoryResource{stream: m, transaction: func(_ context.Context, _ OwnedProbeAcceptance, f func() error) error {
		_ = f()
		panic("synthetic provider panic")
	}}
	s := ownedFixture(t, r)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic hidden")
			}
		}()
		_ = s.CommitProbeHTTPObservation(context.Background(), ownedObservation())
	}()
	v, e := s.Evidence(context.Background())
	if e != nil || len(v) != 0 {
		t.Fatal("panic evidence", v, e)
	}
	if !s.terminal.Load() {
		t.Fatal("panic did not seal")
	}
}
func TestOwnedProbeSessionCapacityAndCloseFailureDebt(t *testing.T) {
	before := len(ownedProbeSlots)
	var sessions []*OwnedProbeSession
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := before; i < cap(ownedProbeSlots); i++ {
		m := &ownedMemoryStream{closed: make(chan struct{})}
		s, e := NewOwnedProbeSession(ctx, &issuedMemoryResource{stream: m}, ownedConfig())
		if e != nil {
			t.Fatal(e)
		}
		sessions = append(sessions, s)
	}
	extra := &ownedMemoryStream{closed: make(chan struct{})}
	if _, e := NewOwnedProbeSession(ctx, &issuedMemoryResource{stream: extra}, ownedConfig()); !errors.Is(e, ErrCapacity) {
		t.Fatal(e)
	}
	if extra.closeCount.Load() != 0 {
		t.Fatal("capacity failure consumed")
	}
	for _, s := range sessions {
		s.Revoke()
		if e := s.Wait(ctx); e != nil {
			t.Fatal(e)
		}
	}
	failed := &ownedMemoryStream{closed: make(chan struct{}), err: errors.New("close uncertain")}
	s, e := NewOwnedProbeSession(ctx, &issuedMemoryResource{stream: failed}, ownedConfig())
	if e != nil {
		t.Fatal(e)
	}
	s.Revoke()
	if !errors.Is(s.Wait(ctx), ErrDrainUncertain) {
		t.Fatal("failed close settled")
	}
	s.Revoke()
	if failed.closeCount.Load() != 1 || len(ownedProbeSlots) != before+1 {
		t.Fatal("failed close debt lost")
	}
	// Intentionally retained process-lifetime debt, exactly as production policy.
}

func TestOwnedProbeSessionCopiesPolicyAndBoundsEvidence(t *testing.T) {
	m := &ownedMemoryStream{closed: make(chan struct{})}
	c := ownedConfig()
	c.EvidenceLimit = 1
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, e := NewOwnedProbeSession(ctx, &issuedMemoryResource{stream: m}, c)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		s.Revoke()
		if e := s.Wait(context.Background()); e != nil {
			t.Error(e)
		}
	}()
	c.NIC.ServicePorts[0] = 443
	c.Paths[0] = "/"
	if e := s.CommitProbeHTTPObservation(ctx, ownedObservation()); e != nil {
		t.Fatal("caller mutation changed copied policy", e)
	}
	// The record keeps only the newest EvidenceLimit observations; probing never
	// stops because the record filled.
	for status := 201; status <= 203; status++ {
		v := ownedObservation()
		v.StatusCode = status
		if e := s.CommitProbeHTTPObservation(ctx, v); e != nil {
			t.Fatal("full evidence record refused a later observation", e)
		}
	}
	if got, e := s.Evidence(ctx); e != nil || len(got) != 1 || got[0].StatusCode != 203 {
		t.Fatal("evidence limit", got, e)
	}
	for _, change := range []func(*ProbeHTTPObservation){func(v *ProbeHTTPObservation) { v.Route.PeerPort = 443 }, func(v *ProbeHTTPObservation) { v.Route.LocalPort = 39999 }, func(v *ProbeHTTPObservation) { v.Route.LocalPort = 41024 }, func(v *ProbeHTTPObservation) { v.Route.PeerMAC[5]++ }} {
		v := ownedObservation()
		change(&v)
		if !errors.Is(s.CommitProbeHTTPObservation(ctx, v), ErrOwnedProbeSession) {
			t.Fatal("bad tuple accepted")
		}
	}
	wrong := config()
	wrong.Port = 443
	if s.CommitFrame(ctx, FromGuest, reverseTCPFrame(wrong), func() error { t.Fatal("wrong service callback"); return nil }) == nil {
		t.Fatal("wrong frame service accepted")
	}
}
func TestOwnedProbeSessionSilentLifetimeClosesAutonomously(t *testing.T) {
	m := &ownedMemoryStream{closed: make(chan struct{})}
	ctx := ownedSilentDeadline{context.Background(), time.Now().Add(30 * time.Millisecond)}
	s, e := NewOwnedProbeSession(ctx, &issuedMemoryResource{stream: m}, ownedConfig())
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-m.closed:
	case <-time.After(time.Second):
		s.Revoke()
		t.Fatal("non-notifying lifetime not enforced")
	}
	wait, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := s.Wait(wait); e != nil {
		t.Fatal(e)
	}
}

type ownedUncooperativeIO struct {
	entered, release, closed chan struct{}
	once                     sync.Once
}

func (m *ownedUncooperativeIO) Read([]byte) (int, error) {
	close(m.entered)
	<-m.release
	return 0, io.EOF
}
func (m *ownedUncooperativeIO) Write([]byte) (int, error) {
	close(m.entered)
	<-m.release
	return 0, io.ErrClosedPipe
}
func (m *ownedUncooperativeIO) Close() error { m.once.Do(func() { close(m.closed) }); return nil }
func TestOwnedProbeSessionFacadeIOJoinAfterClose(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			before := len(ownedProbeSlots)
			m := &ownedUncooperativeIO{entered: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{})}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s, e := NewOwnedProbeSession(ctx, &issuedMemoryResource{stream: m}, ownedConfig())
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if write {
					_, _ = s.Stream().Write([]byte{1})
				} else {
					_, _ = s.Stream().Read(make([]byte, 1))
				}
			}()
			<-m.entered
			s.Revoke()
			<-m.closed
			wait, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer stop()
			if s.Wait(wait) == nil {
				t.Fatal("I/O debt released before return")
			}
			if len(ownedProbeSlots) != before+1 {
				t.Fatal("slot lost")
			}
			close(m.release)
			<-done
			if e := s.Wait(ctx); e != nil {
				t.Fatal(e)
			}
			if len(ownedProbeSlots) != before {
				t.Fatal("joined I/O not released")
			}
		})
	}
}
