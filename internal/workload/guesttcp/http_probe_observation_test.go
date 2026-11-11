package guesttcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// This memory-only owner verifies lifecycle and tuple binding. It does not
// qualify a physical owner, implement provider policy or turn a status into a score.
type probeHTTPMemoryOwner struct {
	mu           sync.Mutex
	revoked      bool
	observations []ProbeHTTPObservation
	probes       map[guestnet.TCPRoute]*ProbeExchange
	before       func(context.Context, ProbeHTTPObservation) error
	frame        func(context.Context, FrameDirection, []byte, func() error) error
}

func (o *probeHTTPMemoryOwner) CommitFrame(ctx context.Context, d FrameDirection, frame []byte, apply func() error) error {
	commit := func() error {
		o.mu.Lock()
		defer o.mu.Unlock()
		if o.revoked {
			return ErrClosed
		}
		if err := observationContextError(ctx); err != nil {
			return err
		}
		return apply()
	}
	if o.frame != nil {
		return o.frame(ctx, d, frame, commit)
	}
	return commit()
}

func (o *probeHTTPMemoryOwner) CommitProbeHTTPObservation(ctx context.Context, v ProbeHTTPObservation) error {
	o.mu.Lock()
	p := o.probes[v.Route]
	o.mu.Unlock()
	if p == nil || p.session.Route() != v.Route {
		return errors.New("observation did not name the actual request's child")
	}
	select {
	case <-p.done:
	default:
		return errors.New("publication preceded child pump and public Dial join")
	}
	p.engine.mu.Lock()
	closed := p.engine.closed && p.engine.conn == nil
	p.engine.mu.Unlock()
	if !closed {
		return errors.New("publication preceded engine cleanup")
	}
	if o.before != nil {
		if err := o.before(ctx, v); err != nil {
			return err
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.revoked {
		return ErrClosed
	}
	if err := observationContextError(ctx); err != nil {
		return err
	}
	o.observations = append(o.observations, v)
	return nil
}

func (o *probeHTTPMemoryOwner) bind(route guestnet.TCPRoute, p *ProbeExchange) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.probes == nil {
		o.probes = make(map[guestnet.TCPRoute]*ProbeExchange)
	}
	o.probes[route] = p
}
func (o *probeHTTPMemoryOwner) revoke() { o.mu.Lock(); o.revoked = true; o.mu.Unlock() }
func (o *probeHTTPMemoryOwner) snapshot() []ProbeHTTPObservation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]ProbeHTTPObservation(nil), o.observations...)
}

type probeHTTPRequest struct {
	request *http.Request
	route   guestnet.TCPRoute
	probe   *ProbeExchange
}

type probeHTTPResult struct {
	value ProbeHTTPObservation
	err   error
}

type probeHTTPFixture struct {
	transport                     *PersistentTransport
	remote                        *memoryStream
	service                       *persistentServiceFixture
	requests                      chan probeHTTPRequest
	cancelLifetime, cancelService context.CancelFunc
	callers                       sync.WaitGroup
}

