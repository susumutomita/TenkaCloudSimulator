package guesttcp

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// Controllable, memory-only dependency. Blocking variants deliberately violate
// the service contract to check retained ownership, not physical revocation.
type transportServiceFixture struct {
	output, injected                         chan []byte
	closed, closeEntered                     chan struct{}
	closeRelease, readRelease, injectRelease <-chan struct{}
	readEntered, injectEntered               chan struct{}
	closeError                               error
	closes                                   atomic.Int32
}

func newTransportServiceFixture() *transportServiceFixture {
	return &transportServiceFixture{output: make(chan []byte, 16), injected: make(chan []byte, 16), closed: make(chan struct{}), closeEntered: make(chan struct{})}
}

func (s *transportServiceFixture) Inject(frame []byte) error {
	if s.injectEntered != nil {
		close(s.injectEntered)
	}
	if s.injectRelease != nil {
		<-s.injectRelease
	}
	s.injected <- append([]byte(nil), frame...)
	return nil
}

func (s *transportServiceFixture) ReadFrame(ctx context.Context) ([]byte, error) {
	if s.readEntered != nil {
		close(s.readEntered)
	}
	if s.readRelease != nil {
		<-s.readRelease
		return nil, ErrClosed
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, ErrClosed
	case frame := <-s.output:
		return frame, nil
	}
}

func (s *transportServiceFixture) Close() error {
	s.closes.Add(1)
	close(s.closeEntered)
	close(s.closed)
	if s.closeRelease != nil {
		<-s.closeRelease
	}
	return s.closeError
}

func transportServiceRoute() guestnet.TCPRoute {
	c := listenConfig()
	return guestnet.TCPRoute{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, LocalPort: c.LocalPort, AnyPeerPort: true}
}

func transportServiceOutput() []byte {
	c := listenConfig()
	return listenFrame(ListenConfig{LocalIP: c.PeerIP, PeerIP: c.LocalIP, LocalMAC: c.PeerMAC, PeerMAC: c.LocalMAC, LocalPort: 41000}, c.LocalPort, 123, 2)
}

func TestTransportServiceConstructorFailureLeavesOwnership(t *testing.T) {
	valid := transportServiceRoute()
	for _, test := range []struct {
		name                   string
		mutate                 func(*guestnet.TCPRoute, *Config)
		nilService, nilContext bool
	}{
		{"nil service", nil, true, false},
		{"nil context", nil, false, true},
		{"guest IP", func(r *guestnet.TCPRoute, _ *Config) { r.PeerIP = netip.MustParseAddr("10.0.1.5") }, false, false},
		{"guest MAC", func(r *guestnet.TCPRoute, _ *Config) { r.PeerMAC[5]++ }, false, false},
		{"gateway MAC", func(r *guestnet.TCPRoute, _ *Config) { r.LocalMAC[5] += 2 }, false, false},
		{"fixed peer port", func(r *guestnet.TCPRoute, _ *Config) { r.AnyPeerPort = false; r.PeerPort = 41000 }, false, false},
		{"nonzero wildcard port", func(r *guestnet.TCPRoute, _ *Config) { r.PeerPort = 41000 }, false, false},
		{"zero local port", func(r *guestnet.TCPRoute, _ *Config) { r.LocalPort = 0 }, false, false},
		{"overlap", func(r *guestnet.TCPRoute, c *Config) { r.LocalIP = c.LocalIP; r.LocalPort = LocalPort }, false, false},
		{"invalid engine", func(_ *guestnet.TCPRoute, c *Config) { c.Port = 0 }, false, false},
		{"engine rejects link-local source", func(_ *guestnet.TCPRoute, c *Config) { c.LocalIP = netip.MustParseAddr("169.254.169.1") }, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			route, c := valid, config()
			if test.mutate != nil {
				test.mutate(&route, &c)
			}
			fixture := newTransportServiceFixture()
			var service PassiveService = fixture
			if test.nilService {
				var typedNil *transportServiceFixture
				service = typedNil
			}
			ctx := context.Background()
			if test.nilContext {
				ctx = nil
			}
			stream := newBlockedStream(false)
			tr, err := NewTransportWithService(ctx, c, stream, memoryCommitter, nil, service, route)
			if tr != nil || err == nil {
				t.Fatalf("invalid constructor succeeded: %v", err)
			}
			if stream.closes.Load() != 0 || fixture.closes.Load() != 0 {
				t.Fatal("constructor failure consumed caller-owned resource")
			}
		})
	}
}

