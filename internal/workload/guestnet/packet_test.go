package guestnet

import (
	"encoding/binary"
	"errors"
	"net/netip"
	"testing"
)

func packetFixture(options, payload []byte) []byte {
	b := make([]byte, 54+len(options)+len(payload))
	copy(b[:12], []byte{2, 1, 2, 3, 4, 5, 2, 6, 7, 8, 9, 10})
	binary.BigEndian.PutUint16(b[12:], 0x0800)
	ip := b[14:]
	ip[0] = 0x45
	ip[8] = 64
	ip[9] = 6
	binary.BigEndian.PutUint16(ip[2:], uint16(len(ip)))
	copy(ip[12:20], []byte{10, 0, 0, 1, 10, 0, 0, 2})
	tcp := ip[20:]
	binary.BigEndian.PutUint16(tcp, 12345)
	binary.BigEndian.PutUint16(tcp[2:], 80)
	binary.BigEndian.PutUint32(tcp[4:], 0x12345678)
	binary.BigEndian.PutUint32(tcp[8:], 0x87654321)
	tcp[12] = byte((20+len(options))/4) << 4
	tcp[13] = 2
	binary.BigEndian.PutUint16(tcp[14:], 4096)
	copy(tcp[20:], options)
	copy(tcp[20+len(options):], payload)
	fixPacketChecksums(b)
	return b
}

func fixPacketChecksums(b []byte) {
	ip := b[14:]
	ip[10] = 0
	ip[11] = 0
	binary.BigEndian.PutUint16(ip[10:], ^checksumSum(ip[:20]))
	tcp := ip[20:]
	tcp[16] = 0
	tcp[17] = 0
	pseudo := append([]byte(nil), ip[12:20]...)
	pseudo = append(pseudo, 0, 6, byte(len(tcp)>>8), byte(len(tcp)))
	binary.BigEndian.PutUint16(tcp[16:], ^foldSum(uint32(checksumSum(pseudo))+uint32(checksumSum(tcp))))
}

func TestParseTCPFrame(t *testing.T) {
	for _, payload := range [][]byte{nil, {1}, {1, 2}, {1, 2, 3}} {
		// Typical Linux SYN: MSS, SACK permitted, timestamp, NOP, window scale.
		b := packetFixture([]byte{2, 4, 5, 180, 4, 2, 8, 10, 0, 0, 0, 1, 0, 0, 0, 0, 1, 3, 3, 7}, payload)
		p, err := ParseTCPFrame(append(b, 0, 0, 0, 0))
		if err != nil {
			t.Fatal(err)
		}
		if p.SourceIP != netip.MustParseAddr("10.0.0.1") || p.DestinationIP != netip.MustParseAddr("10.0.0.2") || p.SourcePort != 12345 || p.DestinationPort != 80 || p.Sequence != 0x12345678 || p.Acknowledgment != 0x87654321 || p.Flags != 2 || p.Window != 4096 || p.PayloadLength != len(payload) || p.SourceMAC != [6]byte{2, 6, 7, 8, 9, 10} || p.DestinationMAC != [6]byte{2, 1, 2, 3, 4, 5} {
			t.Fatalf("bad tuple: %+v", p)
		}
		before := p
		for i := range b {
			b[i] = 0
		}
		if p != before {
			t.Fatal("aliased packet")
		}
	}
}

func TestRejectTCPFrames(t *testing.T) {
	cases := map[string]func([]byte){
		"vlan":           func(b []byte) { binary.BigEndian.PutUint16(b[12:], 0x8100) },
		"ipv6":           func(b []byte) { binary.BigEndian.PutUint16(b[12:], 0x86dd) },
		"ip options":     func(b []byte) { b[14] = 0x46 },
		"version":        func(b []byte) { b[14] = 0x65 },
		"udp":            func(b []byte) { b[23] = 17 },
		"ttl":            func(b []byte) { b[22] = 0 },
		"short total":    func(b []byte) { binary.BigEndian.PutUint16(b[16:], 39) },
		"long total":     func(b []byte) { binary.BigEndian.PutUint16(b[16:], 1500) },
		"reserved ip":    func(b []byte) { b[20] = 0x80 },
		"more fragments": func(b []byte) { b[20] = 0x20 },
		"offset":         func(b []byte) { b[21] = 1 },
		"source port":    func(b []byte) { b[34] = 0; b[35] = 0 },
		"dest port":      func(b []byte) { b[36] = 0; b[37] = 0 },
		"short tcp":      func(b []byte) { b[46] = 0x40 },
		"long tcp":       func(b []byte) { b[46] = 0xf0 },
		"reserved tcp":   func(b []byte) { b[46] |= 1 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			b := packetFixture(nil, nil)
			mutate(b)
			fixPacketChecksums(b)
			if p, e := ParseTCPFrame(b); !errors.Is(e, ErrPacket) || p != (TCPPacket{}) {
				t.Fatalf("accepted: %+v %v", p, e)
			}
		})
	}
	b := packetFixture(nil, []byte{1, 2, 3})
	for _, offset := range []int{24, 50, 56} {
		corrupted := append([]byte(nil), b...)
		corrupted[offset] ^= 1
		if _, e := ParseTCPFrame(corrupted); e == nil {
			t.Fatal("bad checksum", offset)
		}
	}
	for n := 0; n < len(b); n++ {
		if _, e := ParseTCPFrame(b[:n]); e == nil {
			t.Fatal("truncation", n)
		}
	}
	if _, e := ParseTCPFrame(make([]byte, 1515)); e == nil {
		t.Fatal("oversized")
	}
}

