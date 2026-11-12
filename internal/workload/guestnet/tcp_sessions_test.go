package guestnet

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func probeNICFixture() ProbeNIC {
	route := tcpRouteFixtures()[0]
	return ProbeNIC{LocalIP: route.LocalIP, PeerIP: route.PeerIP, LocalMAC: route.LocalMAC, PeerMAC: route.PeerMAC, ServicePorts: []uint16{80, 8080}}
}
func probeRegistry(t *testing.T) *TCPProbeSessions {
	t.Helper()
	r, err := NewTCPProbeSessions(context.Background(), probeNICFixture(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		r.Revoke()
		if err := r.Wait(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return r
}
func openProbe(t *testing.T, r *TCPProbeSessions, port uint16) *TCPProbeSession {
	t.Helper()
	s, err := r.Open(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func retireProbe(t *testing.T, s *TCPProbeSession) {
	t.Helper()
	s.Revoke()
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func probeFrame(s *TCPProbeSession, direction TCPDirection) []byte {
	return tcpRouteFrame(s.Route(), direction, s.Route().PeerPort)
}
func readProbe(t *testing.T, s *TCPProbeSession) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := s.ReadFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return frame
}

func TestProbeSessionsIsolationRetirementAndCopiedFrames(t *testing.T) {
	r := probeRegistry(t)
	a, b := openProbe(t, r, 80), openProbe(t, r, 8080)
	if a.Route().LocalPort == b.Route().LocalPort {
		t.Fatal("tuple reused")
	}
	fa, fb := probeFrame(a, TCPFromPeer), probeFrame(b, TCPFromPeer)
	wantA, wantB := append([]byte(nil), fa...), append([]byte(nil), fb...)
	var wg sync.WaitGroup
	for _, frame := range [][]byte{fa, fb} {
		wg.Add(1)
		go func(frame []byte) {
			defer wg.Done()
			if accepted, err := r.RouteIncoming(frame); !accepted || err != nil {
				t.Errorf("route: %t %v", accepted, err)
			}
		}(frame)
	}
	wg.Wait()
	clear(fa)
	clear(fb)
	if !bytes.Equal(readProbe(t, a), wantA) || !bytes.Equal(readProbe(t, b), wantB) {
		t.Fatal("cross-session delivery or aliased input")
	}
	retireProbe(t, a)
	c := openProbe(t, r, 80)
	if c.Route().LocalPort == a.Route().LocalPort || c.Route().LocalPort == b.Route().LocalPort {
		t.Fatal("retired tuple reused")
	}
	if accepted, err := r.RouteIncoming(wantA); accepted || err != nil {
		t.Fatal("retired frame was not discarded", accepted, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if frame, err := c.ReadFrame(ctx); frame != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("old reply crossed into new session", err)
	}
	if accepted, err := r.RouteIncoming(wantB); !accepted || err != nil {
		t.Fatal("retiring sibling closed registry", err)
	}
	if !bytes.Equal(readProbe(t, b), wantB) {
		t.Fatal("sibling frame changed")
	}
	calls := 0
	if err := a.Transmit(context.Background(), probeFrame(a, TCPToPeer), func(context.Context, []byte) error { calls++; return nil }); !errors.Is(err, ErrProbeClosed) || calls != 0 {
		t.Fatal("retired output admitted", err, calls)
	}
}

func TestProbeSessionsBoundPendingReadAndDiscardQueuedOnRevoke(t *testing.T) {
	r := probeRegistry(t)
	s := openProbe(t, r, 80)
	done := make(chan error, 1)
	go func() { _, err := s.ReadFrame(context.Background()); done <- err }()
	joined := false
	t.Cleanup(func() {
		s.Revoke()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("read did not join during cleanup")
			}
		}
	})
	deadline := time.Now().Add(time.Second)
	for {
		r.mu.Lock()
		reading := s.reading
		r.mu.Unlock()
		if reading {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("read did not enter")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := s.ReadFrame(context.Background()); !errors.Is(err, ErrProbeBusy) {
		t.Fatal("second read queued", err)
	}
	s.Revoke()
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, ErrProbeClosed) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not join")
	}
	next := openProbe(t, r, 8080)
	if accepted, err := r.RouteIncoming(probeFrame(next, TCPFromPeer)); !accepted || err != nil {
		t.Fatal(err)
	}
	retireProbe(t, next)
	if frame, err := next.ReadFrame(context.Background()); frame != nil || !errors.Is(err, ErrProbeClosed) {
		t.Fatal("buffer survived revocation", err)
	}
}

func TestProbeSessionsRetainBlockedDeliveryAndCapacity(t *testing.T) {
	r := probeRegistry(t)
	sessions := make([]*TCPProbeSession, MaxActiveProbeSessions)
	for i := range sessions {
		sessions[i] = openProbe(t, r, 80)
	}
	if _, err := r.Open(context.Background(), 80); !errors.Is(err, ErrProbeCapacity) {
		t.Fatal("active limit missing", err)
	}
	s := sessions[0]
	input := probeFrame(s, TCPToPeer)
	want := append([]byte(nil), input...)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Transmit(context.Background(), input, func(ctx context.Context, owned []byte) error {
			close(entered)
			<-release
			if !bytes.Equal(owned, want) {
				return errors.New("output alias")
			}
			return nil
		})
	}()
	joined := false
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("delivery not joined")
			}
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("delivery not entered")
	}
	clear(input)
	other := sessions[1]
	calls := 0
	if err := other.Transmit(context.Background(), probeFrame(other, TCPToPeer), func(context.Context, []byte) error { calls++; return nil }); !errors.Is(err, ErrProbeBusy) || calls != 0 {
		t.Fatal("shared delivery queued", err, calls)
	}
	s.Revoke()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := s.Wait(ctx); !errors.Is(err, ErrProbeDrainPending) {
		t.Fatal("pending callback called drained", err)
	}
	cancel()
	select {
	case <-r.Lifetime().Done():
	case <-time.After(time.Second):
		t.Fatal("revoked pending delivery left shared admission live")
	}
	if _, err := r.Open(context.Background(), 80); !errors.Is(err, ErrProbeClosed) {
		t.Fatal("shared admission survived uncertain delivery", err)
	}
	r.mu.Lock()
	retained := s.inflight == 1 && !s.drained && r.live >= 1 && r.sending && !r.drained
	r.mu.Unlock()
	if !retained {
		t.Fatal("pending delivery debt was released")
	}
	r.Revoke()
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := r.Wait(ctx); !errors.Is(err, ErrProbeDrainPending) {
		t.Fatal("registry released pending callback", err)
	}
	cancel()
	close(release)
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, ErrProbeClosed) {
			t.Fatal("late delivery succeeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery did not return")
	}
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestProbeSessionsNeverReuseLifetimePorts(t *testing.T) {
	r := probeRegistry(t)
	for i := 0; i < MaxProbeSessions; i++ {
		s := openProbe(t, r, 80)
		if want := ProbeSourcePort + uint16(i); s.Route().LocalPort != want {
			t.Fatal("port sequence changed", i, s.Route())
		}
		retireProbe(t, s)
	}
	if _, err := r.Open(context.Background(), 80); !errors.Is(err, ErrProbeCapacity) {
		t.Fatal("tombstones reused", err)
	}
}

func TestProbeSessionsRejectCrossSessionOutputAndUnownedInput(t *testing.T) {
	for _, kind := range []string{"other output", "other peer", "other service", "unallocated", "malformed", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			r := probeRegistry(t)
			a, b := openProbe(t, r, 80), openProbe(t, r, 8080)
			calls := 0
			if kind == "other output" {
				if err := a.Transmit(context.Background(), probeFrame(b, TCPToPeer), func(context.Context, []byte) error { calls++; return nil }); err == nil || calls != 0 {
					t.Fatal("cross-session output accepted", err)
				}
			} else {
				route := a.Route()
				switch kind {
				case "other peer":
					route.PeerMAC[5]++
				case "other service":
					route.PeerPort++
				case "unallocated":
					route.LocalPort += 50
				}
				frame := tcpRouteFrame(route, TCPFromPeer, route.PeerPort)
				if kind == "malformed" {
					frame[30] ^= 1
				}
				if kind == "overflow" {
					for i := 0; i < probeQueueSize; i++ {
						if accepted, err := r.RouteIncoming(frame); !accepted || err != nil {
							t.Fatal(err)
						}
					}
				}
				if accepted, err := r.RouteIncoming(frame); accepted || err == nil {
					t.Fatal("invalid input admitted", accepted, err)
				}
			}
			if r.Lifetime().Err() == nil {
				t.Fatal("registry not revoked")
			}
			if _, err := r.Open(context.Background(), 80); !errors.Is(err, ErrProbeClosed) {
				t.Fatal("new work after terminal failure", err)
			}
		})
	}
}