func TestTransportServiceIngressSelectsBeforeInjection(t *testing.T) {
	service := newTransportServiceFixture()
	local, remote := memoryStreams()
	route := transportServiceRoute()
	tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, nil, service, route)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	// Mutating caller configuration cannot retarget the constructed classifier.
	route.LocalPort++
	frame := listenFrame(listenConfig(), 41000, 123, 2)
	if err := guestnet.NewEncoder(remote).WriteFrame(frame); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-service.injected:
		if len(got) != len(frame) {
			t.Fatal("service did not receive selected frame")
		}
	case <-time.After(time.Second):
		t.Fatal("service injection blocked")
	}
	if tr.engine.packets != 0 {
		t.Fatal("service frame was trial-injected into probe")
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if tr.packets != 1 || service.closes.Load() != 1 {
		t.Fatal("service frame or cleanup ownership lost")
	}
}

func TestTransportServiceInvalidIngressIsAccountedBeforeSelection(t *testing.T) {
	valid := listenFrame(listenConfig(), 41000, 123, 2)
	malformed := append([]byte(nil), valid...)
	malformed[50] ^= 1
	spoofedMAC := append([]byte(nil), valid...)
	spoofedMAC[11] ^= 1
	spoofed := listenConfig()
	spoofed.PeerIP = netip.MustParseAddr("10.0.1.5")
	wrongPort := listenConfig()
	wrongPort.LocalPort++
	for _, frame := range [][]byte{valid[:30], malformed, spoofedMAC, listenFrame(spoofed, 41000, 123, 2), listenFrame(wrongPort, 41000, 123, 2), transportServiceOutput()} {
		service := newTransportServiceFixture()
		local, remote := memoryStreams()
		tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, nil, service, transportServiceRoute())
		if err != nil {
			t.Fatal(err)
		}
		if err := guestnet.NewEncoder(remote).WriteFrame(frame); err != nil {
			t.Fatal(err)
		}
		awaitSignal(t, tr.done, "invalid shared ingress")
		if tr.Wait() == nil || tr.packets != 1 || tr.engine.packets != 0 || len(service.injected) != 0 || len(remote.in) != 0 {
			t.Fatal("invalid ingress reached an endpoint or bypassed accounting")
		}
	}
}

func TestTransportServiceOwnerRejectsBothDirections(t *testing.T) {
	for _, direction := range []FrameDirection{FromGuest, ToGuest} {
		service := newTransportServiceFixture()
		local, remote := memoryStreams()
		denied := errors.New("memory owner denies service frame")
		owner := commitFunc(func(_ context.Context, d FrameDirection, _ []byte, _ func() error) error {
			if d != direction {
				t.Error("unexpected direction before denied service frame")
			}
			return denied
		})
		tr, err := NewTransportWithService(context.Background(), config(), local, owner, nil, service, transportServiceRoute())
		if err != nil {
			t.Fatal(err)
		}
		if direction == FromGuest {
			if err := guestnet.NewEncoder(remote).WriteFrame(listenFrame(listenConfig(), 41000, 123, 2)); err != nil {
				t.Fatal(err)
			}
		} else {
			service.output <- transportServiceOutput()
		}
		awaitSignal(t, tr.done, "denied service frame")
		if !errors.Is(tr.Wait(), denied) || len(service.injected) != 0 || len(remote.in) != 0 || tr.engine.packets != 0 {
			t.Fatal("denied service traffic crossed boundary")
		}
	}
}

