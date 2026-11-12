package guestnet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
	"sync"
	"testing"
)

func arpFixtureConfig() ARPConfig {
	return ARPConfig{
		GuestIP:    netip.MustParseAddr("10.0.0.10"),
		GatewayIP:  netip.MustParseAddr("10.0.0.1"),
		GuestMAC:   [6]byte{2, 1, 2, 3, 4, 5},
		GatewayMAC: [6]byte{2, 6, 7, 8, 9, 10},
	}
}

func arpRequestFixture(c ARPConfig) []byte {
	frame := make([]byte, 42)
	copy(frame[:6], []byte{255, 255, 255, 255, 255, 255})
	copy(frame[6:12], c.GuestMAC[:])
	binary.BigEndian.PutUint16(frame[12:14], 0x0806)
	copy(frame[14:22], []byte{0, 1, 8, 0, 6, 4, 0, 1})
	copy(frame[22:28], c.GuestMAC[:])
	guestIP := c.GuestIP.As4()
	copy(frame[28:32], guestIP[:])
	gatewayIP := c.GatewayIP.As4()
	copy(frame[38:42], gatewayIP[:])
	return frame
}

func arpReplyFixture() []byte {
	// Independent expected wire bytes: guest Ethernet destination, gateway
	// Ethernet source; Ethernet/IPv4 ARP reply from gateway to guest; zero pad.
	return []byte{
		2, 1, 2, 3, 4, 5, 2, 6, 7, 8, 9, 10, 8, 6,
		0, 1, 8, 0, 6, 4, 0, 2,
		2, 6, 7, 8, 9, 10, 10, 0, 0, 1,
		2, 1, 2, 3, 4, 5, 10, 0, 0, 10,
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	}
}

func TestARPResponder(t *testing.T) {
	c := arpFixtureConfig()
	r, err := NewARPResponder(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, directed := range []bool{false, true} {
		for _, knownTarget := range []bool{false, true} {
			for size := 42; size <= 60; size++ {
				request := append(arpRequestFixture(c), make([]byte, size-42)...)
				if directed {
					copy(request[:6], c.GatewayMAC[:])
				}
				if knownTarget {
					copy(request[32:38], c.GatewayMAC[:])
				}
				before := bytes.Clone(request)
				reply, err := r.Reply(request)
				if err != nil || !bytes.Equal(reply, arpReplyFixture()) {
					t.Fatalf("directed=%v knownTarget=%v size=%d reply=%x error=%v", directed, knownTarget, size, reply, err)
				}
				if !bytes.Equal(before, request) {
					t.Fatal("request mutated")
				}
			}
		}
	}
}

func TestARPResponderConfig(t *testing.T) {
	cases := map[string]func(*ARPConfig){
		"missing guest IP":   func(c *ARPConfig) { c.GuestIP = netip.Addr{} },
		"missing gateway IP": func(c *ARPConfig) { c.GatewayIP = netip.Addr{} },
		"guest IPv6":         func(c *ARPConfig) { c.GuestIP = netip.MustParseAddr("2001:db8::1") },
		"gateway IPv6":       func(c *ARPConfig) { c.GatewayIP = netip.MustParseAddr("2001:db8::2") },
		"mapped guest":       func(c *ARPConfig) { c.GuestIP = netip.MustParseAddr("::ffff:10.0.0.10") },
		"mapped gateway":     func(c *ARPConfig) { c.GatewayIP = netip.MustParseAddr("::ffff:10.0.0.1") },
		"unspecified guest":  func(c *ARPConfig) { c.GuestIP = netip.IPv4Unspecified() },
		"unspecified gateway": func(c *ARPConfig) {
			c.GatewayIP = netip.IPv4Unspecified()
		},
		"multicast guest":   func(c *ARPConfig) { c.GuestIP = netip.MustParseAddr("224.0.0.1") },
		"multicast gateway": func(c *ARPConfig) { c.GatewayIP = netip.MustParseAddr("224.0.0.1") },
		"broadcast guest":   func(c *ARPConfig) { c.GuestIP = netip.MustParseAddr("255.255.255.255") },
		"broadcast gateway": func(c *ARPConfig) { c.GatewayIP = netip.MustParseAddr("255.255.255.255") },
		"loopback guest":    func(c *ARPConfig) { c.GuestIP = netip.MustParseAddr("127.0.0.1") },
		"loopback gateway":  func(c *ARPConfig) { c.GatewayIP = netip.MustParseAddr("127.0.0.1") },
		"link local guest":  func(c *ARPConfig) { c.GuestIP = netip.MustParseAddr("169.254.1.1") },
		"link local gateway": func(c *ARPConfig) {
			c.GatewayIP = netip.MustParseAddr("169.254.1.1")
		},
		"same address":          func(c *ARPConfig) { c.GatewayIP = c.GuestIP },
		"zero guest MAC":        func(c *ARPConfig) { c.GuestMAC = [6]byte{} },
		"zero gateway MAC":      func(c *ARPConfig) { c.GatewayMAC = [6]byte{} },
		"multicast guest MAC":   func(c *ARPConfig) { c.GuestMAC[0] = 1 },
		"multicast gateway MAC": func(c *ARPConfig) { c.GatewayMAC[0] = 1 },
		"broadcast guest MAC":   func(c *ARPConfig) { c.GuestMAC = [6]byte{255, 255, 255, 255, 255, 255} },
		"broadcast gateway MAC": func(c *ARPConfig) { c.GatewayMAC = [6]byte{255, 255, 255, 255, 255, 255} },
		"same MAC":              func(c *ARPConfig) { c.GatewayMAC = c.GuestMAC },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := arpFixtureConfig()
			mutate(&c)
			if r, err := NewARPResponder(c); !errors.Is(err, ErrARPConfig) || r != nil {
				t.Fatalf("accepted config: responder=%+v err=%v", r, err)
			}
		})
	}
	for _, r := range []*ARPResponder{nil, {}} {
		if reply, err := r.Reply(arpRequestFixture(arpFixtureConfig())); !errors.Is(err, ErrARPConfig) || reply != nil {
			t.Fatalf("invalid responder returned %x, %v", reply, err)
		}
	}
}

