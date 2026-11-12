package guesttcp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
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

// One independent userspace peer survives concurrent and repeated logical
// exchanges. This is memory TCP composition, not a physical NIC/owner adapter.
func TestProbeSessionsRepeatedMemoryTCPOnOnePeer(t *testing.T) {
	c := config()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	registry, err := guestnet.NewTCPProbeSessions(ctx, guestnet.ProbeNIC{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, ServicePorts: []uint16{80, 8080}}, time.Now().Add(time.Minute))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	peerStack, link, frontend := peer(t, c)
	api, err := gonet.ListenTCP(peerStack, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(c.PeerIP.As4()), Port: 8080}, ipv4.ProtocolNumber)
	if err != nil {
		cancel()
		registry.Revoke()
		_ = registry.Wait(context.Background())
		_ = frontend.Close()
		link.Close()
		peerStack.Destroy()
		t.Fatal(err)
	}
	var workers, active sync.WaitGroup
	// The future NIC owner needs a bounded physical writer/drain barrier.
	// This memory fixture has only two concurrent producers.
	var wire sync.Mutex
	var capturedMu sync.Mutex
	var captured []byte
	received := make(chan error, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			p := link.ReadContext(ctx)
			if p == nil {
				return
			}
			view := p.ToView()
			frame := append([]byte(nil), view.AsSlice()...)
			view.Release()
			p.DecRef()
			capturedMu.Lock()
			captured = append([]byte(nil), frame...)
			capturedMu.Unlock()
			if _, err := registry.RouteIncoming(frame); err != nil {
				select {
				case received <- err:
				default:
				}
				return
			}
		}
	}()
	serverResults := make(chan error, 4)
	serve := func(ln interface {
		Accept() (net.Conn, error)
		Close() error
	}) {
		defer workers.Done()
		for n := 0; n < 2; n++ {
			conn, err := ln.Accept()
			if err != nil {
				serverResults <- err
				return
			}
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			request, err := http.ReadRequest(bufio.NewReader(conn))
			if err == nil {
				_ = request.Body.Close()
				_, err = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			}
			// gonet Close does not stop connection deadline timers.
			_ = conn.SetDeadline(time.Time{})
			_ = conn.Close()
			serverResults <- err
		}
	}
	workers.Add(2)
	go serve(frontend)
	go serve(api)
	t.Cleanup(func() {
		wire.Lock()
		registry.Revoke()
		cancel()
		wire.Unlock()
		_ = frontend.Close()
		_ = api.Close()
		active.Wait()
		workers.Wait()
		link.Close()
		peerStack.Destroy()
		if err := registry.Wait(context.Background()); err != nil {
			t.Error(err)
		}
	})
	run := func(port uint16) error {
		session, err := registry.Open(ctx, port)
		if err != nil {
			return err
		}
		cfg := c
		cfg.Port = port
		cfg.SourcePort = session.Route().LocalPort
		engine, err := New(cfg)
		if err != nil {
			session.Revoke()
			_ = session.Wait(context.Background())
			return err
		}
		exchange, stop := context.WithCancel(ctx)
		stopEngine := context.AfterFunc(exchange, engine.Close)
		defer stopEngine()
		var pumps sync.WaitGroup
		pumps.Add(2)
		go func() {
			defer pumps.Done()
			for {
				frame, err := engine.ReadFrame(exchange)
				if err != nil {
					return
				}
				wire.Lock()
				err = session.Transmit(exchange, frame, func(_ context.Context, owned []byte) error {
					packet := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(owned)})
					link.InjectInbound(0, packet)
					packet.DecRef()
					return nil
				})
				wire.Unlock()
				if err != nil {
					return
				}
			}
		}()
		go func() {
			defer pumps.Done()
			for {
				frame, err := session.ReadFrame(exchange)
				if err != nil {
					return
				}
				if err = engine.Inject(frame); err != nil {
					return
				}
			}
		}()
		defer func() {
			// Join any admitted memory write before normal per-probe teardown.
			// Cancelling its context mid-write would revoke the shared registry.
			wire.Lock()
			session.Revoke()
			stop()
			wire.Unlock()
			engine.Close()
			pumps.Wait()
			_ = session.Wait(context.Background())
		}()
		conn, err := engine.Dial(exchange)
		if err != nil {
			return err
		}
		if _, err = io.WriteString(conn, "GET /healthz HTTP/1.1\r\nHost: private.invalid\r\nConnection: close\r\n\r\n"); err != nil {
			return err
		}
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != 200 || string(body) != "ok" {
			return fmt.Errorf("memory response %d %q: %w", response.StatusCode, body, err)
		}
		return nil
	}
	for round := 0; round < 2; round++ {
		results := make(chan error, 2)
		active.Add(2)
		go func() { defer active.Done(); results <- run(80) }()
		go func() { defer active.Done(); results <- run(8080) }()
		for range 2 {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		if registry.Lifetime().Err() != nil {
			t.Fatal("logical completion closed persistent registry")
		}
		capturedMu.Lock()
		old := append([]byte(nil), captured...)
		capturedMu.Unlock()
		if len(old) == 0 {
			t.Fatal("no actual peer frames")
		}
		if accepted, err := registry.RouteIncoming(old); accepted || err != nil {
			t.Fatal("retired real TCP frame entered a later exchange", accepted, err)
		}
	}
	for range 4 {
		if err := <-serverResults; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-received:
		t.Fatal("peer frame pump failed", err)
	default:
	}
}
