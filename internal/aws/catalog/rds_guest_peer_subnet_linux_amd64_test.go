//go:build linux && amd64

package catalog

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
)

// The catalog and replacement subnet groups are real SQLite rows and native
// RDS Query requests. Only the fixed host probe is synthetic; no guest or SQL
// engine is asserted here.
func TestPrivatePostgresPeerNeverAdoptsRecreatedSubnetGroup(t *testing.T) {
	for _, when := range []string{"before first probe", "during host probe", "in-place edit during host probe", "unrelated group"} {
		t.Run(when, func(t *testing.T) {
			db, cfg := guardDB(t), guardConfig()
			_, owner, err := NewWithOwnedPrivatePostgres(db, cfg, guardOptions(t))
			if err != nil {
				t.Fatal(err)
			}
			writer, err := New(db, cfg, Services())
			if err != nil {
				t.Fatal(err)
			}
			h := &harness{t: t, engine: writer}
			create := func(name string) {
				h.query("CreateDBSubnetGroup", "DBSubnetGroupName", name, "DBSubnetGroupDescription", "private",
					"SubnetIds.SubnetIdentifier.1", "subnet-01234567", "SubnetIds.SubnetIdentifier.2", "subnet-12345678").result(t, "CreateDBSubnetGroup")
			}
			const group = "private-group"
			create(group)
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			instance := guardRow()
			if instance.Seq, err = nextSeq(tx); err != nil {
				t.Fatal(err)
			}
			if err := insertRow(tx, instance); err != nil {
				t.Fatal(err)
			}
			if ok, err := owner.reserve(tx, instance); err != nil || !ok {
				t.Fatalf("owner reserve: %t %v", ok, err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			owner.used, owner.key = true, generationOf(instance)
			defer func() {
				if err := owner.Close(); err != nil {
					t.Error(err)
				}
			}()
			replace := func() {
				h.query("DeleteDBSubnetGroup", "DBSubnetGroupName", group).result(t, "DeleteDBSubnetGroup")
				create(group) // same ARN, same two subnets; different creation sequence
			}
			if when == "before first probe" {
				replace()
			}
			child := &peerProbeFixture{run: func(context.Context) error { return nil }}
			owner.child = child
			if when != "before first probe" {
				child.run = func(context.Context) error {
					switch when {
					case "during host probe":
						replace()
					case "in-place edit during host probe":
						h.query("ModifyDBSubnetGroup", "DBSubnetGroupName", group, "DBSubnetGroupDescription", "edited",
							"SubnetIds.SubnetIdentifier.1", "subnet-01234567", "SubnetIds.SubnetIdentifier.2", "subnet-12345678").result(t, "ModifyDBSubnetGroup")
					case "unrelated group":
						create("unrelated")
					}
					return nil
				}
			}
			matches := 0
			match := func(context.Context, *sql.Tx, RDSPrivatePostgresRefs) error { matches++; return nil }
			check := func(wantErr error, nProbes, nMatches int) {
				t.Helper()
				err := owner.ProbeGuestPeer(t.Context(), db, cfg.Account, guardRegion, match)
				if !errors.Is(err, wantErr) || child.probes != nProbes || matches != nMatches {
					t.Fatalf("err=%v, host probes=%d, policy reads=%d; want %v, %d, %d", err, child.probes, matches, wantErr, nProbes, nMatches)
				}
			}
			switch when {
			case "before first probe":
				check(rdsruntime.ErrNotReady, 0, 0)
			case "during host probe":
				check(rdsruntime.ErrNotReady, 1, 1)
				check(rdsruntime.ErrNotReady, 1, 1) // fresh call must not adopt the replacement
			case "in-place edit during host probe":
				check(rdsruntime.ErrNotReady, 1, 1)
				child.run = func(context.Context) error { return nil }
				check(nil, 2, 3) // same incarnation, now stable across both checks
			case "unrelated group":
				check(nil, 1, 2)
			}
		})
	}
}
