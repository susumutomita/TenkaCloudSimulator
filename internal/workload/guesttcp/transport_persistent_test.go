package guesttcp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

type persistentServiceFixture struct {
	*transportServiceFixture
	ctx           context.Context
	lifetimeCalls atomic.Int32
}

func (s *persistentServiceFixture) Lifetime() context.Context { s.lifetimeCalls.Add(1); return s.ctx }
func persistentFixtureService(t *testing.T) *persistentServiceFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	return &persistentServiceFixture{transportServiceFixture: newTransportServiceFixture(), ctx: ctx}
}
func persistentConfig() guestnet.ProbeNIC {
	c := config()
	return guestnet.ProbeNIC{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, ServicePorts: []uint16{80, 8080}}
}
func persistentFixture(t *testing.T, owner FrameCommitter) (*PersistentTransport, *memoryStream, *persistentServiceFixture) {
	t.Helper()
	service := persistentFixtureService(t)
	local, remote := memoryStreams()
	ac := arpBinding(config())
	tr, err := NewPersistentTransportUntil(context.Background(), persistentConfig(), local, owner, &ac, service, transportServiceRoute(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Error(err)
		}
	})
	return tr, remote, service
}
func persistentProbe(t *testing.T, tr *PersistentTransport, ctx context.Context, port uint16) *ProbeExchange {
	t.Helper()
	p, err := tr.OpenProbe(ctx, port)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPersistentConstructorPreservesOwnership(t *testing.T) {
	for _, name := range []string{"first future tuple", "last future tuple", "peer MAC", "missing finite lifetime", "nil lifetime", "nil service", "expired", "excessive", "nil owner", "invalid arp"} {
		t.Run(name, func(t *testing.T) {
			s := persistentFixtureService(t)
			var service RevocablePassiveService = s
			nic := persistentConfig()
			route := transportServiceRoute()
			ctx := context.Background()
			expires := time.Now().Add(time.Minute)
			stream := newBlockedStream(false)
			var owner FrameCommitter = memoryCommitter
			ac := arpBinding(config())
			switch name {
			case "first future tuple":
				route.LocalIP = nic.LocalIP
				route.LocalPort = guestnet.ProbeSourcePort
			case "last future tuple":
				route.LocalIP = nic.LocalIP
				route.LocalPort = guestnet.ProbeSourcePort + guestnet.MaxProbeSessions - 1
			case "peer MAC":
				route.PeerMAC[5]++
			case "missing finite lifetime":
				s.ctx = context.Background()
			case "nil lifetime":
				s.ctx = nil
			case "nil service":
				var absent *persistentServiceFixture
				service = absent
			case "expired":
				ctx = canceledTransportContext()
			case "excessive":
				expires = time.Now().Add(MaxPassiveLifetime + time.Minute)
			case "nil owner":
				owner = nil
			case "invalid arp":
				ac.GuestMAC[5]++
			}
			baseline := len(transportSlots)
			tr, err := NewPersistentTransportUntil(ctx, nic, stream, owner, &ac, service, route, expires)
			if tr != nil || err == nil {
				t.Fatal("invalid persistent admission", err)
			}
			if stream.closes.Load() != 0 || s.closes.Load() != 0 || len(transportSlots) != baseline {
				t.Fatal("rejected constructor consumed dependencies/capacity")
			}
		})
	}
}

func TestPersistentProbeRetirementPreservesSharedService(t *testing.T) {
	baseline := len(slots)
	tr, remote, s := persistentFixture(t, memoryCommitter)
	if s.lifetimeCalls.Load() != 1 {
		t.Fatal("service lifetime recaptured")
	}
	if _, err := NewHTTPObserver(tr.nic.base, "/"); !errors.Is(err, ErrConfig) {
		t.Fatal("exchange observer accepted persistent transport", err)
	}
	if _, err := tr.nic.base.Dial(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal("implicit default engine", err)
	}
	var probes []*ProbeExchange
	for range guestnet.MaxActiveProbeSessions {
		probes = append(probes, persistentProbe(t, tr, context.Background(), 80))
	}
	if _, err := tr.OpenProbe(context.Background(), 80); !errors.Is(err, ErrCapacity) {
		t.Fatal("active child limit", err)
	}
	first := probes[0].session.Route().LocalPort
	for _, p := range probes {
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	}
	next := persistentProbe(t, tr, context.Background(), 8080)
	if next.session.Route().LocalPort <= first {
		t.Fatal("tuple reused")
	}
	if tr.Lifetime().Err() != nil || s.closes.Load() != 0 {
		t.Fatal("normal probe retirement ended metadata")
	}
	select {
	case <-remote.closed:
		t.Fatal("normal probe retirement closed NIC")
	default:
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if len(slots) != baseline || s.closes.Load() != 1 {
		t.Fatal("owned child/service cleanup lost")
	}
}

func TestPersistentQueuedProbeRetiresBeforeWriterAdmission(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var owners, retiredOwnerCalls atomic.Int32
	owner := commitFunc(func(_ context.Context, d FrameDirection, frame []byte, apply func() error) error {
		if d != ToGuest {
			t.Error("unexpected inbound")
		}
		if packet, err := guestnet.ParseTCPFrame(frame); err != nil {
			return err
		} else if packet.SourcePort == guestnet.ProbeSourcePort+1 {
			retiredOwnerCalls.Add(1)
		}
		if owners.Add(1) == 1 {
			close(entered)
			<-release
		}
		return apply()
	})
	tr, remote, _ := persistentFixture(t, owner)
	a, b := persistentProbe(t, tr, context.Background(), 80), persistentProbe(t, tr, context.Background(), 8080)
	var dials sync.WaitGroup
	dials.Add(2)
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = tr.Close(); dials.Wait() })
	go func() { defer dials.Done(); _, _ = a.Dial(context.Background()) }()
	go func() {
		defer dials.Done()
		select {
		case <-entered:
			_, _ = b.Dial(context.Background())
		case <-tr.Lifetime().Done():
		}
	}()
	awaitSignal(t, entered, "selected probe writer")
	until := time.Now().Add(time.Second)
	for len(tr.nic.queue) == 0 {
		if time.Now().After(until) {
			t.Fatal("second probe did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := b.CloseContext(short); !errors.Is(err, ErrDrainPending) {
		t.Fatal("queued work falsely drained", err)
	}
	cancel()
	if tr.Lifetime().Err() != nil {
		t.Fatal("queued-only retirement revoked shared lifetime")
	}
	once.Do(func() { close(release) })
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if owners.Load() < 1 || retiredOwnerCalls.Load() != 0 {
		t.Fatal("retired queued frame reached owner", owners.Load(), retiredOwnerCalls.Load())
	}
	if len(remote.in) == 0 {
		t.Fatal("selected live frame was not written")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	dials.Wait()
	if tr.Lifetime().Err() != nil {
		t.Fatal("graceful retirement closed shared transport")
	}
}

func TestPersistentEnteredProbeExpiryFencesWriterAndRetainsDebt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	callback := make(chan error, 1)
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		close(entered)
		<-release
		err := apply()
		callback <- err
		return err
	})
	tr, remote, s := persistentFixture(t, owner)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	p := persistentProbe(t, tr, ctx, 80)
	dial := make(chan struct{})
	go func() { _, _ = p.Dial(context.Background()); close(dial) }()
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = tr.Close(); <-dial })
	awaitSignal(t, entered, "entered probe owner")
	awaitSignal(t, tr.Lifetime().Done(), "registry failure propagation")
	short, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := tr.CloseContext(short); !errors.Is(err, ErrDrainPending) {
		t.Fatal("entered callback falsely drained", err)
	}
	stop()
	if len(transportSlots) == 0 {
		t.Fatal("unsettled callback released shared capacity")
	}
	awaitSignal(t, s.closeEntered, "independent service close")
	once.Do(func() { close(release) })
	deadline, _ := p.Lifetime().Deadline()
	if time.Now().Before(deadline) || p.Lifetime().Err() == nil {
		t.Fatal("probe did not actually expire")
	}
	// The original deadline causes shared registry cancellation; that captured
	// cancellation may be observed before the per-call deadline error.
	if err := <-callback; !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatal("late writer admitted", err)
	}
	awaitSignal(t, tr.nic.base.done, "joined persistent transport")
	if len(remote.in) != 0 {
		t.Fatal("expired SYN reached wire")
	}
}

