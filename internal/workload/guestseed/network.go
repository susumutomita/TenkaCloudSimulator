package guestseed

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// IPv4Network is trusted fixed NIC configuration, not guest-selected authority.
// MACAddress is a lowercase, colon-separated locally administered unicast MAC.
// Address is a canonical RFC1918 IPv4 address with a /16 through /28 prefix.
// Gateway is a distinct usable IPv4 address within that subnet. There are no
// participant-extensible device names, paths, routes, DNS or YAML fragments.
// Values are comparable and copied; this type grants no attachment permission.
type IPv4Network struct {
	MACAddress string
	Address    string
	Gateway    string
}

var ErrNetwork = errors.New("invalid or unsupported private guest network configuration")

// BuildWithNetwork adds a separate NoCloud network-config file to Build's
// deterministic seed. The original UserData is copied verbatim. It requests a
// single eth0 interface, MTU 1500, static IPv4 and a default route through the
// supplied private gateway. It configures no DHCP, DNS or IPv6, and creates no
// host forwarding, resolver, NAT, interface or transport.
//
// This uses cloud-init's documented version 1 physical/static schema, including
// persistent name/MAC mapping. See cloud-init 24.1 network-config-format-v1.rst.
// The exact image/cloud-init renderer must be separately qualified. Omitted IPv6
// configuration does NOT disable kernel router advertisements or link-local
// addresses. This file is initialization data, never a containment boundary.
func BuildWithNetwork(instanceID, hostName string, userData []byte, config IPv4Network) ([]byte, error) {
	network, err := config.marshal()
	if err != nil {
		return nil, err
	}
	return build(instanceID, hostName, userData, network)
}

func (c IPv4Network) marshal() ([]byte, error) {
	// Reject aliases before rendering. Length bounds also keep parsing and output
	// bounded when a trusted caller accidentally supplies untrusted values.
	if len(c.MACAddress) != 17 || len(c.Address) < 10 || len(c.Address) > 18 || len(c.Gateway) < 7 || len(c.Gateway) > 15 {
		return nil, ErrNetwork
	}
	mac, err := net.ParseMAC(c.MACAddress)
	if err != nil || len(mac) != 6 || mac.String() != c.MACAddress || mac[0]&3 != 2 {
		return nil, ErrNetwork
	}
	prefix, err := netip.ParsePrefix(c.Address)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.String() != c.Address || prefix.Bits() < 16 || prefix.Bits() > 28 {
		return nil, ErrNetwork
	}
	gateway, err := netip.ParseAddr(c.Gateway)
	if err != nil || !gateway.Is4() || gateway.String() != c.Gateway || gateway == prefix.Addr() {
		return nil, ErrNetwork
	}
	network := prefix.Masked()
	usable := func(ip netip.Addr) bool {
		// The next-address test rejects the subnet broadcast without overflow.
		return network.Contains(ip) && ip != network.Addr() && network.Contains(ip.Next())
	}
	if !usable(prefix.Addr()) || !usable(gateway) {
		return nil, ErrNetwork
	}
	return []byte(fmt.Sprintf("version: 1\nconfig:\n  - type: physical\n    name: eth0\n    mac_address: %q\n    mtu: 1500\n    subnets:\n      - type: static\n        control: auto\n        address: %q\n        gateway: %q\n", c.MACAddress, c.Address, c.Gateway)), nil
}
