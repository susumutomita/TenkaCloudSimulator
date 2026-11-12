package guesttcp

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sync"
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
	"gvisor.dev/gvisor/pkg/waiter"
)

const maxListenerSessions = 8

// ListenConfig is immutable trusted configuration for one passive service and
// one peer. Unlike Config.Port, LocalPort is the listening service's port.
// The service may use IPv4 link-local; the peer must be global-unicast IPv4.
// Guest-selected source ports identify TCP attempts, never instance authority.
type ListenConfig struct {
	LocalIP, PeerIP   netip.Addr
	LocalMAC, PeerMAC [6]byte
	LocalPort         uint16
}

type listenerAttempt struct {
	sequence uint32
	sealed   bool
}

// Listener is a concrete passive gVisor stack, not an OS listener. Only bounded
// IPv4/TCP frames for the configured pair are accepted. It has no host socket,
// dialer, default route, DNS, forwarding, ARP handling or production attachment.
// Its eight stack slots and 32-worker budget are shared with Engine. Eight
// retained source-port attempts bound half-open, pending and accepted sessions;
// closed ports are not reusable. A fresh SYN after a handshake/abort is rejected,
// even on a previously used port. The listening backlog is explicitly eight.
//
// All input/output shares 4096 frames/1 MiB, a 16-frame queue, 16-KiB TCP buffers
// per endpoint and an absolute ten-second default lifetime. NewListenerUntil
// explicitly admits a fixed boot-service window of at most MaxPassiveLifetime.
// An earlier parent deadline always wins; no traffic extends either deadline.
// Invalid frames and exhausted budgets terminate admission. Only one Accept may
// be pending. The caller owns application goroutines, which must remain bounded.
//
// Inject/ReadFrame validate wire tuples, not physical identity or provider policy.
// Returned frames are not dispatch permits. The future physical owner must fence
// actual ingress and delivery with current ownership/revocation. Do not copy a
// Listener after use. Closing it cannot recall bytes already returned.
type Listener struct {
	mu            sync.Mutex
	once          sync.Once
	accepts       sync.WaitGroup
	config        ListenConfig
	parent, ctx   context.Context
	cancel        context.CancelFunc
	expires       time.Time
	stack         *stack.Stack
	link          *channel.Endpoint
	listener      *gonet.TCPListener
	workers       int
	reserved      bool
	closed        bool
	accepting     bool
	ports         map[uint16]listenerAttempt
	connections   map[*listenerConnection]struct{}
	packets, size int
}

// NewListener creates only memory endpoints. The outbound Engine/Transport
// constructors and their global-unicast/fixed-tuple contracts are unchanged.
func NewListener(parent context.Context, c ListenConfig) (*Listener, error) {
	return newListener(parent, c, time.Now().Add(MaxLifetime))
}

// NewListenerUntil explicitly admits a passive boot service until the trusted
// owner's original absolute deadline, no more than MaxPassiveLifetime away.
// The eight lifetime attempts and all cumulative quotas are unchanged, not
// replenished over the longer window. This does not establish owner authority,
// whole-boot success or a persistent, automatically renewed network service.
func NewListenerUntil(parent context.Context, c ListenConfig, deadline time.Time) (*Listener, error) {
	return newListener(parent, c, deadline)
}