func TestProbeSessionsRetiredTrafficUsesCumulativeBudgets(t *testing.T) {
	for _, kind := range []string{"frames", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			r := probeRegistry(t)
			s := openProbe(t, r, 80)
			frame := probeFrame(s, TCPFromPeer)
			retireProbe(t, s)
			r.mu.Lock()
			if kind == "frames" {
				r.frames = maxNICFrames - 1
			} else {
				r.bytes = maxNICBytes - len(frame)
			}
			r.mu.Unlock()
			if accepted, err := r.RouteIncoming(frame); accepted || err != nil {
				t.Fatal("last retired budget rejected", err)
			}
			if accepted, err := r.RouteIncoming(frame); accepted || !errors.Is(err, ErrProbeCapacity) {
				t.Fatal("retired input bypassed budget", err)
			}
		})
	}
}

type probeDelayedDeadline struct {
	context.Context
	expiry time.Time
}

func (c probeDelayedDeadline) Deadline() (time.Time, bool) { return c.expiry, true }

func TestProbeSessionsIndependentDeadlines(t *testing.T) {
	r := probeRegistry(t)
	deadline := time.Now().Add(30 * time.Millisecond)
	parent := probeDelayedDeadline{context.Background(), deadline}
	s, err := r.Open(parent, 80)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Lifetime().Deadline(); !got.Equal(deadline) {
		t.Fatal("session deadline renewed")
	}
	if frame, err := s.ReadFrame(context.Background()); frame != nil || err == nil {
		t.Fatal("expired read succeeded", err)
	}
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if parent.Err() != nil {
		t.Fatal("fixture notified unexpectedly")
	}
	next := openProbe(t, r, 80)
	readParent := probeDelayedDeadline{context.Background(), time.Now().Add(20 * time.Millisecond)}
	if _, err := next.ReadFrame(readParent); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("read relied on parent notification", err)
	}
	if next.Lifetime().Err() != nil {
		t.Fatal("read timeout revoked independent session")
	}
	rootParent := probeDelayedDeadline{context.Background(), time.Now().Add(30 * time.Millisecond)}
	bounded, err := NewTCPProbeSessions(rootParent, probeNICFixture(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bounded.Revoke(); _ = bounded.Wait(context.Background()) })
	if err := bounded.Wait(context.Background()); err != nil {
		t.Fatal("registry depended on parent notification", err)
	}
}

