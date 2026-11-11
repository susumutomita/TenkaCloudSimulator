package guesttcp

import (
	"context"
	"encoding/binary"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type issuedPersistentMemory struct {
	*issuedMemoryResource
	descriptor OwnedProbeDescriptor
}

func (r *issuedPersistentMemory) PersistentDescriptor() OwnedProbeDescriptor { return r.descriptor }
func persistentOwnedDescriptor() OwnedProbeDescriptor {
	c := ownedConfig()
	c.Paths = []string{"/", "/healthz"}
	a := arpBinding(config())
	return OwnedProbeDescriptor{Config: c, Binding: scopeBinding(), Services: []HTTPProbeService{{Name: "web", Port: 80, Path: "/healthz"}}, Passive: transportServiceRoute(), ARP: &a}
}
func TestOwnedProbeAttachmentRealMemoryTCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	local, remote := memoryStreams()
	var arpIn, arpOut, passiveIn, passiveOut atomic.Int32
	r := &issuedPersistentMemory{issuedMemoryResource: &issuedMemoryResource{stream: local, transaction: func(_ context.Context, a OwnedProbeAcceptance, f func() error) error {
		if a.Descriptor == nil || a.Descriptor.Binding != scopeBinding() {
			t.Error("lost issuance descriptor")
		}
		if len(a.Frame) >= 14 && binary.BigEndian.Uint16(a.Frame[12:14]) == 0x806 {
			if a.Direction == FromGuest {
				arpIn.Add(1)
			} else {
				arpOut.Add(1)
			}
		}
		if packet, err := guestnet.ParseTCPFrame(a.Frame); err == nil {
			route := transportServiceRoute()
			if a.Direction == FromGuest && packet.DestinationIP == route.LocalIP && packet.DestinationPort == route.LocalPort {
				passiveIn.Add(1)
			}
			if a.Direction == ToGuest && packet.SourceIP == route.LocalIP && packet.SourcePort == route.LocalPort {
				passiveOut.Add(1)
			}
		}
		return f()
	}}, descriptor: persistentOwnedDescriptor()}
	owner, e := NewOwnedPersistentProbeSession(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	passive := passiveHTTPFixtureUntil(t, ctx, time.Now().Add(3*time.Second))
	admission, stopAdmission := context.WithCancel(context.Background())
	h, e := owner.AttachPersistent(admission, passive, time.Now().Add(3*time.Second))
	if e != nil {
		_ = passive.Close()
		if h != nil {
			_ = h.CloseContext(ctx)
		}
		t.Fatal(e)
	}
	stopAdmission() // Must not cancel successful persistent lifetime.
	peerStack, link, ln := gatewayPeer(t, config(), *r.descriptor.ARP)
	subnet, routeErr := tcpip.NewSubnet(tcpip.AddrFrom4(r.descriptor.Passive.LocalIP.As4()), tcpip.MaskFromBytes([]byte{255, 255, 255, 255}))
	if routeErr != nil {
		t.Fatal(routeErr)
	}
	peerStack.SetRouteTable(append(peerStack.GetRouteTable(), tcpip.Route{Destination: subnet, Gateway: tcpip.AddrFrom4(r.descriptor.ARP.GatewayIP.As4()), NIC: 1}))
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() {
		defer pumps.Done()
		decoder := guestnet.NewDecoder(remote)
		for {
			frame, e := decoder.ReadFrame()
			if e != nil {
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
			v := p.ToView()
			frame := append([]byte(nil), v.AsSlice()...)
			v.Release()
			p.DecRef()
			if encoder.WriteFrame(frame) != nil {
				return
			}
		}
	}()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })}
	served := make(chan struct{})
	go func() { defer close(served); _ = server.Serve(ln) }()
	defer func() {
		_ = server.Close()
		cancel()
		_ = remote.Close()
		wait, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		if e := h.CloseContext(wait); e != nil {
			t.Error(e)
		}
		pumps.Wait()
		<-served
		link.Close()
		peerStack.Destroy()
	}()
	route := transportServiceRoute()
	conn, err := gonet.DialTCPWithBind(ctx, peerStack, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(route.PeerIP.As4()), Port: 41000}, tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(route.LocalIP.As4()), Port: route.LocalPort}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal("passive handshake", err)
	}
	err = memoryRouteHTTPExchange(ctx, conn, "GET /healthz HTTP/1.1\r\nHost: passive.invalid\r\nConnection: close\r\n\r\n", "passive exchange without a probe")
	_ = conn.SetDeadline(time.Time{})
	_ = conn.Close()
	if err != nil {
		t.Fatal("passive exchange", err)
	}
	if passiveIn.Load() == 0 || passiveOut.Load() == 0 {
		t.Fatal("passive route bypassed owner")
	}
	wrong := listenConfig()
	wrong.LocalPort++
	if owner.CommitFrame(ctx, FromGuest, listenFrame(wrong, 41001, 1, 2), func() error { t.Error("wrong passive route entered"); return nil }) == nil {
		t.Fatal("wrong passive route accepted")
	}
	// Mutating supplier storage after capture cannot change named policy/binding.
	r.descriptor.Binding.Owner[0] = 99
	r.descriptor.Config.NIC.ServicePorts[0] = 443
	r.descriptor.Services[0].Port = 443
	r.descriptor.ARP.GuestMAC[5]++
	scope := h.Scope()
	if scope == nil || !h.ServiceTransferred() {
		t.Fatal("unpublished attachment")
	}
	if other, e := owner.AttachPersistent(ctx, passive, time.Now().Add(time.Second)); e == nil || other != nil {
		t.Fatal("duplicate attachment")
	}
	op, e := scope.Start(ctx, operationKey(77), "web")
	if e != nil {
		t.Fatal(e)
	}
	result := operationSettled(t, op)
	if result.ResultError != nil || result.Observation.StatusCode != 204 {
		t.Fatal(result)
	}
	if arpIn.Load() == 0 || arpOut.Load() == 0 {
		t.Fatal("ARP bypassed provider")
	}
	owner.Revoke()
	if h.Scope() != scope {
		t.Fatal("retained scope hidden")
	}
	if _, ok, e := scope.Snapshot(operationKey(77)); e != nil || !ok {
		t.Fatal("retained discovery", e)
	}
	before, _ := owner.Evidence(context.Background())
	if late, e := scope.Start(ctx, operationKey(78), "web"); e == nil {
		if result := operationSettled(t, late); result.ResultError == nil {
			t.Fatal("new execution accepted after revoke")
		}
	}
	after, _ := owner.Evidence(context.Background())
	if len(after) != len(before) {
		t.Fatal("revoked start published evidence")
	}
}
func TestOwnedProbeAttachmentFailureKeepsCleanupAndPassiveOwnership(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &ownedMemoryStream{closed: make(chan struct{})}
	r := &issuedPersistentMemory{&issuedMemoryResource{stream: m}, persistentOwnedDescriptor()}
	owner, e := NewOwnedPersistentProbeSession(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	passive := persistentFixtureService(t)
	h, e := owner.AttachPersistent(ctx, passive, time.Now().Add(-time.Second))
	if e == nil || h == nil || h.ServiceTransferred() || h.Scope() != nil {
		t.Fatal("failure handoff", h, e)
	}
	if passive.closes.Load() != 0 {
		t.Fatal("untransferred service closed")
	}
	_ = passive.Close()
	if e := h.CloseContext(ctx); e != nil {
		t.Fatal(e)
	}
	if again, e := owner.AttachPersistent(ctx, passive, time.Now().Add(time.Second)); e == nil || again != nil {
		t.Fatal("reused failed attachment")
	}
}

type admissionCancelService struct {
	*Listener
	cancel context.CancelFunc
}

func (s *admissionCancelService) Lifetime() context.Context { s.cancel(); return s.Listener.Lifetime() }
func TestOwnedProbeAttachmentAdmissionExpiryAfterTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	local, remote := memoryStreams()
	defer remote.Close()
	owner, e := NewOwnedPersistentProbeSession(ctx, &issuedPersistentMemory{&issuedMemoryResource{stream: local}, persistentOwnedDescriptor()})
	if e != nil {
		t.Fatal(e)
	}
	listener, e := NewListenerUntil(ctx, listenConfig(), time.Now().Add(time.Second))
	if e != nil {
		owner.Revoke()
		t.Fatal(e)
	}
	admission, stop := context.WithCancel(context.Background())
	defer stop()
	h, e := owner.AttachPersistent(admission, &admissionCancelService{listener, stop}, time.Now().Add(time.Second))
	if e == nil || h == nil || !h.ServiceTransferred() || h.Scope() != nil {
		t.Fatal("post-transfer failure lost cleanup", h, e)
	}
	if e := h.CloseContext(ctx); e != nil {
		t.Fatal("composed cleanup", e)
	}
}
