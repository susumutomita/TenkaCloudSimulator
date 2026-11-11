//go:build linux

package guestvm

import (
	"context"
	"fmt"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"net"
	"net/netip"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestprocess"
)

// NICInvocation retains the canonical private Unix QMP/QGA endpoints and paused
// startup. Only the backend-created FD3 carries Ethernet socket-netdev frames.
// The MAC must be the immutable guest-side MAC in the provider's seed/policy.
func (p *Prepared) NICInvocation(mac [6]byte) (Invocation, error) {
	if p == nil || p.network == nil || net.HardwareAddr(mac[:]).String() != p.network.MACAddress || mac == ([6]byte{}) || mac[0]&1 != 0 {
		return Invocation{}, fmt.Errorf("invalid VM NIC MAC")
	}
	inv := p.Invocation()
	args := make([]string, 0, len(inv.Args)+4)
	replaced := false
	for i := 0; i < len(inv.Args); i++ {
		if inv.Args[i] == "-nic" && i+1 < len(inv.Args) && inv.Args[i+1] == "none" {
			if replaced {
				return Invocation{}, fmt.Errorf("duplicate NIC option")
			}
			replaced = true
			i++
			args = append(args, "-netdev", "socket,id=probe,fd=3", "-device", fmt.Sprintf("virtio-net-pci,netdev=probe,mac=%02x:%02x:%02x:%02x:%02x:%02x,csum=off,gso=off,guest_tso4=off,guest_tso6=off,guest_ecn=off,guest_ufo=off,host_tso4=off,host_tso6=off,host_ecn=off,host_ufo=off", mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]))
		} else {
			args = append(args, inv.Args[i])
		}
	}
	if !replaced {
		return Invocation{}, fmt.Errorf("canonical disabled NIC missing")
	}
	inv.Args = args
	return inv, nil
}

// LaunchPersistentNIC is the canonical non-test caller of concrete owned NIC
// acquisition. It executes real syscalls; source-only tests must inject below
// this boundary, never call it under a held socket/VM permission.
func (p *Prepared) LaunchPersistentNIC(ctx context.Context, mac [6]byte) (*guestprocess.NICLaunch, error) {
	inv, err := p.NICInvocation(mac)
	if err != nil {
		return nil, err
	}
	return guestprocess.BeginNICLaunch(ctx, guestprocess.Spec{Executable: inv.Executable, Args: inv.Args, Env: inv.Env})
}

// MatchesNIC verifies the actual immutable seed relation, not merely a comment
// or an independently supplied descriptor. It grants no launch authority.
func (p *Prepared) MatchesNIC(n guestnet.ProbeNIC, arp *guestnet.ARPConfig) bool {
	if p == nil || p.network == nil {
		return false
	}
	prefix, err := netip.ParsePrefix(p.network.Address)
	return err == nil && arp != nil && arp.GatewayIP.String() == p.network.Gateway && arp.GuestIP == n.PeerIP && arp.GuestMAC == n.PeerMAC && prefix.Addr() == n.PeerIP && net.HardwareAddr(n.PeerMAC[:]).String() == p.network.MACAddress
}