func newListener(parent context.Context, c ListenConfig, deadline time.Time) (*Listener, error) {
	if nilInterface(parent) || !c.LocalIP.Is4() || !(c.LocalIP.IsGlobalUnicast() || c.LocalIP.IsLinkLocalUnicast()) || !c.PeerIP.Is4() || !c.PeerIP.IsGlobalUnicast() || c.LocalIP == c.PeerIP || c.LocalPort == 0 || !validMAC(c.LocalMAC) || !validMAC(c.PeerMAC) || c.LocalMAC == c.PeerMAC {
		return nil, ErrConfig
	}
	expires, err := passiveDeadline(parent, deadline)
	if err != nil {
		return nil, err
	}
	select {
	case slots <- struct{}{}:
	default:
		return nil, ErrCapacity
	}
	workers := runtime.GOMAXPROCS(0)
	workerBudget.Lock()
	if workers > maxWorkers || workerBudget.used > maxWorkers-workers {
		workerBudget.Unlock()
		<-slots
		return nil, ErrCapacity
	}
	workerBudget.used += workers
	workerBudget.Unlock()
	ctx, cancel := context.WithDeadline(parent, expires)
	l := &Listener{config: c, parent: parent, ctx: ctx, cancel: cancel, expires: expires,
		workers: workers, reserved: true, ports: make(map[uint16]listenerAttempt), connections: make(map[*listenerConnection]struct{})}
	l.stack = stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{arp.NewProtocol, ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	l.link = channel.New(queueSize, guestnet.MaxFrameBytes, tcpip.LinkAddress(string(c.LocalMAC[:])))
	if runtime.GOMAXPROCS(0) != workers {
		_ = l.Close()
		return nil, ErrCapacity
	}
	fail := func(err tcpip.Error) (*Listener, error) {
		_ = l.Close()
		return nil, fmt.Errorf("private TCP listener setup: %s", err.String())
	}
	if err := l.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpip.TCPSendBufferSizeRangeOption{Min: 4096, Default: 16384, Max: 16384}); err != nil {
		return fail(err)
	}
	if err := l.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpip.TCPReceiveBufferSizeRangeOption{Min: 4096, Default: 16384, Max: 16384}); err != nil {
		return fail(err)
	}
	if err := l.stack.CreateNIC(1, ethernet.New(l.link)); err != nil {
		return fail(err)
	}
	local := tcpip.AddrFrom4(c.LocalIP.As4())
	if err := l.stack.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: local.WithPrefix()}, stack.AddressProperties{}); err != nil {
		return fail(err)
	}
	peer := tcpip.AddrFrom4(c.PeerIP.As4())
	route, err := tcpip.NewSubnet(peer, tcpip.MaskFromBytes([]byte{255, 255, 255, 255}))
	if err != nil {
		_ = l.Close()
		return nil, err
	}
	l.stack.SetRouteTable([]tcpip.Route{{Destination: route, NIC: 1}})
	if err := l.stack.AddStaticNeighbor(1, ipv4.ProtocolNumber, peer, tcpip.LinkAddress(string(c.PeerMAC[:]))); err != nil {
		return fail(err)
	}
	var queue waiter.Queue
	ep, epErr := l.stack.NewEndpoint(tcp.ProtocolNumber, ipv4.ProtocolNumber, &queue)
	if epErr != nil {
		return fail(epErr)
	}
	if err := ep.Bind(tcpip.FullAddress{NIC: 1, Addr: local, Port: c.LocalPort}); err != nil {
		ep.Close()
		return fail(err)
	}
	if err := ep.Listen(maxListenerSessions); err != nil {
		ep.Close()
		return fail(err)
	}
	// gonet.ListenTCP uses backlog 4096; use its endpoint adapter instead.
	l.listener = gonet.NewTCPListener(l.stack, &queue, ep)
	if !l.activeLocked() {
		_ = l.Close()
		return nil, ErrClosed
	}
	go func() { <-ctx.Done(); _ = l.Close() }()
	return l, nil
}

// activeLocked checks absolute expiry as well as cancellation, so a delayed
// timer callback cannot extend admission. After publication callers hold mu.
func (l *Listener) activeLocked() bool {
	return l != nil && !l.closed && l.ctx != nil && l.ctx.Err() == nil && l.parent.Err() == nil && time.Now().Before(l.expires)
}

func (l *Listener) Accept() (net.Conn, error) {
	if l == nil {
		return nil, ErrClosed
	}
	l.mu.Lock()
	if !l.activeLocked() {
		l.mu.Unlock()
		return nil, ErrClosed
	}
	if l.accepting {
		l.mu.Unlock()
		return nil, ErrCapacity
	}
	l.accepting = true
	l.accepts.Add(1)
	l.mu.Unlock()
	raw, err := l.listener.Accept()
	l.mu.Lock()
	l.accepting = false
	if err == nil {
		remote, ok := raw.RemoteAddr().(*net.TCPAddr)
		if !l.activeLocked() || !ok || remote.Port < 1 || remote.Port > 65535 {
			err = ErrClosed
		} else if len(l.connections) >= maxListenerSessions {
			l.cancel()
			err = ErrCapacity
		} else if _, ok := l.ports[uint16(remote.Port)]; !ok {
			err = ErrConfig
		} else {
			attempt := l.ports[uint16(remote.Port)]
			attempt.sealed = true
			l.ports[uint16(remote.Port)] = attempt
			conn := &listenerConnection{raw: raw, owner: l}
			l.connections[conn] = struct{}{}
			l.mu.Unlock()
			l.accepts.Done()
			return conn, nil
		}
	}
	l.mu.Unlock()
	if raw != nil {
		_ = raw.SetDeadline(time.Time{})
		_ = raw.Close()
	}
	l.accepts.Done()
	return nil, err
}

func (l *Listener) Addr() net.Addr {
	if l == nil {
		return nil
	}
	return &net.TCPAddr{IP: net.IP(l.config.LocalIP.AsSlice()), Port: int(l.config.LocalPort)}
}

// Lifetime is this listener's fixed admission lifetime. It is immutable and is
// canceled before Close joins work. A transport may observe it to reject late
// delivery callbacks; it is not physical owner or packet-policy authority.
func (l *Listener) Lifetime() context.Context {
	if l == nil {
		return nil
	}
	return l.ctx
}

func (l *Listener) accountLocked(n int) bool {
	if n < guestnet.MinFrameBytes || n > guestnet.MaxFrameBytes || l.packets >= maxFrames || l.size > maxBytes-n {
		return false
	}
	l.packets++
	l.size += n
	return true
}