func TestTCPOptions(t *testing.T) {
	good := [][]byte{{0, 0, 0, 0}, {1, 1, 1, 1}, {2, 4, 5, 180}, {3, 3, 14, 0}, {4, 2, 0, 0}, {8, 10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}}
	bad := [][]byte{{0, 1, 0, 0}, {2, 0, 0, 0}, {2, 1, 0, 0}, {2, 5, 0, 0}, {2, 4, 0, 0}, {3, 3, 15, 0}, {4, 2, 4, 2}, {8, 2, 0, 0}, {30, 2, 0, 0}, {1, 1, 1, 2}}
	for _, opt := range good {
		if _, e := ParseTCPFrame(packetFixture(opt, nil)); e != nil {
			t.Fatalf("rejected %v", opt)
		}
	}
	for _, opt := range bad {
		if _, e := ParseTCPFrame(packetFixture(opt, nil)); e == nil {
			t.Fatalf("accepted %v", opt)
		}
	}
	for _, opt := range good[2:5] {
		b := packetFixture(opt, nil)
		b[47] = 16
		fixPacketChecksums(b)
		if _, e := ParseTCPFrame(b); e == nil {
			t.Fatal("SYN-only option accepted")
		}
	}
	sack := []byte{5, 10, 0, 0, 0, 1, 0, 0, 0, 2, 0, 0}
	b := packetFixture(sack, nil)
	if _, e := ParseTCPFrame(b); e == nil {
		t.Fatal("SYN SACK accepted")
	}
	b[47] = 16
	fixPacketChecksums(b)
	if _, e := ParseTCPFrame(b); e != nil {
		t.Fatal(e)
	}
}

func TestChecksumKnownVectors(t *testing.T) {
	// RFC 1071 section 3 example and a manually summed odd-length vector.
	if checksumSum([]byte{0, 1, 0xf2, 3, 0xf4, 0xf5, 0xf6, 0xf7}) != 0xddf2 {
		t.Fatal("RFC checksum")
	}
	if checksumSum([]byte{1, 2, 3}) != 0x0402 {
		t.Fatal("odd checksum")
	}
}

func FuzzParseTCPFrame(f *testing.F) {
	f.Add(packetFixture(nil, nil))
	f.Add(packetFixture(nil, []byte{1}))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		p, e := ParseTCPFrame(b)
		if e != nil {
			if p != (TCPPacket{}) {
				t.Fatal("partial tuple")
			}
		} else if p.PayloadLength < 0 || p.PayloadLength > 1460 || !p.SourceIP.Is4() || !p.DestinationIP.Is4() {
			t.Fatal("invalid tuple")
		}
	})
}

func TestTCPPseudoHeaderAndDF(t *testing.T) {
	b := packetFixture(nil, []byte{1, 2, 3})
	b[20] = 0x40
	fixPacketChecksums(b)
	if _, e := ParseTCPFrame(b); e != nil {
		t.Fatal("DF rejected", e)
	}
	// Change the source address and repair only the IPv4 checksum. TCP's
	// pseudoheader must independently detect the changed endpoint.
	b[26] ^= 1
	b[24] = 0
	b[25] = 0
	binary.BigEndian.PutUint16(b[24:], ^checksumSum(b[14:34]))
	if _, e := ParseTCPFrame(b); e == nil {
		t.Fatal("pseudoheader not checked")
	}
}
