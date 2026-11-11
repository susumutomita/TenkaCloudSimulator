package guesttcp

import (
	"context"
	"errors"
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
	"gvisor.dev/gvisor/pkg/tcpip/link/ethernet"
	"gvisor.dev/gvisor/pkg/tcpip/network/arp"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

func config() Config {
	return Config{LocalIP: netip.MustParseAddr("198.51.100.2"), PeerIP: netip.MustParseAddr("10.0.1.4"), LocalMAC: [6]byte{2, 0, 0, 0, 0, 1}, PeerMAC: [6]byte{2, 0, 0, 0, 0, 2}, Port: 80}
}

// The peer is a second independent userspace stack, not a guest VM or host
// socket. This exercises real TCP framing/handshake/HTTP without net.Pipe mocks.
func peer(t *testing.T, c Config) (*stack.Stack, *channel.Endpoint, net.Listener) {
	t.Helper()
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{arp.NewProtocol, ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	link := channel.New(16, 1514, tcpip.LinkAddress(string(c.PeerMAC[:])))
	if err := s.CreateNIC(1, ethernet.New(link)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddrFrom4(c.PeerIP.As4()).WithPrefix()}, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	subnet, err := tcpip.NewSubnet(tcpip.AddrFrom4(c.LocalIP.As4()), tcpip.MaskFromBytes([]byte{255, 255, 255, 255}))
	if err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: subnet, NIC: 1}})
	if err := s.AddStaticNeighbor(1, ipv4.ProtocolNumber, tcpip.AddrFrom4(c.LocalIP.As4()), tcpip.LinkAddress(string(c.LocalMAC[:]))); err != nil {
		t.Fatal(err)
	}
	ln, e := gonet.ListenTCP(s, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(c.PeerIP.As4()), Port: c.Port}, ipv4.ProtocolNumber)
	if e != nil {
		t.Fatal(e)
	}
	return s, link, ln
}
func TestMemoryTCPHTTPExchange(t *testing.T) {
	t.Run("normal", func(t *testing.T) { memoryTCPHTTPExchange(t, false) })
	t.Run("lost initial SYN", func(t *testing.T) { memoryTCPHTTPExchange(t, true) })
}
func memoryTCPHTTPExchange(t *testing.T, dropFirst bool) {
	c := config()
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	s, link, ln := peer(t, c)
	defer s.Destroy()
	defer link.Close()
	defer ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		defer pumps.Done()
		first := true
		for {
			frame, err := e.ReadFrame(ctx)
			if err != nil {
				return
			}
			if first && dropFirst {
				first = false
				continue
			}
			first = false
			p := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
			link.InjectInbound(0, p)
			p.DecRef()
		}
	}()
	go func() {
		defer pumps.Done()
		for {
			p := link.ReadContext(ctx)
			if p == nil {
				return
			}
			view := p.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			p.DecRef()
			if err := e.Inject(frame); err != nil {
				return
			}
			for i := range frame {
				frame[i] = 0
			} // Caller may reuse its receive buffer immediately.
		}
	}()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("userspace TCP response"))
	})}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(ln) }()
	defer func() { server.Close(); cancel(); e.Close(); pumps.Wait(); <-served }()
	conn, err := e.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("http://private.invalid/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	transport.CloseIdleConnections()
	if err != nil || response.StatusCode != 200 || string(body) != "userspace TCP response" {
		t.Fatalf("response %d %q %v", response.StatusCode, body, err)
	}
}
func TestCloseCancelsRealPendingHandshake(t *testing.T) {
	e, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := e.Dial(context.Background()); done <- err }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	frame, err := e.ReadFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	packet, err := guestnet.ParseTCPFrame(frame)
	if err != nil || packet.Flags&2 == 0 {
		t.Fatalf("not real SYN: %+v %v", packet, err)
	}
	e.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed handshake succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("dial stuck after close")
	}
	if _, err = e.ReadFrame(ctx); err == nil {
		t.Fatal("frame after close")
	}
	if err = e.Inject(frame); err == nil {
		t.Fatal("inject after close")
	}
	if _, err = e.Dial(ctx); err == nil {
		t.Fatal("dial after close")
	}
}
func TestFixedTupleAndBounds(t *testing.T) {
	e, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.Dial(ctx); done <- err }()
	syn, err := e.ReadFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Dial(ctx); err == nil {
		t.Fatal("second connection admitted")
	}
	if err = e.Inject(syn); err == nil {
		t.Fatal("wrong direction accepted")
	}
	if err = e.Inject(make([]byte, 1515)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	e.Close()
	<-done
	for _, change := range []func(*Config){func(c *Config) { c.LocalIP = c.PeerIP }, func(c *Config) { c.PeerIP = netip.MustParseAddr("::1") }, func(c *Config) { c.Port = 0 }, func(c *Config) { c.PeerMAC[0] = 1 }, func(c *Config) { c.LocalMAC = [6]byte{} }, func(c *Config) { c.PeerMAC = c.LocalMAC }} {
		c := config()
		change(&c)
		if engine, err := New(c); err == nil {
			engine.Close()
			t.Fatal("invalid binding accepted")
		}
	}
}
func TestCapacityAndDeadline(t *testing.T) {
	var engines []*Engine
	defer func() {
		for _, e := range engines {
			e.Close()
		}
	}()
	for i := 0; i < cap(slots); i++ {
		e, err := New(config())
		if errors.Is(err, ErrCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		engines = append(engines, e)
	}
	if e, err := New(config()); err == nil {
		e.Close()
		t.Fatal("unbounded engines")
	}
	if len(engines) == 0 {
		t.Fatal("no engine capacity")
	}
	e := engines[0]
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.ReadFrame(ctx); err == nil {
		t.Fatal("read ignored cancellation")
	}
	if _, err := e.ReadFrame(nil); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := e.Dial(nil); err == nil {
		t.Fatal("nil dial context")
	}
}

func TestAggregatePacketBudgets(t *testing.T) {
	for _, size := range []int{0, guestnet.MaxFrameBytes} {
		t.Run(string(rune('a'+size%26)), func(t *testing.T) {
			e, err := New(config())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			done := make(chan error, 1)
			go func() { _, err := e.Dial(context.Background()); done <- err }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			syn, err := e.ReadFrame(ctx)
			if err != nil {
				t.Fatal(err)
			}
			expectedIndex := maxFrames - 1
			if size > 0 && (maxBytes-len(syn))/size < expectedIndex {
				expectedIndex = (maxBytes - len(syn)) / size
			}
			exceeded := false
			for i := 0; i <= maxFrames; i++ {
				err = e.Inject(make([]byte, size))
				if errors.Is(err, ErrCapacity) {
					exceeded = true
					if i != expectedIndex {
						t.Fatalf("budget exhausted at %d, want %d", i, expectedIndex)
					}
					break
				}
			}
			if !exceeded {
				t.Fatal("aggregate budget not enforced")
			}
			e.Close()
			if err = <-done; err == nil {
				t.Fatal("budget-exhausted dial succeeded")
			}
			replacement, err := New(config())
			if err != nil {
				t.Fatal("capacity not recovered", err)
			}
			replacement.Close()
		})
	}
}
func TestLifetimeExpires(t *testing.T) {
	e, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), MaxLifetime+2*time.Second)
	defer cancel()
	start := time.Now()
	_, err = e.ReadFrame(ctx)
	if !errors.Is(err, ErrClosed) || time.Since(start) > MaxLifetime+time.Second {
		t.Fatalf("unbounded lifetime %v %s", err, time.Since(start))
	}
	e.Close()
	if err = e.Inject(nil); !errors.Is(err, ErrClosed) {
		t.Fatal("expired engine accepted input")
	}
}
