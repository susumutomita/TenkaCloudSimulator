package guesttcp

import (
	"context"
	"io"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// NewPassiveTransport owns one framed exchange for a trusted passive service,
// without constructing an Engine, reserving an active stack slot or starting a
// probe. It uses Transport's existing decoder, serialized encoder, owner fence,
// aggregate budgets, gateway ARP and retained cleanup accounting. Dial and
// NewHTTPObserver reject this mode; neither silently creates an active endpoint.
//
// The copied route fixes the local address/port, peer IP and both MACs, and must
// explicitly allow AnyPeerPort. Optional ARP must match that peer and local MAC.
// Route syntax and tuple matching are not physical ownership or provider policy.
// The FrameCommitter must independently authorize every actual frame delivery.
//
// Ownership of BOTH stream and service transfers only on constructor success.
// Every error leaves both with the caller. The exchange expires within the
// original ten-second MaxLifetime bound or the earlier parent/service lifetime.
// Nothing extends the supplied service's own deadline. Close, expiry and failure
// revoke the whole exchange; unresolved or failed cleanup retains transport debt.
// This is not persistent boot metadata, a runtime attachment or a default service.
func NewPassiveTransport(ctx context.Context, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig, service PassiveService, route guestnet.TCPRoute) (*Transport, error) {
	return newPassiveTransport(ctx, stream, owner, arpConfig, service, route, time.Now().Add(MaxLifetime), false)
}

// NewPassiveTransportUntil explicitly retains one passive service and framed
// stream until the trusted owner's original fixed deadline, bounded by
// MaxPassiveLifetime and any earlier parent/service expiry. The service must
// implement RevocablePassiveService with a fixed deadline. Its captured lifetime
// gates callbacks even when a response already awaits owner admission. No context
// replacement or traffic renews that lifetime. FrameCommitter must still fence
// physical/policy revocation atomically with a write already in progress.
// All cumulative quotas and retained cleanup accounting remain in force.
// Neither activity nor token TTL renews this window.
// Constructor failure leaves both dependencies with the caller. This is not
// physical lease enforcement or an automatically enabled guest NIC.
func NewPassiveTransportUntil(ctx context.Context, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig, service PassiveService, route guestnet.TCPRoute, deadline time.Time) (*Transport, error) {
	return newPassiveTransport(ctx, stream, owner, arpConfig, service, route, deadline, true)
}

func newPassiveTransport(ctx context.Context, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig, service PassiveService, route guestnet.TCPRoute, deadline time.Time, requireLifetime bool) (*Transport, error) {
	deadline, err := passiveDeadline(ctx, deadline)
	if err != nil {
		return nil, err
	}
	if nilInterface(service) || !route.AnyPeerPort {
		return nil, ErrConfig
	}
	routes, err := guestnet.NewTCPRoutes([]guestnet.TCPRoute{route})
	if err != nil {
		return nil, err
	}
	// Config supplies only the common ARP binding in this mode. There is no
	// active Engine constructor, probe tuple or caller-selected dial target.
	c := Config{LocalIP: route.LocalIP, PeerIP: route.PeerIP, LocalMAC: route.LocalMAC, PeerMAC: route.PeerMAC}
	return newTransport(ctx, c, stream, owner, arpConfig, service, routes, deadline, requireLifetime)
}
