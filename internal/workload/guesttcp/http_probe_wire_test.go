package guesttcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestnet"
)

func wireTestSnapshot() ScopedHTTPProbeSnapshot {
	start := time.UnixMilli(1700000000000)
	route := guestnet.TCPRoute{LocalIP: netip.MustParseAddr("192.0.2.1"), PeerIP: netip.MustParseAddr("192.0.2.2"), LocalMAC: [6]byte{2, 0, 0, 0, 0, 1}, PeerMAC: [6]byte{2, 0, 0, 0, 0, 2}, LocalPort: 40000, PeerPort: 80}
	return ScopedHTTPProbeSnapshot{Binding: scopeBinding(), Service: HTTPProbeService{Name: "web", Port: 80, Path: "/"}, Operation: HTTPProbeOperationSnapshot{Key: operationKey(1), Port: 80, Path: "/", StartedAt: start, Deadline: start.Add(time.Second), RouteReady: true, Route: route, ResultReady: true, Settled: true, Observation: ProbeHTTPObservation{HTTPObservation: HTTPObservation{StatusCode: 200, StartedAt: start.Add(time.Millisecond), ReceivedAt: start.Add(20 * time.Millisecond)}, Route: route, Path: "/"}}}
}
func TestHTTPProbeWireSharedGolden(t *testing.T) {
	data, err := os.ReadFile("../../../test/fixtures/probe-operation-wire-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string          `json:"name"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			s := wireTestSnapshot()
			var got *HTTPProbeWireSnapshot
			switch c.Name {
			case "unknown":
			case "pending", "allocated":
				s.Operation.ResultReady = false
				s.Operation.Settled = false
				s.Operation.Observation = ProbeHTTPObservation{}
				s.Operation.RouteReady = c.Name == "allocated"
			case "ready-unsettled":
				s.Operation.Settled = false
			case "success":
			case "probe_failed", "canceled", "deadline_exceeded", "expired-no-route":
				s.Operation.Observation = ProbeHTTPObservation{}
				s.Operation.ResultError = errors.New("private internal details must not escape")
				if c.Name == "canceled" {
					s.Operation.ResultError = context.Canceled
				}
				if c.Name == "expired-no-route" {
					s.Operation.RouteReady = false
					s.Operation.Deadline = s.Operation.StartedAt.Add(-time.Millisecond)
				}
				if c.Name == "deadline_exceeded" || c.Name == "expired-no-route" {
					s.Operation.ResultError = errors.Join(context.DeadlineExceeded, errors.New("private detail"))
				}
			default:
				t.Fatal("unknown golden case", c.Name)
			}
			if c.Name != "unknown" {
				got, err = probeSnapshotWire(s)
				if err != nil {
					t.Fatal(err)
				}
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var a, b any
			_ = json.Unmarshal(encoded, &a)
			_ = json.Unmarshal(c.Snapshot, &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("golden mismatch\ngot %s\nwant %s", encoded, c.Snapshot)
			}
		})
	}
}
func TestHTTPProbeWireRejectsContradictions(t *testing.T) {
	cases := map[string]func(*ScopedHTTPProbeSnapshot){
		"zero binding":            func(v *ScopedHTTPProbeSnapshot) { v.Binding.Owner = [32]byte{} },
		"zero key":                func(v *ScopedHTTPProbeSnapshot) { v.Operation.Key = HTTPProbeOperationKey{} },
		"wrong service":           func(v *ScopedHTTPProbeSnapshot) { v.Service.Port = 8080 },
		"unknown path":            func(v *ScopedHTTPProbeSnapshot) { v.Operation.Path = "/else"; v.Service.Path = "/else" },
		"settled unready":         func(v *ScopedHTTPProbeSnapshot) { v.Operation.ResultReady = false },
		"unready result":          func(v *ScopedHTTPProbeSnapshot) { v.Operation.ResultReady = false; v.Operation.Settled = false },
		"missing route":           func(v *ScopedHTTPProbeSnapshot) { v.Operation.RouteReady = false },
		"wildcard route":          func(v *ScopedHTTPProbeSnapshot) { v.Operation.Route.AnyPeerPort = true },
		"zero port":               func(v *ScopedHTTPProbeSnapshot) { v.Operation.Route.LocalPort = 0 },
		"wrong observation route": func(v *ScopedHTTPProbeSnapshot) { v.Operation.Observation.Route.LocalPort++ },
		"error and success":       func(v *ScopedHTTPProbeSnapshot) { v.Operation.ResultError = context.Canceled },
		"late":                    func(v *ScopedHTTPProbeSnapshot) { v.Operation.Observation.ReceivedAt = v.Operation.Deadline },
		"submillisecond late": func(v *ScopedHTTPProbeSnapshot) {
			v.Operation.Deadline = v.Operation.Deadline.Add(time.Microsecond)
			v.Operation.Observation.ReceivedAt = v.Operation.Deadline.Add(time.Microsecond)
		},
		"unsafe epoch":   func(v *ScopedHTTPProbeSnapshot) { v.Operation.StartedAt = time.UnixMilli(maxWireMillis + 1) },
		"overflow epoch": func(v *ScopedHTTPProbeSnapshot) { v.Operation.StartedAt = time.Unix(1<<62, 0) },
		"zero epoch":     func(v *ScopedHTTPProbeSnapshot) { v.Operation.StartedAt = time.Time{} },
		"inverted observation": func(v *ScopedHTTPProbeSnapshot) {
			v.Operation.Observation.ReceivedAt = v.Operation.Observation.StartedAt.Add(-time.Nanosecond)
		},
		"invalid status": func(v *ScopedHTTPProbeSnapshot) { v.Operation.Observation.StatusCode = 600 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := wireTestSnapshot()
			mutate(&v)
			if got, err := probeSnapshotWire(v); got != nil || !errors.Is(err, ErrConfig) {
				t.Fatal("contradiction accepted", got, err)
			}
		})
	}
}
func TestHTTPProbeWireReadsActualRetainedOperation(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	scope := bindTestScope(t, f)
	if value, found, err := scope.WireSnapshot(operationKey(99)); value != nil || found || err != nil {
		t.Fatal("unknown fabricated", value, found, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	h, err := scope.Start(ctx, operationKey(1), "web")
	if err != nil {
		t.Fatal(err)
	}
	operationSettled(t, h)
	value, found, err := scope.WireSnapshot(operationKey(1))
	if err != nil || !found || value == nil || !value.Settled || value.Result.Observation == nil || value.Route.LocalPort == 0 {
		t.Fatal("actual operation not projected", value, found, err)
	}
	value.Binding.Owner = "mutated"
	value.Route.LocalPort = 1
	again, _, err := scope.WireSnapshot(operationKey(1))
	if err != nil || again.Binding.Owner == "mutated" || again.Route.LocalPort == 1 {
		t.Fatal("wire result mutated retained state", err)
	}
}
