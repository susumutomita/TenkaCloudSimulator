package guesttcp

import (
	"context"
	"sort"
)

// HTTPProbeScopeBinding contains fixed-size provider-issued identities, not Host
// problem or scoring metadata. Equality is not authentication or physical proof.
// The trusted provider must retain the capability created for its actual NIC.
type HTTPProbeScopeBinding struct {
	Owner, Deployment, Lease, BackendIncarnation [32]byte
}

// HTTPProbeService is an immutable named target chosen by the trusted provider.
// Names are bounded ASCII labels; callers cannot supply an address or path to Start.
type HTTPProbeService struct {
	Name string
	Port uint16
	Path string
}

type sealedHTTPProbeScope struct {
	nic      *persistentNIC
	binding  HTTPProbeScopeBinding
	services []HTTPProbeService
}

// HTTPProbeOperationScope is an in-process capability, not a serializable token.
// Copying this wrapper retains the same sealed NIC-owned identity and budgets.
// No production registration, authentication or physical owner is supplied here.
type HTTPProbeOperationScope struct{ state *sealedHTTPProbeScope }

type ScopedHTTPProbeSnapshot struct {
	Binding   HTTPProbeScopeBinding
	Service   HTTPProbeService
	Operation HTTPProbeOperationSnapshot
}

func validProbeScopeBinding(b HTTPProbeScopeBinding) bool {
	zero := [32]byte{}
	return b.Owner != zero && b.Deployment != zero && b.Lease != zero && b.BackendIncarnation != zero
}
func validProbeServiceName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func sameProbeScope(s *sealedHTTPProbeScope, b HTTPProbeScopeBinding, services []HTTPProbeService) bool {
	if s.binding != b || len(s.services) != len(services) {
		return false
	}
	for i := range services {
		if s.services[i] != services[i] {
			return false
		}
	}
	return true
}

// BindHTTPProbeOperations seals one scope and service table for this NIC lifetime.
// Identical registration, including through another wrapper, returns its existing
// capability even after revocation for read-only settlement discovery. Conflicting
// registration never replaces it. New registration checks current admission; this
// check is not atomic with owner revocation and does not grant execution rights.
// Only a trusted provider may call this constructor; matching IDs grant no authority.
func (t *PersistentTransport) BindHTTPProbeOperations(binding HTTPProbeScopeBinding, services []HTTPProbeService) (*HTTPProbeOperationScope, error) {
	if t == nil || t.nic == nil || t.nic.base == nil || !validProbeScopeBinding(binding) || len(services) == 0 || len(services) > 8 {
		return nil, ErrConfig
	}
	copyServices := append([]HTTPProbeService(nil), services...)
	sort.Slice(copyServices, func(i, j int) bool { return copyServices[i].Name < copyServices[j].Name })
	for i, service := range copyServices {
		if !validProbeServiceName(service.Name) || (service.Path != "/" && service.Path != "/healthz") || (i > 0 && copyServices[i-1].Name == service.Name) {
			return nil, ErrConfig
		}
		allowed := false
		for _, port := range t.nic.config.ServicePorts {
			if port == service.Port {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, ErrConfig
		}
	}
	n := t.nic
	n.operationsMu.Lock()
	existing := n.operationScope
	n.operationsMu.Unlock()
	if existing != nil {
		if !sameProbeScope(existing.state, binding, copyServices) {
			return nil, ErrHTTPProbeOperationBinding
		}
		return existing, nil
	}
	owner, ok := n.base.owner.(ProbeHTTPObservationCommitter)
	if !ok || nilInterface(owner) {
		return nil, ErrConfig
	}
	if err := n.base.admissionError(); err != nil {
		return nil, err
	}
	n.operationsMu.Lock()
	defer n.operationsMu.Unlock()
	if existing = n.operationScope; existing != nil {
		if !sameProbeScope(existing.state, binding, copyServices) {
			return nil, ErrHTTPProbeOperationBinding
		}
		return existing, nil
	}
	scope := &HTTPProbeOperationScope{state: &sealedHTTPProbeScope{nic: n, binding: binding, services: copyServices}}
	n.operationScope = scope
	return scope, nil
}

// Binding and Services return values/copies; mutation cannot rebind the NIC.
func (s *HTTPProbeOperationScope) Binding() (HTTPProbeScopeBinding, error) {
	if s == nil || s.state == nil {
		return HTTPProbeScopeBinding{}, ErrConfig
	}
	return s.state.binding, nil
}
func (s *HTTPProbeOperationScope) Services() []HTTPProbeService {
	if s == nil || s.state == nil {
		return nil
	}
	return append([]HTTPProbeService(nil), s.state.services...)
}
func (s *HTTPProbeOperationScope) service(name string) (HTTPProbeService, bool) {
	if s == nil || s.state == nil {
		return HTTPProbeService{}, false
	}
	for _, service := range s.state.services {
		if service.Name == name {
			return service, true
		}
	}
	return HTTPProbeService{}, false
}

// Start records at most one operation for the original finite caller deadline.
// Exact duplicates return the retained handle without consulting a new context.
// A different service or raw/scoped ownership cannot adopt that key. New expired
// or unbounded calls reject before retention; later execution errors are retained.
// Physical frame/publication fencing remains the transport owner's obligation.
func (s *HTTPProbeOperationScope) Start(ctx context.Context, key HTTPProbeOperationKey, serviceName string) (*HTTPProbeOperation, error) {
	service, ok := s.service(serviceName)
	if !ok {
		return nil, ErrConfig
	}
	t := &PersistentTransport{nic: s.state.nic}
	return t.startHTTPObservation(ctx, key, service.Port, service.Path, s.state, service.Name)
}

// Snapshot is read-only and remains available after NIC revocation. Missing or
// foreign keys mean unknown, never settled. It performs no current-lease adoption,
// retry, cancellation, work dispatch or whole-NIC cleanup certification.
func (s *HTTPProbeOperationScope) Snapshot(key HTTPProbeOperationKey) (ScopedHTTPProbeSnapshot, bool, error) {
	if s == nil || s.state == nil || s.state.nic == nil {
		return ScopedHTTPProbeSnapshot{}, false, ErrConfig
	}
	n := s.state.nic
	n.operationsMu.Lock()
	h := n.operations[key]
	n.operationsMu.Unlock()
	if h == nil || h.scope != s.state {
		return ScopedHTTPProbeSnapshot{}, false, nil
	}
	service, ok := s.service(h.service)
	if !ok {
		return ScopedHTTPProbeSnapshot{}, false, ErrHTTPProbeOperationBinding
	}
	snapshot, err := h.Snapshot()
	if err != nil {
		return ScopedHTTPProbeSnapshot{}, false, err
	}
	return ScopedHTTPProbeSnapshot{Binding: s.state.binding, Service: service, Operation: snapshot}, true, nil
}
