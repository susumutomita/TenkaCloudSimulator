package guesttcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// This test dispatcher demonstrates tuple selection only. It is not a shared
// Transport or physical/policy owner, and provides no aggregate lifetime/budget
// contract. Engine and Listener retain their independent ten-second limits.
type memoryTCPRouteDispatch struct {
	routes   *guestnet.TCPRoutes
	engine   *Engine
	listener *Listener
	guest    *channel.Endpoint
	mu       sync.Mutex
	calls    [2][2]int // direction (from/to guest), then constructor route index
	first    [2][2][]byte
}

func (d *memoryTCPRouteDispatch) record(direction, route int, frame []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls[direction][route]++
	if d.first[direction][route] == nil {
		d.first[direction][route] = append([]byte(nil), frame...)
	}
}

func (d *memoryTCPRouteDispatch) fromGuest(frame []byte) error {
	route, err := d.routes.Classify(guestnet.TCPFromPeer, frame)
	if err != nil {
		return err
	}
	// Select before either Inject: trying the listener first can revoke it.
	switch route {
	case 0:
		d.record(0, route, frame)
		return d.engine.Inject(frame)
	case 1:
		d.record(0, route, frame)
		return d.listener.Inject(frame)
	default:
		return guestnet.ErrTCPRoute
	}
}

func (d *memoryTCPRouteDispatch) toGuest(expected int, frame []byte) error {
	route, err := d.routes.Classify(guestnet.TCPToPeer, frame)
	if err != nil {
		return err
	}
	// A valid tuple for the other endpoint is not this producer's output.
	if route != expected {
		return guestnet.ErrTCPRoute
	}
	d.record(1, route, frame)
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
	d.guest.InjectInbound(0, pkt)
	pkt.DecRef()
	return nil
}

