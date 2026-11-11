package guestnet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net/netip"
)

var (
	ErrARPConfig = errors.New("invalid private guest ARP binding")
	ErrARP       = errors.New("malformed or unauthorized private guest ARP request")
)

const (
	arpFrameBytes       = 14 + 28
	arpPaddedFrameBytes = 60 // Minimum Ethernet frame, excluding FCS.
)

// ARPConfig is trusted, immutable NIC configuration, never learned from guest
// packets. GatewayIP may differ from a TCP probe's source address; GatewayMAC
// must identify the same private Ethernet endpoint used for return traffic.
type ARPConfig struct {
	GuestIP, GatewayIP   netip.Addr
	GuestMAC, GatewayMAC [6]byte
}

// ARPResponder answers one fixed guest's requests for its fixed gateway. It
// maintains no neighbor table, forwards no packets and opens no sockets. A reply
// is only a wire value: the owner must separately bound traffic and enforce the
// current lease and physical-attachment lifetime at dispatch.
type ARPResponder struct {
	config ARPConfig
	ready  bool
}

func NewARPResponder(c ARPConfig) (*ARPResponder, error) {
	validMAC := func(m [6]byte) bool { return m != ([6]byte{}) && m[0]&1 == 0 }
	if !c.GuestIP.Is4() || !c.GatewayIP.Is4() || !c.GuestIP.IsGlobalUnicast() || !c.GatewayIP.IsGlobalUnicast() || c.GuestIP == c.GatewayIP || !validMAC(c.GuestMAC) || !validMAC(c.GatewayMAC) || c.GuestMAC == c.GatewayMAC {
		return nil, ErrARPConfig
	}
	return &ARPResponder{config: c, ready: true}, nil
}

// Reply accepts only untagged Ethernet/IPv4 ARP requests whose Ethernet source,
// sender MAC and sender IP all match the configured guest, and whose target IP
// is the configured gateway. The Ethernet destination must be broadcast or the
// gateway MAC; the target MAC may be zero or the gateway MAC. This permits both
// initial resolution and directed refresh without accepting gratuitous ARP,
// probes, replies, other peers or other targets.
//
// Input is 42 to 60 bytes with only zero Ethernet padding. Output is a fresh
// 60-byte, zero-padded reply containing only the trusted configuration. Neither
// input nor previously returned storage is retained. Nil and zero-value
// responders fail closed. Concurrent calls are safe if callers do not mutate
// their input while Reply is reading it.
func (r *ARPResponder) Reply(frame []byte) ([]byte, error) {
	if r == nil || !r.ready {
		return nil, ErrARPConfig
	}
	if len(frame) < arpFrameBytes || len(frame) > arpPaddedFrameBytes || binary.BigEndian.Uint16(frame[12:14]) != 0x0806 {
		return nil, ErrARP
	}
	for _, b := range frame[arpFrameBytes:] {
		if b != 0 {
			return nil, ErrARP
		}
	}
	c := r.config
	destination := [6]byte(frame[:6])
	if (destination != [6]byte{255, 255, 255, 255, 255, 255} && destination != c.GatewayMAC) || [6]byte(frame[6:12]) != c.GuestMAC {
		return nil, ErrARP
	}
	arp := frame[14:arpFrameBytes]
	if binary.BigEndian.Uint16(arp[:2]) != 1 || binary.BigEndian.Uint16(arp[2:4]) != 0x0800 || arp[4] != 6 || arp[5] != 4 || binary.BigEndian.Uint16(arp[6:8]) != 1 {
		return nil, ErrARP
	}
	targetMAC := [6]byte(arp[18:24])
	if [6]byte(arp[8:14]) != c.GuestMAC || [4]byte(arp[14:18]) != c.GuestIP.As4() || [4]byte(arp[24:28]) != c.GatewayIP.As4() || (targetMAC != [6]byte{} && targetMAC != c.GatewayMAC) {
		return nil, ErrARP
	}
	return r.trustedReply(), nil
}

// MatchesReply accepts only the canonical reply derived from this immutable binding.
func (r *ARPResponder) MatchesReply(frame []byte) bool {
	return r != nil && r.ready && bytes.Equal(frame, r.trustedReply())
}

func (r *ARPResponder) trustedReply() []byte {
	c := r.config
	// Do not reflect request bytes, including padding, back to the guest.
	reply := make([]byte, arpPaddedFrameBytes)
	copy(reply[:6], c.GuestMAC[:])
	copy(reply[6:12], c.GatewayMAC[:])
	binary.BigEndian.PutUint16(reply[12:14], 0x0806)
	arp := reply[14:arpFrameBytes]
	binary.BigEndian.PutUint16(arp[:2], 1)
	binary.BigEndian.PutUint16(arp[2:4], 0x0800)
	arp[4], arp[5] = 6, 4
	binary.BigEndian.PutUint16(arp[6:8], 2)
	copy(arp[8:14], c.GatewayMAC[:])
	gatewayIP := c.GatewayIP.As4()
	copy(arp[14:18], gatewayIP[:])
	copy(arp[18:24], c.GuestMAC[:])
	guestIP := c.GuestIP.As4()
	copy(arp[24:28], guestIP[:])
	return reply
}