// Both listeners belong to one independent gVisor stack. All framing, ARP,
// TCP handshakes and HTTP requests use memoryStreams; there are no OS sockets.
// The peer outlives every logical probe and accepts concurrent/repeated ports.
func newProbeHTTPFixture(t *testing.T, owner *probeHTTPMemoryOwner, parent context.Context, nic guestnet.ProbeNIC, reply func(net.Conn, probeHTTPRequest)) *probeHTTPFixture {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	serviceCtx, cancelService := context.WithTimeout(context.Background(), time.Minute)
	service := &persistentServiceFixture{transportServiceFixture: newTransportServiceFixture(), ctx: serviceCtx}
	local, remote := memoryStreams()
	ac := arpBinding(config())
	tr, err := NewPersistentTransportUntil(ctx, nic, local, owner, &ac, service, transportServiceRoute(), time.Now().Add(time.Minute))
	if err != nil {
		cancel()
		cancelService()
		_ = local.Close()
		_ = service.Close()
		t.Fatal(err)
	}
	f := &probeHTTPFixture{transport: tr, remote: remote, service: service, requests: make(chan probeHTTPRequest, 32), cancelLifetime: cancel, cancelService: cancelService}
	wireCtx, cancelWire := context.WithCancel(context.Background())
	var workers, handlers sync.WaitGroup
	var connectionsMu sync.Mutex
	connections := make(map[net.Conn]struct{})
	stopping := false
	var listeners []net.Listener
	var destroyPeer func()
	// Register before constructing the peer so early setup failures also join
	// transport ownership. Blocking tests register gate releases after this.
	t.Cleanup(func() {
		cancel()
		cancelService()
		cancelWire()
		for _, ln := range listeners {
			_ = ln.Close()
		}
		connectionsMu.Lock()
		stopping = true
		for c := range connections {
			_ = c.SetDeadline(time.Time{}) // gonet Close alone retains timers.
			_ = c.Close()
		}
		connectionsMu.Unlock()
		_ = remote.Close()
		if err := tr.Close(); err != nil {
			t.Error("persistent fixture cleanup:", err)
		}
		workers.Wait()
		handlers.Wait()
		f.callers.Wait()
		waitHTTPWorkers(t)
		if destroyPeer != nil {
			destroyPeer()
		}
	})
	peerStack, link, frontend := gatewayPeer(t, config(), ac)
	listeners = append(listeners, frontend)
	destroyPeer = func() { link.Close(); peerStack.Destroy() }
	api, err := gonet.ListenTCP(peerStack, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(nic.PeerIP.As4()), Port: 8080}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	listeners = append(listeners, api)
	workers.Add(2)
	go func() {
		defer workers.Done()
		decoder := guestnet.NewDecoder(remote)
		for {
			frame, err := decoder.ReadFrame()
			if err != nil {
				return
			}
			packet := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
			link.InjectInbound(0, packet)
			packet.DecRef()
		}
	}()
	go func() {
		defer workers.Done()
		encoder := guestnet.NewEncoder(remote)
		for {
			packet := link.ReadContext(wireCtx)
			if packet == nil {
				return
			}
			view := packet.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			packet.DecRef()
			if encoder.WriteFrame(frame) != nil {
				return
			}
		}
	}()
	serve := func(ln net.Listener) {
		defer workers.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			connectionsMu.Lock()
			if stopping {
				_ = conn.Close()
				connectionsMu.Unlock()
				return
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			connections[conn] = struct{}{}
			handlers.Add(1)
			connectionsMu.Unlock()
			go func() {
				defer handlers.Done()
				defer func() {
					connectionsMu.Lock()
					_ = conn.SetDeadline(time.Time{})
					_ = conn.Close()
					delete(connections, conn)
					connectionsMu.Unlock()
				}()
				r, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
					return
				}
				_ = r.Body.Close()
				// Derive the expected tuple from the peer's actual accepted TCP
				// connection, then independently locate that specific child.
				local, e1 := netip.ParseAddrPort(conn.RemoteAddr().String())
				peer, e2 := netip.ParseAddrPort(conn.LocalAddr().String())
				if e1 != nil || e2 != nil {
					t.Error("invalid memory TCP address", e1, e2)
					return
				}
				route := guestnet.TCPRoute{LocalIP: local.Addr(), PeerIP: peer.Addr(), LocalMAC: nic.LocalMAC, PeerMAC: nic.PeerMAC, LocalPort: local.Port(), PeerPort: peer.Port()}
				var child *ProbeExchange
				tr.nic.mu.Lock()
				for p := range tr.nic.probes {
					if p.session.Route() == route {
						child = p
						break
					}
				}
				tr.nic.mu.Unlock()
				if child == nil {
					t.Error("request did not identify a live probe", route)
					return
				}
				owner.bind(route, child)
				request := probeHTTPRequest{r, route, child}
				select {
				case f.requests <- request:
				case <-wireCtx.Done():
					return
				}
				reply(conn, request)
			}()
		}
	}
	workers.Add(len(listeners))
	for _, ln := range listeners {
		go serve(ln)
	}
	return f
}

