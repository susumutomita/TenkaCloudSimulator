package guesttcp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"time"
)

// HTTPProbeCommand is a decoded v1 command, never authority or an endpoint.
// Only Dispatch on an independently selected capability can use it.
type HTTPProbeCommand struct {
	Version  int                       `json:"version"`
	Command  string                    `json:"command"`
	Binding  HTTPProbeWireBinding      `json:"binding"`
	Service  string                    `json:"service"`
	Key      string                    `json:"key"`
	Deadline *HTTPProbeCommandDeadline `json:"deadline,omitempty"`
}
type HTTPProbeCommandDeadline struct {
	Domain  string `json:"domain"`
	EpochMs int64  `json:"epochMs"`
}

// DecodeHTTPProbeCommand rejects duplicate/unknown fields and oversized input.
// The wire never selects the capability that will execute this command.
func DecodeHTTPProbeCommand(data []byte) (HTTPProbeCommand, error) {
	return decodeProbeCommandVersion(data, 1, "epochMs")
}

func decodeProbeCommandVersion(data []byte, version int, timeField string) (HTTPProbeCommand, error) {
	var out HTTPProbeCommand
	if len(data) == 0 || len(data) > 2048 {
		return out, ErrConfig
	}
	d := json.NewDecoder(bytes.NewReader(data))
	value, err := probeCommandJSON(d)
	if err != nil {
		return out, ErrConfig
	}
	if _, err = d.Token(); err != io.EOF {
		return out, ErrConfig
	}
	m, ok := value.(map[string]any)
	if !ok {
		return out, ErrConfig
	}
	kind, _ := m["command"].(string)
	keys := []string{"version", "command", "binding", "service", "key"}
	if kind == "start" {
		keys = append(keys, "deadline")
	} else if kind != "read" {
		return out, ErrConfig
	}
	if !probeCommandFields(m, keys...) || m["version"] != float64(version) {
		return out, ErrConfig
	}
	b, ok := m["binding"].(map[string]any)
	if !ok || !probeCommandFields(b, "owner", "deployment", "lease", "backendIncarnation") {
		return out, ErrConfig
	}
	strs := make([]string, 4)
	for i, k := range []string{"owner", "deployment", "lease", "backendIncarnation"} {
		strs[i], ok = b[k].(string)
		if !ok || !probeCommandHex(strs[i], 32) {
			return out, ErrConfig
		}
	}
	service, ok := m["service"].(string)
	if !ok || !validProbeServiceName(service) {
		return out, ErrConfig
	}
	key, ok := m["key"].(string)
	if !ok || !probeCommandHex(key, 16) {
		return out, ErrConfig
	}
	out = HTTPProbeCommand{Version: version, Command: kind, Binding: HTTPProbeWireBinding{strs[0], strs[1], strs[2], strs[3]}, Service: service, Key: key}
	if kind == "start" {
		v, ok := m["deadline"].(map[string]any)
		if !ok || !probeCommandFields(v, "domain", timeField) {
			return HTTPProbeCommand{}, ErrConfig
		}
		domain, ok := v["domain"].(string)
		if !ok || !validProbeServiceName(domain) {
			return HTTPProbeCommand{}, ErrConfig
		}
		n, ok := v[timeField].(float64)
		if !ok || n < 1 || n > float64(maxWireMillis) || math.Trunc(n) != n {
			return HTTPProbeCommand{}, ErrConfig
		}
		out.Deadline = &HTTPProbeCommandDeadline{domain, int64(n)}
	}
	return out, nil
}
func probeCommandFields(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}
func probeCommandHex(s string, n int) bool {
	if len(s) != 2*n {
		return false
	}
	nonzero := false
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
		nonzero = nonzero || c != '0'
	}
	return nonzero
}

// Token parsing rejects duplicate keys before object materialization. Arrays are
// not part of this protocol; depth is bounded by the 2048-byte input ceiling.
func probeCommandJSON(d *json.Decoder) (any, error) {
	t, e := d.Token()
	if e != nil {
		return nil, e
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return t, nil
	}
	if delim != '{' {
		return nil, ErrConfig
	}
	m := map[string]any{}
	for d.More() {
		k, e := d.Token()
		if e != nil {
			return nil, e
		}
		s, ok := k.(string)
		if !ok {
			return nil, ErrConfig
		}
		if _, exists := m[s]; exists {
			return nil, ErrConfig
		}
		v, e := probeCommandJSON(d)
		if e != nil {
			return nil, e
		}
		m[s] = v
	}
	end, e := d.Token()
	if e != nil || end != json.Delim('}') {
		return nil, ErrConfig
	}
	return m, nil
}

