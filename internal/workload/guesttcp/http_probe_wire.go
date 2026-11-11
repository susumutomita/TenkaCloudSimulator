package guesttcp

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

// HTTPProbeWireSnapshot is a versioned representation, not a network endpoint
// or authentication mechanism. Its binding is opaque provider-issued identity.
// A consumer must match it against an independently trusted registry.
type HTTPProbeWireSnapshot struct {
	ClockDomain string               `json:"clockDomain,omitempty"`
	Version     int                  `json:"version"`
	Binding     HTTPProbeWireBinding `json:"binding"`
	Service     HTTPProbeWireService `json:"service"`
	Key         string               `json:"key"`
	StartedAtMs int64                `json:"startedAtMs"`
	DeadlineMs  int64                `json:"deadlineMs"`
	ResultReady bool                 `json:"resultReady"`
	Settled     bool                 `json:"settled"`
	Route       *HTTPProbeWireRoute  `json:"route,omitempty"`
	Result      *HTTPProbeWireResult `json:"result,omitempty"`
}
type HTTPProbeWireBinding struct {
	Owner              string `json:"owner"`
	Deployment         string `json:"deployment"`
	Lease              string `json:"lease"`
	BackendIncarnation string `json:"backendIncarnation"`
}
type HTTPProbeWireService struct {
	Name string `json:"name"`
	Port uint16 `json:"port"`
	Path string `json:"path"`
}
type HTTPProbeWireRoute struct {
	LocalIP     string `json:"localIP"`
	PeerIP      string `json:"peerIP"`
	LocalMAC    string `json:"localMAC"`
	PeerMAC     string `json:"peerMAC"`
	LocalPort   uint16 `json:"localPort"`
	PeerPort    uint16 `json:"peerPort"`
	AnyPeerPort bool   `json:"anyPeerPort"`
}
type HTTPProbeWireResult struct {
	Error       string                    `json:"error,omitempty"`
	Observation *HTTPProbeWireObservation `json:"observation,omitempty"`
}
type HTTPProbeWireObservation struct {
	Route        HTTPProbeWireRoute `json:"route"`
	Path         string             `json:"path"`
	Status       int                `json:"status"`
	StartedAtMs  int64              `json:"startedAtMs"`
	ReceivedAtMs int64              `json:"receivedAtMs"`
}

// WireSnapshot reads retained state only. found=false means unknown, never
// settled, canceled or safe to repeat. Callers may encode unknown as JSON null.
// Backend epoch milliseconds must not be compared to a Host monotonic clock.
func (s *HTTPProbeOperationScope) WireSnapshot(key HTTPProbeOperationKey) (*HTTPProbeWireSnapshot, bool, error) {
	v, found, err := s.Snapshot(key)
	if err != nil || !found {
		return nil, found, err
	}
	wire, err := probeSnapshotWire(v)
	if err != nil {
		return nil, false, err
	}
	return wire, true, nil
}

const maxWireMillis int64 = 1<<53 - 1