func (f *probeHTTPFixture) observe(o *ProbeHTTPObserver, ctx context.Context) <-chan probeHTTPResult {
	done := make(chan probeHTTPResult, 1)
	f.callers.Add(1)
	go func() {
		defer f.callers.Done()
		v, err := o.Observe(ctx)
		done <- probeHTTPResult{v, err}
	}()
	return done
}
func newProbeObserver(t *testing.T, f *probeHTTPFixture, port uint16, path string) *ProbeHTTPObserver {
	t.Helper()
	o, err := NewProbeHTTPObserver(f.transport, port, path)
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func probeHTTPAwait(t *testing.T, result <-chan probeHTTPResult) probeHTTPResult {
	t.Helper()
	select {
	case r := <-result:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("observation did not return within the test bound")
		return probeHTTPResult{}
	}
}
func probeHTTPReply(c net.Conn, _ probeHTTPRequest) {
	_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n")
}
func assertProbeHTTPSharedLive(t *testing.T, f *probeHTTPFixture) {
	t.Helper()
	if f.transport.Lifetime().Err() != nil || f.service.closes.Load() != 0 || f.service.lifetimeCalls.Load() != 1 {
		t.Fatal("observation consumed shared NIC/service lifetime")
	}
	select {
	case <-f.remote.closed:
		t.Fatal("observation closed shared stream")
	case <-f.transport.nic.base.done:
		t.Fatal("observation joined the whole NIC instead of its child")
	default:
	}
}
func assertProbeHTTPPending(t *testing.T, o *ProbeHTTPObserver) {
	t.Helper()
	// This parent advertises a real deadline but delays Done/Err forever.
	ctx := passiveDeadlineParent{Context: context.Background(), deadline: time.Now().Add(20 * time.Millisecond)}
	start := time.Now()
	if err := o.WaitContext(ctx); !errors.Is(err, ErrDrainPending) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unsettled observer wait=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("WaitContext depended on delayed parent notification")
	}
}

func TestProbeHTTPObservationRepeatedConcurrentTupleBinding(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://must-not-be-contacted.invalid:1234")
	t.Setenv("ALL_PROXY", "http://must-not-be-contacted.invalid:1234")
	owner := new(probeHTTPMemoryOwner)
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), func(c net.Conn, r probeHTTPRequest) {
		status := 302
		if r.route.PeerPort == 8080 {
			status = 503
		}
		_, _ = fmt.Fprintf(c, "HTTP/1.1 %d observed\r\nLocation: http://10.0.1.4:8080/healthz\r\nSet-Cookie: secret=value\r\nContent-Length: 0\r\n\r\n", status)
	})
	seen := make(map[uint16]bool)
	returned := make(map[guestnet.TCPRoute]ProbeHTTPObservation)
	for round := 0; round < 2; round++ {
		observers := []*ProbeHTTPObserver{newProbeObserver(t, f, 80, "/"), newProbeObserver(t, f, 8080, "/healthz")}
		results := []<-chan probeHTTPResult{f.observe(observers[0], context.Background()), f.observe(observers[1], context.Background())}
		for i, result := range results {
			r := probeHTTPAwait(t, result)
			wantPort, wantStatus, wantPath := uint16(80), 302, "/"
			if i == 1 {
				wantPort, wantStatus, wantPath = 8080, 503, "/healthz"
			}
			if r.err != nil || r.value.StatusCode != wantStatus || r.value.Route.PeerPort != wantPort || r.value.Path != wantPath || r.value.Route.AnyPeerPort || seen[r.value.Route.LocalPort] || r.value.StartedAt.IsZero() || r.value.ReceivedAt.Before(r.value.StartedAt) {
				t.Fatalf("port/status/unique child binding lost: %+v error=%v", r.value, r.err)
			}
			seen[r.value.Route.LocalPort] = true
			returned[r.value.Route] = r.value
			if err := observers[i].WaitContext(context.Background()); err != nil {
				t.Fatal("successful worker did not settle", err)
			}
		}
		assertProbeHTTPSharedLive(t, f)
	}
	observations := owner.snapshot()
	if len(observations) != 4 || len(f.requests) != 4 {
		t.Fatalf("redirect/retry or missing publication: observations=%d requests=%d", len(observations), len(f.requests))
	}
	for _, v := range observations {
		if returned[v.Route] != v {
			t.Fatal("owner publication differed from returned route/path/status/timestamps", v)
		}
	}
	for range 4 {
		r := <-f.requests
		path := "/"
		if r.route.PeerPort == 8080 {
			path = "/healthz"
		}
		request := r.request
		if request.Method != "GET" || request.RequestURI != path || request.Host != "10.0.1.4:"+strconv.Itoa(int(r.route.PeerPort)) || !request.Close || request.ContentLength != 0 || len(request.Header) != 1 || request.Header.Get("Connection") != "close" {
			t.Fatalf("unexpected fixed request: %+v", request)
		}
		found := false
		for _, v := range observations {
			found = found || v.Route == r.route
		}
		if !found {
			t.Fatal("published route differs from actual accepted TCP tuple", r.route)
		}
	}
	waitHTTPWorkers(t)
}