func TestARPResponderRejectsRequests(t *testing.T) {
	c := arpFixtureConfig()
	r, err := NewARPResponder(c)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func([]byte){
		"IPv4 frame":        func(b []byte) { binary.BigEndian.PutUint16(b[12:14], 0x0800) },
		"VLAN frame":        func(b []byte) { binary.BigEndian.PutUint16(b[12:14], 0x8100) },
		"IPv6 frame":        func(b []byte) { binary.BigEndian.PutUint16(b[12:14], 0x86dd) },
		"non Ethernet":      func(b []byte) { binary.BigEndian.PutUint16(b[14:16], 2) },
		"non IPv4 ARP":      func(b []byte) { binary.BigEndian.PutUint16(b[16:18], 0x86dd) },
		"wrong MAC length":  func(b []byte) { b[18] = 8 },
		"wrong IP length":   func(b []byte) { b[19] = 16 },
		"ARP reply":         func(b []byte) { binary.BigEndian.PutUint16(b[20:22], 2) },
		"reverse ARP":       func(b []byte) { binary.BigEndian.PutUint16(b[20:22], 3) },
		"unknown operation": func(b []byte) { binary.BigEndian.PutUint16(b[20:22], 0x101) },
		"zero destination":  func(b []byte) { clear(b[:6]) },
		"guest destination": func(b []byte) { copy(b[:6], c.GuestMAC[:]) },
		"other destination": func(b []byte) { copy(b[:6], []byte{2, 0, 0, 0, 0, 99}) },
		"multicast destination": func(b []byte) {
			copy(b[:6], []byte{1, 0, 94, 0, 0, 1})
		},
		"other target MAC": func(b []byte) { copy(b[32:38], []byte{2, 0, 0, 0, 0, 99}) },
		"guest target MAC": func(b []byte) { copy(b[32:38], c.GuestMAC[:]) },
		"broadcast target MAC": func(b []byte) {
			copy(b[32:38], []byte{255, 255, 255, 255, 255, 255})
		},
		"gratuitous guest": func(b []byte) { copy(b[38:42], b[28:32]) },
		"gateway spoof":    func(b []byte) { copy(b[28:32], b[38:42]) },
		"ARP probe":        func(b []byte) { clear(b[28:32]) },
		"other target IP":  func(b []byte) { b[41] = 2 },
		"other guest IP":   func(b []byte) { b[31] = 11 },
		"consistent other source MAC": func(b []byte) {
			b[11] ^= 1
			b[27] ^= 1
		},
		"padding": func(b []byte) { b[59] = 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := append(arpRequestFixture(c), make([]byte, 18)...)
			mutate(request)
			if reply, err := r.Reply(request); !errors.Is(err, ErrARP) || reply != nil {
				t.Fatalf("accepted request: reply=%x err=%v", reply, err)
			}
		})
	}
	// Every bit of all asserted identity/protocol fields is checked. Altering
	// either Ethernet/ARP source independently must not be enough to impersonate
	// the assigned guest, nor may an alternate gateway target be learned.
	for _, field := range [][2]int{{6, 12}, {12, 22}, {22, 32}, {38, 42}} {
		for i := field[0]; i < field[1]; i++ {
			for bit := byte(1); bit != 0; bit <<= 1 {
				request := arpRequestFixture(c)
				request[i] ^= bit
				if reply, err := r.Reply(request); !errors.Is(err, ErrARP) || reply != nil {
					t.Fatalf("accepted changed identity/protocol byte=%d bit=%x reply=%x err=%v", i, bit, reply, err)
				}
			}
		}
	}
	request := arpRequestFixture(c)
	for size := 0; size < 42; size++ {
		if reply, err := r.Reply(request[:size]); !errors.Is(err, ErrARP) || reply != nil {
			t.Fatalf("accepted truncation size=%d", size)
		}
	}
	for _, size := range []int{61, MaxFrameBytes, MaxFrameBytes + 1, 65536} {
		oversized := make([]byte, size)
		copy(oversized, request)
		if reply, err := r.Reply(oversized); !errors.Is(err, ErrARP) || reply != nil {
			t.Fatalf("accepted oversize=%d", size)
		}
	}
	for i := 42; i < 60; i++ {
		padded := append(bytes.Clone(request), make([]byte, 18)...)
		padded[i] = 1
		if reply, err := r.Reply(padded); !errors.Is(err, ErrARP) || reply != nil {
			t.Fatalf("accepted nonzero padding at byte=%d", i)
		}
	}
	// Rejected traffic cannot poison the fixed responder or make it learn.
	if reply, err := r.Reply(request); err != nil || !bytes.Equal(reply, arpReplyFixture()) {
		t.Fatalf("fixed mapping changed: %x, %v", reply, err)
	}
}

