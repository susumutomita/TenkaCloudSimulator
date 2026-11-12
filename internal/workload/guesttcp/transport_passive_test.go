package guesttcp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// Generic HTTP fixture with actual bounded userspace TCP and joined HTTP work.
// It is not IMDS, a provider binding or physical-owner qualification.
type passiveHTTPFixture struct {
	*Listener
	server      *http.Server
	served      chan struct{}
	connections sync.WaitGroup
	once        sync.Once
	err         error
}

func newPassiveHTTPFixture(t *testing.T, ctx context.Context) *passiveHTTPFixture {
	return passiveHTTPFixtureUntil(t, ctx, time.Time{})
}

func passiveHTTPFixtureUntil(t *testing.T, ctx context.Context, deadline time.Time) *passiveHTTPFixture {
	t.Helper()
	var listener *Listener
	var err error
	if deadline.IsZero() {
		listener, err = NewListener(ctx, listenConfig())
	} else {
		listener, err = NewListenerUntil(ctx, listenConfig(), deadline)
	}
	if err != nil {
		t.Fatal(err)
	}
	s := &passiveHTTPFixture{Listener: listener, served: make(chan struct{})}
	s.server = &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
				http.NotFound(w, r)
				return
			}
			_, _ = io.WriteString(w, "passive exchange without a probe")
		}),
		ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, MaxHeaderBytes: 1024,
		ConnState: func(_ net.Conn, state http.ConnState) {
			switch state {
			case http.StateNew:
				s.connections.Add(1)
			case http.StateClosed, http.StateHijacked:
				s.connections.Done()
			}
		},
	}
	s.server.SetKeepAlivesEnabled(false)
	go func() { defer close(s.served); _ = s.server.Serve(listener) }()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func (s *passiveHTTPFixture) Close() error {
	s.once.Do(func() { _ = s.Listener.Close(); s.err = s.server.Close(); <-s.served; s.connections.Wait() })
	return s.err
}

func passiveWorkerCount() int {
	workerBudget.Lock()
	defer workerBudget.Unlock()
	return workerBudget.used
}

func TestPassiveTransportGatewayARPAndHTTPWithoutProbe(t *testing.T) {
	testPassiveTransportGatewayARPAndHTTP(t, false)
}

func TestPassiveTransportBootWindowGatewayARPAndHTTPAfterExchangeExpiry(t *testing.T) {
	testPassiveTransportGatewayARPAndHTTP(t, true)
}