func TestProbeHTTPObservationConfigurationSingleUseAndDefaults(t *testing.T) {
	owner := new(probeHTTPMemoryOwner)
	nic := persistentConfig()
	f := newProbeHTTPFixture(t, owner, context.Background(), nic, probeHTTPReply)
	nic.ServicePorts[0] = 9090 // Caller mutation must not change the copied allowlist.
	for _, path := range []string{"", "http://host/", "//host/", "/healthz?secret=x", "/\r\nAuthorization: bad", "/other"} {
		if o, err := NewProbeHTTPObserver(f.transport, 80, path); o != nil || !errors.Is(err, ErrConfig) {
			t.Fatalf("invalid path %q admitted: %v", path, err)
		}
	}
	for _, port := range []uint16{0, 443, 9090} {
		if o, err := NewProbeHTTPObserver(f.transport, port, "/"); o != nil || !errors.Is(err, ErrConfig) {
			t.Fatalf("unapproved port %d admitted: %v", port, err)
		}
	}
	for _, tr := range []*PersistentTransport{nil, {}} {
		if o, err := NewProbeHTTPObserver(tr, 80, "/"); o != nil || !errors.Is(err, ErrConfig) {
			t.Fatal("absent persistent transport admitted", err)
		}
	}
	if _, err := NewHTTPObserver(f.transport.nic.base, "/"); !errors.Is(err, ErrConfig) {
		t.Fatal("legacy whole-transport observer accepted persistent NIC", err)
	}
	if conn, err := f.transport.nic.base.Dial(context.Background()); conn != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("persistent NIC gained a default Dial", err)
	}
	var absent *ProbeHTTPObserver
	if v, err := absent.Observe(context.Background()); v != (ProbeHTTPObservation{}) || !errors.Is(err, ErrConfig) {
		t.Fatal("nil observer admitted", err)
	}
	if err := absent.WaitContext(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal("nil observer wait admitted", err)
	}
	o := newProbeObserver(t, f, 80, "/")
	if err := o.WaitContext(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal("unstarted observer wait admitted", err)
	}
	var typedNil *probeObservationContext
	for _, ctx := range []context.Context{nil, typedNil, canceledTransportContext(), elapsedDeadlineContext{context.Background()}} {
		v, err := o.Observe(ctx)
		if err == nil || v != (ProbeHTTPObservation{}) {
			t.Fatal("invalid/canceled/elapsed caller admitted", v, err)
		}
	}
	if len(f.requests) != 0 || len(owner.snapshot()) != 0 {
		t.Fatal("constructor or rejected call performed HTTP work")
	}
	if v, err := o.Observe(context.Background()); err != nil || v.StatusCode != 200 {
		t.Fatal("pre-admission rejection consumed observer", v, err)
	}
	if v, err := o.Observe(context.Background()); v != (ProbeHTTPObservation{}) || !errors.Is(err, ErrCapacity) {
		t.Fatal("single-use observation replayed", v, err)
	}
	if err := o.WaitContext(nil); !errors.Is(err, ErrConfig) {
		t.Fatal("nil wait context admitted", err)
	}
	assertProbeHTTPSharedLive(t, f)
	withoutFence, _, _ := persistentFixture(t, memoryCommitter)
	if o, err := NewProbeHTTPObserver(withoutFence, 80, "/"); o != nil || !errors.Is(err, ErrConfig) {
		t.Fatal("different/missing final owner fence admitted", err)
	}
	if err := f.transport.Close(); err != nil {
		t.Fatal(err)
	}
	if o, err := NewProbeHTTPObserver(f.transport, 80, "/"); o != nil || err == nil {
		t.Fatal("closed persistent NIC admitted observer")
	}
}

