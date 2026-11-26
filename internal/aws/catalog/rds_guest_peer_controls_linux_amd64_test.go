//go:build linux && amd64

package catalog

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
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

type peerProbeFixture struct {
	run    func(context.Context) error
	probes int
	closes int
}

func (p *peerProbeFixture) Probe(ctx context.Context) error {
	p.probes++
	return p.run(ctx)
}

func (p *peerProbeFixture) Close() error {
	p.closes++
	return nil
}

func TestPrivatePostgresGuestPeerRechecksAfterHostProbe(t *testing.T) {
	probeFailed := errors.New("fixture host probe failed")
	for _, test := range []struct {
		name                   string
		want                   error
		matchCalls, probeCalls int
	}{
		{"stable", nil, 2, 1},
		{"denied initially", rdsruntime.ErrNotReady, 1, 0},
		{"host failed", probeFailed, 1, 1},
		{"dependency deleted during host probe", rdsruntime.ErrNotReady, 1, 1},
		{"context cancelled during host probe", rdsruntime.ErrNotReady, 1, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := guardDB(t)
			cfg := guardConfig()
			_, owner, err := NewWithOwnedPrivatePostgres(db, cfg, guardOptions(t))
			if err != nil {
				t.Fatal(err)
			}
			owned := reserveGuardRows(t, db, owner)
			owner.key, owner.used = generationOf(owned), true
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var writer *Engine
			if test.name == "dependency deleted during host probe" {
				writer, err = New(db, cfg, Services())
				if err != nil {
					t.Fatal(err)
				}
			}
			child := &peerProbeFixture{run: func(ctx context.Context) error {
				switch test.name {
				case "host failed":
					return probeFailed
				case "context cancelled during host probe":
					cancel()
				case "dependency deleted during host probe":
					// A separate metadata engine has no process lock. The
					// catalog's single SQLite connection must be free while
					// the host probe is running; exercise the native writer.
					h := &harness{t: t, engine: writer}
					form := url.Values{"Action": {"DeleteDBSubnetGroup"}, "Version": {"2014-10-31"}, "DBSubnetGroupName": {"private-group"}}
					req := httptest.NewRequestWithContext(ctx, "POST", "/", strings.NewReader(form.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					sign(req, "rds", guardRegion)
					h.send(req).result(t, "DeleteDBSubnetGroup")
					expectQuery(t, h.query("DescribeDBSubnetGroups", "DBSubnetGroupName", "private-group"), 404, "DBSubnetGroupNotFoundFault")
				}
				return nil
			}}
			owner.child = child // no guest or database process in this fixture
			t.Cleanup(func() {
				if err := owner.Close(); err != nil || child.closes != 1 {
					t.Errorf("owner did not close and release only its fixture lease: %v, closes=%d", err, child.closes)
				}
			})

			var matches int
			err = owner.ProbeGuestPeer(ctx, db, cfg.Account, guardRegion, func(ctx context.Context, tx *sql.Tx, refs RDSPrivatePostgresRefs) error {
				matches++
				if !owner.currentLease(ctx, tx) || refs.Region != guardRegion ||
					!slices.Equal(refs.SecurityGroupIDs, []string{"sg-01234567"}) ||
					!slices.Equal(refs.SubnetIDs, []string{"subnet-01234567", "subnet-12345678"}) {
					t.Fatal("guest policy was not applied to the exact fenced generation and its canonical references")
				}
				if test.name == "denied initially" {
					return errors.New("fixture peer refused")
				}
				return nil
			})
			if !errors.Is(err, test.want) || matches != test.matchCalls || child.probes != test.probeCalls {
				t.Fatalf("got err=%v, policy reads=%d, host probes=%d; want err=%v, policy reads=%d, host probes=%d",
					err, matches, child.probes, test.want, test.matchCalls, test.probeCalls)
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if child.closes != 0 || !owner.currentLease(t.Context(), tx) {
				t.Fatal("preflight unexpectedly retired its owned DB generation")
			}
		})
	}
}