func testPassiveTransportGatewayARPAndHTTP(t *testing.T, bootWindow bool) {
	t.Helper()
	started := time.Now()
	deadline := time.Time{}
	lifetime := 4 * time.Second
	if bootWindow {
		lifetime += MaxLifetime
		deadline = started.Add(lifetime)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lifetime)
	defer cancel()
	service := passiveHTTPFixtureUntil(t, ctx, deadline)
	route := transportServiceRoute()
	ac := arpBinding(config())
	local, remote := memoryStreams()
	var arpCounts, tcpCounts [2]atomic.Int32
	// Memory-only owner: this is no AWS lease or revocation implementation.
	owner := commitFunc(func(_ context.Context, d FrameDirection, frame []byte, apply func() error) error {
		i := 0
		if d == ToGuest {
			i = 1
		}
		if binary.BigEndian.Uint16(frame[12:14]) == 0x0806 {
			arpCounts[i].Add(1)
		} else {
			tcpCounts[i].Add(1)
		}
		clear(frame)
		return apply()
	})
	stacks, workers := len(slots), passiveWorkerCount()
	var tr *Transport
	var err error
	if bootWindow {
		tr, err = NewPassiveTransportUntil(ctx, local, owner, &ac, service, route, deadline)
	} else {
		tr, err = NewPassiveTransport(ctx, local, owner, &ac, service, route)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if tr.engine != nil || len(slots) != stacks || passiveWorkerCount() != workers || cap(tr.results) != 2 || tr.serviceRoute != 0 {
		t.Fatal("passive transport allocated active endpoint/workers or wrong pumps")
	}
	if conn, err := tr.Dial(ctx); conn != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("passive mode admitted Dial", err)
	}
	if observer, err := NewHTTPObserver(tr, "/healthz"); observer != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("passive mode admitted observation", err)
	}
	if tr.ctx.Err() != nil {
		t.Fatal("rejected active API revoked passive service")
	}

	// One independent guest stack, one NIC, no probe server, and NO static
	// neighbor. The sole /32 route resolves its gateway with actual ARP.
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{arp.NewProtocol, ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	defer s.Destroy()
	link := channel.New(16, guestnet.MaxFrameBytes, tcpip.LinkAddress(string(route.PeerMAC[:])))
	defer link.Close()
	if err := s.CreateNIC(1, ethernet.New(link)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4(route.PeerIP.As4()), PrefixLen: 24}}, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	subnet, err := tcpip.NewSubnet(tcpip.AddrFrom4(route.LocalIP.As4()), tcpip.MaskFromBytes([]byte{255, 255, 255, 255}))
	if err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: subnet, Gateway: tcpip.AddrFrom4(ac.GatewayIP.As4()), NIC: 1}})
	var pumps sync.WaitGroup
	pumps.Go(func() {
		decoder := guestnet.NewDecoder(remote)
		for {
			frame, err := decoder.ReadFrame()
			if err != nil {
				return
			}
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
			link.InjectInbound(0, pkt)
			pkt.DecRef()
		}
	})
	pumps.Go(func() {
		encoder := guestnet.NewEncoder(remote)
		for {
			pkt := link.ReadContext(ctx)
			if pkt == nil {
				return
			}
			view := pkt.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			pkt.DecRef()
			if encoder.WriteFrame(frame) != nil {
				return
			}
		}
	})
	defer func() { cancel(); _ = tr.Close(); pumps.Wait() }()
	if bootWindow {
		// Actual elapsed time proves that neither the listener nor shared
		// framed stream silently retains its old ten-second timeout. This is
		// memory TCP/ARP evidence, not guest boot or physical enforcement.
		select {
		case <-time.After(time.Until(started.Add(MaxLifetime + 50*time.Millisecond))):
		case <-ctx.Done():
			t.Fatal("boot window expired before delayed requests", ctx.Err())
		}
	}
	for _, port := range []uint16{41000, 41001} {
		conn, err := gonet.DialTCPWithBind(ctx, s, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(route.PeerIP.As4()), Port: port}, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(route.LocalIP.As4()), Port: route.LocalPort}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatal(err)
		}
		err = memoryRouteHTTPExchange(ctx, conn, "GET /healthz HTTP/1.1\r\nHost: passive.invalid\r\nConnection: close\r\n\r\n", "passive exchange without a probe")
		_ = conn.SetDeadline(time.Time{})
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if arpCounts[0].Load() == 0 || arpCounts[0].Load() != arpCounts[1].Load() || tcpCounts[0].Load() < 6 || tcpCounts[1].Load() < 6 {
		t.Fatal("passive gateway and both HTTP connections did not cross both fences")
	}
	if tr.engine != nil || tr.ctx.Err() != nil {
		t.Fatal("passive HTTP exchange needed an active endpoint or terminated early")
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.served:
	default:
		t.Fatal("transport cleanup did not join HTTP service")
	}
}

func TestPassiveTransportNeedsNoActiveStackCapacity(t *testing.T) {
	var engines []*Engine
	t.Cleanup(func() {
		for _, engine := range engines {
			engine.Close()
		}
	})
	// The first exhausted resource depends on GOMAXPROCS: it can be the
	// eight stack slots or the 32-worker allowance. Do not mutate either.
	for {
		engine, err := New(config())
		if errors.Is(err, ErrCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		engines = append(engines, engine)
	}
	if engine, err := New(config()); engine != nil || !errors.Is(err, ErrCapacity) {
		if engine != nil {
			engine.Close()
		}
		t.Fatal("active stack admission was not exhausted", err)
	}
	stacks, workers := len(slots), passiveWorkerCount()
	local, _ := memoryStreams()
	service := newTransportServiceFixture()
	tr, err := NewPassiveTransport(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal("passive mode requires an extra Engine slot", err)
	}
	defer tr.Close()
	if len(slots) != stacks || passiveWorkerCount() != workers || tr.engine != nil {
		t.Fatal("passive mode changed active stack/worker accounting")
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if len(slots) != stacks || passiveWorkerCount() != workers {
		t.Fatal("passive close released someone else's stack/worker reservation")
	}
}

func TestPassiveTransportConstructorFailuresPreserveOwnership(t *testing.T) {
	for _, name := range []string{"nil context", "nil stream", "nil owner", "expired timer", "canceled parent", "nil service", "active route", "malformed route", "wrong ARP peer", "wrong ARP MAC"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			route := transportServiceRoute()
			ac := arpBinding(config())
			fixture := newTransportServiceFixture()
			var service PassiveService = fixture
			want := ErrConfig
			switch name {
			case "nil context":
				ctx = nil
			case "expired timer":
				ctx = elapsedDeadlineContext{context.Background()}
				want = context.DeadlineExceeded
			case "canceled parent":
				ctx = canceledTransportContext()
				want = context.Canceled
			case "nil service":
				var typedNil *transportServiceFixture
				service = typedNil
			case "active route":
				route.AnyPeerPort = false
				route.PeerPort = 41000
			case "malformed route":
				route.LocalPort = 0
				want = guestnet.ErrTCPRouteConfig
			case "wrong ARP peer":
				ac.GuestMAC[5]++
			case "wrong ARP MAC":
				ac.GatewayMAC[5] += 2
			}
			stream := newBlockedStream(false)
			var suppliedStream io.ReadWriteCloser = stream
			var owner FrameCommitter = memoryCommitter
			if name == "nil stream" {
				suppliedStream = nil
			}
			if name == "nil owner" {
				owner = nil
			}
			stacks, workers, debt := len(slots), passiveWorkerCount(), len(transportSlots)
			tr, err := NewPassiveTransport(ctx, suppliedStream, owner, &ac, service, route)
			if tr != nil || !errors.Is(err, want) {
				t.Fatalf("invalid passive constructor: %v, want %v", err, want)
			}
			if stream.closes.Load() != 0 || fixture.closes.Load() != 0 || len(slots) != stacks || passiveWorkerCount() != workers || len(transportSlots) != debt {
				t.Fatal("failed constructor consumed dependency ownership or capacity")
			}
		})
	}
}