func TestProbeHTTPObservationSharedHeaderBounds(t *testing.T) {
	for _, response := range []string{
		"HTTP/1.1 200 OK\n\n",
		"HTTP/1.1 200 OK\r\nX: a\r\n folded\r\n\r\n",
		"HTTP/1.1 200 OK\r\nContent-Length: 1\r\nContent-Length: 2\r\n\r\n",
		"HTTP/1.1 200 OK\r\nX: " + strings.Repeat("x", maxHTTPHeaderBytes) + "\r\n\r\n",
		strings.Repeat("HTTP/1.1 103 Hint\r\n\r\n", maxHTTPInformational+1) + "HTTP/1.1 200 OK\r\n\r\n",
	} {
		t.Run(strconv.Itoa(len(response)), func(t *testing.T) {
			owner := new(probeHTTPMemoryOwner)
			f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), func(c net.Conn, _ probeHTTPRequest) { _, _ = io.WriteString(c, response) })
			o := newProbeObserver(t, f, 80, "/")
			v, err := o.Observe(context.Background())
			if err == nil || v != (ProbeHTTPObservation{}) || len(owner.snapshot()) != 0 {
				t.Fatal("malformed header published a status", v, err)
			}
			if err := o.WaitContext(context.Background()); err != nil {
				t.Fatal("failed parser worker did not settle", err)
			}
			if v, err := o.Observe(context.Background()); err == nil || v != (ProbeHTTPObservation{}) {
				t.Fatal("failed admitted observer retried", v, err)
			}
		})
	}
}

func TestProbeHTTPObservationDoesNotConsumeBody(t *testing.T) {
	owner := new(probeHTTPMemoryOwner)
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), func(c net.Conn, _ probeHTTPRequest) {
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 1000000000\r\n\r\n")
		var b [1]byte
		_, _ = c.Read(b[:]) // No body is sent; child closure must finish observation.
	})
	o := newProbeObserver(t, f, 80, "/healthz")
	r := probeHTTPAwait(t, f.observe(o, context.Background()))
	if r.err != nil || r.value.StatusCode != 200 || len(owner.snapshot()) != 1 {
		t.Fatal("header-only observation waited for a response body", r.value, r.err)
	}
	assertProbeHTTPSharedLive(t, f)
}

func TestProbeHTTPObservationCapturedDeadline(t *testing.T) {
	for _, name := range []string{"eight seconds", "caller", "NIC"} {
		t.Run(name, func(t *testing.T) {
			owner := new(probeHTTPMemoryOwner)
			var captured time.Time
			owner.before = func(ctx context.Context, _ ProbeHTTPObservation) error { captured, _ = ctx.Deadline(); return nil }
			var caller, parent context.Context = context.Background(), context.Background()
			deadline := time.Now().Add(3 * time.Second)
			if name == "caller" {
				caller = passiveDeadlineParent{Context: context.Background(), deadline: deadline}
			}
			if name == "NIC" {
				parent = passiveDeadlineParent{Context: context.Background(), deadline: deadline}
			}
			f := newProbeHTTPFixture(t, owner, parent, persistentConfig(), probeHTTPReply)
			v, err := newProbeObserver(t, f, 80, "/").Observe(caller)
			if err != nil {
				t.Fatal(err)
			}
			if name == "eight seconds" {
				deadline = v.StartedAt.Add(8 * time.Second)
			}
			if !captured.Equal(deadline) {
				t.Fatalf("publication deadline=%v, want original bound %v", captured, deadline)
			}
		})
	}
}

