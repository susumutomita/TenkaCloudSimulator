package guesttcp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"reflect"
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

func listenConfig() ListenConfig {
	return ListenConfig{LocalIP: netip.MustParseAddr("169.254.169.254"), PeerIP: netip.MustParseAddr("10.0.1.4"), LocalMAC: [6]byte{2, 0, 0, 0, 0, 1}, PeerMAC: [6]byte{2, 0, 0, 0, 0, 2}, LocalPort: 80}
}

func listenerFixture(t *testing.T) (*Listener, func(uint16) net.Conn) {
	t.Helper()
	c := listenConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	l, err := NewListener(ctx, c)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	s, link, unused := peer(t, Config{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, Port: c.LocalPort})
	var pumps sync.WaitGroup
	pumps.Go(func() {
		for {
			frame, err := l.ReadFrame(ctx)
			if err != nil {
				return
			}
			pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(frame)})
			link.InjectInbound(0, pkt)
			pkt.DecRef()
		}
	})
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
			if l.Inject(frame) != nil {
				return
			}
			clear(frame) // Stack must not retain caller-owned input.
		}
	})
	var clients []net.Conn
	var mu sync.Mutex
	t.Cleanup(func() {
		cancel()
		_ = l.Close()
		pumps.Wait()
		mu.Lock()
		for _, conn := range clients {
			_ = conn.SetDeadline(time.Time{})
			_ = conn.Close()
		}
		mu.Unlock()
		_ = unused.Close()
		link.Close()
		s.Destroy()
	})
	return l, func(port uint16) net.Conn {
		t.Helper()
		conn, err := gonet.DialTCPWithBind(ctx, s,
			tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(c.PeerIP.As4()), Port: port},
			tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(c.LocalIP.As4()), Port: c.LocalPort}, ipv4.ProtocolNumber)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		clients = append(clients, conn)
		mu.Unlock()
		return conn
	}
}

