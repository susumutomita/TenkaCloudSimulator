package guestnet

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
	"testing"
)

func tcpRouteFixtures() []TCPRoute {
	probe := TCPRoute{
		LocalIP: netip.MustParseAddr("192.0.2.10"), PeerIP: netip.MustParseAddr("10.0.1.4"),
		LocalMAC: [6]byte{2, 0, 0, 0, 0, 1}, PeerMAC: [6]byte{2, 0, 0, 0, 0, 2},
		LocalPort: 40000, PeerPort: 80,
	}
	service := probe
	service.LocalIP, service.LocalPort = netip.MustParseAddr("169.254.169.254"), 80
	service.PeerPort, service.AnyPeerPort = 0, true
	return []TCPRoute{probe, service}
}

func tcpRouteFrame(r TCPRoute, direction TCPDirection, peerPort uint16) []byte {
	f := packetFixture(nil, nil)
	local, peer := r.LocalIP.As4(), r.PeerIP.As4()
	if direction == TCPFromPeer {
		copy(f[:6], r.LocalMAC[:])
		copy(f[6:12], r.PeerMAC[:])
		copy(f[26:30], peer[:])
		copy(f[30:34], local[:])
		binary.BigEndian.PutUint16(f[34:36], peerPort)
		binary.BigEndian.PutUint16(f[36:38], r.LocalPort)
	} else {
		copy(f[:6], r.PeerMAC[:])
		copy(f[6:12], r.LocalMAC[:])
		copy(f[26:30], local[:])
		copy(f[30:34], peer[:])
		binary.BigEndian.PutUint16(f[34:36], r.LocalPort)
		binary.BigEndian.PutUint16(f[36:38], peerPort)
	}
	fixPacketChecksums(f)
	return f
}

func TestTCPRoutesSeparateSharedNICServices(t *testing.T) {
	configs := tcpRouteFixtures()
	routes, err := NewTCPRoutes(configs)
	if err != nil {
		t.Fatal(err)
	}
	// Both routes share exactly the same peer IP/MAC and local (gateway) MAC.
	// No endpoint sees a frame before its route has been selected.
	for _, direction := range []TCPDirection{TCPFromPeer, TCPToPeer} {
		for i, route := range configs {
			ports := []uint16{route.PeerPort}
			if route.AnyPeerPort {
				ports = []uint16{1, 80, 40000, 41000, 65535}
			}
			for _, port := range ports {
				f := tcpRouteFrame(route, direction, port)
				index, err := routes.Classify(direction, f)
				if err != nil || index != i {
					t.Fatalf("route %d direction %d port %d: %d %v", i, direction, port, index, err)
				}
				wrongDirection := TCPFromPeer
				if direction == TCPFromPeer {
					wrongDirection = TCPToPeer
				}
				if index, err := routes.Classify(wrongDirection, f); index != -1 || !errors.Is(err, ErrTCPRoute) {
					t.Fatalf("accepted reversed direction: %d %v", index, err)
				}
			}
		}
	}
}

func TestTCPRoutesRejectAmbiguousConfiguration(t *testing.T) {
	base := tcpRouteFixtures()[0]
	anyPort := base
	anyPort.PeerPort, anyPort.AnyPeerPort = 0, true
	for _, list := range [][]TCPRoute{{base, base}, {base, anyPort}, {anyPort, base}, {anyPort, anyPort}} {
		if r, err := NewTCPRoutes(list); r != nil || !errors.Is(err, ErrTCPRouteConfig) {
			t.Fatalf("accepted overlapping list: %v %v", r, err)
		}
	}
	// Exact peer ports are distinct, with no first-match or wildcard precedence.
	second := base
	second.PeerPort++
	r, err := NewTCPRoutes([]TCPRoute{base, second})
	if err != nil {
		t.Fatal(err)
	}
	for i, route := range []TCPRoute{base, second} {
		for _, direction := range []TCPDirection{TCPFromPeer, TCPToPeer} {
			if index, err := r.Classify(direction, tcpRouteFrame(route, direction, route.PeerPort)); err != nil || index != i {
				t.Fatalf("distinct peer port: %d %v", index, err)
			}
		}
	}
}