func TestProbeSessionsCallbackFailureIsTerminalNotRetry(t *testing.T) {
	r := probeRegistry(t)
	s := openProbe(t, r, 80)
	sentinel := errors.New("partial delivery")
	if err := s.Transmit(context.Background(), probeFrame(s, TCPToPeer), func(context.Context, []byte) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if r.Lifetime().Err() == nil {
		t.Fatal("uncertain shared stream remained active")
	}
	calls := 0
	if err := s.Transmit(context.Background(), probeFrame(s, TCPToPeer), func(context.Context, []byte) error { calls++; return nil }); !errors.Is(err, ErrProbeClosed) || calls != 0 {
		t.Fatal("failed delivery replayed", err)
	}
}

func TestProbeSessionsConstructorAndOpenValidation(t *testing.T) {
	before := len(probeRegistries)
	for _, kind := range []string{"nil", "expired", "too long", "empty ports", "duplicate ports", "zero port", "link local", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			nic := probeNICFixture()
			deadline := time.Now().Add(time.Minute)
			switch kind {
			case "nil":
				ctx = nil
			case "expired":
				deadline = time.Now().Add(-time.Second)
			case "too long":
				deadline = time.Now().Add(MaxNICSessionLifetime + time.Minute)
			case "empty ports":
				nic.ServicePorts = nil
			case "duplicate ports":
				nic.ServicePorts = []uint16{80, 80}
			case "zero port":
				nic.ServicePorts = []uint16{0}
			case "link local":
				nic.LocalIP = tcpRouteFixtures()[1].LocalIP
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if r, err := NewTCPProbeSessions(ctx, nic, deadline); r != nil || err == nil {
				t.Fatal("invalid registry accepted", err)
			}
		})
	}
	if len(probeRegistries) != before {
		t.Fatal("constructor rejection leaked admission")
	}
	r := probeRegistry(t)
	if _, err := r.Open(context.Background(), 22); !errors.Is(err, ErrProbeSession) {
		t.Fatal("unapproved service", err)
	}
	if _, err := r.Open(nil, 80); !errors.Is(err, ErrProbeSession) {
		t.Fatal("nil lifetime", err)
	}
	expired := probeDelayedDeadline{context.Background(), time.Now().Add(-time.Second)}
	if _, err := r.Open(expired, 80); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired lifetime admitted", err)
	}
	s := openProbe(t, r, 80)
	if s.Route().LocalPort != ProbeSourcePort {
		t.Fatal("rejected admission consumed tuple")
	}
}