// Inject copies an admissible frame before passing it to the TCP implementation.
// The first packet on a new source port must be a payload-free SYN. Retained
// attempt records reject source-port reuse, including after connection close.
func (l *Listener) Inject(frame []byte) error {
	if l == nil {
		return ErrClosed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.activeLocked() {
		return ErrClosed
	}
	fail := func(err error) error { l.cancel(); return err }
	if !l.accountLocked(len(frame)) {
		return fail(ErrCapacity)
	}
	p, err := guestnet.ParseTCPFrame(frame)
	if err != nil {
		return fail(err)
	}
	c := l.config
	if p.SourceMAC != c.PeerMAC || p.DestinationMAC != c.LocalMAC || p.SourceIP != c.PeerIP || p.DestinationIP != c.LocalIP || p.DestinationPort != c.LocalPort {
		return fail(guestnet.ErrPacket)
	}
	attempt, seen := l.ports[p.SourcePort]
	if p.Flags&2 != 0 {
		if p.Flags&(1|4|16) != 0 || p.PayloadLength != 0 || seen && (attempt.sealed || attempt.sequence != p.Sequence) {
			return fail(guestnet.ErrPacket)
		}
		if !seen {
			if len(l.ports) >= maxListenerSessions {
				return fail(ErrCapacity)
			}
			l.ports[p.SourcePort] = listenerAttempt{sequence: p.Sequence}
		}
	} else {
		if !seen {
			return fail(guestnet.ErrPacket)
		}
		attempt.sealed = true
		l.ports[p.SourcePort] = attempt
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), frame...))})
	l.link.InjectInbound(0, pkt)
	pkt.DecRef()
	return nil
}

func (l *Listener) ReadFrame(ctx context.Context) ([]byte, error) {
	if l == nil || nilInterface(ctx) {
		return nil, ErrConfig
	}
	l.mu.Lock()
	active := l.activeLocked()
	l.mu.Unlock()
	if !active || ctx.Err() != nil {
		return nil, ErrClosed
	}
	call, cancel := context.WithCancel(l.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	pkt := l.link.ReadContext(call)
	if pkt == nil {
		return nil, ErrClosed
	}
	defer pkt.DecRef()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.activeLocked() || ctx.Err() != nil {
		return nil, ErrClosed
	}
	if !l.accountLocked(pkt.Size()) {
		l.cancel()
		return nil, ErrCapacity
	}
	view := pkt.ToView()
	defer view.Release()
	frame := append([]byte(nil), view.AsSlice()...)
	p, err := guestnet.ParseTCPFrame(frame)
	c := l.config
	_, admitted := l.ports[p.DestinationPort]
	if err != nil || !admitted || p.SourceMAC != c.LocalMAC || p.DestinationMAC != c.PeerMAC || p.SourceIP != c.LocalIP || p.DestinationIP != c.PeerIP || p.SourcePort != c.LocalPort {
		l.cancel()
		return nil, guestnet.ErrPacket
	}
	return frame, nil
}

// Close gates frame admission, interrupts Accept and every accepted connection,
// clears gonet deadline timers, joins pending Accept, and destroys stack workers
// before returning shared capacity. No external callbacks or stream I/O occur.
func (l *Listener) Close() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		if l.cancel != nil {
			l.cancel()
		}
		if l.link != nil {
			l.link.Attach(nil)
		}
		connections := make([]*listenerConnection, 0, len(l.connections))
		for conn := range l.connections {
			connections = append(connections, conn)
		}
		l.mu.Unlock()
		if l.listener != nil {
			l.listener.Shutdown()
			_ = l.listener.Close()
		}
		for _, conn := range connections {
			_ = conn.Close()
		}
		l.accepts.Wait()
		if l.link != nil {
			l.link.Close()
		}
		if l.stack != nil {
			l.stack.Destroy()
		}
		if l.reserved {
			workerBudget.Lock()
			workerBudget.used -= l.workers
			workerBudget.Unlock()
			<-slots
		}
	})
	return nil
}

type listenerConnection struct {
	raw    net.Conn
	owner  *Listener
	once   sync.Once
	closed bool // guarded by owner.mu
	err    error
}

func (c *listenerConnection) Read(p []byte) (int, error)  { return c.raw.Read(p) }
func (c *listenerConnection) Write(p []byte) (int, error) { return c.raw.Write(p) }
func (c *listenerConnection) LocalAddr() net.Addr         { return c.raw.LocalAddr() }
func (c *listenerConnection) RemoteAddr() net.Addr        { return c.raw.RemoteAddr() }
func (c *listenerConnection) Close() error {
	c.once.Do(func() {
		c.owner.mu.Lock()
		c.closed = true
		c.owner.mu.Unlock()
		_ = c.raw.SetDeadline(time.Time{})
		c.err = c.raw.Close()
	})
	return c.err
}
func (c *listenerConnection) SetDeadline(t time.Time) error {
	return c.setDeadline(t, c.raw.SetDeadline)
}
func (c *listenerConnection) SetReadDeadline(t time.Time) error {
	return c.setDeadline(t, c.raw.SetReadDeadline)
}
func (c *listenerConnection) SetWriteDeadline(t time.Time) error {
	return c.setDeadline(t, c.raw.SetWriteDeadline)
}
func (c *listenerConnection) setDeadline(t time.Time, set func(time.Time) error) error {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if c.closed || !c.owner.activeLocked() {
		return ErrClosed
	}
	if t.IsZero() || t.After(c.owner.expires) {
		t = c.owner.expires
	}
	return set(t)
}
