package guesttcp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
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

type commitFunc func(context.Context, FrameDirection, []byte, func() error) error

func (f commitFunc) CommitFrame(ctx context.Context, d FrameDirection, frame []byte, apply func() error) error {
	return f(ctx, d, frame, apply)
}

// This is a memory-only test owner, NOT an implementation of an AWS fence.
var memoryCommitter = commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error { return apply() })

// Two bounded chunk queues provide memory stream I/O without host sockets or
// net.Pipe. Each read is fragmented to exercise the actual framing decoder.
type memoryStream struct {
	in, out chan []byte
	closed  chan struct{}
	once    *sync.Once
	pending []byte
}

func memoryStreams() (*memoryStream, *memoryStream) {
	a, b := make(chan []byte, 16), make(chan []byte, 16)
	closed, once := make(chan struct{}), new(sync.Once)
	return &memoryStream{in: a, out: b, closed: closed, once: once}, &memoryStream{in: b, out: a, closed: closed, once: once}
}

func (s *memoryStream) Read(p []byte) (int, error) {
	if len(s.pending) == 0 {
		select {
		case <-s.closed:
			return 0, io.ErrClosedPipe
		case s.pending = <-s.in:
		}
	}
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	default:
	}
	n := min(len(p), len(s.pending), 7)
	copy(p, s.pending[:n])
	s.pending = s.pending[n:]
	return n, nil
}

func (s *memoryStream) Write(p []byte) (int, error) {
	if len(p) > guestnet.MaxFrameBytes+4 {
		return 0, guestnet.ErrFrameSize
	}
	select {
	case <-s.closed:
		return 0, io.ErrClosedPipe
	case s.out <- append([]byte(nil), p...):
		return len(p), nil
	}
}

func (s *memoryStream) Close() error { s.once.Do(func() { close(s.closed) }); return nil }

func arpBinding(c Config) guestnet.ARPConfig {
	return guestnet.ARPConfig{GuestIP: c.PeerIP, GuestMAC: c.PeerMAC, GatewayIP: netip.MustParseAddr("10.0.1.1"), GatewayMAC: c.LocalMAC}
}

func requestARP(c guestnet.ARPConfig) []byte {
	f := make([]byte, 42)
	copy(f[:6], []byte{255, 255, 255, 255, 255, 255})
	copy(f[6:12], c.GuestMAC[:])
	binary.BigEndian.PutUint16(f[12:14], 0x0806)
	binary.BigEndian.PutUint16(f[14:16], 1)
	binary.BigEndian.PutUint16(f[16:18], 0x0800)
	f[18], f[19] = 6, 4
	binary.BigEndian.PutUint16(f[20:22], 1)
	copy(f[22:28], c.GuestMAC[:])
	ip, gateway := c.GuestIP.As4(), c.GatewayIP.As4()
	copy(f[28:32], ip[:])
	copy(f[38:42], gateway[:])
	return f
}

// Unlike peer() in engine_test, this independent stack has NO static neighbor
// for the remote source. Its off-subnet return route requires actual ARP for
// GatewayIP. The transport must answer before real TCP/HTTP can complete.
func gatewayPeer(t *testing.T, c Config, ac guestnet.ARPConfig) (*stack.Stack, *channel.Endpoint, net.Listener) {
	t.Helper()
	s := stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{arp.NewProtocol, ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	link := channel.New(16, guestnet.MaxFrameBytes, tcpip.LinkAddress(string(c.PeerMAC[:])))
	if err := s.CreateNIC(1, ethernet.New(link)); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4(c.PeerIP.As4()), PrefixLen: 24}}, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	subnet, err := tcpip.NewSubnet(tcpip.AddrFrom4(c.LocalIP.As4()), tcpip.MaskFromBytes([]byte{255, 255, 255, 255}))
	if err != nil {
		t.Fatal(err)
	}
	s.SetRouteTable([]tcpip.Route{{Destination: subnet, Gateway: tcpip.AddrFrom4(ac.GatewayIP.As4()), NIC: 1}})
	ln, listenErr := gonet.ListenTCP(s, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(c.PeerIP.As4()), Port: c.Port}, ipv4.ProtocolNumber)
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	return s, link, ln
}