func TestPersistentServiceExpiryFencesQueuedResponse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	late := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := &persistentServiceFixture{transportServiceFixture: newTransportServiceFixture(), ctx: ctx}
	local, remote := memoryStreams()
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		close(entered)
		<-release
		err := apply()
		late <- err
		return err
	})
	tr, err := NewPersistentTransportUntil(context.Background(), persistentConfig(), local, owner, nil, s, transportServiceRoute(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = tr.Close() })
	s.output <- transportServiceOutput()
	awaitSignal(t, entered, "service owner")
	cancel()
	awaitSignal(t, tr.Lifetime().Done(), "service revocation")
	if err := tr.CloseContext(canceledTransportContext()); !errors.Is(err, ErrDrainPending) {
		t.Fatal("stalled service response lost debt", err)
	}
	once.Do(func() { close(release) })
	if err := <-late; !errors.Is(err, context.Canceled) {
		t.Fatal("stale service frame accepted", err)
	}
	awaitSignal(t, tr.nic.base.done, "service revocation joined")
	if len(remote.in) != 0 {
		t.Fatal("stale metadata response written")
	}
}

func TestPersistentRetiredIngressBudgetAndInvalidIsolation(t *testing.T) {
	tr, remote, s := persistentFixture(t, memoryCommitter)
	p := persistentProbe(t, tr, context.Background(), 80)
	route := p.session.Route()
	old := sourcePortFrame(Config{LocalIP: route.LocalIP, PeerIP: route.PeerIP, LocalMAC: route.LocalMAC, PeerMAC: route.PeerMAC}, true, route.LocalPort, route.PeerPort)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	successor := persistentProbe(t, tr, context.Background(), 80)
	if err := guestnet.NewEncoder(remote).WriteFrame(old); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for {
		tr.nic.base.budgetMu.Lock()
		count := tr.nic.base.packets
		tr.nic.base.budgetMu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("retired ingress not accounted")
		}
		time.Sleep(time.Millisecond)
	}
	successor.engine.mu.Lock()
	packets := successor.engine.packets
	successor.engine.mu.Unlock()
	if packets != 0 || len(s.injected) != 0 {
		t.Fatal("retired traffic crossed sessions")
	}
	tr.nic.base.budgetMu.Lock()
	tr.nic.base.packets = maxPersistentFrames - 1
	tr.nic.base.budgetMu.Unlock()
	if err := guestnet.NewEncoder(remote).WriteFrame(old); err != nil {
		t.Fatal(err)
	}
	if err := guestnet.NewEncoder(remote).WriteFrame(old); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, tr.nic.base.done, "cumulative persistent limit")
	if !errors.Is(tr.WaitContext(context.Background()), ErrCapacity) {
		t.Fatal("retired traffic bypassed persistent budget")
	}
}

