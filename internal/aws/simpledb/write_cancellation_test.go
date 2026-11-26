package simpledb

import (
	"context"
	"database/sql"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Put checks liveness before validation, then write checks again before
// acquiring the gate. Observe only the second check, and capture its result
// before signaling, so the baseline cannot slip through a pre-canceled path.
type observedWriteContext struct {
	context.Context
	checks  atomic.Int32
	checked chan error
}

func (c *observedWriteContext) Err() error {
	err := c.Context.Err()
	if c.checks.Add(1) == 2 {
		c.checked <- err
	}
	return err
}

func awaitWriteStep[T any](t *testing.T, ready <-chan T, step string) T {
	t.Helper()
	select {
	case value := <-ready:
		return value
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", step)
		var zero T
		return zero
	}
}

func TestCancelledQueuedConditionalPut(t *testing.T) {
	s, _ := fixture(t)
	ctx := t.Context()
	const account, region, domain, item = "a", "r", "domain", "row"
	old := map[string][]string{"version": {"old"}, "payload": {"before"}}
	next := map[string][]string{"version": {"new"}, "payload": {"after"}}
	if err := s.CreateDomain(ctx, account, region, domain); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, account, region, domain, item, old, nil); err != nil {
		t.Fatal(err)
	}

	// Hold the production writer slot while a real SQLite write is still
	// uncommitted. Starting/canceling the waiter depends only on these
	// handshakes, never on query speed or a sleep.
	entered, unblock := make(chan error, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	holderResult, holderDone := make(chan error, 1), make(chan struct{})
	go func() {
		defer close(holderDone)
		holderResult <- s.write(ctx, func(tx *sql.Tx) error {
			err := saveAttributes(ctx, tx, account, region, domain, "other", map[string][]string{"head": {"committed"}})
			entered <- err
			if err == nil {
				<-unblock
			}
			return err
		})
	}()
	defer func() {
		release()
		awaitWriteStep(t, holderDone, "holder cleanup")
	}()
	if err := awaitWriteStep(t, entered, "SQLite holder"); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	probe := &observedWriteContext{Context: cancelled, checked: make(chan error, 1)}
	result, done := make(chan error, 1), make(chan struct{})
	put := func(callCtx context.Context) error {
		condition := &writeCondition{name: "version", value: "old"}
		return s.put(callCtx, account, region, domain, item, next,
			map[string]bool{"version": true, "payload": true}, condition)
	}
	go func() {
		defer close(done)
		result <- put(probe)
	}()
	defer func() {
		cancel()
		release()
		awaitWriteStep(t, done, "waiter cleanup")
	}()
	if err := awaitWriteStep(t, probe.checked, "live waiter"); err != nil {
		t.Fatalf("waiter was canceled before reaching the write slot: %v", err)
	}
	cancel()
	if err := awaitWriteStep(t, result, "cancelled waiter before releasing holder"); err != storeError("Cancelled") {
		t.Fatalf("queued write returned %v, want Cancelled", err)
	}

	release()
	if err := awaitWriteStep(t, holderResult, "holder commit"); err != nil {
		t.Fatalf("canceling the waiter affected the unrelated transaction: %v", err)
	}
	check := func(name string, want map[string][]string) {
		t.Helper()
		got, err := s.Get(ctx, account, region, domain, name, nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %v (%v), want %v", name, got, err, want)
		}
	}
	check(item, old)
	check("other", map[string][]string{"head": {"committed"}})
	if err := put(ctx); err != nil {
		t.Fatalf("legitimate conditional write after cancellation: %v", err)
	}
	check(item, next)
}
