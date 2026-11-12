//go:build linux

package guesttcp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/probeclock"
)

func ownedClock(t *testing.T) *probeclock.Clock {
	t.Helper()
	for i := 0; i < 50; i++ {
		if c, err := probeclock.New(); err == nil {
			return c
		}
	}
	t.Fatal("owned monotonic clock unavailable")
	return nil
}

func commandV2(t *testing.T, d *HTTPProbeCommandDispatcherV2, key HTTPProbeOperationKey, kind string, ticks int64, mutate func(map[string]any)) []byte {
	t.Helper()
	c := map[string]any{"version": 2, "command": kind, "binding": map[string]string{"owner": d.binding.Owner, "deployment": d.binding.Deployment, "lease": d.binding.Lease, "backendIncarnation": d.binding.BackendIncarnation}, "service": d.service, "key": hex.EncodeToString(key[:])}
	if kind == "start" {
		c["deadline"] = map[string]any{"domain": d.clock.Domain(), "ticksMs": ticks}
	}
	if mutate != nil {
		mutate(c)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHTTPProbeCommandV2UsesOwnedMonotonicDeadlines(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	clock := ownedClock(t)
	if _, err := NewHTTPProbeCommandDispatcherV2(s, "web", nil); !errors.Is(err, ErrConfig) {
		t.Fatal("nil clock accepted")
	}
	if _, err := NewHTTPProbeCommandDispatcherV2(s, "absent", clock); !errors.Is(err, ErrConfig) {
		t.Fatal("unknown service accepted")
	}
	d, err := NewHTTPProbeCommandDispatcherV2(s, "web", clock)
	if err != nil {
		t.Fatal(err)
	}
	var nilDispatcher *HTTPProbeCommandDispatcherV2
	if _, err = nilDispatcher.Dispatch(nil); !errors.Is(err, ErrConfig) {
		t.Fatal("nil dispatcher dispatched")
	}
	key := operationKey(901)
	if got, err := d.Dispatch(commandV2(t, d, key, "read", 0, nil)); err != nil || got != nil {
		t.Fatal("unknown read became work", got, err)
	}
	deadline := clock.NowMs() + 1500
	got, err := d.Dispatch(commandV2(t, d, key, "start", deadline, nil))
	if err != nil || got == nil || got.DeadlineMs != deadline {
		t.Fatal("start", got, err)
	}
	h, err := s.Start(nil, key, "web")
	if err != nil {
		t.Fatal(err)
	}
	if final := operationSettled(t, h); final.ResultError != nil {
		t.Fatal("handler canceled", final.ResultError)
	}
	// A retained key is rediscovered, never re-admitted with a new deadline.
	again, err := d.Dispatch(commandV2(t, d, key, "start", clock.NowMs()+2000, nil))
	if err != nil || again == nil || again.DeadlineMs != deadline || !again.Settled {
		t.Fatal("duplicate start renewed", again, err)
	}
	read, err := d.Dispatch(commandV2(t, d, key, "read", 0, nil))
	if err != nil || read == nil || !read.Settled {
		t.Fatal("read", read, err)
	}
	for name, c := range map[string]struct {
		ticks  int64
		mutate func(map[string]any)
		want   error
	}{
		"past":         {clock.NowMs() - 1, nil, context.DeadlineExceeded},
		"too far":      {clock.NowMs() + HTTPObservationTimeout.Milliseconds() + 5000, nil, ErrConfig},
		"other domain": {clock.NowMs() + 1000, func(m map[string]any) { m["deadline"].(map[string]any)["domain"] = "mono-other" }, ErrConfig},
		"other owner": {clock.NowMs() + 1000, func(m map[string]any) {
			m["binding"].(map[string]string)["owner"] = d.binding.Deployment
		}, ErrHTTPProbeOperationBinding},
		"other service": {clock.NowMs() + 1000, func(m map[string]any) { m["service"] = "health" }, ErrHTTPProbeOperationBinding},
	} {
		k := operationKey(uint64(910 + len(name)))
		_, err := d.Dispatch(commandV2(t, d, k, "start", c.ticks, c.mutate))
		if !errors.Is(err, c.want) {
			t.Fatalf("%s: got %v want %v", name, err, c.want)
		}
		if _, found, _ := s.Snapshot(k); found {
			t.Fatalf("%s: refused command retained work", name)
		}
	}
	if _, err = d.Dispatch([]byte(`{"version":1}`)); err == nil {
		t.Fatal("v1/malformed command accepted by v2")
	}
	if _, err = d.Dispatch(commandV2(t, d, operationKey(990), "start", 1<<54, nil)); err == nil {
		t.Fatal("out-of-range ticks admitted")
	}
	_ = time.Millisecond
}
