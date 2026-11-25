//go:build linux && amd64

package catalog

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"testing"
)

// Exercise the real private DB + subnet group rows, durable ownership fence
// and transaction. This does not substitute metadata for a native postmaster.
func TestPrivatePostgresGuestPeerGeneration(t *testing.T) {
	db := guardDB(t)
	cfg := guardConfig()
	_, c, err := NewWithOwnedPrivatePostgres(db, cfg, guardOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	r := reserveGuardRows(t, db, c)
	c.key = generationOf(r)
	ctx := t.Context()
	n := 0
	check := func(ctx context.Context, tx *sql.Tx, refs RDSPrivatePostgresRefs) error {
		n++
		if !c.currentLease(ctx, tx) || refs.Region != r.Region ||
			!slices.Equal(refs.SecurityGroupIDs, []string{"sg-01234567"}) ||
			!slices.Equal(refs.SubnetIDs, []string{"subnet-01234567", "subnet-12345678"}) {
			t.Fatal("not the canonical fenced row and resolved DB private subnet group")
		}
		return nil
	}
	if !c.currentMatching(ctx, check) || n != 1 {
		t.Fatal("current generation did not join its real saved dependencies")
	}
	if c.currentMatching(ctx, func(context.Context, *sql.Tx, RDSPrivatePostgresRefs) error { return errors.New("peer denied") }) ||
		!c.currentMatching(ctx, check) || n != 2 {
		t.Fatal("denying a peer must not release a healthy resource's fence")
	}
	// A well-formed DB row alone has no live child; no callback may run.
	mustNotRun := func(context.Context, *sql.Tx, RDSPrivatePostgresRefs) error {
		t.Fatal("peer policy was consulted outside its admitted lifecycle")
		return nil
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for _, fn := range []func() error{
		func() error {
			return (*OwnedPrivatePostgres)(nil).ProbeGuestPeer(ctx, db, cfg.Account, guardRegion, mustNotRun)
		},
		func() error { return c.ProbeGuestPeer(cancelled, db, cfg.Account, guardRegion, mustNotRun) },
		func() error { return c.ProbeGuestPeer(ctx, guardDB(t), cfg.Account, guardRegion, mustNotRun) },
		func() error { return c.ProbeGuestPeer(ctx, db, "111111111111", guardRegion, mustNotRun) },
		func() error { return c.ProbeGuestPeer(ctx, db, cfg.Account, "us-west-2", mustNotRun) },
		func() error { return c.ProbeGuestPeer(ctx, db, cfg.Account, guardRegion, mustNotRun) },
	} {
		if fn() == nil {
			t.Fatal("unowned peer admitted")
		}
	}
	c.mu.Lock() // a retained EC2 fence must not wait on a long DB mutation
	err = c.ProbeGuestPeer(ctx, db, cfg.Account, guardRegion, mustNotRun)
	c.mu.Unlock()
	if err == nil {
		t.Fatal("busy workload admitted")
	}
	c.key.gen++
	if c.currentMatching(ctx, mustNotRun) {
		t.Fatal("stale canonical generation admitted")
	}
	c.key = generationOf(r)
	if err := c.Close(); err != nil || c.currentMatching(ctx, mustNotRun) {
		t.Fatal("retired generation admitted", err)
	}
}