func TestFramedTransportGatewayARPAndHTTP(t *testing.T) {
	c := config()
	ac := arpBinding(c)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	local, remote := memoryStreams()
	var requests, replies atomic.Int32
	owner := commitFunc(func(_ context.Context, d FrameDirection, frame []byte, apply func() error) error {
		if binary.BigEndian.Uint16(frame[12:14]) == 0x0806 {
			if d == FromGuest {
				requests.Add(1)
			} else {
				replies.Add(1)
			}
		}
		// Metadata given to the owner cannot mutate bytes actually delivered.
		clear(frame)
		return apply()
	})
	tr, err := NewTransport(ctx, c, local, owner, &ac)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	s, link, ln := gatewayPeer(t, c, ac)
	defer s.Destroy()
	defer link.Close()
	defer ln.Close()
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		defer pumps.Done()
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
		defer pumps.Done()
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
			if err := encoder.WriteFrame(frame); err != nil {
				return
			}
		}
	}()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("framed TCP through resolved gateway"))
	})}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(ln) }()
	defer func() { _ = server.Close(); cancel(); _ = tr.Close(); pumps.Wait(); <-served }()
	conn, err := tr.Dial(ctx)
	if err != nil {
		t.Fatalf("real gateway handshake: %v (transport %v)", err, tr.Wait())
	}
	httpTransport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil }}
	defer httpTransport.CloseIdleConnections()
	client := &http.Client{Transport: httpTransport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get("http://private.invalid/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "framed TCP through resolved gateway" {
		t.Fatalf("HTTP result: status=%d body=%q error=%v", response.StatusCode, body, err)
	}
	if requests.Load() == 0 || requests.Load() != replies.Load() {
		t.Fatalf("gateway resolution did not cross both fences: requests=%d replies=%d", requests.Load(), replies.Load())
	}
}

type fixedReadStream struct {
	io.Reader
	writes, closes atomic.Int32
}

func (s *fixedReadStream) Write(p []byte) (int, error) { s.writes.Add(1); return len(p), nil }
func (s *fixedReadStream) Close() error                { s.closes.Add(1); return nil }

func framedBytes(frame []byte) []byte {
	var data bytes.Buffer
	_ = guestnet.NewEncoder(&data).WriteFrame(frame)
	return data.Bytes()
}

func reverseTCPFrame(c Config) []byte {
	f := make([]byte, 54)
	copy(f[:6], c.LocalMAC[:])
	copy(f[6:12], c.PeerMAC[:])
	binary.BigEndian.PutUint16(f[12:14], 0x0800)
	ip := f[14:34]
	ip[0], ip[8], ip[9] = 0x45, 64, 6
	binary.BigEndian.PutUint16(ip[2:4], 40)
	source, destination := c.PeerIP.As4(), c.LocalIP.As4()
	copy(ip[12:16], source[:])
	copy(ip[16:20], destination[:])
	tcp := f[34:]
	binary.BigEndian.PutUint16(tcp[:2], c.Port)
	binary.BigEndian.PutUint16(tcp[2:4], LocalPort)
	tcp[12], tcp[13] = 0x50, 0x10
	binary.BigEndian.PutUint16(tcp[14:16], 4096)
	checksum := func(data []byte) uint16 {
		var sum uint32
		for i := 0; i < len(data); i += 2 {
			sum += uint32(binary.BigEndian.Uint16(data[i : i+2]))
		}
		for sum>>16 != 0 {
			sum = (sum & 0xffff) + sum>>16
		}
		return ^uint16(sum)
	}
	binary.BigEndian.PutUint16(ip[10:12], checksum(ip))
	pseudo := append(append([]byte(nil), ip[12:20]...), 0, 6, 0, 20)
	pseudo = append(pseudo, tcp...)
	binary.BigEndian.PutUint16(tcp[16:18], checksum(pseudo))
	return f
}