func wireMillis(t time.Time) (int64, error) {
	if t.IsZero() || t.Before(time.UnixMilli(1)) || t.After(time.UnixMilli(maxWireMillis)) {
		return 0, ErrConfig
	}
	n := t.UnixMilli()
	if n <= 0 || n > maxWireMillis {
		return 0, ErrConfig
	}
	return n, nil
}
func probeWireRoute(r guestnet.TCPRoute) (HTTPProbeWireRoute, error) {
	if !r.LocalIP.Is4() || !r.PeerIP.Is4() || r.LocalPort < guestnet.ProbeSourcePort || int(r.LocalPort) >= int(guestnet.ProbeSourcePort)+guestnet.MaxProbeSessions || r.PeerPort == 0 || r.AnyPeerPort || r.LocalMAC == ([6]byte{}) || r.PeerMAC == ([6]byte{}) {
		return HTTPProbeWireRoute{}, ErrConfig
	}
	return HTTPProbeWireRoute{LocalIP: r.LocalIP.String(), PeerIP: r.PeerIP.String(), LocalMAC: net.HardwareAddr(r.LocalMAC[:]).String(), PeerMAC: net.HardwareAddr(r.PeerMAC[:]).String(), LocalPort: r.LocalPort, PeerPort: r.PeerPort, AnyPeerPort: false}, nil
}
func probeSnapshotWire(v ScopedHTTPProbeSnapshot) (*HTTPProbeWireSnapshot, error) {
	return probeSnapshotWireClock(v, 1, "", wireMillis)
}
func probeSnapshotWireClock(v ScopedHTTPProbeSnapshot, version int, domain string, millis func(time.Time) (int64, error)) (*HTTPProbeWireSnapshot, error) {
	op := v.Operation
	if !validProbeScopeBinding(v.Binding) || !validProbeServiceName(v.Service.Name) || v.Service.Port == 0 || (v.Service.Path != "/" && v.Service.Path != "/healthz") || op.Key == (HTTPProbeOperationKey{}) || op.Port != v.Service.Port || op.Path != v.Service.Path || op.Settled && !op.ResultReady {
		return nil, ErrConfig
	}
	started, err := millis(op.StartedAt)
	if err != nil {
		return nil, err
	}
	deadline, err := millis(op.Deadline)
	if err != nil || deadline-started > HTTPObservationTimeout.Milliseconds() || (deadline <= started && op.RouteReady) {
		return nil, ErrConfig
	}
	out := &HTTPProbeWireSnapshot{Version: version, ClockDomain: domain, Binding: HTTPProbeWireBinding{Owner: hex.EncodeToString(v.Binding.Owner[:]), Deployment: hex.EncodeToString(v.Binding.Deployment[:]), Lease: hex.EncodeToString(v.Binding.Lease[:]), BackendIncarnation: hex.EncodeToString(v.Binding.BackendIncarnation[:])}, Service: HTTPProbeWireService{Name: v.Service.Name, Port: v.Service.Port, Path: v.Service.Path}, Key: hex.EncodeToString(op.Key[:]), StartedAtMs: started, DeadlineMs: deadline, ResultReady: op.ResultReady, Settled: op.Settled}
	if op.RouteReady {
		r, err := probeWireRoute(op.Route)
		if err != nil || r.PeerPort != op.Port {
			return nil, ErrConfig
		}
		out.Route = &r
	}
	if !op.ResultReady {
		if op.ResultError != nil || op.Observation != (ProbeHTTPObservation{}) {
			return nil, ErrConfig
		}
		return out, nil
	}
	out.Result = &HTTPProbeWireResult{}
	if op.ResultError != nil {
		if op.Observation != (ProbeHTTPObservation{}) {
			return nil, ErrConfig
		}
		switch {
		case errors.Is(op.ResultError, context.DeadlineExceeded):
			out.Result.Error = "deadline_exceeded"
		case errors.Is(op.ResultError, context.Canceled):
			out.Result.Error = "canceled"
		default:
			out.Result.Error = "probe_failed"
		}
		return out, nil
	}
	obs := op.Observation
	if !op.RouteReady || obs.Route != op.Route || obs.Path != op.Path || obs.StatusCode < 100 || obs.StatusCode > 599 || obs.StartedAt.Before(op.StartedAt) || obs.ReceivedAt.Before(obs.StartedAt) || !obs.ReceivedAt.Before(op.Deadline) {
		return nil, ErrConfig
	}
	os, err := millis(obs.StartedAt)
	if err != nil {
		return nil, err
	}
	received, err := millis(obs.ReceivedAt)
	if err != nil || received >= deadline {
		return nil, ErrConfig
	}
	out.Result.Observation = &HTTPProbeWireObservation{Route: *out.Route, Path: obs.Path, Status: obs.StatusCode, StartedAtMs: os, ReceivedAtMs: received}
	return out, nil
}
