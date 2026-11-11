//go:build linux

package guestvm

import (
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
	"net/netip"
	"strings"
	"testing"
)

// Pure invocation checks: no launch, socket, process or private endpoint use.
func TestCanonicalNICInvocationMemoryOnly(t *testing.T) {
	mac := [6]byte{2, 0, 0, 0, 0, 2}
	p := &Prepared{directory: "/private/instance", emulator: "/verified/qemu", firmware: "/verified/bios", network: &guestseed.IPv4Network{MACAddress: "02:00:00:00:00:02", Address: "10.0.1.4/24", Gateway: "10.0.1.1"}}
	old := p.Invocation()
	got, err := p.NICInvocation(mac)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.Args, " ")
	for _, required := range []string{"-S", "socket,id=probe,fd=3", "virtio-net-pci,netdev=probe,mac=02:00:00:00:00:02", "guest_tso4=off", "guest_tso6=off", "guest_ufo=off", "host_tso4=off", "host_tso6=off", "host_ufo=off", "-qmp unix:/private/instance/qmp.sock,server=on,wait=off", "path=/private/instance/qga.sock,server=on,wait=off", "org.qemu.guest_agent.0"} {
		if !strings.Contains(joined, required) {
			t.Fatal("missing fixed contract", required)
		}
	}
	if strings.Contains(joined, "-nic none") || got.Executable != old.Executable || len(got.Env) != 0 {
		t.Fatal("unexpected invocation")
	}
	if _, err = p.NICInvocation([6]byte{2, 0, 0, 0, 0, 3}); err == nil {
		t.Fatal("wrong seed MAC allowed")
	}
	n := guestnet.ProbeNIC{PeerMAC: mac, PeerIP: netip.MustParseAddr("10.0.1.4")}
	arp := &guestnet.ARPConfig{GuestMAC: mac, GuestIP: n.PeerIP, GatewayIP: netip.MustParseAddr("10.0.1.1")}
	if !p.MatchesNIC(n, arp) {
		t.Fatal("matching seed rejected")
	}
	arp.GatewayIP = netip.MustParseAddr("10.0.1.2")
	if p.MatchesNIC(n, arp) {
		t.Fatal("wrong gateway")
	}
	p.network = nil
	if _, err = p.NICInvocation(mac); err == nil {
		t.Fatal("nonnetwork preparation allowed")
	}
}