func TestPersistentFailedEndpointAndCloseRetainSharedDebt(t *testing.T) {
	tr, remote, s := persistentFixture(t, memoryCommitter)
	release := make(chan struct{})
	s.closeRelease = release
	var once sync.Once
	p := persistentProbe(t, tr, context.Background(), 80)
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = tr.Close() })
	if err := tr.CloseContext(canceledTransportContext()); !errors.Is(err, ErrDrainPending) {
		t.Fatal(err)
	}
	awaitSignal(t, s.closeEntered, "blocked passive close")
	awaitSignal(t, remote.closed, "independent stream close")
	awaitSignal(t, p.done, "independent probe teardown")
	if len(transportSlots) == 0 {
		t.Fatal("failed dependency join released capacity")
	}
	once.Do(func() { close(release) })
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentOpenShutdownRaceJoinsEveryChild(t *testing.T) {
	for range 10 {
		tr, _, _ := persistentFixture(t, memoryCommitter)
		var wg sync.WaitGroup
		ready := make(chan struct{})
		for range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-ready
				p, err := tr.OpenProbe(context.Background(), 80)
				if err == nil {
					_ = p.Close()
				}
			}()
		}
		close(ready)
		_ = tr.Close()
		wg.Wait()
		tr.nic.mu.Lock()
		count := len(tr.nic.probes)
		sealed := tr.nic.sealed
		tr.nic.mu.Unlock()
		if count != 0 || !sealed {
			t.Fatal("child survived shutdown", count, sealed)
		}
	}
}