func TestProbeHTTPObservationPublicationSettlementAndRevocation(t *testing.T) {
	for _, revoke := range []string{"joined publication", "caller", "NIC", "service", "owner"} {
		t.Run(revoke, func(t *testing.T) {
			owner := new(probeHTTPMemoryOwner)
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			owner.before = func(context.Context, ProbeHTTPObservation) error { close(entered); <-release; return nil }
			f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			caller, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			o := newProbeObserver(t, f, 80, "/")
			result := f.observe(o, caller)
			awaitSignal(t, entered, "publication after child join")
			assertProbeHTTPSharedLive(t, f)
			assertProbeHTTPPending(t, o)
			switch revoke {
			case "caller":
				cancel()
			case "NIC":
				f.cancelLifetime()
			case "service":
				f.cancelService()
			case "owner":
				owner.revoke()
			}
			once.Do(func() { close(release) })
			r := probeHTTPAwait(t, result)
			if err := o.WaitContext(context.Background()); err != nil {
				t.Fatal("released publication did not actually settle", err)
			}
			if revoke == "joined publication" {
				if r.err != nil || r.value.StatusCode != 200 || len(owner.snapshot()) != 1 {
					t.Fatal("joined publication failed after release", r.value, r.err)
				}
				assertProbeHTTPSharedLive(t, f)
			} else if r.err == nil || r.value != (ProbeHTTPObservation{}) || len(owner.snapshot()) != 0 {
				t.Fatal("revocation after child cleanup published an observation", r.value, r.err)
			}
		})
	}
}

func TestProbeHTTPObservationBlockedPublicationSharesGlobalCapacity(t *testing.T) {
	waitHTTPWorkers(t)
	if cap(observationSlots) != 8 {
		t.Fatal("observation capacity is not the fixed global eight")
	}
	owner := new(probeHTTPMemoryOwner)
	entered, release := make(chan struct{}, 8), make(chan struct{})
	var once sync.Once
	owner.before = func(context.Context, ProbeHTTPObservation) error { entered <- struct{}{}; <-release; return nil }
	f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
	// Register again after the legacy fixture below; either early failure path
	// must release every deliberately blocked publication before fixture teardown.
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var held []*ProbeHTTPObserver
	for range cap(observationSlots) {
		o := newProbeObserver(t, f, 80, "/")
		held = append(held, o)
		// Capacity retention is synchronized to publication, not TCP startup speed.
		caller, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		result := f.observe(o, caller)
		awaitSignal(t, entered, "blocked publication")
		cancel()
		r := probeHTTPAwait(t, result)
		if !errors.Is(r.err, context.Canceled) || r.value != (ProbeHTTPObservation{}) {
			t.Fatal("caller cancellation lost", r.value, r.err)
		}
		assertProbeHTTPPending(t, o)
	}
	if len(observationSlots) != 8 || len(owner.snapshot()) != 0 {
		t.Fatal("timeout released unsettled publication debt")
	}
	f.transport.nic.mu.Lock()
	children := len(f.transport.nic.probes)
	f.transport.nic.mu.Unlock()
	if children != 0 {
		t.Fatal("blocked publication retained already-joined children", children)
	}
	assertProbeHTTPSharedLive(t, f)
	available := newProbeObserver(t, f, 8080, "/healthz")
	if v, err := available.Observe(context.Background()); !errors.Is(err, ErrCapacity) || v != (ProbeHTTPObservation{}) {
		t.Fatal("ninth probe observation bypassed retained capacity", v, err)
	}
	if err := available.WaitContext(context.Background()); !errors.Is(err, ErrConfig) {
		t.Fatal("capacity rejection marked observer admitted", err)
	}
	legacyOwner := new(httpMemoryOwner)
	legacyFixture := newHTTPFixture(t, legacyOwner, nil, func(c net.Conn, _ *http.Request) {
		defer c.SetDeadline(time.Time{})
		_, _ = io.WriteString(c, "HTTP/1.1 204 No Content\r\n\r\n")
	})
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	legacy, err := NewHTTPObserver(legacyFixture.transport, "/")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := legacy.Observe(context.Background()); !errors.Is(err, ErrCapacity) || v != (HTTPObservation{}) {
		t.Fatal("legacy observer bypassed probe's global admission debt", v, err)
	}
	once.Do(func() { close(release) })
	for _, o := range held {
		if err := o.WaitContext(context.Background()); err != nil {
			t.Fatal("released expired publication did not settle", err)
		}
	}
	if len(observationSlots) != 0 || len(owner.snapshot()) != 0 {
		t.Fatal("settled publication retained capacity or published late")
	}
	if v, err := legacy.Observe(context.Background()); err != nil || v.StatusCode != 204 || legacyOwner.count() != 1 {
		t.Fatal("legacy capacity did not recover", v, err)
	}
	if v, err := available.Observe(context.Background()); err != nil || v.StatusCode != 200 {
		t.Fatal("capacity rejection consumed persistent observer", v, err)
	}
	if err := available.WaitContext(context.Background()); err != nil {
		t.Fatal("recovered observation settlement", err)
	}
	if len(owner.snapshot()) != 1 {
		t.Fatal("late publication occurred after capacity recovery")
	}
	assertProbeHTTPSharedLive(t, f)
}