func TestTransportServiceRejectsOtherProducerOutput(t *testing.T) {
	service := newTransportServiceFixture()
	local, remote := memoryStreams()
	var ownerCalls atomic.Int32
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		ownerCalls.Add(1)
		return apply()
	})
	tr, err := NewTransportWithService(context.Background(), config(), local, owner, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	// The probe producer may not emit the service's otherwise-valid tuple.
	if err := tr.deliverTCP(0, transportServiceOutput()); !errors.Is(err, guestnet.ErrTCPRoute) {
		t.Fatal("probe emitted service tuple", err)
	}
	// Nor may the service emit the probe's otherwise-valid tuple.
	c := config()
	service.output <- listenFrame(ListenConfig{LocalIP: c.PeerIP, PeerIP: c.LocalIP, LocalMAC: c.PeerMAC, PeerMAC: c.LocalMAC, LocalPort: c.Port}, LocalPort, 123, 2)
	awaitSignal(t, tr.done, "wrong producer teardown")
	if !errors.Is(tr.Wait(), guestnet.ErrTCPRoute) || len(remote.in) != 0 || tr.packets != 2 || ownerCalls.Load() != 0 {
		t.Fatal("wrong-producer output bypassed route or accounting")
	}
}

func TestTransportServiceSharesAggregateBudgets(t *testing.T) {
	for _, kind := range []string{"input packets", "output packets", "output bytes", "rejected output"} {
		t.Run(kind, func(t *testing.T) {
			service := newTransportServiceFixture()
			local, remote := memoryStreams()
			ac := arpBinding(config())
			tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, &ac, service, transportServiceRoute())
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			// Earlier probe/ARP frames consume the same allowance, never a
			// separate metadata allowance. Test packet and byte exhaustion.
			tr.budgetMu.Lock()
			if kind == "output bytes" {
				tr.bytes = maxBytes - len(transportServiceOutput()) + 1
			} else {
				tr.packets = maxFrames
			}
			tr.budgetMu.Unlock()
			if kind == "input packets" {
				if err := guestnet.NewEncoder(remote).WriteFrame(listenFrame(listenConfig(), 41000, 123, 2)); err != nil {
					t.Fatal(err)
				}
			} else {
				frame := transportServiceOutput()
				if kind == "rejected output" {
					frame[50] ^= 1
				}
				service.output <- frame
			}
			awaitSignal(t, tr.done, "shared service budget")
			if !errors.Is(tr.Wait(), ErrCapacity) || len(remote.in) != 0 || len(service.injected) != 0 {
				t.Fatal("service bypassed shared frame/byte budget")
			}
		})
	}
}

func TestTransportServiceCancellationRejectsLateOwnerCallback(t *testing.T) {
	service := newTransportServiceFixture()
	local, remote := memoryStreams()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		close(entered)
		<-release
		return apply()
	})
	tr, err := NewTransportWithService(context.Background(), config(), local, owner, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { once.Do(func() { close(release) }); joinTransport(t, tr) }()
	if err := guestnet.NewEncoder(remote).WriteFrame(listenFrame(listenConfig(), 41000, 123, 2)); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, entered, "service owner callback")
	requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
	awaitSignal(t, service.closeEntered, "service closure independent of callback")
	once.Do(func() { close(release) })
	joinTransport(t, tr)
	if len(service.injected) != 0 {
		t.Fatal("late callback injected after cancellation")
	}
}