func TestTCPRoutesComposeEngineAndListenerOnOneMemoryNIC(t *testing.T) {
	c, service := config(), listenConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	l, err := NewListener(ctx, service)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	routes, err := guestnet.NewTCPRoutes([]guestnet.TCPRoute{
		{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, LocalPort: LocalPort, PeerPort: c.Port},
		{LocalIP: service.LocalIP, PeerIP: service.PeerIP, LocalMAC: service.LocalMAC, PeerMAC: service.PeerMAC, LocalPort: service.LocalPort, AnyPeerPort: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.PeerIP != service.PeerIP || c.PeerMAC != service.PeerMAC || c.LocalMAC != service.LocalMAC {
		t.Fatal("fixtures must share the guest IP/MAC and local gateway MAC")
	}
	// The existing peer helper creates exactly one independent guest stack/NIC
	// and its HTTP listener. Add a fixed metadata /32, never a default route.
	s, link, guestHTTP := peer(t, c)
	defer s.Destroy()
	defer link.Close()
	defer guestHTTP.Close()
	subnet, err := tcpip.NewSubnet(tcpip.AddrFrom4(service.LocalIP.As4()), tcpip.MaskFromBytes([]byte{255, 255, 255, 255}))
	if err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable(append(s.GetRouteTable(), tcpip.Route{Destination: subnet, NIC: 1}))
	if err := s.AddStaticNeighbor(1, ipv4.ProtocolNumber, tcpip.AddrFrom4(service.LocalIP.As4()), tcpip.LinkAddress(string(service.LocalMAC[:]))); err != nil {
		t.Fatal(err)
	}
	dispatch := &memoryTCPRouteDispatch{routes: routes, engine: e, listener: l, guest: link}

	// Rejection happens before any endpoint or guest delivery. A malformed or
	// unmatched frame must not be tried against both concrete adapters.
	probeTuple := ListenConfig{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, LocalPort: LocalPort}
	for _, tuple := range []ListenConfig{probeTuple, service} {
		port := uint16(41000)
		if tuple == probeTuple {
			port = c.Port
		}
		valid := listenFrame(tuple, port, 123, 2)
		malformed := append([]byte(nil), valid...)
		malformed[50] ^= 1
		spoofMAC := append([]byte(nil), valid...)
		spoofMAC[11] ^= 1
		spoofIP := tuple
		spoofIP.PeerIP = netip.MustParseAddr("10.0.1.5")
		wrongPort := tuple
		wrongPort.LocalPort++
		for _, frame := range [][]byte{valid[:30], malformed, spoofMAC, listenFrame(spoofIP, port, 123, 2), listenFrame(wrongPort, port, 123, 2)} {
			if err := dispatch.fromGuest(frame); err == nil {
				t.Fatal("invalid guest frame reached an endpoint")
			}
		}
	}
	for route, tuple := range []ListenConfig{probeTuple, service} {
		peerPort := uint16(41000)
		if route == 0 {
			peerPort = c.Port
		}
		outboundTuple := ListenConfig{LocalIP: tuple.PeerIP, PeerIP: tuple.LocalIP, LocalMAC: tuple.PeerMAC, PeerMAC: tuple.LocalMAC, LocalPort: peerPort}
		outbound := listenFrame(outboundTuple, tuple.LocalPort, 123, 2)
		if got, err := routes.Classify(guestnet.TCPToPeer, outbound); err != nil || got != route {
			t.Fatalf("outbound fixture route %d: %d %v", route, got, err)
		}
		if err := dispatch.toGuest(1-route, outbound); !errors.Is(err, guestnet.ErrTCPRoute) {
			t.Fatalf("output from the wrong producer was accepted: %v", err)
		}
		if err := dispatch.fromGuest(outbound); !errors.Is(err, guestnet.ErrTCPRoute) {
			t.Fatalf("reversed input direction was accepted: %v", err)
		}
		if err := dispatch.toGuest(route, listenFrame(tuple, peerPort, 123, 2)); !errors.Is(err, guestnet.ErrTCPRoute) {
			t.Fatalf("reversed output direction was accepted: %v", err)
		}
	}
	if dispatch.calls != ([2][2]int{}) || e.ctx.Err() != nil || l.ctx.Err() != nil {
		t.Fatal("rejected traffic reached or revoked an endpoint")
	}

	var pumps sync.WaitGroup
	pumpErrors := make(chan error, 3)
	for route, read := range []func(context.Context) ([]byte, error){e.ReadFrame, l.ReadFrame} {
		pumps.Go(func() {
			for {
				frame, err := read(ctx)
				if err == nil {
					err = dispatch.toGuest(route, frame)
				}
				if err != nil {
					if ctx.Err() == nil {
						pumpErrors <- fmt.Errorf("route %d output: %w", route, err)
					}
					return
				}
			}
		})
	}
	pumps.Go(func() {
		for {
			pkt := link.ReadContext(ctx)
			if pkt == nil {
				return
			}
			view := pkt.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			pkt.DecRef()
			if err := dispatch.fromGuest(frame); err != nil {
				if ctx.Err() == nil {
					pumpErrors <- fmt.Errorf("guest output: %w", err)
				}
				return
			}
			clear(frame) // Neither concrete endpoint may retain caller storage.
		}
	})
	defer func() { cancel(); e.Close(); _ = l.Close(); pumps.Wait() }()

	probeSeen, metadataFinished := make(chan struct{}), make(chan struct{})
	guestServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		close(probeSeen)
		select {
		case <-metadataFinished:
			_, _ = io.WriteString(w, "guest service reached after metadata")
		case <-ctx.Done():
		}
	})}
	// Only a generic in-memory fixture: no AWS authority, token service or
	// production metadata availability is inferred from this HTTP exchange.
	metadataServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token" && r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "300":
			_, _ = io.WriteString(w, "memory-only-token")
		case r.Method == http.MethodGet && r.URL.Path == "/latest/meta-data/hostname" && r.Header.Get("X-aws-ec2-metadata-token") == "memory-only-token":
			_, _ = io.WriteString(w, "ip-10-0-1-4.memory.invalid")
		default:
			http.Error(w, "unexpected metadata fixture request", http.StatusBadRequest)
		}
	})}
	var servers sync.WaitGroup
	servers.Go(func() { _ = guestServer.Serve(guestHTTP) })
	servers.Go(func() { _ = metadataServer.Serve(l) })
	defer func() { cancel(); _ = guestServer.Close(); _ = metadataServer.Close(); servers.Wait() }()

	// Start the probe and leave its HTTP response pending while the SAME guest
	// stack initiates both service requests. Sequential unrelated stacks would
	// not satisfy this overlap or the common NIC dispatch counters below.
	probeResult := make(chan error, 1)
	go func() {
		conn, err := e.Dial(ctx)
		if err == nil {
			err = memoryRouteHTTPExchange(ctx, conn, "GET /healthz HTTP/1.1\r\nHost: guest.invalid\r\n\r\n", "guest service reached after metadata")
		}
		probeResult <- err
	}()
	select {
	case <-probeSeen:
	case err := <-probeResult:
		t.Fatalf("probe ended before reaching the guest service: %v", err)
	case err := <-pumpErrors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for i, exchange := range []struct{ request, body string }{
		{"PUT /latest/api/token HTTP/1.1\r\nHost: 169.254.169.254\r\nX-aws-ec2-metadata-token-ttl-seconds: 300\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", "memory-only-token"},
		{"GET /latest/meta-data/hostname HTTP/1.1\r\nHost: 169.254.169.254\r\nX-aws-ec2-metadata-token: memory-only-token\r\nConnection: close\r\n\r\n", "ip-10-0-1-4.memory.invalid"},
	} {
		conn, err := gonet.DialTCPWithBind(ctx, s,
			tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(c.PeerIP.As4()), Port: uint16(41000 + i)},
			tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(service.LocalIP.As4()), Port: service.LocalPort}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatal(err)
		}
		err = memoryRouteHTTPExchange(ctx, conn, exchange.request, exchange.body)
		_ = conn.SetDeadline(time.Time{})
		_ = conn.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-probeResult:
		t.Fatalf("probe response did not wait for metadata completion: %v", err)
	default:
	}
	close(metadataFinished)
	select {
	case err := <-probeResult:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-pumpErrors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-pumpErrors:
		t.Fatal(err)
	default:
	}
	dispatch.mu.Lock()
	defer dispatch.mu.Unlock()
	for direction, counts := range dispatch.calls {
		for route, count := range counts {
			if count < 3 {
				t.Fatalf("route %d direction %d did not exchange a TCP handshake and HTTP: %d", route, direction, count)
			}
			got, err := routes.Classify([]guestnet.TCPDirection{guestnet.TCPFromPeer, guestnet.TCPToPeer}[direction], dispatch.first[direction][route])
			if err != nil || got != route {
				t.Fatalf("captured actual traffic crossed routes: %d %v", got, err)
			}
		}
	}
}

func memoryRouteHTTPExchange(ctx context.Context, conn net.Conn, request, want string) error {
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	if _, err := io.WriteString(conn, request); err != nil {
		return err
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1024))
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != want {
		return fmt.Errorf("HTTP response %d %q, want 200 %q: %w", response.StatusCode, body, want, err)
	}
	return nil
}