func TestTCPRoutesConfigurationBoundsAndInvalidValues(t *testing.T) {
	base := tcpRouteFixtures()[0]
	for name, mutate := range map[string]func(*TCPRoute){
		"empty local":      func(r *TCPRoute) { r.LocalIP = netip.Addr{} },
		"local IPv6":       func(r *TCPRoute) { r.LocalIP = netip.MustParseAddr("2001:db8::1") },
		"mapped IPv4":      func(r *TCPRoute) { r.LocalIP = netip.MustParseAddr("::ffff:192.0.2.10") },
		"local loopback":   func(r *TCPRoute) { r.LocalIP = netip.MustParseAddr("127.0.0.1") },
		"peer link local":  func(r *TCPRoute) { r.PeerIP = netip.MustParseAddr("169.254.1.2") },
		"peer multicast":   func(r *TCPRoute) { r.PeerIP = netip.MustParseAddr("224.0.0.1") },
		"peer unspecified": func(r *TCPRoute) { r.PeerIP = netip.IPv4Unspecified() },
		"equal IP":         func(r *TCPRoute) { r.LocalIP = r.PeerIP },
		"zero local MAC":   func(r *TCPRoute) { r.LocalMAC = [6]byte{} },
		"zero peer MAC":    func(r *TCPRoute) { r.PeerMAC = [6]byte{} },
		"multicast local":  func(r *TCPRoute) { r.LocalMAC[0] = 1 },
		"multicast peer":   func(r *TCPRoute) { r.PeerMAC[0] = 1 },
		"equal MAC":        func(r *TCPRoute) { r.LocalMAC = r.PeerMAC },
		"zero local port":  func(r *TCPRoute) { r.LocalPort = 0 },
		"implicit any":     func(r *TCPRoute) { r.PeerPort = 0 },
		"mixed any exact":  func(r *TCPRoute) { r.AnyPeerPort = true },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			mutate(&r)
			if routes, err := NewTCPRoutes([]TCPRoute{r}); routes != nil || !errors.Is(err, ErrTCPRouteConfig) {
				t.Fatalf("invalid route accepted: %v %v", routes, err)
			}
		})
	}
	configs := make([]TCPRoute, MaxTCPRoutes+1)
	for i := range configs {
		configs[i] = base
		configs[i].LocalPort += uint16(i)
	}
	for _, n := range []int{0, MaxTCPRoutes + 1} {
		if r, err := NewTCPRoutes(configs[:n]); r != nil || !errors.Is(err, ErrTCPRouteConfig) {
			t.Fatalf("accepted count %d", n)
		}
	}
	for _, n := range []int{1, MaxTCPRoutes} {
		routes, err := NewTCPRoutes(configs[:n])
		if err != nil {
			t.Fatal(err)
		}
		for i, r := range configs[:n] {
			if index, err := routes.Classify(TCPFromPeer, tcpRouteFrame(r, TCPFromPeer, r.PeerPort)); err != nil || index != i {
				t.Fatalf("boundary route %d: %d %v", i, index, err)
			}
		}
	}
}

