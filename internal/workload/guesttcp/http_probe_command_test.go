package guesttcp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestHTTPProbeCommandGolden(t *testing.T) {
	data, err := os.ReadFile("../../../test/fixtures/probe-command-wire-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name  string
		JSON  string
		Valid bool
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			got, err := DecodeHTTPProbeCommand([]byte(c.JSON))
			if (err == nil) != c.Valid {
				t.Fatalf("valid=%v err=%v", c.Valid, err)
			}
			if err == nil {
				b, _ := json.Marshal(got)
				if _, err = DecodeHTTPProbeCommand(b); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func commandFor(t *testing.T, d *HTTPProbeCommandDispatcher, key HTTPProbeOperationKey, kind string, deadline int64) []byte {
	t.Helper()
	c := HTTPProbeCommand{Version: 1, Command: kind, Binding: d.binding, Service: d.service, Key: hex.EncodeToString(key[:])}
	if kind == "start" {
		c.Deadline = &HTTPProbeCommandDeadline{d.clock.Domain, deadline}
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestHTTPProbeCommandDispatchAbsoluteAndDiscovery(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	now := time.Now().UnixMilli()
	clock := HTTPProbeCommandClock{"unix-backend-1", now - 1000, now + 10000}
	d, err := NewHTTPProbeCommandDispatcher(s, "web", clock)
	if err != nil {
		t.Fatal(err)
	}
	clock.Domain = "mutated"
	deadline := now + 1500
	key := operationKey(701)
	if _, err = d.Dispatch(commandFor(t, d, key, "start", deadline)); err != nil {
		t.Fatal(err)
	}
	h, err := s.Start(nil, key, "web")
	if err != nil {
		t.Fatal(err)
	}
	final := operationSettled(t, h)
	if final.Deadline.UnixMilli() != deadline || final.ResultError != nil {
		t.Fatalf("changed absolute deadline or handler canceled: %+v", final)
	}
	// A newly constructed view with an expired qualification can still rediscover
	// the same admitted operation. It cannot grant admission to a new key.
	expired, err := NewHTTPProbeCommandDispatcher(s, "web", HTTPProbeCommandClock{d.clock.Domain, 1, 2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := expired.Dispatch(commandFor(t, expired, key, "start", 1))
	if err != nil || got == nil || got.DeadlineMs != deadline || !got.Settled {
		t.Fatal("duplicate renewed or rejected", got, err)
	}
	got, err = expired.Dispatch(commandFor(t, expired, key, "read", 0))
	if err != nil || got == nil || !got.Settled {
		t.Fatal("expiry broke discovery", err)
	}
	if _, err = expired.Dispatch(commandFor(t, expired, operationKey(702), "start", now+1000)); err == nil {
		t.Fatal("expired clock admitted")
	}
	if got, err = expired.Dispatch(commandFor(t, expired, operationKey(702), "read", 0)); err != nil || got != nil {
		t.Fatal("unknown became work", got, err)
	}
}
func TestHTTPProbeCommandRejectsBeforeAdmission(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	now := time.Now().UnixMilli()
	d, err := NewHTTPProbeCommandDispatcher(s, "web", HTTPProbeCommandClock{"unix-backend-1", now - 1000, now + 10000})
	if err != nil {
		t.Fatal(err)
	}
	for i, change := range []func(*HTTPProbeCommand){func(c *HTTPProbeCommand) { c.Binding.Owner = c.Binding.Deployment }, func(c *HTTPProbeCommand) { c.Service = "health" }, func(c *HTTPProbeCommand) { c.Deadline.Domain = "other" }, func(c *HTTPProbeCommand) { c.Deadline.EpochMs = now - 1 }, func(c *HTTPProbeCommand) { c.Deadline.EpochMs = now + 20000 }} {
		key := operationKey(uint64(710 + i))
		b := commandFor(t, d, key, "start", now+1000)
		c, _ := DecodeHTTPProbeCommand(b)
		change(&c)
		b, _ = json.Marshal(c)
		if _, err = d.Dispatch(b); err == nil {
			t.Fatal("invalid admitted", i)
		}
		if _, found, err := s.Snapshot(key); found || err != nil {
			t.Fatal("retained invalid", i)
		}
	}
	_, err = d.Dispatch(commandFor(t, d, operationKey(750), "start", now-1))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestHTTPProbeCommandReadIsolationAndDeadlineCap(t *testing.T) {
	f := newProbeHTTPFixture(t, new(probeHTTPMemoryOwner), context.Background(), persistentConfig(), probeHTTPReply)
	s := bindTestScope(t, f)
	now := time.Now().UnixMilli()
	d, err := NewHTTPProbeCommandDispatcher(s, "web", HTTPProbeCommandClock{"unix-backend-1", now - 1000, now + 30000})
	if err != nil {
		t.Fatal(err)
	}
	key := operationKey(760)
	requested := now + 20000
	if _, err = d.Dispatch(commandFor(t, d, key, "start", requested)); err != nil {
		t.Fatal(err)
	}
	h, err := s.Start(nil, key, "web")
	if err != nil {
		t.Fatal(err)
	}
	final := operationSettled(t, h)
	if final.Deadline.UnixMilli() >= requested || final.Deadline.After(final.StartedAt.Add(HTTPObservationTimeout)) {
		t.Fatal("scope extended or omitted cap", final)
	}
	for _, mutate := range []func(*HTTPProbeCommand){func(c *HTTPProbeCommand) { c.Binding.Owner = c.Binding.Lease }, func(c *HTTPProbeCommand) { c.Service = "health" }} {
		c, err := DecodeHTTPProbeCommand(commandFor(t, d, key, "read", 0))
		if err != nil {
			t.Fatal(err)
		}
		mutate(&c)
		raw, _ := json.Marshal(c)
		if got, err := d.Dispatch(raw); got != nil || !errors.Is(err, ErrHTTPProbeOperationBinding) {
			t.Fatal("read exposed foreign capability", got, err)
		}
	}
}
