package guestnet

import (
	"errors"
	"net/netip"
)

const MaxTCPRoutes = 8

var (
	ErrTCPRouteConfig = errors.New("invalid or overlapping private TCP routes")
	ErrTCPRoute       = errors.New("no private TCP route matches frame")
)

// TCPDirection is relative to the configured local endpoint, not an AWS policy
// direction or an authorization decision. The trusted dispatcher supplies it.
type TCPDirection uint8

const (
	TCPFromPeer TCPDirection = iota + 1
	TCPToPeer
)

// TCPRoute describes one exact Ethernet/IPv4 pair and local TCP port. An active
// connection also fixes PeerPort. A passive service explicitly sets AnyPeerPort
// with PeerPort zero; only the peer's nonzero TCP port may vary. LocalIP may be
// link-local, while PeerIP must be global-unicast IPv4. These values come from
// trusted configuration, never fields learned from a frame.
type TCPRoute struct {
	LocalIP, PeerIP   netip.Addr
	LocalMAC, PeerMAC [6]byte
	LocalPort         uint16
	PeerPort          uint16
	AnyPeerPort       bool
}

// TCPRoutes is an immutable, bounded classifier. A result is a constructor-list
// index, NOT a dispatch permit, TCP state or physical owner. There is no route
// discovery, longest-prefix selection, forwarding, callback, queue or I/O.
// Classify never injects into an endpoint: a mismatch must not be discovered by
// trying endpoints in sequence, because a rejected injection may revoke one.
//
// Callers still own lifetime and aggregate byte/frame budgets, including rejected
// traffic, and must fence current physical/policy authority at actual delivery.
// They must validate each output against its EXPECTED route index rather than
// permit one endpoint to emit traffic matching another. TCP state and allowed
// peer ports remain the selected Engine/Listener's responsibility. ARP is outside
// this classifier. Constructed values are safe for concurrent reads.
type TCPRoutes struct {
	routes [MaxTCPRoutes]TCPRoute
	count  int
}

func NewTCPRoutes(routes []TCPRoute) (*TCPRoutes, error) {
	if len(routes) == 0 || len(routes) > MaxTCPRoutes {
		return nil, ErrTCPRouteConfig
	}
	r := &TCPRoutes{count: len(routes)}
	for i, route := range routes {
		if !validTCPRoute(route) {
			return nil, ErrTCPRouteConfig
		}
		for _, other := range r.routes[:i] {
			if route.LocalIP == other.LocalIP && route.PeerIP == other.PeerIP &&
				route.LocalMAC == other.LocalMAC && route.PeerMAC == other.PeerMAC &&
				route.LocalPort == other.LocalPort &&
				(route.AnyPeerPort || other.AnyPeerPort || route.PeerPort == other.PeerPort) {
				return nil, ErrTCPRouteConfig
			}
		}
		r.routes[i] = route
	}
	return r, nil
}

func validTCPRoute(r TCPRoute) bool {
	validMAC := func(m [6]byte) bool { return m != ([6]byte{}) && m[0]&1 == 0 }
	return r.LocalIP.Is4() && (r.LocalIP.IsGlobalUnicast() || r.LocalIP.IsLinkLocalUnicast()) &&
		r.PeerIP.Is4() && r.PeerIP.IsGlobalUnicast() && r.LocalIP != r.PeerIP &&
		validMAC(r.LocalMAC) && validMAC(r.PeerMAC) && r.LocalMAC != r.PeerMAC &&
		r.LocalPort != 0 && (r.AnyPeerPort && r.PeerPort == 0 || !r.AnyPeerPort && r.PeerPort != 0)
}

// Classify validates the complete frame using ParseTCPFrame before selecting its
// sole matching route. It returns -1 on every failure. Nil and zero-value routes,
// invalid direction, malformed frames and unmatched tuples fail closed. No input
// storage is retained; callers must not mutate the frame during this call.
func (r *TCPRoutes) Classify(direction TCPDirection, frame []byte) (int, error) {
	if r == nil || r.count == 0 || (direction != TCPFromPeer && direction != TCPToPeer) {
		return -1, ErrTCPRoute
	}
	p, err := ParseTCPFrame(frame)
	if err != nil {
		return -1, err
	}
	localIP, peerIP := p.DestinationIP, p.SourceIP
	localMAC, peerMAC := p.DestinationMAC, p.SourceMAC
	localPort, peerPort := p.DestinationPort, p.SourcePort
	if direction == TCPToPeer {
		localIP, peerIP = p.SourceIP, p.DestinationIP
		localMAC, peerMAC = p.SourceMAC, p.DestinationMAC
		localPort, peerPort = p.SourcePort, p.DestinationPort
	}
	for i, route := range r.routes[:r.count] {
		if localIP == route.LocalIP && peerIP == route.PeerIP && localMAC == route.LocalMAC &&
			peerMAC == route.PeerMAC && localPort == route.LocalPort &&
			(route.AnyPeerPort || peerPort == route.PeerPort) {
			return i, nil
		}
	}
	return -1, ErrTCPRoute
}
