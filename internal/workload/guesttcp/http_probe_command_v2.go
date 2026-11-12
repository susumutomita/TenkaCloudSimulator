package guesttcp

import (
	"context"
	"encoding/hex"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
)

// V2 accepts only absolute process-clock deadlines. The server selects the
// capability and clock; neither is supplied by the command or HTTP principal.
type HTTPProbeCommandDispatcherV2 struct {
	scope   *HTTPProbeOperationScope
	binding HTTPProbeWireBinding
	service string
	clock   *probeclock.Clock
}

func NewHTTPProbeCommandDispatcherV2(scope *HTTPProbeOperationScope, service string, clock *probeclock.Clock) (*HTTPProbeCommandDispatcherV2, error) {
	if clock == nil {
		return nil, ErrConfig
	}
	b, e := scope.Binding()
	if e != nil {
		return nil, e
	}
	if _, ok := scope.service(service); !ok {
		return nil, ErrConfig
	}
	return &HTTPProbeCommandDispatcherV2{scope, HTTPProbeWireBinding{hex.EncodeToString(b.Owner[:]), hex.EncodeToString(b.Deployment[:]), hex.EncodeToString(b.Lease[:]), hex.EncodeToString(b.BackendIncarnation[:])}, service, clock}, nil
}
func (d *HTTPProbeCommandDispatcherV2) snapshot(key HTTPProbeOperationKey) (*HTTPProbeWireSnapshot, error) {
	v, found, e := d.scope.Snapshot(key)
	if e != nil || !found {
		return nil, e
	}
	return probeSnapshotWireClock(v, 2, d.clock.Domain(), d.clock.Millis)
}
func (d *HTTPProbeCommandDispatcherV2) Dispatch(data []byte) (*HTTPProbeWireSnapshot, error) {
	if d == nil {
		return nil, ErrConfig
	}
	c, e := decodeProbeCommandVersion(data, 2, "ticksMs")
	if e != nil {
		return nil, e
	}
	if c.Binding != d.binding || c.Service != d.service {
		return nil, ErrHTTPProbeOperationBinding
	}
	raw, _ := hex.DecodeString(c.Key)
	var key HTTPProbeOperationKey
	copy(key[:], raw)
	if c.Command == "read" {
		return d.snapshot(key)
	}
	if c.Deadline.Domain != d.clock.Domain() {
		return nil, ErrConfig
	}
	if v, found, e := d.scope.Snapshot(key); e != nil {
		return nil, e
	} else if found {
		if v.Service.Name != d.service {
			return nil, ErrHTTPProbeOperationBinding
		}
		if _, e = d.scope.Start(nil, key, d.service); e != nil {
			return nil, e
		}
		return d.snapshot(key)
	}
	// Bounded absolute deadline. No handler lifetime, receipt-time budget rebase,
	// or refresh of a retained key is permitted.
	now := d.clock.NowMs()
	ticks := c.Deadline.EpochMs
	if ticks <= now {
		return nil, context.DeadlineExceeded
	}
	if ticks-now > HTTPObservationTimeout.Milliseconds() {
		return nil, ErrConfig
	}
	deadline, e := d.clock.TimeAt(ticks)
	if e != nil {
		return nil, e
	}
	ctx := probeCommandDeadlineContext{Context: context.Background(), deadline: deadline}
	if _, e = d.scope.Start(ctx, key, d.service); e != nil {
		return nil, e
	}
	return d.snapshot(key)
}
