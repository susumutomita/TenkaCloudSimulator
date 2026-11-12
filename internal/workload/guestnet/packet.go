package guestnet

import (
	"encoding/binary"
	"errors"
	"net/netip"
)

// ErrPacket denotes malformed or unsupported input. Parsing grants no authority.
var ErrPacket = errors.New("malformed or unsupported guest TCP packet")

// TCPPacket contains only value-owned observations, never admission authority or
// a reference into guest memory. It is not a TCP connection or protocol engine.
type TCPPacket struct {
	SourceMAC, DestinationMAC   [6]byte
	SourceIP, DestinationIP     netip.Addr
	SourcePort, DestinationPort uint16
	Sequence, Acknowledgment    uint32
	Flags                       uint8
	Window                      uint16
	PayloadLength               int
}

// ParseTCPFrame accepts bounded, untagged Ethernet carrying IPv4 TCP. IPv4
// options, fragmentation, reserved flags, and unsupported TCP options fail
// closed. Ethernet padding is ignored only outside the IPv4 total length.
// This checks syntax and checksums, not peer identity, routing or TCP state.
func ParseTCPFrame(frame []byte) (TCPPacket, error) {
	var p TCPPacket
	if len(frame) < 14+20+20 || len(frame) > MaxFrameBytes || binary.BigEndian.Uint16(frame[12:14]) != 0x0800 {
		return p, ErrPacket
	}
	ip := frame[14:]
	if ip[0] != 0x45 || ip[9] != 6 || ip[8] == 0 {
		return p, ErrPacket
	}
	total := int(binary.BigEndian.Uint16(ip[2:4]))
	// DF is permitted, but MF, the reserved bit and any fragment offset are not.
	if total < 40 || total > len(ip) || binary.BigEndian.Uint16(ip[6:8])&0xbfff != 0 || checksumSum(ip[:20]) != 0xffff {
		return p, ErrPacket
	}
	ip = ip[:total]
	tcp := ip[20:]
	header := int(tcp[12]>>4) * 4
	// The low nibble includes reserved bits and the unsupported NS flag.
	if header < 20 || header > len(tcp) || tcp[12]&15 != 0 || binary.BigEndian.Uint16(tcp[:2]) == 0 || binary.BigEndian.Uint16(tcp[2:4]) == 0 {
		return p, ErrPacket
	}
	var pseudo [12]byte
	copy(pseudo[:8], ip[12:20])
	pseudo[9] = 6
	binary.BigEndian.PutUint16(pseudo[10:], uint16(len(tcp)))
	if foldSum(uint32(checksumSum(pseudo[:]))+uint32(checksumSum(tcp))) != 0xffff || !validTCPOptions(tcp[20:header], tcp[13]) {
		return p, ErrPacket
	}
	copy(p.DestinationMAC[:], frame[:6])
	copy(p.SourceMAC[:], frame[6:12])
	p.SourceIP = netip.AddrFrom4([4]byte{ip[12], ip[13], ip[14], ip[15]})
	p.DestinationIP = netip.AddrFrom4([4]byte{ip[16], ip[17], ip[18], ip[19]})
	p.SourcePort = binary.BigEndian.Uint16(tcp[:2])
	p.DestinationPort = binary.BigEndian.Uint16(tcp[2:4])
	p.Sequence = binary.BigEndian.Uint32(tcp[4:8])
	p.Acknowledgment = binary.BigEndian.Uint32(tcp[8:12])
	p.Flags = tcp[13]
	p.Window = binary.BigEndian.Uint16(tcp[14:16])
	p.PayloadLength = len(tcp) - header
	return p, nil
}

func foldSum(sum uint32) uint16 {
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return uint16(sum)
}

func checksumSum(b []byte) uint16 {
	var sum uint32
	for len(b) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(b))
		b = b[2:]
	}
	if len(b) == 1 {
		sum += uint32(b[0]) << 8
	}
	return foldSum(sum)
}

func validTCPOptions(options []byte, flags uint8) bool {
	var seen [256]bool
	for len(options) > 0 {
		kind := options[0]
		if kind == 0 {
			for _, b := range options[1:] {
				if b != 0 {
					return false
				}
			}
			return true
		}
		if kind == 1 {
			options = options[1:]
			continue
		}
		if len(options) < 2 || int(options[1]) < 2 || int(options[1]) > len(options) || seen[kind] {
			return false
		}
		n := int(options[1])
		seen[kind] = true
		switch kind {
		case 2:
			if n != 4 || flags&2 == 0 || binary.BigEndian.Uint16(options[2:4]) == 0 {
				return false
			}
		case 3:
			if n != 3 || flags&2 == 0 || options[2] > 14 {
				return false
			}
		case 4:
			if n != 2 || flags&2 == 0 {
				return false
			}
		case 5:
			if flags&2 != 0 || flags&16 == 0 || n < 10 || n > 34 || (n-2)%8 != 0 {
				return false
			}
		case 8:
			if n != 10 {
				return false
			}
		default:
			return false
		}
		options = options[n:]
	}
	return true
}