func TestFramedTransportRejectsInputAndCloses(t *testing.T) {
	c := config()
	ac := arpBinding(c)
	badARP := requestARP(ac)
	badARP[22] ^= 2
	unknown := make([]byte, 60)
	binary.BigEndian.PutUint16(unknown[12:14], 0x86dd)
	badTCP := reverseTCPFrame(c)
	if _, err := guestnet.ParseTCPFrame(badTCP); err != nil {
		t.Fatalf("invalid TCP test fixture: %v", err)
	}
	wrongTuple := append([]byte(nil), badTCP...)
	wrongTuple[6] ^= 2
	badTCP[40] ^= 1 // Corrupt TCP checksum.
	for _, tc := range []struct {
		name string
		wire []byte
		want error
		arp  *guestnet.ARPConfig
	}{
		{"truncated header", []byte{0, 0}, io.ErrUnexpectedEOF, nil},
		{"undersized", []byte{0, 0, 0, 13}, guestnet.ErrFrameSize, nil},
		{"oversized", []byte{0, 0, 5, 235}, guestnet.ErrFrameSize, nil},
		{"truncated body", []byte{0, 0, 0, 60, 1}, io.ErrUnexpectedEOF, nil},
		{"unknown type", framedBytes(unknown), guestnet.ErrPacket, nil},
		{"bad TCP checksum", framedBytes(badTCP), guestnet.ErrPacket, nil},
		{"wrong TCP tuple", framedBytes(wrongTuple), guestnet.ErrPacket, nil},
		{"ARP disabled", framedBytes(requestARP(ac)), guestnet.ErrPacket, nil},
		{"spoofed ARP", framedBytes(badARP), guestnet.ErrARP, &ac},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := &fixedReadStream{Reader: bytes.NewReader(tc.wire)}
			var commits atomic.Int32
			owner := commitFunc(func(context.Context, FrameDirection, []byte, func() error) error { commits.Add(1); return nil })
			tr, err := NewTransport(context.Background(), c, stream, owner, tc.arp)
			if err != nil {
				t.Fatal(err)
			}
			if err := tr.Wait(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if stream.closes.Load() != 1 || stream.writes.Load() != 0 || commits.Load() != 0 {
				t.Fatalf("invalid frame escaped: closes=%d writes=%d commits=%d", stream.closes.Load(), stream.writes.Load(), commits.Load())
			}
			if len(tc.wire) >= 4 && int(binary.BigEndian.Uint32(tc.wire[:4])) == len(tc.wire)-4 && tr.packets != 1 {
				t.Fatal("rejected decoded input did not consume frame budget")
			}
		})
	}
}

// Close interrupts either operation without acquiring a lock held by Write.
type blockedStream struct {
	closed, writeStarted, readExited, writeExited chan struct{}
	once                                          sync.Once
	writes, closes                                atomic.Int32
	short                                         bool
}

func newBlockedStream(short bool) *blockedStream {
	return &blockedStream{closed: make(chan struct{}), writeStarted: make(chan struct{}), readExited: make(chan struct{}), writeExited: make(chan struct{}), short: short}
}

func (s *blockedStream) Read([]byte) (int, error) {
	defer close(s.readExited)
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockedStream) Write(p []byte) (int, error) {
	s.writes.Add(1)
	close(s.writeStarted)
	defer close(s.writeExited)
	if s.short {
		return len(p) - 1, nil
	}
	<-s.closed
	return 0, io.ErrClosedPipe
}

func (s *blockedStream) Close() error {
	s.once.Do(func() { s.closes.Add(1); close(s.closed) })
	return nil
}

func awaitSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestFramedTransportCancelsBlockedIOAndJoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newBlockedStream(false)
	var ownerLock sync.Mutex
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		ownerLock.Lock()
		defer ownerLock.Unlock()
		return apply()
	})
	tr, err := NewTransport(ctx, config(), stream, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	dialDone := make(chan struct{})
	go func() { defer close(dialDone); _, _ = tr.Dial(context.Background()) }()
	awaitSignal(t, stream.writeStarted, "blocked write inside owner fence")
	cancel()
	awaitSignal(t, tr.done, "joined transport cancellation")
	awaitSignal(t, dialDone, "cancelled dial")
	awaitSignal(t, stream.readExited, "read worker exit")
	awaitSignal(t, stream.writeExited, "write worker exit")
	if !errors.Is(tr.Wait(), context.Canceled) || stream.closes.Load() != 1 {
		t.Fatalf("cancellation %v, close calls %d", tr.Wait(), stream.closes.Load())
	}
}

func TestFramedTransportCancelsPendingAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newBlockedStream(false)
	entered, exited := make(chan struct{}), make(chan struct{})
	owner := commitFunc(func(ctx context.Context, _ FrameDirection, _ []byte, _ func() error) error {
		close(entered)
		defer close(exited)
		<-ctx.Done()
		return ctx.Err()
	})
	tr, err := NewTransport(ctx, config(), stream, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	dialDone := make(chan struct{})
	go func() { defer close(dialDone); _, _ = tr.Dial(context.Background()) }()
	awaitSignal(t, entered, "pending owner admission")
	cancel()
	awaitSignal(t, tr.done, "pending admission teardown")
	awaitSignal(t, exited, "owner exit")
	awaitSignal(t, dialDone, "pending admission dial")
	if stream.writes.Load() != 0 || stream.closes.Load() != 1 {
		t.Fatal("pending admission emitted output or escaped closure")
	}
}

func TestFramedTransportOwnerRevokesBlockedWriteBeforeTakingFence(t *testing.T) {
	stream := newBlockedStream(false)
	var fence sync.Mutex
	revoked := false
	denied := errors.New("test owner revoked")
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		fence.Lock()
		defer fence.Unlock()
		if revoked {
			return denied
		}
		return apply()
	})
	tr, err := NewTransport(context.Background(), config(), stream, owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	dialDone := make(chan struct{})
	go func() { defer close(dialDone); _, _ = tr.Dial(context.Background()) }()
	awaitSignal(t, stream.writeStarted, "write holding owner fence")
	revocationDone := make(chan struct{})
	go func() {
		// This ordering is part of the trusted owner contract. Acquiring fence
		// before Close would deadlock against the outstanding blocked write.
		_ = stream.Close()
		fence.Lock()
		revoked = true
		fence.Unlock()
		close(revocationDone)
	}()
	awaitSignal(t, revocationDone, "physical close before owner revocation")
	select {
	case <-stream.writeExited:
	default:
		t.Fatal("revocation completed while prior delivery was still active")
	}
	awaitSignal(t, tr.done, "revoked transport join")
	awaitSignal(t, dialDone, "revoked dial join")
	if err := owner.CommitFrame(context.Background(), ToGuest, nil, func() error { t.Error("post-revocation delivery"); return nil }); !errors.Is(err, denied) {
		t.Fatalf("owner did not retain revocation: %v", err)
	}
}

func TestFramedTransportShortWriteNeverRetries(t *testing.T) {
	for _, ignoreApplyError := range []bool{false, true} {
		t.Run(map[bool]string{false: "propagated", true: "owner ignores error"}[ignoreApplyError], func(t *testing.T) {
			stream := newBlockedStream(true)
			owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
				err := apply()
				if ignoreApplyError {
					return nil
				}
				return err
			})
			tr, err := NewTransport(context.Background(), config(), stream, owner, nil)
			if err != nil {
				t.Fatal(err)
			}
			dialDone := make(chan struct{})
			go func() { defer close(dialDone); _, _ = tr.Dial(context.Background()) }()
			awaitSignal(t, tr.done, "short write teardown")
			awaitSignal(t, dialDone, "short write dial cancellation")
			if !errors.Is(tr.Wait(), io.ErrShortWrite) || stream.writes.Load() != 1 || stream.closes.Load() != 1 {
				t.Fatalf("short write retried or ignored: %v writes=%d closes=%d", tr.Wait(), stream.writes.Load(), stream.closes.Load())
			}
		})
	}
}

