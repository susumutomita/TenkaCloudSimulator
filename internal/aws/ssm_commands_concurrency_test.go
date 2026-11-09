package aws

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
)

// Both reconcilers read the same persisted record. Hold the first backend
// observation while the second commits terminal success to real SQLite; the
// stale first observer must fail CAS rather than overwrite terminal evidence.
// The double supplies observations only; it is not an executing guest backend.
func TestSSMCommandsConcurrentReconcilePreservesTerminal(t *testing.T) {
	s, b, _, a, leases := ssmFixture(t, 1)
	r := ssmSend(t, s, a, leases)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	b.observe = func(i guestcommand.DurableIntent) (guestcommand.DurableObservation, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return guestcommand.DurableObservation{}, ctx.Err()
			}
			return guestcommand.DurableObservation{Intent: i, State: "InProgress", StartedAt: i.RequestedAt}, nil
		}
		return guestcommand.DurableObservation{Intent: i, State: "Success", StartedAt: i.RequestedAt, FinishedAt: i.RequestedAt, Stdout: "terminal evidence"}, nil
	}
	staleResult := make(chan error, 1)
	go func() {
		_, err := s.Reconcile(ctx, a, r.CommandId, leases[0].Owner.Instance)
		staleResult <- err
	}()
	// Always unblock and join the worker before fixture/database cleanup, even
	// when an assertion fails before the winning reconciliation completes.
	joined := false
	defer func() {
		cancel()
		if !joined {
			select {
			case <-staleResult:
			case <-time.After(10 * time.Second):
				t.Error("stale reconciliation did not stop")
			}
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first observation did not reach barrier:", ctx.Err())
	}
	winner, err := s.Reconcile(ctx, a, r.CommandId, leases[0].Owner.Instance)
	if err != nil || !winner.Successful() {
		t.Fatalf("winning terminal observation: %v %+v", err, winner)
	}
	want := winner.Targets[0].Observation
	close(release)
	select {
	case err = <-staleResult:
		joined = true
		if !errors.Is(err, ErrSSMCommandInvalid) {
			t.Fatalf("stale observer must fail CAS: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("stale reconciliation did not finish:", ctx.Err())
	}
	got, err := s.GetInvocation(ctx, a, r.CommandId, leases[0].Owner.Instance)
	if err != nil || got != want {
		t.Fatalf("stale observer replaced terminal evidence: %v\ngot %+v\nwant %+v", err, got, want)
	}
	// Terminal reads must not consult even the observation-only test double.
	if _, err = s.Reconcile(ctx, a, r.CommandId, leases[0].Owner.Instance); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("unexpected backend observations: %d", calls.Load())
	}
}