func TestPassiveTransportRejectsProbeBeforeOwnerOrInjection(t *testing.T) {
	for _, direction := range []FrameDirection{FromGuest, ToGuest} {
		service := newTransportServiceFixture()
		local, remote := memoryStreams()
		var calls atomic.Int32
		owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
			calls.Add(1)
			return apply()
		})
		tr, err := NewPassiveTransport(context.Background(), local, owner, nil, service, transportServiceRoute())
		if err != nil {
			t.Fatal(err)
		}
		if direction == FromGuest {
			if err := guestnet.NewEncoder(remote).WriteFrame(reverseTCPFrame(config())); err != nil {
				t.Fatal(err)
			}
		} else {
			c := config()
			service.output <- listenFrame(ListenConfig{LocalIP: c.PeerIP, PeerIP: c.LocalIP, LocalMAC: c.PeerMAC, PeerMAC: c.LocalMAC, LocalPort: c.Port}, LocalPort, 123, 2)
		}
		awaitSignal(t, tr.done, "probe rejected in passive mode")
		if !errors.Is(tr.Wait(), guestnet.ErrTCPRoute) || calls.Load() != 0 || len(service.injected) != 0 || len(remote.in) != 0 || tr.packets != 1 {
			t.Fatal("passive mode admitted probe route or bypassed rejected-frame accounting")
		}
	}
}

func TestPassiveTransportOwnerDenialAndAggregateBudget(t *testing.T) {
	for _, kind := range []string{"inbound denial", "outbound denial", "TCP packet budget", "TCP byte budget", "ARP reply budget"} {
		t.Run(kind, func(t *testing.T) {
			service := newTransportServiceFixture()
			local, remote := memoryStreams()
			ac := arpBinding(config())
			denied := errors.New("memory owner denied passive frame")
			var owner FrameCommitter = memoryCommitter
			want := ErrCapacity
			if kind == "inbound denial" || kind == "outbound denial" {
				want = denied
				owner = commitFunc(func(context.Context, FrameDirection, []byte, func() error) error { return denied })
			}
			tr, err := NewPassiveTransport(context.Background(), local, owner, &ac, service, transportServiceRoute())
			if err != nil {
				t.Fatal(err)
			}
			tr.budgetMu.Lock()
			switch kind {
			case "TCP packet budget":
				tr.packets = maxFrames
			case "TCP byte budget":
				tr.bytes = maxBytes - len(transportServiceOutput()) + 1
			case "ARP reply budget":
				tr.packets = maxFrames - 1
			}
			tr.budgetMu.Unlock()
			switch kind {
			case "inbound denial":
				err = guestnet.NewEncoder(remote).WriteFrame(listenFrame(listenConfig(), 41000, 123, 2))
			case "ARP reply budget":
				err = guestnet.NewEncoder(remote).WriteFrame(requestARP(ac))
			default:
				service.output <- transportServiceOutput()
			}
			if err != nil {
				t.Fatal(err)
			}
			awaitSignal(t, tr.done, "passive denial/budget")
			if !errors.Is(tr.Wait(), want) || len(service.injected) != 0 || len(remote.in) != 0 {
				t.Fatal("passive frame bypassed owner or shared budget")
			}
		})
	}
}

type passiveDeadlineParent struct {
	context.Context
	deadline time.Time
}

func (c passiveDeadlineParent) Deadline() (time.Time, bool) { return c.deadline, true }

