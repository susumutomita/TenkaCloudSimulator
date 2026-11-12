package guesttcp

import (
	"context"
	"io"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// PassiveService is one trusted, bounded, in-memory TCP service, never a guest-
// selected handler or a physical authorization source. Inject must copy retained
// frame bytes. ReadFrame must honor its context and return stable owned bytes.
// ReadFrame may use exact ErrClosed for clean closure or caller cancellation;
// report failures with a specific error, preserving any wrapped/joined causes.
// Close must revoke admission, unblock pending calls and join all service work.
// Calls may run concurrently; Close is called once by Transport, never retried.
// A service with unbounded lifetime/queues/work is not a suitable dependency.
//
// Tuple classification is not TCP state validation: the service still owns its
// fixed listener, allowed source ports, lifetime and independent packet budgets.
// A Close error retains Transport capacity even when all calls have returned.
// Close must not synchronously call Transport.Close or Wait. An exact ErrClosed
// observed after shared cancellation is reported as the context error; this is
// cancellation-report disambiguation, not a proof of ordering among concurrent
// causes. Final physical-owner publication must still fence revocation.
type PassiveService interface {
	Inject([]byte) error
	ReadFrame(context.Context) ([]byte, error)
	Close() error
}

// RevocablePassiveService exposes the immutable admission lifetime of one
// trusted service. Lifetime must return the same non-nil context on every call,
// without blocking. Its cancellation is permanent and begins before Close waits
// for work to drain. The service must itself reject work after that lifetime.
// A deadline must remain fixed; replacing a context cannot renew an attachment.
//
// Transport captures this context once, checks it again at every commit callback
// and observes cancellation even while a service frame awaits owner admission.
// This gates local delivery attempts; it cannot recall previously delivered bytes
// or serialize physical/policy revocation with a write already in progress. The
// FrameCommitter still owns that atomic boundary and final observation authority.
type RevocablePassiveService interface {
	PassiveService
	Lifetime() context.Context
}

// NewTransportWithService adds one exact passive TCP route to the existing
// framed transport, sharing its decoder, serialized encoder, owner and aggregate
// packet/byte budget with the probe and optional gateway ARP. The route is copied
// and must describe the same guest IP/MAC and local gateway MAC as c, with one
// fixed local service port and AnyPeerPort. Overlap with the probe is rejected.
// The supplied service must implement this trusted route; every emitted frame is
// checked against its own route before the physical owner sees it. The same
// FrameCommitter must authorize each route independently in both directions;
// permission for the probe never implies permission for the passive service.
// A RevocablePassiveService's captured lifetime additionally gates every frame
// and shortens this transport's deadline; plain PassiveService keeps the original
// short-exchange contract without an independently observable admission context.
//
// Ownership of BOTH stream and service transfers only on success. Every error
// leaves both with the caller. Any route, pump, owner or lifetime failure closes
// the entire exchange. The original Engine deadline still bounds the transport
// to ten seconds; a service's earlier expiry is not extended. Completing an HTTP
// observation also closes the service. This is not a persistent boot service or
// a production physical owner, provider binding, SG/NACL decision or NIC attach.
func NewTransportWithService(ctx context.Context, c Config, stream io.ReadWriteCloser, owner FrameCommitter, arpConfig *guestnet.ARPConfig, service PassiveService, route guestnet.TCPRoute) (*Transport, error) {
	c = c.normalized()
	if nilInterface(service) || route.PeerIP != c.PeerIP || route.PeerMAC != c.PeerMAC ||
		route.LocalMAC != c.LocalMAC || !route.AnyPeerPort {
		return nil, ErrConfig
	}
	routes, err := guestnet.NewTCPRoutes([]guestnet.TCPRoute{
		{LocalIP: c.LocalIP, PeerIP: c.PeerIP, LocalMAC: c.LocalMAC, PeerMAC: c.PeerMAC, LocalPort: c.SourcePort, PeerPort: c.Port},
		route,
	})
	if err != nil {
		return nil, err
	}
	return newTransport(ctx, c, stream, owner, arpConfig, service, routes, time.Time{}, false)
}

func (t *Transport) receiveRouted(frame []byte) error {
	route, err := t.routes.Classify(guestnet.TCPFromPeer, frame)
	if err != nil {
		return err
	}
	if t.engine != nil && route == 0 {
		return t.commit(FromGuest, frame, func() error { return t.engine.Inject(frame) })
	}
	if t.service != nil && route == t.serviceRoute {
		return t.commit(FromGuest, frame, func() error { return t.service.Inject(frame) })
	}
	return guestnet.ErrTCPRoute
}

func (t *Transport) sendService() error {
	for {
		frame, err := t.service.ReadFrame(t.ctx)
		if err != nil {
			if err == ErrClosed {
				if revoked := serviceLifetimeError(t.serviceLifetime, t.serviceExpires); revoked != nil {
					return revoked
				}
			}
			// Listener uses this exact clean-close sentinel for caller
			// cancellation. Preserve the intentional transport cause so an
			// HTTP observation does not depend on which canceled pump wakes
			// first. Never erase a distinct, wrapped or joined service failure.
			if err == ErrClosed && t.ctx.Err() != nil {
				return t.ctx.Err()
			}
			return err
		}
		if err := t.deliverTCP(t.serviceRoute, frame); err != nil {
			return err
		}
	}
}

func (t *Transport) deliverTCP(expected int, frame []byte) error {
	if t.routes != nil {
		// Account rejected output too; it must not bypass the shared allowance.
		if err := t.account(len(frame)); err != nil {
			return err
		}
		route, err := t.routes.Classify(guestnet.TCPToPeer, frame)
		if err != nil {
			return err
		}
		if route != expected {
			return guestnet.ErrTCPRoute
		}
		if t.persistent != nil {
			return t.persistent.submit(t.ctx, nil, frame)
		}
		return t.commit(ToGuest, frame, func() error { return t.encoder.WriteFrame(frame) })
	}
	return t.deliver(frame)
}