func TestProbeHTTPObservationRetainsSlotUntilChildCleanup(t *testing.T) {
	for _, point := range []string{"Dial SYN", "received final header"} {
		t.Run(point, func(t *testing.T) {
			waitHTTPWorkers(t)
			owner := new(probeHTTPMemoryOwner)
			entered, release := make(chan struct{}), make(chan struct{})
			var blockOnce, releaseOnce sync.Once
			owner.frame = func(_ context.Context, d FrameDirection, frame []byte, apply func() error) error {
				packet, err := guestnet.ParseTCPFrame(frame)
				selected := err == nil && d == ToGuest && packet.Flags&2 != 0 && point == "Dial SYN"
				selected = selected || (d == FromGuest && bytes.Contains(frame, []byte("HTTP/1.1 200")) && point == "received final header")
				if !selected {
					return apply()
				}
				if point == "received final header" {
					if err := apply(); err != nil {
						return err
					}
				}
				blockOnce.Do(func() { close(entered); <-release })
				if point == "Dial SYN" {
					return apply()
				}
				return nil
			}
			f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
			t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			caller, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			o := newProbeObserver(t, f, 80, "/")
			result := f.observe(o, caller)
			awaitSignal(t, entered, "unsettled actual child work")
			if point == "received final header" {
				// Establish that parsing reached normal child Close while the
				// receive callback is still entered, before canceling the caller.
				request := <-f.requests
				until := time.Now().Add(time.Second)
				for {
					request.probe.mu.Lock()
					closing, closeErr := request.probe.closing, request.probe.err
					request.probe.mu.Unlock()
					if closing {
						if closeErr != nil || request.probe.session.Lifetime().Err() != nil {
							t.Fatal("header did not reach normal child cleanup", closeErr)
						}
						break
					}
					if time.Now().After(until) {
						t.Fatal("parsed header did not begin child cleanup")
					}
					time.Sleep(time.Millisecond)
				}
			}
			cancel()
			r := probeHTTPAwait(t, result)
			if r.err == nil || r.value != (ProbeHTTPObservation{}) {
				t.Fatal("unfinished child returned a status", r.value, r.err)
			}
			assertProbeHTTPPending(t, o)
			if len(observationSlots) != 1 || len(owner.snapshot()) != 0 {
				t.Fatal("timeout forgot child cleanup debt or published early")
			}
			f.transport.nic.mu.Lock()
			children := len(f.transport.nic.probes)
			f.transport.nic.mu.Unlock()
			if children != 1 {
				t.Fatal("unjoined actual child was retired", children)
			}
			releaseOnce.Do(func() { close(release) })
			if err := o.WaitContext(context.Background()); err != nil {
				t.Fatal("released failed child did not settle", err)
			}
			if len(observationSlots) != 0 || len(owner.snapshot()) != 0 {
				t.Fatal("settled failed child retained capacity or published late")
			}
			if point == "Dial SYN" && len(f.requests) != 0 {
				t.Fatal("canceled blocked SYN reached the peer")
			}
		})
	}
}

