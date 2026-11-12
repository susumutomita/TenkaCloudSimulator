package guesttcp

import (
	"context"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
	"sync"
	"time"
)

// IssuedPersistentProbeResource supplies one immutable issuance descriptor from
// the SAME exclusive resource as Stream and Transact. No concrete issuer exists.
type IssuedPersistentProbeResource interface {
	IssuedProbeResource
	PersistentDescriptor() OwnedProbeDescriptor
}
type OwnedProbeDescriptor struct {
	Config   OwnedProbeConfig
	Binding  HTTPProbeScopeBinding
	Services []HTTPProbeService
	Passive  guestnet.TCPRoute
	ARP      *guestnet.ARPConfig
}

func copyOwnedDescriptor(d OwnedProbeDescriptor) OwnedProbeDescriptor {
	d.Config.NIC.ServicePorts = append([]uint16(nil), d.Config.NIC.ServicePorts...)
	d.Config.Paths = append([]string(nil), d.Config.Paths...)
	d.Services = append([]HTTPProbeService(nil), d.Services...)
	if d.ARP != nil {
		a := *d.ARP
		d.ARP = &a
	}
	return d
}

// Descriptor validation precedes ownership transfer. Its metadata is not proof
// of physical authority; the resource's authoritative transaction remains required.
func NewOwnedPersistentProbeSession(ctx context.Context, r IssuedPersistentProbeResource) (*OwnedProbeSession, error) {
	if nilInterface(r) {
		return nil, ErrOwnedProbeSession
	}
	d := copyOwnedDescriptor(r.PersistentDescriptor())
	n := d.Config.NIC
	p := d.Passive
	if !validProbeScopeBinding(d.Binding) || len(d.Services) < 1 || len(d.Services) > 8 || !p.AnyPeerPort || p.PeerIP != n.PeerIP || p.PeerMAC != n.PeerMAC || p.LocalMAC != n.LocalMAC || (p.LocalIP == n.LocalIP && p.LocalPort >= guestnet.ProbeSourcePort && int(p.LocalPort) < int(guestnet.ProbeSourcePort)+guestnet.MaxProbeSessions) {
		return nil, ErrOwnedProbeSession
	}
	routes, e := guestnet.NewTCPRoutes([]guestnet.TCPRoute{p})
	if e != nil {
		return nil, e
	}
	names := map[string]bool{}
	for _, v := range d.Services {
		port, path := false, false
		for _, p := range n.ServicePorts {
			port = port || p == v.Port
		}
		for _, p := range d.Config.Paths {
			path = path || p == v.Path
		}
		if !validProbeServiceName(v.Name) || names[v.Name] || !port || !path {
			return nil, ErrOwnedProbeSession
		}
		names[v.Name] = true
	}
	var arp *guestnet.ARPResponder
	if d.ARP != nil {
		if d.ARP.GuestIP != n.PeerIP || d.ARP.GuestMAC != n.PeerMAC || d.ARP.GatewayMAC != n.LocalMAC {
			return nil, ErrOwnedProbeSession
		}
		arp, e = guestnet.NewARPResponder(*d.ARP)
		if e != nil {
			return nil, e
		}
	}
	s, e := NewOwnedProbeSession(ctx, r, d.Config)
	if e != nil {
		return nil, e
	}
	s.descriptor = &d
	s.passive = routes
	s.arp = arp
	return s, nil
}

// OwnedProbeAttachment retains independent raw-owner and transport obligations.
// Scope is published once after complete success and remains readable after revoke.
// Constructor failure returns this handle after reservation, even if no transport
// took ownership of the passive service. ServiceTransferred is authoritative for
// this handoff only, not physical VM ownership.
type OwnedProbeAttachment struct {
	owner              *OwnedProbeSession
	transport          *PersistentTransport
	scope              *HTTPProbeOperationScope
	serviceTransferred bool
	mu                 sync.Mutex
}

func (h *OwnedProbeAttachment) Scope() *HTTPProbeOperationScope {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.scope
}
func (h *OwnedProbeAttachment) ServiceTransferred() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.serviceTransferred
}
func (h *OwnedProbeAttachment) CloseContext(ctx context.Context) error {
	if h == nil || nilInterface(ctx) {
		return ErrOwnedProbeSession
	}
	h.owner.Revoke()
	var e error
	if h.transport != nil {
		e = h.transport.CloseContext(ctx)
	}
	return errors.Join(e, h.owner.Wait(ctx))
}
func (h *OwnedProbeAttachment) WaitContext(ctx context.Context) error {
	if h == nil || nilInterface(ctx) {
		return ErrOwnedProbeSession
	}
	var e error
	if h.transport != nil {
		e = h.transport.WaitContext(ctx)
	}
	return errors.Join(e, h.owner.Wait(ctx))
}

// ctx bounds attachment admission only. Persistent lifetime is the captured
// owner context, intersected with service and fixed requested deadline; no
// request-scoped defer cancel can terminate or renew a successful attachment.
func (s *OwnedProbeSession) AttachPersistent(ctx context.Context, service RevocablePassiveService, deadline time.Time) (*OwnedProbeAttachment, error) {
	if s == nil || nilInterface(ctx) {
		return nil, ErrOwnedProbeSession
	}
	s.ioMu.Lock()
	if s.current(ctx) != nil || s.descriptor == nil || s.attached {
		s.ioMu.Unlock()
		return nil, ErrOwnedProbeSession
	}
	s.attached = true
	s.ioMu.Unlock()
	h := &OwnedProbeAttachment{owner: s}
	fail := func(e error) (*OwnedProbeAttachment, error) {
		s.Revoke()
		if h.transport != nil {
			h.transport.nic.base.beginClose()
		}
		return h, e
	}
	d := s.descriptor
	t, e := NewPersistentTransportUntil(s.ctx, d.Config.NIC, s.Stream(), s, d.ARP, service, d.Passive, deadline)
	if e != nil {
		return fail(e)
	}
	h.transport = t
	h.serviceTransferred = true
	scope, e := t.BindHTTPProbeOperations(d.Binding, d.Services)
	if e != nil {
		return fail(e)
	}
	s.ioMu.Lock()
	e = s.current(ctx)
	if e == nil {
		h.scope = scope
	}
	s.ioMu.Unlock()
	if e != nil {
		return fail(e)
	}
	return h, nil
}