func TestTransportServiceUnresolvedCallsRetainCapacity(t *testing.T) {
	for _, kind := range []string{"Close", "ReadFrame", "Inject"} {
		t.Run(kind, func(t *testing.T) {
			baseline := len(transportSlots)
			service := newTransportServiceFixture()
			release, entered := make(chan struct{}), make(chan struct{})
			switch kind {
			case "Close":
				service.closeRelease = release
				entered = service.closeEntered
			case "ReadFrame":
				service.readRelease = release
				service.readEntered = entered
			case "Inject":
				service.injectRelease = release
				service.injectEntered = entered
			}
			local, remote := memoryStreams()
			tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, nil, service, transportServiceRoute())
			if err != nil {
				t.Fatal(err)
			}
			var once sync.Once
			defer func() { once.Do(func() { close(release) }); joinTransport(t, tr) }()
			if kind == "Inject" {
				if err := guestnet.NewEncoder(remote).WriteFrame(listenFrame(listenConfig(), 41000, 123, 2)); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "Close" {
				awaitSignal(t, entered, "blocked service call")
			}
			requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
			awaitSignal(t, entered, "blocked service cleanup")
			awaitSignal(t, local.closed, "independent stream cleanup")
			tr.engine.Close()
			if len(transportSlots) != baseline+1 {
				t.Fatal("unresolved service lost transport debt")
			}
			once.Do(func() { close(release) })
			joinTransport(t, tr)
			if len(transportSlots) != baseline || service.closes.Load() != 1 {
				t.Fatal("service drain failed to release only once")
			}
		})
	}
}

func TestTransportServiceCloseErrorRetainsPermanentDebt(t *testing.T) {
	baseline := len(transportSlots)
	service := newTransportServiceFixture()
	failure := errors.New("test service cleanup uncertain")
	service.closeError = failure
	local, _ := memoryStreams()
	tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := tr.Close(); !errors.Is(err, ErrDrainUncertain) || !errors.Is(err, failure) {
			t.Fatal("uncertain service closure lost", err)
		}
	}
	if len(transportSlots) != baseline+1 || service.closes.Load() != 1 {
		t.Fatal("uncertain cleanup was retried or forgiven")
	}
	// This fixture has no external resources. Production has no debt waiver.
	t.Cleanup(func() { <-transportSlots })
}

func TestTransportServiceReadFailureClosesJointExchange(t *testing.T) {
	service := newTransportServiceFixture()
	local, _ := memoryStreams()
	tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	service.output <- nil // malformed service result is terminal, not an empty poll.
	awaitSignal(t, tr.done, "service output failure")
	if tr.Wait() == nil || service.closes.Load() != 1 {
		t.Fatal("service failure left exchange live")
	}
	awaitSignal(t, local.closed, "joint stream closure")
}

func TestTransportServiceCanceledConstructorLeavesOwnership(t *testing.T) {
	service := newTransportServiceFixture()
	stream := newBlockedStream(false)
	tr, err := NewTransportWithService(canceledTransportContext(), config(), stream, memoryCommitter, nil, service, transportServiceRoute())
	if tr != nil || !errors.Is(err, context.Canceled) || stream.closes.Load() != 0 || service.closes.Load() != 0 {
		t.Fatalf("canceled constructor consumed ownership: %v", err)
	}
}

func TestTransportServiceBlockedCloseSaturatesRetainedCapacity(t *testing.T) {
	if len(transportSlots) != 0 {
		t.Fatal("unexpected existing transport debt")
	}
	release := make(chan struct{})
	var once sync.Once
	var transports []*Transport
	t.Cleanup(func() {
		once.Do(func() { close(release) })
		for _, tr := range transports {
			joinTransport(t, tr)
		}
	})
	for range cap(transportSlots) {
		service := newTransportServiceFixture()
		service.closeRelease = release
		local, _ := memoryStreams()
		tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, nil, service, transportServiceRoute())
		if err != nil {
			t.Fatal(err)
		}
		transports = append(transports, tr)
		requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
		awaitSignal(t, service.closeEntered, "service retained close")
		tr.engine.Close()
	}
	service := newTransportServiceFixture()
	stream := newBlockedStream(false)
	tr, err := NewTransportWithService(context.Background(), config(), stream, memoryCommitter, nil, service, transportServiceRoute())
	if tr != nil || !errors.Is(err, ErrCapacity) || stream.closes.Load() != 0 || service.closes.Load() != 0 {
		t.Fatalf("capacity failure consumed ownership: %v", err)
	}
	if len(transportSlots) != cap(transportSlots) || len(slots) != 0 {
		t.Fatal("service debt incorrectly follows engine capacity")
	}
	once.Do(func() { close(release) })
	for _, tr := range transports {
		joinTransport(t, tr)
	}
	if len(transportSlots) != 0 {
		t.Fatal("joined service debt retained after successful cleanup")
	}
}

