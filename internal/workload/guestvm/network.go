package guestvm

import (
	"context"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
)

// PrepareWithNetwork is an explicit internal opt-in to a separate static IPv4
// NoCloud configuration. All Prepare authority and lifetime requirements still
// apply. The trusted lifecycle owner must durably bind the fixed NIC values to
// this acquire intent; this function cannot establish that binding from values
// alone. The unchanged version 1 Record's SeedSHA256 covers the entire seed,
// including network-config. Receipt inspection remains read-only, never replay.
//
// No NIC is attached: Invocation and Launch remain paused and -nic none. They
// cannot use this seed's network configuration. Image/renderer compatibility,
// physical attachment, IPv6 suppression and packet-policy/lifetime fencing need
// separate qualification before any network-enabled runtime may consume it.
// Preparation never runs cloud-init, UserData, a host command or a socket.
func PrepareWithNetwork(ctx context.Context, parent string, bundle *guestbundle.Bundle, layout Layout, intent guestlease.Intent, userData []byte, network guestseed.IPv4Network) (*Prepared, error) {
	return prepare(ctx, parent, bundle, layout, intent, userData, &network)
}