func TestPassiveTransportLifetimeIsAbsoluteAndNotRenewed(t *testing.T) {
	service := newTransportServiceFixture()
	local, _ := memoryStreams()
	before := time.Now()
	tr, err := NewPassiveTransport(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := tr.ctx.Deadline()
	if deadline.Before(before.Add(MaxLifetime)) || deadline.After(time.Now().Add(MaxLifetime)) {
		t.Fatal("passive constructor did not retain original ten-second bound")
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}

	// This parent's timer notification deliberately never arrives. The earlier
	// absolute deadline must still create an independent transport timer.
	expires := time.Now().Add(20 * time.Millisecond)
	parent := passiveDeadlineParent{Context: context.Background(), deadline: expires}
	service = newTransportServiceFixture()
	local, _ = memoryStreams()
	tr, err = NewPassiveTransport(parent, local, memoryCommitter, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	if deadline, _ := tr.ctx.Deadline(); deadline != expires || tr.lifetime != parent {
		t.Fatal("passive transport changed original parent lifetime")
	}
	awaitSignal(t, tr.done, "independent absolute parent expiry")
	if !errors.Is(tr.Wait(), context.DeadlineExceeded) || service.closes.Load() != 1 {
		t.Fatal("passive lifetime depended on delayed parent notification")
	}

	serviceCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := NewListener(serviceCtx, listenConfig())
	if err != nil {
		t.Fatal(err)
	}
	local, _ = memoryStreams()
	tr, err = NewPassiveTransport(context.Background(), local, memoryCommitter, nil, listener, transportServiceRoute())
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	cancel()
	awaitSignal(t, tr.done, "earlier supplied service expiry")
	if tr.Wait() == nil {
		t.Fatal("transport renewed supplied service lifetime")
	}
}

func TestPassiveTransportCloseDebtAndCapacity(t *testing.T) {
	if len(transportSlots) != 0 {
		t.Fatal("unexpected existing debt")
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
		tr, err := NewPassiveTransport(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute())
		if err != nil {
			t.Fatal(err)
		}
		transports = append(transports, tr)
		requirePendingDrain(t, tr.CloseContext(canceledTransportContext()), context.Canceled)
		awaitSignal(t, service.closeEntered, "passive blocked cleanup")
		awaitSignal(t, local.closed, "stream closes independently")
	}
	service := newTransportServiceFixture()
	stream := newBlockedStream(false)
	tr, err := NewPassiveTransport(context.Background(), stream, memoryCommitter, nil, service, transportServiceRoute())
	if tr != nil || !errors.Is(err, ErrCapacity) || service.closes.Load() != 0 || stream.closes.Load() != 0 {
		t.Fatal("passive cleanup debt bypass or failed ownership transfer", err)
	}
	once.Do(func() { close(release) })
	for _, tr := range transports {
		joinTransport(t, tr)
	}
	if len(transportSlots) != 0 {
		t.Fatal("passive successful cleanup did not release debt")
	}

	failure := errors.New("passive service Close unconfirmed")
	service = newTransportServiceFixture()
	service.closeError = failure
	local, _ := memoryStreams()
	tr, err = NewPassiveTransport(context.Background(), local, memoryCommitter, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := tr.Close(); !errors.Is(err, ErrDrainUncertain) || !errors.Is(err, failure) {
			t.Fatal("passive uncertain close lost debt", err)
		}
	}
	if len(transportSlots) != 1 || service.closes.Load() != 1 {
		t.Fatal("passive uncertain close retried or forgiven")
	}
	// Memory fixture has no resource. There is no production debt waiver.
	t.Cleanup(func() { <-transportSlots })
}

func TestPassiveTransportActiveAPIsLeaveServiceAlive(t *testing.T) {
	service := newTransportServiceFixture()
	local, remote := memoryStreams()
	// Even an owner implementing observation publication cannot enable an
	// absent Engine or convert this existing service into a probe transport.
	owner := &httpMemoryOwner{}
	tr, err := NewPassiveTransport(context.Background(), local, owner, nil, service, transportServiceRoute())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	for _, ctx := range []context.Context{nil, context.Background(), canceledTransportContext()} {
		if conn, err := tr.Dial(ctx); conn != nil || !errors.Is(err, ErrConfig) {
			t.Fatal("active Dial accepted in passive mode", err)
		}
	}
	if observer, err := NewHTTPObserver(tr, "/healthz"); observer != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("observation accepted in passive mode", err)
	}
	if tr.ctx.Err() != nil || service.closes.Load() != 0 || owner.count() != 0 {
		t.Fatal("rejected active API consumed live passive exchange")
	}
	if err := guestnet.NewEncoder(remote).WriteFrame(listenFrame(listenConfig(), 41000, 123, 2)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.injected:
	case <-time.After(time.Second):
		t.Fatal("passive service no longer admits frames after rejected active APIs")
	}
}
