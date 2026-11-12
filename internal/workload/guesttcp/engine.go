// Package guesttcp adapts the established gVisor userspace TCP implementation to
// bounded, private Ethernet frames. It opens no host socket and attaches no VM.
// This package is not wired into production provisioning or packet authorization.
package guesttcp

import (
	"context"
	"errors"
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
)

const (
	LocalPort   uint16 = 40000
	MaxLifetime        = 10 * time.Second
	maxFrames          = 4096
	maxBytes           = 1 << 20
	queueSize          = 16
)

var (
	ErrClosed   = errors.New("private TCP engine closed")
	ErrConfig   = errors.New("invalid private TCP binding")
	ErrCapacity = errors.New("private TCP capacity exhausted")
	slots       = make(chan struct{}, 8)
)

// Config is immutable trusted runtime configuration, not guest/HTTP input.
// A single engine can dial exactly this peer/port once. Both addresses and MACs
// must come from the physical owner's binding, which is not implemented here.
type Config struct {
	LocalIP, PeerIP   netip.Addr
	LocalMAC, PeerMAC [6]byte
	Port              uint16
	// SourcePort is selected by the trusted caller, never a guest or request.
	// Zero preserves LocalPort. The caller owns tuple uniqueness across engines.
	SourcePort uint16
}

func (c Config) normalized() Config {
	if c.SourcePort == 0 {
		c.SourcePort = LocalPort
	}
	return c
}

var workerBudget struct {
	sync.Mutex
	used int
}

const maxWorkers = 32

type Engine struct {
	workers         int
	mu              sync.Mutex
	once            sync.Once
	dial            sync.WaitGroup
	config          Config
	stack           *stack.Stack
	link            *channel.Endpoint
	conn            net.Conn // Registered under mu before dial.Done; closed by the owner.
	ctx             context.Context
	cancel          context.CancelFunc
	closed, started bool
	packets, bytes  int
}

// New makes only an in-memory IPv4/TCP stack. The sole route is the fixed peer
// /32 and a static neighbor avoids unbounded ARP discovery. No host dialer, DNS,
// NAT, forwarding, default route, listeners, IPv6, GSO or checksum offload exists.
// This first adapter rejects ARP packets; a real guest's address/gateway setup
// and bounded ARP integration remain separate prerequisites.
func New(c Config) (*Engine, error) {
	c = c.normalized()
	if !c.LocalIP.Is4() || !c.PeerIP.Is4() || !c.LocalIP.IsGlobalUnicast() || !c.PeerIP.IsGlobalUnicast() || c.LocalIP == c.PeerIP || c.Port == 0 || !validMAC(c.LocalMAC) || !validMAC(c.PeerMAC) || c.LocalMAC == c.PeerMAC {
		return nil, ErrConfig
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
	ctx, cancel := context.WithTimeout(context.Background(), MaxLifetime)
	e := &Engine{config: c, ctx: ctx, cancel: cancel, workers: workers}
	e.stack = stack.New(stack.Options{NetworkProtocols: []stack.NetworkProtocolFactory{arp.NewProtocol, ipv4.NewProtocol}, TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol}})
	e.link = channel.New(queueSize, guestnet.MaxFrameBytes, tcpip.LinkAddress(string(c.LocalMAC[:])))
	if runtime.GOMAXPROCS(0) != workers {
		e.Close()
		return nil, ErrCapacity
	}
	fail := func(err tcpip.Error) (*Engine, error) {
		e.Close()
		return nil, fmt.Errorf("private TCP setup: %s", err.String())
	}
	if err := e.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpip.TCPSendBufferSizeRangeOption{Min: 4096, Default: 16384, Max: 16384}); err != nil {
		return fail(err)
	}
	if err := e.stack.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpip.TCPReceiveBufferSizeRangeOption{Min: 4096, Default: 16384, Max: 16384}); err != nil {
		return fail(err)
	}
	if err := e.stack.CreateNIC(1, ethernet.New(e.link)); err != nil {
		return fail(err)
	}
	addr := tcpip.AddrFrom4(c.LocalIP.As4())
	if err := e.stack.AddProtocolAddress(1, tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: addr.WithPrefix()}, stack.AddressProperties{}); err != nil {
		return fail(err)
	}
	peer := tcpip.AddrFrom4(c.PeerIP.As4())
	route, err := tcpip.NewSubnet(peer, tcpip.MaskFromBytes([]byte{255, 255, 255, 255}))
	if err != nil {
		e.Close()
		return nil, err
	}
	e.stack.SetRouteTable([]tcpip.Route{{Destination: route, NIC: 1}})
	if err := e.stack.AddStaticNeighbor(1, ipv4.ProtocolNumber, peer, tcpip.LinkAddress(string(c.PeerMAC[:]))); err != nil {
		return fail(err)
	}
	go func() { <-ctx.Done(); e.Close() }()
	return e, nil
}
func validMAC(m [6]byte) bool { return m != ([6]byte{}) && m[0]&1 == 0 }