func TestTCPRoutesRejectSpoofedAndMalformedFrames(t *testing.T) {
	configs := tcpRouteFixtures()
	routes, err := NewTCPRoutes(configs)
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func([]byte){
		"source MAC": func(f []byte) { f[11] ^= 1 },
		"dest MAC":   func(f []byte) { f[5] ^= 1 },
		"source IP":  func(f []byte) { f[29] ^= 1 },
		"dest IP":    func(f []byte) { f[33] ^= 1 },
		"local port": func(f []byte) { f[37] ^= 1 },
		"zero peer":  func(f []byte) { f[34], f[35] = 0, 0 },
		"fragment":   func(f []byte) { f[20] |= 0x20 },
		"UDP":        func(f []byte) { f[23] = 17 },
		"TTL":        func(f []byte) { f[22] = 0 },
		"VLAN":       func(f []byte) { binary.BigEndian.PutUint16(f[12:14], 0x8100) },
		"IPv6":       func(f []byte) { binary.BigEndian.PutUint16(f[12:14], 0x86dd) },
		"ARP":        func(f []byte) { binary.BigEndian.PutUint16(f[12:14], 0x0806) },
	}
	for _, c := range configs {
		for name, mutate := range mutations {
			t.Run(name+"/"+c.LocalIP.String(), func(t *testing.T) {
				f := tcpRouteFrame(c, TCPFromPeer, 80)
				mutate(f)
				fixPacketChecksums(f)
				if index, err := routes.Classify(TCPFromPeer, f); index != -1 || err == nil {
					t.Fatalf("bad frame accepted: %d %v", index, err)
				}
			})
		}
	}
	f := tcpRouteFrame(configs[0], TCPFromPeer, 81)
	if index, err := routes.Classify(TCPFromPeer, f); index != -1 || !errors.Is(err, ErrTCPRoute) {
		t.Fatal("unmatched exact peer port", index, err)
	}
	f = tcpRouteFrame(configs[0], TCPFromPeer, 80)
	for _, offset := range []int{24, 50} {
		bad := append([]byte(nil), f...)
		bad[offset] ^= 1
		if index, err := routes.Classify(TCPFromPeer, bad); index != -1 || !errors.Is(err, ErrPacket) {
			t.Fatal("bad checksum accepted", index, err)
		}
	}
	for n := 0; n < len(f); n++ {
		if index, err := routes.Classify(TCPFromPeer, f[:n]); index != -1 || !errors.Is(err, ErrPacket) {
			t.Fatal("truncation accepted", n, index, err)
		}
	}
	if index, err := routes.Classify(TCPFromPeer, make([]byte, MaxFrameBytes+1)); index != -1 || !errors.Is(err, ErrPacket) {
		t.Fatal("oversized frame accepted", index, err)
	}
	for _, routes := range []*TCPRoutes{nil, {}} {
		if index, err := routes.Classify(TCPFromPeer, f); index != -1 || !errors.Is(err, ErrTCPRoute) {
			t.Fatal("uninitialized classifier accepted", index, err)
		}
	}
	for _, direction := range []TCPDirection{0, 3, 255} {
		if index, err := routes.Classify(direction, f); index != -1 || !errors.Is(err, ErrTCPRoute) {
			t.Fatal("invalid direction accepted", index, err)
		}
	}
}

func TestTCPRoutesCopiedConfigurationAndConcurrentReads(t *testing.T) {
	configs := tcpRouteFixtures()
	f := tcpRouteFrame(configs[0], TCPFromPeer, 80)
	routes, err := NewTCPRoutes(configs)
	if err != nil {
		t.Fatal(err)
	}
	clear(configs)
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 100 {
				if index, err := routes.Classify(TCPFromPeer, f); index != 0 || err != nil {
					t.Errorf("copied configuration: %d %v", index, err)
					return
				}
			}
		})
	}
	workers.Wait()
	clear(f)
	if index, err := routes.Classify(TCPFromPeer, f); index != -1 || !errors.Is(err, ErrPacket) {
		t.Fatal("retained a previous frame classification", index, err)
	}
}

func FuzzTCPRoutes(f *testing.F) {
	configs := tcpRouteFixtures()
	routes, err := NewTCPRoutes(configs)
	if err != nil {
		f.Fatal(err)
	}
	for _, direction := range []TCPDirection{TCPFromPeer, TCPToPeer} {
		for _, c := range configs {
			f.Add(uint8(direction), tcpRouteFrame(c, direction, 80))
		}
	}
	f.Add(uint8(0), []byte{})
	f.Fuzz(func(t *testing.T, direction uint8, frame []byte) {
		index, err := routes.Classify(TCPDirection(direction), frame)
		if err != nil && index != -1 || err == nil && (index < 0 || index >= len(configs)) {
			t.Fatalf("inconsistent classification: %d %v", index, err)
		}
	})
}
