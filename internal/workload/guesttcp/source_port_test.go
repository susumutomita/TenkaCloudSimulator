package guesttcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
)

// Each exchange uses two real, independent gVisor stacks and bounded memory
// streams. No OS socket, HTTP server process or VM is involved.
func TestSourcePortMemoryHTTP(t *testing.T) {
	for _, source := range []uint16{0, 1, 41001, 41002, 65535} {
		for _, routed := range []bool{false, true} {
			t.Run(fmt.Sprintf("port=%d/routed=%t", source, routed), func(t *testing.T) {
				sourcePortMemoryHTTP(t, source, routed)
			})
		}
	}
}

func sourcePortMemoryHTTP(t *testing.T, source uint16, routed bool) {
	t.Helper()
	c := config()
	c.SourcePort = source
	want := source
	if want == 0 {
		want = LocalPort
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	local, remote := memoryStreams()
	var inbound, outbound atomic.Int32
	owner := commitFunc(func(_ context.Context, direction FrameDirection, frame []byte, apply func() error) error {
		p, err := guestnet.ParseTCPFrame(frame)
		if err != nil {
			return err
		}
		switch direction {
		case FromGuest:
			if p.SourcePort != c.Port || p.DestinationPort != want {
				return fmt.Errorf("unexpected inbound ports %d -> %d", p.SourcePort, p.DestinationPort)
			}
			inbound.Add(1)
		case ToGuest:
			if p.SourcePort != want || p.DestinationPort != c.Port {
				return fmt.Errorf("unexpected outbound ports %d -> %d", p.SourcePort, p.DestinationPort)
			}
			outbound.Add(1)
		default:
			return ErrConfig
		}
		return apply()
	})
	var tr *Transport
	var err error
	if routed {
		tr, err = NewTransportWithService(ctx, c, local, owner, nil, newTransportServiceFixture(), transportServiceRoute())
	} else {
		tr, err = NewTransport(ctx, c, local, owner, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	if tr.engine.config.SourcePort != want {
		t.Fatalf("copied source port = %d, want %d", tr.engine.config.SourcePort, want)
	}
	// The constructor must not keep a caller-controlled source-port reference.
	c.SourcePort = 49999
	s, link, ln := peer(t, c)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		decoder := guestnet.NewDecoder(remote)
		for {
			frame, err := decoder.ReadFrame()
			if err != nil {
				return
			}
			p := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
			link.InjectInbound(0, p)
			p.DecRef()
		}
	}()
	go func() {
		defer workers.Done()
		encoder := guestnet.NewEncoder(remote)
		for {
			p := link.ReadContext(ctx)
			if p == nil {
				return
			}
			view := p.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			p.DecRef()
			if encoder.WriteFrame(frame) != nil {
				return
			}
		}
	}()
	peerResult := make(chan error, 1)
	go func() {
		defer workers.Done()
		conn, err := ln.Accept()
		if err != nil {
			peerResult <- err
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		addr, ok := conn.RemoteAddr().(*net.TCPAddr)
		if !ok || addr.Port != int(want) {
			peerResult <- fmt.Errorf("peer remote address = %v, want port %d", conn.RemoteAddr(), want)
			return
		}
		r, err := http.ReadRequest(bufio.NewReader(conn))
		if err != nil {
			peerResult <- err
			return
		}
		_ = r.Body.Close()
		if r.Method != "GET" || r.RequestURI != "/healthz" {
			peerResult <- fmt.Errorf("unexpected request %s %s", r.Method, r.RequestURI)
			return
		}
		_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		peerResult <- err
		// Keep the peer alive until the test has consumed the complete response.
		<-ctx.Done()
	}()
	t.Cleanup(func() {
		cancel()
		_ = remote.Close()
		_ = tr.Close()
		_ = ln.Close()
		workers.Wait()
		link.Close()
		s.Destroy()
	})
	conn, err := tr.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.TCPAddr); !ok || addr.Port != int(want) {
		t.Fatalf("local address = %v, want port %d", conn.LocalAddr(), want)
	}
	if _, err = io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: private.invalid\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("HTTP status=%d body=%q error=%v", response.StatusCode, body, err)
	}
	if err := <-peerResult; err != nil {
		t.Fatal(err)
	}
	if inbound.Load() == 0 || outbound.Load() == 0 {
		t.Fatal("real TCP did not traverse both source-port validation boundaries")
	}
}

func sourcePortFrame(c Config, fromPeer bool, localPort, peerPort uint16) []byte {
	if fromPeer {
		return listenFrame(ListenConfig{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, LocalPort: localPort}, peerPort, 123, 2)
	}
	return listenFrame(ListenConfig{LocalIP: c.PeerIP, PeerIP: c.LocalIP, LocalMAC: c.PeerMAC, PeerMAC: c.LocalMAC, LocalPort: peerPort}, localPort, 123, 2)
}

func TestSourcePortEngineRejectsWrongInputPorts(t *testing.T) {
	c := config()
	c.SourcePort = 41001
	e, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	done := make(chan error, 1)
	go func() { _, err := e.Dial(ctx); done <- err }()
	defer func() { cancel(); e.Close(); <-done }()
	frame, err := e.ReadFrame(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p, err := guestnet.ParseTCPFrame(frame)
	if err != nil || p.SourcePort != c.SourcePort || p.Flags&2 == 0 {
		t.Fatalf("actual SYN = %+v, error=%v", p, err)
	}
	for _, ports := range [][2]uint16{{LocalPort, c.Port}, {c.SourcePort + 1, c.Port}, {c.SourcePort, c.Port + 1}} {
		if err := e.Inject(sourcePortFrame(c, true, ports[0], ports[1])); !errors.Is(err, guestnet.ErrPacket) {
			t.Fatalf("input ports %v accepted: %v", ports, err)
		}
	}
	e.mu.Lock()
	packets := e.packets
	e.mu.Unlock()
	if packets < 4 { // The actual SYN and every rejected frame consume allowance.
		t.Fatalf("rejected input bypassed engine budget: %d", packets)
	}
}

func TestSourcePortEngineValidatesOutputPorts(t *testing.T) {
	c := config()
	c.SourcePort = 41001
	for _, ports := range [][2]uint16{{c.SourcePort, c.Port}, {LocalPort, c.Port}, {c.SourcePort + 1, c.Port}, {c.SourcePort, c.Port + 1}} {
		t.Run(fmt.Sprint(ports), func(t *testing.T) {
			e, err := New(c)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			// Put a checksum-valid frame at the stack's output boundary to
			// exercise ReadFrame's independent fail-closed tuple validation.
			p := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(sourcePortFrame(c, false, ports[0], ports[1]))})
			var packets stack.PacketBufferList
			packets.PushBack(p)
			n, writeErr := e.link.WritePackets(packets)
			packets.DecRef()
			if n != 1 || writeErr != nil {
				t.Fatalf("queue output: %d %v", n, writeErr)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			frame, err := e.ReadFrame(ctx)
			if ports == [2]uint16{c.SourcePort, c.Port} {
				if err != nil || len(frame) == 0 {
					t.Fatalf("configured output rejected: %v", err)
				}
			} else if !errors.Is(err, guestnet.ErrPacket) || frame != nil {
				t.Fatalf("wrong output ports accepted: %v", err)
			}
		})
	}
}

func TestSourcePortTransportRejectsInputBeforeOwner(t *testing.T) {
	c := config()
	c.SourcePort = 41001
	for _, routed := range []bool{false, true} {
		for _, ports := range [][2]uint16{{LocalPort, c.Port}, {c.SourcePort + 1, c.Port}, {c.SourcePort, c.Port + 1}} {
			t.Run(fmt.Sprintf("routed=%t/ports=%v", routed, ports), func(t *testing.T) {
				local, remote := memoryStreams()
				var calls atomic.Int32
				owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
					calls.Add(1)
					return apply()
				})
				var tr *Transport
				var err error
				if routed {
					tr, err = NewTransportWithService(context.Background(), c, local, owner, nil, newTransportServiceFixture(), transportServiceRoute())
				} else {
					tr, err = NewTransport(context.Background(), c, local, owner, nil)
				}
				if err != nil {
					t.Fatal(err)
				}
				defer tr.Close()
				if err := guestnet.NewEncoder(remote).WriteFrame(sourcePortFrame(c, true, ports[0], ports[1])); err != nil {
					t.Fatal(err)
				}
				awaitSignal(t, tr.done, "invalid source-port teardown")
				want := guestnet.ErrPacket
				if routed {
					want = guestnet.ErrTCPRoute
				}
				if err := tr.Wait(); !errors.Is(err, want) || calls.Load() != 0 || tr.engine.packets != 0 || tr.packets != 1 {
					t.Fatalf("input crossed validation or budget: error=%v owner=%d engine=%d transport=%d", err, calls.Load(), tr.engine.packets, tr.packets)
				}
			})
		}
	}
}

func TestSourcePortServiceOverlapUsesEffectivePort(t *testing.T) {
	for _, test := range []struct {
		source, service uint16
		overlap         bool
	}{{0, LocalPort, true}, {41001, 41001, true}, {41001, LocalPort, false}} {
		t.Run(fmt.Sprintf("source=%d/service=%d", test.source, test.service), func(t *testing.T) {
			c := config()
			c.SourcePort = test.source
			route := transportServiceRoute()
			route.LocalIP, route.LocalPort = c.LocalIP, test.service
			service := newTransportServiceFixture()
			stream := newBlockedStream(false)
			tr, err := NewTransportWithService(context.Background(), c, stream, memoryCommitter, nil, service, route)
			if test.overlap {
				if tr != nil || !errors.Is(err, guestnet.ErrTCPRouteConfig) || stream.closes.Load() != 0 || service.closes.Load() != 0 {
					t.Fatalf("overlap accepted or caller ownership consumed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			if err := tr.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