// Dial creates one real userspace TCP connection. Cancellation stops the entire
// single-use engine; retries require a separately authorized new incarnation.
func (e *Engine) Dial(ctx context.Context) (net.Conn, error) {
	if ctx == nil {
		return nil, ErrConfig
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	if e.closed || e.ctx.Err() != nil {
		e.mu.Unlock()
		return nil, ErrClosed
	}
	if e.started {
		e.mu.Unlock()
		return nil, ErrCapacity
	}
	e.started = true
	e.dial.Add(1)
	e.mu.Unlock()
	call, cancel := context.WithCancel(e.ctx)
	stop := context.AfterFunc(ctx, cancel)
	conn, err := gonet.DialTCPWithBind(call, e.stack, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(e.config.LocalIP.As4()), Port: e.config.SourcePort}, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(e.config.PeerIP.As4()), Port: e.config.Port}, ipv4.ProtocolNumber)
	stop()
	cancel()
	e.mu.Lock()
	if err == nil {
		if e.closed || e.ctx.Err() != nil || ctx.Err() != nil {
			_ = conn.Close()
			err = ErrClosed
		} else {
			// Close must own the connection before dial.Done allows stack
			// teardown, even if it races with returning the wrapper below.
			e.conn = conn
		}
	}
	e.mu.Unlock()
	e.dial.Done()
	if err != nil {
		e.Close()
		return nil, err
	}
	return &connection{conn: conn, owner: e}, nil
}

type connection struct {
	// Do not embed the underlying connection: its deadline methods create
	// timers that gonet.TCPConn.Close does not stop.
	conn  net.Conn
	owner *Engine
}

func (c *connection) Read(b []byte) (int, error)  { return c.conn.Read(b) }
func (c *connection) Write(b []byte) (int, error) { return c.conn.Write(b) }
func (c *connection) LocalAddr() net.Addr         { return c.conn.LocalAddr() }
func (c *connection) RemoteAddr() net.Addr        { return c.conn.RemoteAddr() }
func (c *connection) Close() error                { c.owner.Close(); return nil }

// Deadlines may shorten, but never extend, the owner's absolute lifetime.
// A zero time removes the caller's timeout only up to the engine's expiry.
// Read and Write never hold owner.mu: closing the endpoint must interrupt them.
func (c *connection) SetDeadline(t time.Time) error {
	return c.setDeadline(t, c.conn.SetDeadline)
}
func (c *connection) SetReadDeadline(t time.Time) error {
	return c.setDeadline(t, c.conn.SetReadDeadline)
}
func (c *connection) SetWriteDeadline(t time.Time) error {
	return c.setDeadline(t, c.conn.SetWriteDeadline)
}
func (c *connection) setDeadline(t time.Time, set func(time.Time) error) error {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if c.owner.closed || c.owner.ctx.Err() != nil {
		return ErrClosed
	}
	expires, _ := c.owner.ctx.Deadline() // New always supplies an absolute deadline.
	if !time.Now().Before(expires) {
		return ErrClosed // Context timer delivery may lag the absolute deadline.
	}
	if t.IsZero() || t.After(expires) {
		t = expires
	}
	return set(t)
}