func TestPersistentWriterExitSealsEnqueue(t *testing.T) {
	tr, _, _ := persistentFixture(t, memoryCommitter)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- tr.nic.submit(context.Background(), nil, transportServiceOutput()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed writer admitted output")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue stranded after final drain")
	}
}

func TestPersistentCleanupWaitUsesOriginalCallerDeadline(t *testing.T) {
	tr, _, service := persistentFixture(t, memoryCommitter)
	release := make(chan struct{})
	service.closeRelease = release
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = tr.Close() })
	_ = tr.CloseContext(canceledTransportContext())
	awaitSignal(t, service.closeEntered, "blocked service close")
	for _, wait := range []func(context.Context) error{tr.WaitContext, tr.CloseContext} {
		parent := passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(20 * time.Millisecond)}
		result := make(chan error, 1)
		go func() { result <- wait(parent) }()
		select {
		case err := <-result:
			if !errors.Is(err, ErrDrainPending) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("cleanup wait result", err)
			}
		case <-time.After(time.Second):
			once.Do(func() { close(release) })
			<-result
			t.Fatal("cleanup wait relied on delayed parent notification")
		}
	}
	once.Do(func() { close(release) })
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
}

// No supervisor is started here: the callback must observe registry revocation
// synchronously, rather than depending on propagation to the base context.
func TestPersistentRegistryRevocationFencesPassiveAndARPCallbacks(t *testing.T) {
	for _, direction := range []FrameDirection{FromGuest, ToGuest} {
		registry, err := guestnet.NewTCPProbeSessions(context.Background(), persistentConfig(), time.Now().Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		applied := 0
		tr := &Transport{ctx: context.Background(), persistent: &persistentNIC{registry: registry, lifetime: registry.Lifetime()}}
		tr.owner = commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
			registry.Revoke()
			if tr.ctx.Err() != nil {
				t.Error("base context unexpectedly canceled")
			}
			return apply()
		})
		err = tr.commit(direction, transportServiceOutput(), func() error { applied++; return nil })
		if !errors.Is(err, context.Canceled) || applied != 0 {
			t.Fatal("late passive/ARP delivery bypassed registry revocation", err, applied)
		}
		if err := registry.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersistentGracefulCloseJoinsSelectedWriteWithoutRevokingSibling(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	result := make(chan error, 1)
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		close(entered)
		<-release
		err := apply()
		result <- err
		return err
	})
	tr, remote, _ := persistentFixture(t, owner)
	a, b := persistentProbe(t, tr, context.Background(), 80), persistentProbe(t, tr, context.Background(), 8080)
	dial := make(chan struct{})
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = tr.Close(); <-dial })
	go func() { _, _ = a.Dial(context.Background()); close(dial) }()
	awaitSignal(t, entered, "selected graceful write")
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := a.CloseContext(short); !errors.Is(err, ErrDrainPending) {
		t.Fatal("selected write falsely joined", err)
	}
	cancel()
	if a.Lifetime().Err() != nil || b.Lifetime().Err() != nil || tr.Lifetime().Err() != nil {
		t.Fatal("normal close revoked admitted write or sibling")
	}
	once.Do(func() { close(release) })
	if err := <-result; err != nil {
		t.Fatal("already selected write failed graceful join", err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if len(remote.in) == 0 || b.Lifetime().Err() != nil || tr.Lifetime().Err() != nil {
		t.Fatal("normal retirement lost selected write or persistent lifetime")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentShortDialFailureRevokesAlreadySelectedSYN(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	late := make(chan error, 1)
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		close(entered)
		<-release
		err := apply()
		late <- err
		return err
	})
	tr, remote, _ := persistentFixture(t, owner)
	p := persistentProbe(t, tr, context.Background(), 80)
	call, cancel := context.WithCancel(context.Background())
	defer cancel()
	dial := make(chan error, 1)
	t.Cleanup(func() { once.Do(func() { close(release) }); _ = tr.Close() })
	go func() { _, err := p.Dial(call); dial <- err }()
	awaitSignal(t, entered, "selected SYN before Dial cancellation")
	cancel()
	select {
	case err := <-dial:
		if err == nil {
			t.Fatal("canceled Dial succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled Dial did not settle")
	}
	awaitSignal(t, tr.Lifetime().Done(), "aborted Engine registry fence")
	if p.Lifetime().Err() == nil {
		t.Fatal("failed Engine left session admission live")
	}
	once.Do(func() { close(release) })
	if err := <-late; err == nil {
		t.Fatal("failed Dial emitted stale SYN")
	}
	awaitSignal(t, tr.nic.base.done, "aborted selected SYN joined")
	if len(remote.in) != 0 {
		t.Fatal("stale SYN reached memory wire after Dial failed")
	}
}

// Cancels after the writer's Err check but during its unchanged Deadline read.
// This forces the real race between its precheck and Transmit's admission.
type persistentCancelAtDeadlineRead struct {
	context.Context
	expires time.Time
	cancel  context.CancelFunc
	once    sync.Once
}

func (c *persistentCancelAtDeadlineRead) Deadline() (time.Time, bool) {
	c.once.Do(c.cancel)
	return c.expires, true
}
func TestPersistentUnenteredTransmitExpiryKeepsSharedWriter(t *testing.T) {
	var calls atomic.Int32
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		calls.Add(1)
		return apply()
	})
	tr, remote, service := persistentFixture(t, owner)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := persistentProbe(t, tr, parent, 80)
	expires, _ := p.Lifetime().Deadline()
	ctx := &persistentCancelAtDeadlineRead{Context: p.Lifetime(), expires: expires, cancel: cancel}
	frame := sourcePortFrame(config(), false, p.session.Route().LocalPort, 80)
	request := &persistentWrite{ctx: ctx, probe: p, frame: frame, done: make(chan error, 1)}
	// Place exactly one internally valid request at the writer boundary. No
	// production API exposes this queue or permits unbounded request producers.
	tr.nic.queue <- request
	select {
	case err := <-request.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("unentered cancellation result", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unentered request did not settle")
	}
	awaitSignal(t, p.done, "canceled unentered probe")
	if tr.Lifetime().Err() != nil || calls.Load() != 0 {
		t.Fatal("unentered probe cancellation killed shared writer or reached owner")
	}
	service.output <- transportServiceOutput()
	received := make(chan error, 1)
	go func() { _, err := guestnet.NewDecoder(remote).ReadFrame(); received <- err }()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal("service lost surviving writer", err)
		}
	case <-time.After(time.Second):
		_ = tr.Close()
		<-received
		t.Fatal("service write stranded after unentered expiry")
	}
	if calls.Load() != 1 || tr.Lifetime().Err() != nil {
		t.Fatal("surviving passive write failed")
	}
}