func TestFramedTransportRejectsConstructorAndKeepsCallerOwnership(t *testing.T) {
	c := config()
	ac := arpBinding(c)
	badBinding := ac
	badBinding.GuestMAC[5]++
	badGateway := ac
	badGateway.GatewayIP = ac.GuestIP
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	var nilStream *blockedStream
	var nilOwner commitFunc
	for _, tc := range []struct {
		ctx    context.Context
		config Config
		stream io.ReadWriteCloser
		owner  FrameCommitter
		arp    *guestnet.ARPConfig
	}{
		{nil, c, newBlockedStream(false), memoryCommitter, nil},
		{cancelled, c, newBlockedStream(false), memoryCommitter, nil},
		{context.Background(), Config{}, newBlockedStream(false), memoryCommitter, nil},
		{context.Background(), c, nilStream, memoryCommitter, nil},
		{context.Background(), c, newBlockedStream(false), nilOwner, nil},
		{context.Background(), c, newBlockedStream(false), memoryCommitter, &badBinding},
		{context.Background(), c, newBlockedStream(false), memoryCommitter, &badGateway},
	} {
		tr, err := NewTransport(tc.ctx, tc.config, tc.stream, tc.owner, tc.arp)
		if err == nil {
			_ = tr.Close()
			t.Fatal("invalid transport accepted")
		}
		if s, ok := tc.stream.(*blockedStream); ok && s != nil && s.closes.Load() != 0 {
			t.Fatal("constructor error took caller's stream ownership")
		}
	}
}

func TestFramedTransportCommitScopeRejectsMisuse(t *testing.T) {
	for _, behavior := range []string{"no invocation", "duplicate invocation", "retained invocation"} {
		t.Run(behavior, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var late func() error
			applies := 0
			owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
				switch behavior {
				case "duplicate invocation":
					_ = apply()
					_ = apply()
				case "retained invocation":
					late = apply
				}
				return nil
			})
			tr := &Transport{ctx: ctx, owner: owner}
			if err := tr.commit(ToGuest, []byte{1, 2}, func() error { applies++; return nil }); !errors.Is(err, ErrCommit) {
				t.Fatalf("bad owner accepted: %v", err)
			}
			if late != nil && !errors.Is(late(), ErrCommit) {
				t.Fatal("retained callback remained active")
			}
			want := 0
			if behavior == "duplicate invocation" {
				want = 1
			}
			if applies != want {
				t.Fatalf("delivered %d times, want %d", applies, want)
			}
		})
	}
}

func TestFramedTransportCommitJoinsRacingCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, callbackDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	owner := commitFunc(func(_ context.Context, _ FrameDirection, _ []byte, apply func() error) error {
		// Deliberately violates the synchronous contract. The transport cannot
		// prove same-goroutine execution, but must join a callback already inside.
		go func() { defer close(callbackDone); _ = apply() }()
		<-entered
		return nil
	})
	tr := &Transport{ctx: ctx, owner: owner}
	done := make(chan error, 1)
	go func() {
		done <- tr.commit(ToGuest, nil, func() error { close(entered); <-release; return nil })
	}()
	awaitSignal(t, entered, "racing callback")
	select {
	case <-done:
		t.Fatal("commit scope returned with callback still active")
	default:
	}
	close(release)
	awaitSignal(t, callbackDone, "joined callback")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("commit scope did not finish")
	}
}

func TestFramedTransportCommitRejectsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tr := &Transport{ctx: ctx, owner: memoryCommitter}
	called := false
	if err := tr.commit(ToGuest, nil, func() error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled commit applied: error=%v called=%v", err, called)
	}
}