// Inject accepts only checksum-valid TCP frames for the fixed reverse tuple.
// It serializes injection with revocation. It does NOT authorize AWS traffic:
// the caller must enforce current provider policy before invoking it.
func (e *Engine) Inject(frame []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.ctx.Err() != nil || !e.started {
		return ErrClosed
	}
	if !e.account(len(frame)) {
		e.cancel()
		return ErrCapacity
	}
	p, err := guestnet.ParseTCPFrame(frame)
	if err != nil {
		return err
	}
	c := e.config
	if p.SourceMAC != c.PeerMAC || p.DestinationMAC != c.LocalMAC || p.SourceIP != c.PeerIP || p.DestinationIP != c.LocalIP || p.SourcePort != c.Port || p.DestinationPort != c.SourcePort {
		return guestnet.ErrPacket
	}
	// The decoder buffer remains caller-owned. Copy before delivering to workers.
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(append([]byte(nil), frame...))})
	e.link.InjectInbound(0, pkt)
	pkt.DecRef()
	return nil
}
func (e *Engine) account(n int) bool {
	if n < 0 || n > guestnet.MaxFrameBytes || e.packets >= maxFrames || e.bytes > maxBytes-n {
		return false
	}
	e.packets++
	e.bytes += n
	return true
}

// ReadFrame returns one actual TCP Ethernet frame from the userspace stack.
// Returned bytes are NOT a dispatch permit: a physical owner must revalidate its
// lease/policy generation and serialize revocation with actual transport writes.
// Closing this engine cannot recall bytes already returned to a caller.
func (e *Engine) ReadFrame(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		return nil, ErrConfig
	}
	call, cancel := context.WithCancel(e.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	pkt := e.link.ReadContext(call)
	if pkt == nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrClosed
	}
	defer pkt.DecRef()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.ctx.Err() != nil || ctx.Err() != nil {
		return nil, ErrClosed
	}
	if !e.account(pkt.Size()) {
		e.cancel()
		return nil, ErrCapacity
	}
	view := pkt.ToView()
	defer view.Release()
	frame := append([]byte(nil), view.AsSlice()...)
	p, err := guestnet.ParseTCPFrame(frame)
	if err != nil {
		e.cancel()
		return nil, err
	}
	c := e.config
	if p.SourceMAC != c.LocalMAC || p.DestinationMAC != c.PeerMAC || p.SourceIP != c.LocalIP || p.DestinationIP != c.PeerIP || p.SourcePort != c.SourcePort || p.DestinationPort != c.Port {
		e.cancel()
		return nil, guestnet.ErrPacket
	}
	return frame, nil
}

// Close first revokes injection/output and cancels pending dial, then waits for
// endpoint creation to finish before destroying the stack. It also clears the
// gonet deadline timers, which TCPConn.Close does not stop. channel.Close alone
// would NOT stop InjectInbound. Slots are released only after worker teardown.
func (e *Engine) Close() {
	e.once.Do(func() {
		e.mu.Lock()
		e.closed = true
		e.cancel()
		e.link.Attach(nil)
		conn := e.conn
		e.conn = nil
		e.mu.Unlock()
		if conn != nil {
			// Revocation and deadline mutation share mu, so no caller can
			// recreate a timer after this raw (unclamped) reset.
			_ = conn.SetDeadline(time.Time{})
			_ = conn.Close()
		}
		e.dial.Wait()
		e.link.Close()
		e.stack.Destroy()
		workerBudget.Lock()
		workerBudget.used -= e.workers
		workerBudget.Unlock()
		<-slots
	})
}