func TestListenerMemoryConnectionsAndOwnedCleanup(t *testing.T) {
	l, dial := listenerFixture(t)
	var accepted []*listenerConnection
	for n := range maxListenerSessions {
		client := dial(uint16(41000 + n))
		raw, err := l.Accept()
		if err != nil {
			t.Fatal(err)
		}
		conn := raw.(*listenerConnection)
		accepted = append(accepted, conn)
		if conn.LocalAddr().String() != "169.254.169.254:80" || conn.RemoteAddr().String() != client.LocalAddr().String() {
			t.Fatal("incorrect accepted tuple")
		}
		if err := conn.SetDeadline(time.Now().Add(365 * 24 * time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Write([]byte("request")); err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 7)
		if _, err := io.ReadFull(conn, b); err != nil || string(b) != "request" {
			t.Fatalf("actual TCP payload %q %v", b, err)
		}
		if _, err := conn.Write([]byte("reply")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(client, b[:5]); err != nil || string(b[:5]) != "reply" {
			t.Fatalf("actual TCP reply %q %v", b[:5], err)
		}
		_ = conn.Close()
		_ = client.Close()
	}
	l.mu.Lock()
	retained := len(l.ports) == maxListenerSessions && len(l.connections) == maxListenerSessions
	l.mu.Unlock()
	if !retained {
		t.Fatal("closed sessions lost retained capacity/tombstones")
	}
	// The accept loop remains pending after the eighth accept, rather than
	// returning a fatal capacity error that would cut off its final response.
	done := make(chan error, 1)
	go func() { _, err := l.Accept(); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("final accept returned before revocation: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	_ = l.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("accept after close")
		}
	case <-time.After(time.Second):
		t.Fatal("pending accept outlived close")
	}
	for _, conn := range accepted {
		if !errors.Is(conn.SetDeadline(time.Time{}), ErrClosed) {
			t.Fatal("closed connection recreated deadline")
		}
		raw := reflect.ValueOf(conn.raw.(*gonet.TCPConn)).Elem().FieldByName("deadlineTimer")
		if !raw.FieldByName("readTimer").IsNil() || !raw.FieldByName("writeTimer").IsNil() {
			t.Fatal("accepted connection retained deadline timers")
		}
	}
}

func listenFrame(c ListenConfig, port uint16, sequence uint32, flags byte) []byte {
	b := make([]byte, 54)
	copy(b[:6], c.LocalMAC[:])
	copy(b[6:12], c.PeerMAC[:])
	binary.BigEndian.PutUint16(b[12:14], 0x0800)
	ip := b[14:34]
	ip[0], ip[8], ip[9] = 0x45, 64, 6
	binary.BigEndian.PutUint16(ip[2:4], 40)
	source, destination := c.PeerIP.As4(), c.LocalIP.As4()
	copy(ip[12:16], source[:])
	copy(ip[16:20], destination[:])
	checksum := func(b []byte) uint16 {
		var sum uint32
		for len(b) >= 2 {
			sum += uint32(binary.BigEndian.Uint16(b[:2]))
			b = b[2:]
		}
		if len(b) == 1 {
			sum += uint32(b[0]) << 8
		}
		for sum>>16 != 0 {
			sum = sum&0xffff + sum>>16
		}
		return ^uint16(sum)
	}
	binary.BigEndian.PutUint16(ip[10:12], checksum(ip))
	tcp := b[34:]
	binary.BigEndian.PutUint16(tcp[:2], port)
	binary.BigEndian.PutUint16(tcp[2:4], c.LocalPort)
	binary.BigEndian.PutUint32(tcp[4:8], sequence)
	tcp[12], tcp[13] = 5<<4, flags
	binary.BigEndian.PutUint16(tcp[14:16], 16384)
	pseudo := append(append([]byte(nil), ip[12:20]...), 0, 6, 0, 20)
	binary.BigEndian.PutUint16(tcp[16:18], checksum(append(pseudo, tcp...)))
	return b
}

func newListenerForTest(t *testing.T) *Listener {
	t.Helper()
	l, err := NewListener(context.Background(), listenConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestListenerRejectsMalformedTuplesAndPortChurn(t *testing.T) {
	for _, name := range []string{"checksum", "source-mac", "destination-mac", "source-ip", "destination-ip", "port", "non-syn", "syn-ack", "different-sequence", "reused-port"} {
		t.Run(name, func(t *testing.T) {
			l := newListenerForTest(t)
			c := listenConfig()
			flags, sequence := byte(2), uint32(123)
			switch name {
			case "source-mac":
				c.PeerMAC[5]++
			case "destination-mac":
				c.LocalMAC[5]++
			case "source-ip":
				c.PeerIP = netip.MustParseAddr("10.0.1.5")
			case "destination-ip":
				c.LocalIP = netip.MustParseAddr("169.254.169.253")
			case "port":
				c.LocalPort++
			case "non-syn":
				flags = 16
			case "syn-ack":
				flags = 18
			case "different-sequence", "reused-port":
				if err := l.Inject(listenFrame(c, 41000, sequence, 2)); err != nil {
					t.Fatal(err)
				}
				if name == "reused-port" {
					if err := l.Inject(listenFrame(c, 41000, sequence+1, 4)); err != nil {
						t.Fatal(err)
					}
				} else {
					sequence++
				}
			}
			frame := listenFrame(c, 41000, sequence, flags)
			if name == "checksum" {
				frame[50] ^= 1
			}
			if err := l.Inject(frame); err == nil {
				t.Fatal("unsupported frame admitted")
			}
			if err := l.Inject(listenFrame(listenConfig(), 42000, 1, 2)); !errors.Is(err, ErrClosed) {
				t.Fatal("malformed frame did not end admission", err)
			}
		})
	}
}

func TestListenerAttemptAndAggregateBounds(t *testing.T) {
	l := newListenerForTest(t)
	for n := range maxListenerSessions {
		frame := listenFrame(listenConfig(), uint16(41000+n), uint32(n), 2)
		if err := l.Inject(frame); err != nil {
			t.Fatal(err)
		}
		if err := l.Inject(frame); err != nil {
			t.Fatal("pending initial SYN retransmission rejected", err)
		}
	}
	if len(l.ports) != maxListenerSessions || l.Inject(listenFrame(listenConfig(), 42000, 1, 2)) != ErrCapacity {
		t.Fatal("half-open attempt bound missing")
	}
	_ = l.Close()
	for _, kind := range []string{"frames", "bytes", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			l := newListenerForTest(t)
			frame := listenFrame(listenConfig(), 41000, 1, 2)
			l.mu.Lock()
			switch kind {
			case "frames":
				l.packets = maxFrames
			case "bytes":
				l.size = maxBytes - len(frame) + 1
			case "oversized":
				frame = make([]byte, guestnet.MaxFrameBytes+1)
			}
			l.mu.Unlock()
			if err := l.Inject(frame); !errors.Is(err, ErrCapacity) {
				t.Fatal("aggregate budget not enforced", err)
			}
			if _, err := l.ReadFrame(context.Background()); !errors.Is(err, ErrClosed) {
				t.Fatal("budget exhaustion did not gate output")
			}
		})
	}
}

func TestListenerConfigurationSharedCapacityAndExpiry(t *testing.T) {
	for _, change := range []func(*ListenConfig){
		func(c *ListenConfig) { c.LocalIP = c.PeerIP }, func(c *ListenConfig) { c.LocalIP = netip.MustParseAddr("127.0.0.1") },
		func(c *ListenConfig) { c.LocalIP = netip.MustParseAddr("::1") }, func(c *ListenConfig) { c.PeerIP = netip.MustParseAddr("169.254.1.1") },
		func(c *ListenConfig) { c.LocalPort = 0 }, func(c *ListenConfig) { c.LocalMAC = c.PeerMAC }, func(c *ListenConfig) { c.PeerMAC[0] = 1 },
	} {
		c := listenConfig()
		change(&c)
		if l, err := NewListener(context.Background(), c); err == nil {
			_ = l.Close()
			t.Fatal("invalid listener configuration")
		}
	}
	if _, err := NewListener(nil, listenConfig()); !errors.Is(err, ErrConfig) {
		t.Fatal("nil context")
	}
	if _, err := NewListener(elapsedDeadlineContext{context.Background()}, listenConfig()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("elapsed parent deadline accepted", err)
	}
	engine, err := New(config())
	if err != nil {
		t.Fatal(err)
	}
	var listeners []*Listener
	defer func() {
		engine.Close()
		for _, l := range listeners {
			_ = l.Close()
		}
	}()
	for range cap(slots) {
		l, err := NewListener(context.Background(), listenConfig())
		if errors.Is(err, ErrCapacity) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, l)
	}
	if extra, err := New(config()); err == nil {
		extra.Close()
		t.Fatal("listener did not share Engine capacity")
	}
	engine.Close()
	for _, l := range listeners {
		_ = l.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	l, err := NewListener(ctx, listenConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	accept := make(chan error, 1)
	go func() { _, err := l.Accept(); accept <- err }()
	<-ctx.Done()
	if err := l.Inject(listenFrame(listenConfig(), 41000, 1, 2)); !errors.Is(err, ErrClosed) {
		t.Fatal("expired parent extended admission")
	}
	_ = l.Close()
	select {
	case err := <-accept:
		if err == nil {
			t.Fatal("expired accept succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("expired pending accept did not join")
	}
	// Model delayed timer delivery without changing any production clock.
	l = newListenerForTest(t)
	l.mu.Lock()
	l.expires = time.Now().Add(-time.Nanosecond)
	l.mu.Unlock()
	if l.ctx.Err() != nil {
		t.Fatal("timer unexpectedly fired")
	}
	if _, err := l.Accept(); !errors.Is(err, ErrClosed) {
		t.Fatal("elapsed absolute lifetime accepted")
	}
	if err := l.Inject(listenFrame(listenConfig(), 41000, 1, 2)); !errors.Is(err, ErrClosed) {
		t.Fatal("elapsed absolute lifetime injected")
	}
}

func TestListenerCloseRacesAcceptedIOAndDeadlines(t *testing.T) {
	l, dial := listenerFixture(t)
	_ = dial(41000)
	raw, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	conn := raw.(*listenerConnection)
	read := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); read <- err }()
	var setters sync.WaitGroup
	for _, set := range []func(time.Time) error{conn.SetDeadline, conn.SetReadDeadline, conn.SetWriteDeadline} {
		setters.Go(func() {
			for range 32 {
				if err := set(time.Now().Add(time.Hour)); err != nil && !errors.Is(err, ErrClosed) {
					t.Error(err)
				}
			}
		})
	}
	_ = l.Close()
	setters.Wait()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("blocked read survived close")
		}
	case <-time.After(time.Second):
		t.Fatal("accepted read did not join")
	}
	if conn.SetDeadline(time.Time{}) != ErrClosed {
		t.Fatal("closed deadline setter accepted")
	}
}

func TestListenerPendingReadCancellationAndAcceptCloseRace(t *testing.T) {
	l := newListenerForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	read := make(chan error, 1)
	go func() { _, err := l.ReadFrame(ctx); read <- err }()
	cancel()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("canceled frame read succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("frame read ignored cancellation")
	}
	_ = l.Close()
	for n := range 8 {
		t.Run(string(rune('a'+n)), func(t *testing.T) {
			l, dial := listenerFixture(t)
			_ = dial(41000)
			accepted := make(chan net.Conn, 1)
			go func() { conn, _ := l.Accept(); accepted <- conn }()
			_ = l.Close()
			select {
			case conn := <-accepted:
				if conn != nil && conn.SetDeadline(time.Time{}) != ErrClosed {
					t.Fatal("late accepted connection escaped owned cleanup")
				}
			case <-time.After(time.Second):
				t.Fatal("Accept/Close race did not join")
			}
		})
	}
}

func TestListenerCumulativeWireBudgetsIncludeOutput(t *testing.T) {
	for _, kind := range []string{"frames", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			l := newListenerForTest(t)
			frame := listenFrame(listenConfig(), 41000, 123, 2)
			if err := l.Inject(frame); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			response, err := l.ReadFrame(ctx)
			if err != nil {
				t.Fatal(err)
			}
			l.mu.Lock()
			packets, size := l.packets, l.size
			l.mu.Unlock()
			if packets != 2 || size != len(frame)+len(response) {
				t.Fatal("actual output absent from cumulative budget")
			}
			remaining := maxFrames - packets
			if kind == "bytes" {
				frame = append(frame, make([]byte, guestnet.MaxFrameBytes-len(frame))...)
				remaining = (maxBytes - size) / len(frame)
			}
			for range remaining {
				if err := l.Inject(frame); err != nil {
					t.Fatal("wire budget ended before boundary", err)
				}
			}
			if err := l.Inject(frame); err != ErrCapacity {
				t.Fatal("cumulative wire bound missing", err)
			}
		})
	}
}