// Timer coverage is separate from eight-slot saturation: the immutable caller
// deadline allows TCP startup, then expires while publication is deliberately
// held. Done and Err never signal expiry, so the observer must supply its timer.
func TestProbePublicationNonNotifyingDeadlineRetainsDebt(t *testing.T) {
	for _, operation := range []bool{false, true} {
		t.Run(fmt.Sprintf("operation=%t", operation), func(t *testing.T) {
			waitHTTPWorkers(t)
			entered, release := make(chan time.Time, 1), make(chan struct{})
			var once sync.Once
			owner := &probeHTTPMemoryOwner{before: func(ctx context.Context, _ ProbeHTTPObservation) error {
				d, _ := ctx.Deadline()
				entered <- d
				<-release
				return nil
			}}
			f := newProbeHTTPFixture(t, owner, context.Background(), persistentConfig(), probeHTTPReply)
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			deadline := time.Now().Add(2 * time.Second)
			caller := passiveDeadlineParent{Context: context.Background(), deadline: deadline}
			var observer *ProbeHTTPObserver
			var handle *HTTPProbeOperation
			var result <-chan probeHTTPResult
			if operation {
				handle = startOperation(t, f, caller, operationKey(1), 80, "/")
			} else {
				observer = newProbeObserver(t, f, 80, "/")
				result = f.observe(observer, caller)
			}
			select {
			case captured := <-entered:
				if !captured.Equal(deadline) {
					t.Fatal("publication changed original deadline", captured, deadline)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("publication setup did not finish before immutable caller deadline")
			}
			if operation {
				// The watchdog must not race the two-second contract timer.
				wait, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				s, err := handle.WaitResult(wait)
				if err != nil || !s.ResultReady {
					t.Fatal("deadline result watchdog", s, err)
				}
				if !errors.Is(s.ResultError, context.DeadlineExceeded) || s.Observation != (ProbeHTTPObservation{}) || s.Settled || !s.Deadline.Equal(deadline) {
					t.Fatal("deadline result or unsettled debt lost", s)
				}
				operationPending(t, handle)
			} else {
				var r probeHTTPResult
				select {
				case r = <-result:
				case <-time.After(3 * time.Second):
					t.Fatal("deadline result watchdog")
				}
				if !errors.Is(r.err, context.DeadlineExceeded) || r.value != (ProbeHTTPObservation{}) {
					t.Fatal("deadline result lost", r)
				}
				assertProbeHTTPPending(t, observer)
			}
			if time.Now().Before(deadline) || time.Now().After(deadline.Add(time.Second)) || len(observationSlots) != 1 || len(owner.snapshot()) != 0 {
				t.Fatal("premature expiry, settlement, or publication")
			}
			once.Do(func() { close(release) })
			if operation {
				s := operationSettled(t, handle)
				if !errors.Is(s.ResultError, context.DeadlineExceeded) || s.Observation != (ProbeHTTPObservation{}) {
					t.Fatal("late settlement changed original result", s)
				}
			} else if err := observer.WaitContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(observationSlots) != 0 || len(owner.snapshot()) != 0 {
				t.Fatal("late publication or capacity leak")
			}
			assertProbeHTTPSharedLive(t, f)
		})
	}
}