func TestProbeSessionsCallbackPanicRevokesAndDrains(t *testing.T) {
	r := probeRegistry(t)
	s := openProbe(t, r, 80)
	marker := errors.New("callback panic")
	func() {
		defer func() {
			if got := recover(); got != marker {
				t.Errorf("panic changed: %v", got)
			}
		}()
		_ = s.Transmit(context.Background(), probeFrame(s, TCPToPeer), func(context.Context, []byte) error { panic(marker) })
	}()
	if r.Lifetime().Err() == nil {
		t.Fatal("panic left admission live")
	}
	if err := s.Wait(context.Background()); err != nil {
		t.Fatal("panicked callback retained session debt", err)
	}
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal("panicked callback retained registry debt", err)
	}
}

func TestProbeRegistryCapacityReturnsBeforeDrainPublication(t *testing.T) {
	var registries []*TCPProbeSessions
	t.Cleanup(func() {
		for _, r := range registries {
			r.Revoke()
			if err := r.Wait(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	for i := 0; i < cap(probeRegistries); i++ {
		r, err := NewTCPProbeSessions(context.Background(), probeNICFixture(), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		registries = append(registries, r)
	}
	if r, err := NewTCPProbeSessions(context.Background(), probeNICFixture(), time.Now().Add(time.Minute)); r != nil || !errors.Is(err, ErrProbeCapacity) {
		t.Fatal("global bound missing", err)
	}
	for i := 0; i < 32; i++ {
		old := registries[0]
		old.Revoke()
		if err := old.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		next, err := NewTCPProbeSessions(context.Background(), probeNICFixture(), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal("drain published before capacity returned", err)
		}
		registries[0] = next
	}
}

func TestProbeSessionsSameServiceAndParentCancellationIsolation(t *testing.T) {
	r := probeRegistry(t)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := r.Open(parent, 80)
	if err != nil {
		t.Fatal(err)
	}
	b := openProbe(t, r, 80)
	for _, s := range []*TCPProbeSession{a, b} {
		if accepted, err := r.RouteIncoming(probeFrame(s, TCPFromPeer)); !accepted || err != nil {
			t.Fatal(err)
		}
	}
	if packet, err := ParseTCPFrame(readProbe(t, a)); err != nil || packet.DestinationPort != a.Route().LocalPort {
		t.Fatal("same service crossed A", err)
	}
	if packet, err := ParseTCPFrame(readProbe(t, b)); err != nil || packet.DestinationPort != b.Route().LocalPort {
		t.Fatal("same service crossed B", err)
	}
	cancel()
	if err := a.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.Lifetime().Err() != nil || b.Lifetime().Err() != nil {
		t.Fatal("session cancellation revoked sibling")
	}
	if accepted, err := r.RouteIncoming(probeFrame(b, TCPFromPeer)); !accepted || err != nil {
		t.Fatal(err)
	}
	_ = readProbe(t, b)
}

func TestProbeSessionsTransmitCancellationRetainsDebtUntilReturn(t *testing.T) {
	for _, kind := range []string{"cancel", "delayed deadline"} {
		t.Run(kind, func(t *testing.T) {
			r := probeRegistry(t)
			s := openProbe(t, r, 80)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "delayed deadline" {
				parent = probeDelayedDeadline{context.Background(), time.Now().Add(50 * time.Millisecond)}
			}
			entered, observed, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- s.Transmit(parent, probeFrame(s, TCPToPeer), func(ctx context.Context, _ []byte) error {
					close(entered)
					<-ctx.Done()
					close(observed)
					<-release
					return nil
				})
			}()
			joined := false
			t.Cleanup(func() {
				cancel()
				r.Revoke()
				select {
				case <-release:
				default:
					close(release)
				}
				if !joined {
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("canceled delivery not joined")
					}
				}
			})
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("callback not entered")
			}
			if kind == "cancel" {
				cancel()
			}
			select {
			case <-observed:
			case <-time.After(time.Second):
				t.Fatal("callback cancellation not propagated")
			}
			select {
			case <-r.Lifetime().Done():
			case <-time.After(time.Second):
				t.Fatal("expired delivery left admission live")
			}
			ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
			if err := r.Wait(ctx); !errors.Is(err, ErrProbeDrainPending) {
				t.Fatal("cancel treated as completed cleanup", err)
			}
			stop()
			close(release)
			select {
			case err := <-done:
				joined = true
				if err == nil {
					t.Fatal("late nil result accepted")
				}
			case <-time.After(time.Second):
				t.Fatal("callback did not return")
			}
			if err := r.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProbeSessionsForgedRetiredTupleStillRevokes(t *testing.T) {
	r := probeRegistry(t)
	s := openProbe(t, r, 80)
	route := s.Route()
	retireProbe(t, s)
	route.PeerMAC[5]++
	if accepted, err := r.RouteIncoming(tcpRouteFrame(route, TCPFromPeer, route.PeerPort)); accepted || err == nil {
		t.Fatal("forged retired frame got discard exemption", err)
	}
	if r.Lifetime().Err() == nil {
		t.Fatal("forged retired frame did not revoke registry")
	}
}

func TestProbeRegistryGlobalCapacityRetainsUnfinishedCallback(t *testing.T) {
	var registries []*TCPProbeSessions
	t.Cleanup(func() {
		for _, r := range registries {
			r.Revoke()
			if err := r.Wait(context.Background()); err != nil {
				t.Error(err)
			}
		}
	})
	for i := 0; i < cap(probeRegistries); i++ {
		r, err := NewTCPProbeSessions(context.Background(), probeNICFixture(), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		registries = append(registries, r)
	}
	r := registries[0]
	s := openProbe(t, r, 80)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.Transmit(context.Background(), probeFrame(s, TCPToPeer), func(context.Context, []byte) error { close(entered); <-release; return nil })
	}()
	joined := false
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("global debt callback not joined")
			}
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("callback not entered")
	}
	r.Revoke()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := r.Wait(ctx); !errors.Is(err, ErrProbeDrainPending) {
		t.Fatal(err)
	}
	cancel()
	if next, err := NewTCPProbeSessions(context.Background(), probeNICFixture(), time.Now().Add(time.Minute)); next != nil || !errors.Is(err, ErrProbeCapacity) {
		t.Fatal("pending callback released global debt", err)
	}
	close(release)
	select {
	case err := <-done:
		joined = true
		if err == nil {
			t.Fatal("late success")
		}
	case <-time.After(time.Second):
		t.Fatal("callback not joined")
	}
	if err := r.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, err := NewTCPProbeSessions(context.Background(), probeNICFixture(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal("settled debt not released", err)
	}
	registries[0] = next
}