func TestARPResponderOwnsValues(t *testing.T) {
	c := arpFixtureConfig()
	r, err := NewARPResponder(c)
	if err != nil {
		t.Fatal(err)
	}
	c = ARPConfig{}
	request := arpRequestFixture(arpFixtureConfig())
	reply, err := r.Reply(request)
	if err != nil {
		t.Fatal(err)
	}
	clear(request)
	if !bytes.Equal(reply, arpReplyFixture()) {
		t.Fatal("reply aliases input or original configuration")
	}
	clear(reply)
	request = arpRequestFixture(arpFixtureConfig())
	reply, err = r.Reply(request)
	if err != nil || !bytes.Equal(reply, arpReplyFixture()) {
		t.Fatal("caller mutation changed responder", err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 16 {
				next, err := r.Reply(request)
				if err != nil || !bytes.Equal(next, arpReplyFixture()) {
					t.Error("concurrent reply changed", err)
				}
				clear(next)
			}
		})
	}
	wg.Wait()
	if !bytes.Equal(reply, arpReplyFixture()) {
		t.Fatal("responses share storage")
	}
}

func FuzzARPResponder(f *testing.F) {
	r, err := NewARPResponder(arpFixtureConfig())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(arpRequestFixture(arpFixtureConfig()))
	f.Add(append(arpRequestFixture(arpFixtureConfig()), make([]byte, 18)...))
	f.Add(arpReplyFixture())
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, request []byte) {
		before := bytes.Clone(request)
		reply, err := r.Reply(request)
		if !bytes.Equal(before, request) {
			t.Fatal("request mutated")
		}
		if err != nil {
			if reply != nil || !errors.Is(err, ErrARP) {
				t.Fatal("invalid failure result")
			}
			return
		}
		if len(request) < 42 || len(request) > 60 || !bytes.Equal(reply, arpReplyFixture()) {
			t.Fatal("invalid accepted request or reply")
		}
		c := arpFixtureConfig()
		canonical := arpRequestFixture(c)
		if !bytes.Equal(request[:6], canonical[:6]) && !bytes.Equal(request[:6], c.GatewayMAC[:]) {
			t.Fatal("accepted incorrect Ethernet destination")
		}
		if !bytes.Equal(request[32:38], make([]byte, 6)) && !bytes.Equal(request[32:38], c.GatewayMAC[:]) {
			t.Fatal("accepted incorrect target MAC")
		}
		normalized := bytes.Clone(request[:42])
		copy(normalized[:6], canonical[:6])
		clear(normalized[32:38])
		if !bytes.Equal(normalized, canonical) || !bytes.Equal(request[42:], make([]byte, len(request)-42)) {
			t.Fatal("accepted changed identity, protocol or padding")
		}
	})
}