func TestTransportServiceStartsBothClosesBeforeJoiningEither(t *testing.T) {
	baseline := len(transportSlots)
	local, _ := memoryStreams()
	stream := &stalledCloseStream{memoryStream: local, entered: make(chan struct{}), release: make(chan struct{})}
	service := newTransportServiceFixture()
	release := make(chan struct{})
	service.closeRelease = release
	tr, err := NewTransportWithService(context.Background(), config(), stream, memoryCommitter, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	var streamOnce, serviceOnce sync.Once
	t.Cleanup(func() {
		streamOnce.Do(func() { close(stream.release) })
		serviceOnce.Do(func() { close(release) })
		joinTransport(t, tr)
	})
	requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
	awaitSignal(t, stream.entered, "blocked stream Close starts")
	awaitSignal(t, service.closeEntered, "blocked service Close starts independently")
	tr.engine.Close()
	serviceOnce.Do(func() { close(release) })
	select {
	case <-tr.done:
		t.Fatal("service completion released blocked stream debt")
	default:
	}
	if len(transportSlots) != baseline+1 {
		t.Fatal("independently closing resources lost shared debt")
	}
	streamOnce.Do(func() { close(stream.release) })
	joinTransport(t, tr)
	if len(transportSlots) != baseline {
		t.Fatal("independent close completion did not release capacity")
	}
}

func TestTransportServiceKeepsOriginalDeadlineAndEarlierExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := NewListener(ctx, listenConfig())
	if err != nil {
		t.Fatal(err)
	}
	local, _ := memoryStreams()
	tr, err := NewTransportWithService(context.Background(), config(), local, memoryCommitter, nil, listener, transportServiceRoute())
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	defer tr.Close()
	engineDeadline, _ := tr.engine.ctx.Deadline()
	transportDeadline, _ := tr.ctx.Deadline()
	listenerDeadline, _ := listener.ctx.Deadline()
	if transportDeadline != listenerDeadline || engineDeadline.Before(transportDeadline) {
		t.Fatal("composition reset an existing lifetime")
	}
	// Service cancellation, independent of the transport's parent, must end the
	// shared exchange rather than keep the probe running against a dead service.
	cancel()
	awaitSignal(t, tr.done, "earlier passive lifetime expiry")
	if tr.Wait() == nil || tr.engine.ctx.Err() == nil {
		t.Fatal("service expiry left probe alive")
	}
	awaitSignal(t, local.closed, "earlier service expiry closes stream")
}

type transportServiceReadResult struct{ err error }

func (s transportServiceReadResult) Inject([]byte) error                       { return nil }
func (s transportServiceReadResult) ReadFrame(context.Context) ([]byte, error) { return nil, s.err }
func (s transportServiceReadResult) Close() error                              { return nil }

func TestTransportServiceCancellationKeepsDistinctFailureCauses(t *testing.T) {
	failure := errors.New("independent passive service failure")
	joined := errors.Join(ErrClosed, failure)
	mixed := errors.Join(context.Canceled, failure)
	for _, test := range []struct {
		name         string
		ctx          context.Context
		result, want error
	}{
		{"clean close while active", context.Background(), ErrClosed, ErrClosed},
		{"clean close after cancellation", canceledTransportContext(), ErrClosed, context.Canceled},
		{"independent failure after cancellation", canceledTransportContext(), failure, failure},
		{"joined close failure after cancellation", canceledTransportContext(), joined, joined},
		{"mixed cancellation failure", canceledTransportContext(), mixed, mixed},
	} {
		t.Run(test.name, func(t *testing.T) {
			tr := &Transport{ctx: test.ctx, service: transportServiceReadResult{err: test.result}}
			if got := tr.sendService(); got != test.want {
				t.Fatalf("service terminal result=%v, want exact cause %v", got, test.want)
			}
		})
	}
}