func TestFramedTransportEngineTerminationClosesStream(t *testing.T) {
	stream := newBlockedStream(false)
	tr, err := NewTransport(context.Background(), config(), stream, memoryCommitter, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr.engine.Close()
	awaitSignal(t, tr.done, "engine termination")
	if tr.Wait() == nil || stream.closes.Load() != 1 {
		t.Fatal("engine termination left stream open")
	}
	if err := tr.Close(); err != nil || stream.closes.Load() != 1 {
		t.Fatalf("repeated close: %v calls=%d", err, stream.closes.Load())
	}
}

func TestFramedTransportRevocationBetweenARPCheckAndWrite(t *testing.T) {
	c := config()
	ac := arpBinding(c)
	local, remote := memoryStreams()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ready, release := make(chan struct{}), make(chan struct{})
	var revoked atomic.Bool
	denied := errors.New("test owner revoked")
	owner := commitFunc(func(ctx context.Context, d FrameDirection, _ []byte, apply func() error) error {
		if d == ToGuest {
			close(ready)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			if revoked.Load() {
				return denied
			}
		}
		return apply()
	})
	tr, err := NewTransport(ctx, c, local, owner, &ac)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if err := guestnet.NewEncoder(remote).WriteFrame(requestARP(ac)); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, ready, "ARP outbound admission")
	revoked.Store(true)
	close(release)
	awaitSignal(t, tr.done, "revoked ARP teardown")
	if !errors.Is(tr.Wait(), denied) || len(remote.in) != 0 {
		t.Fatalf("ARP emitted after owner rejection: %v queued=%d", tr.Wait(), len(remote.in))
	}
}

func TestFramedTransportOwnerRejectsInboundBeforeDelivery(t *testing.T) {
	for _, protocol := range []string{"TCP", "ARP"} {
		t.Run(protocol, func(t *testing.T) {
			c := config()
			ac := arpBinding(c)
			frame := reverseTCPFrame(c)
			if protocol == "ARP" {
				frame = requestARP(ac)
			}
			local, remote := memoryStreams()
			denied := errors.New("test owner denied ingress")
			owner := commitFunc(func(_ context.Context, d FrameDirection, _ []byte, _ func() error) error {
				if d != FromGuest {
					t.Error("denied ingress produced output")
				}
				return denied
			})
			tr, err := NewTransport(context.Background(), c, local, owner, &ac)
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			if err := guestnet.NewEncoder(remote).WriteFrame(frame); err != nil {
				t.Fatal(err)
			}
			awaitSignal(t, tr.done, "denied ingress teardown")
			if !errors.Is(tr.Wait(), denied) || tr.engine.packets != 0 || tr.packets != 1 || len(remote.in) != 0 {
				t.Fatalf("denied ingress delivered: %v engine=%d transport=%d queued=%d", tr.Wait(), tr.engine.packets, tr.packets, len(remote.in))
			}
		})
	}
}

func TestFramedTransportARPSharesAggregateBudgets(t *testing.T) {
	for _, budget := range []string{"packets", "bytes", "inbound packets"} {
		t.Run(budget, func(t *testing.T) {
			c := config()
			ac := arpBinding(c)
			local, remote := memoryStreams()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			tr, err := NewTransport(ctx, c, local, memoryCommitter, &ac)
			if err != nil {
				t.Fatal(err)
			}
			defer tr.Close()
			// Seed the shared total as though earlier TCP frames consumed it.
			// An ARP request fits, but its reply must not bypass that total.
			tr.budgetMu.Lock()
			if budget == "packets" {
				tr.packets = maxFrames - 1
			} else if budget == "inbound packets" {
				tr.packets = maxFrames
			} else {
				tr.bytes = maxBytes - 42
			}
			tr.budgetMu.Unlock()
			if err := guestnet.NewEncoder(remote).WriteFrame(requestARP(ac)); err != nil {
				t.Fatal(err)
			}
			awaitSignal(t, tr.done, "ARP budget teardown")
			if !errors.Is(tr.Wait(), ErrCapacity) || len(remote.in) != 0 {
				t.Fatalf("ARP bypassed budget: %v queued=%d", tr.Wait(), len(remote.in))
			}
		})
	}
}