// No ProbeExchange supervisor/Dial continuation is running: Engine cancellation
// must fence output directly, even before either can propagate session abort.
func TestPersistentEngineCancellationDirectlyFencesSelectedWrite(t *testing.T) {
	registry, err := guestnet.NewTCPProbeSessions(context.Background(), persistentConfig(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { registry.Revoke(); _ = registry.Wait(context.Background()) }()
	session, err := registry.Open(context.Background(), 80)
	if err != nil {
		t.Fatal(err)
	}
	c := config()
	c.SourcePort = session.Route().LocalPort
	engine, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	local, remote := memoryStreams()
	defer local.Close()
	n := &persistentNIC{registry: registry, lifetime: registry.Lifetime()}
	p := &ProbeExchange{nic: n, session: session, engine: engine}
	n.base = &Transport{ctx: context.Background(), persistent: n, encoder: guestnet.NewEncoder(local)}
	n.base.owner = commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		engine.Close()
		if session.Lifetime().Err() != nil || n.base.ctx.Err() != nil {
			t.Error("test did not isolate pre-propagation Engine cancellation")
		}
		return apply()
	})
	err = session.Transmit(session.Lifetime(), sourcePortFrame(c, false, c.SourcePort, 80), p.writeFrame)
	if !errors.Is(err, context.Canceled) || len(remote.in) != 0 {
		t.Fatal("closed Engine emitted selected frame", err)
	}
}