// HTTPProbeCommandClock is trusted provider input, copied at construction. It
// qualifies the backend Unix-millisecond domain only, not Host clock conversion.
// Production qualification/authentication/physical ownership are not supplied.
type HTTPProbeCommandClock struct {
	Domain                    string
	ValidFromMs, ValidUntilMs int64
}
type HTTPProbeCommandDispatcher struct {
	scope   *HTTPProbeOperationScope
	binding HTTPProbeWireBinding
	service string
	clock   HTTPProbeCommandClock
}

func NewHTTPProbeCommandDispatcher(scope *HTTPProbeOperationScope, service string, clock HTTPProbeCommandClock) (*HTTPProbeCommandDispatcher, error) {
	b, err := scope.Binding()
	if err != nil {
		return nil, err
	}
	if _, ok := scope.service(service); !ok {
		return nil, ErrConfig
	}
	if !validProbeServiceName(clock.Domain) || clock.ValidFromMs < 1 || clock.ValidUntilMs <= clock.ValidFromMs || clock.ValidUntilMs > maxWireMillis {
		return nil, ErrConfig
	}
	wire := HTTPProbeWireBinding{hex.EncodeToString(b.Owner[:]), hex.EncodeToString(b.Deployment[:]), hex.EncodeToString(b.Lease[:]), hex.EncodeToString(b.BackendIncarnation[:])}
	return &HTTPProbeCommandDispatcher{scope, wire, service, clock}, nil
}

// Dispatch is an in-process command boundary, not a network handler. A nil
// snapshot is unknown, never settlement. Errors do not prove non-execution.
// Request cancellation cannot cancel an admitted operation: the existing scope
// owns its own deadline timer. No handler-scoped defer cancel is installed.
func (d *HTTPProbeCommandDispatcher) Dispatch(data []byte) (*HTTPProbeWireSnapshot, error) {
	if d == nil {
		return nil, ErrConfig
	}
	c, err := DecodeHTTPProbeCommand(data)
	if err != nil {
		return nil, err
	}
	if c.Binding != d.binding || c.Service != d.service {
		return nil, ErrHTTPProbeOperationBinding
	}
	raw, _ := hex.DecodeString(c.Key)
	var key HTTPProbeOperationKey
	copy(key[:], raw)
	if c.Command == "read" {
		v, _, err := d.scope.WireSnapshot(key)
		return v, err
	}
	if c.Deadline.Domain != d.clock.Domain {
		return nil, ErrConfig
	}
	// Retained exact duplicates precede fresh clock qualification/expiry checks.
	// Foreign/raw keys remain protected by the scope's existing atomic admission.
	if v, found, err := d.scope.Snapshot(key); err != nil {
		return nil, err
	} else if found {
		if v.Service.Name != d.service {
			return nil, ErrHTTPProbeOperationBinding
		}
		_, err = d.scope.Start(nil, key, d.service)
		if err != nil {
			return nil, err
		}
		out, _, err := d.scope.WireSnapshot(key)
		return out, err
	}
	now := time.Now().UnixMilli()
	if now < d.clock.ValidFromMs || now >= d.clock.ValidUntilMs || c.Deadline.EpochMs > d.clock.ValidUntilMs {
		return nil, ErrConfig
	}
	deadline := time.UnixMilli(c.Deadline.EpochMs)
	if !time.Now().Before(deadline) {
		return nil, context.DeadlineExceeded
	}
	// Deadline-only parent: Start installs and owns the cancellable timer; this
	// value creates no timer/goroutine needing request-handler cleanup.
	ctx := probeCommandDeadlineContext{Context: context.Background(), deadline: deadline}
	if _, err = d.scope.Start(ctx, key, d.service); err != nil {
		return nil, err
	}
	out, _, err := d.scope.WireSnapshot(key)
	return out, err
}

type probeCommandDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c probeCommandDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }
