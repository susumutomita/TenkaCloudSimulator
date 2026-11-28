package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

// A fault is armed either at admission or after a particular row has actually
// changed. This distinguishes failing a final read/commit from refusing a
// request before any durable work has been attempted.
type kmsDeletionFault struct {
	name          string
	after         string
	action        sqlite3.AuthorizerActionCode
	table, column string
}

func kmsDeletionConnection(t *testing.T, db *sql.DB, configure func(*sqlite3.Conn) error) {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(raw any) error { return configure(raw.(driver.Conn).Raw()) }); err != nil {
		t.Fatal(err)
	}
}

func (fault kmsDeletionFault) install(t *testing.T, db *sql.DB) func() {
	t.Helper()
	armed, fired := fault.after == "", false
	kmsDeletionConnection(t, db, func(conn *sqlite3.Conn) error {
		conn.UpdateHook(func(_ sqlite3.AuthorizerActionCode, _, table string, _ int64) {
			if table == fault.after {
				armed = true
			}
		})
		if fault.action == sqlite3.AUTH_TRANSACTION {
			conn.CommitHook(func() bool {
				if armed {
					fired = true
					return false
				}
				return true
			})
			return nil
		}
		return conn.SetAuthorizer(func(action sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
			if armed && action == fault.action && table == fault.table && (fault.column == "" || column == fault.column) {
				fired = true
				return sqlite3.AUTH_DENY
			}
			return sqlite3.AUTH_OK
		})
	})
	return func() {
		t.Helper()
		kmsDeletionConnection(t, db, func(conn *sqlite3.Conn) error {
			conn.UpdateHook(nil)
			conn.CommitHook(nil)
			return conn.SetAuthorizer(nil)
		})
		if !fired {
			t.Fatalf("SQLite did not reach %s", fault.name)
		}
	}
}

const kmsDeletionRegion = "us-east-1"

type kmsDeletionTeam struct {
	store                  *KMSStore
	target, peer           KMSKeyMetadata
	targetGrant, peerGrant string
	now                    time.Time
}

func newKMSDeletionTeam(t *testing.T) *kmsDeletionTeam {
	t.Helper()
	store, aliases, target, peer := kmsAliasFixture(t)
	f := &kmsDeletionTeam{store: store, target: target, peer: peer,
		now: time.Date(2026, 11, 28, 12, 0, 0, 123000000, time.UTC)}
	store.now = func() time.Time { return f.now }
	roles, err := NewIAMRoleStore(store.db)
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(t.Context(), "deletion-reader", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	attach := func(key KMSKeyMetadata, name string) string {
		t.Helper()
		if err := aliases.CreateAlias(t.Context(), name, key.KeyID, kmsAliasAllow); err != nil {
			t.Fatal(err)
		}
		out := kmsGrantCall(t, &Gateway{KMS: store}, "CreateGrant", map[string]any{
			"KeyId": key.Arn, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"},
		}, 200, "")
		var id string
		if err := json.Unmarshal(out["GrantId"], &id); err != nil || id == "" {
			t.Fatal("could not create synthetic grant", err)
		}
		return id
	}
	f.targetGrant = attach(target, "alias/deletion-target")
	f.peerGrant = attach(peer, "alias/deletion-peer")
	return f
}

// Verify both identity and count. An unrelated surviving dependency cannot
// satisfy the assertion for the deleted key, or vice versa.
func (f *kmsDeletionTeam) checkDependencies(t *testing.T, key KMSKeyMetadata, alias, grant string, present bool) {
	t.Helper()
	var keys, aliases, namedAlias, grants, namedGrant int
	err := f.store.db.QueryRow(`SELECT
	 (SELECT count(*) FROM aws_kms_keys WHERE region=?1 AND key_id=?2),
	 (SELECT count(*) FROM aws_kms_aliases WHERE region=?1 AND key_id=?2),
	 (SELECT count(*) FROM aws_kms_aliases WHERE region=?1 AND key_id=?2 AND name=?3),
	 (SELECT count(*) FROM aws_kms_grants WHERE region=?1 AND key_id=?2),
	 (SELECT count(*) FROM aws_kms_grants WHERE region=?1 AND key_id=?2 AND grant_id=?4)`,
		kmsDeletionRegion, key.KeyID, alias, grant).Scan(&keys, &aliases, &namedAlias, &grants, &namedGrant)
	want := 0
	if present {
		want = 1
	}
	if err != nil || keys != want || aliases != want || namedAlias != want || grants != want || namedGrant != want {
		t.Fatalf("unexpected key/alias/grant identities or counts for %s: %v", key.KeyID, err)
	}
}

func TestKMSDeletionScheduleAndCancelRefuseStorageFailures(t *testing.T) {
	const keys, pending = "aws_kms_keys", "aws_kms_pending_deletions"
	initialRead := kmsDeletionFault{name: "read target", action: sqlite3.AUTH_READ, table: keys, column: "material"}
	admission := kmsDeletionFault{name: "read pending deadlines", action: sqlite3.AUTH_READ, table: pending, column: "due_ms"}
	cases := []struct {
		op     string
		faults []kmsDeletionFault
	}{
		{"ScheduleKeyDeletion", []kmsDeletionFault{
			admission, initialRead,
			{name: "record deadline", action: sqlite3.AUTH_INSERT, table: pending},
			{name: "disable after recording deadline", after: pending, action: sqlite3.AUTH_UPDATE, table: keys, column: "state"},
			{name: "read back disabled key", after: keys, action: sqlite3.AUTH_READ, table: keys, column: "material"},
			{name: "commit verified schedule", after: keys, action: sqlite3.AUTH_TRANSACTION},
		}},
		{"CancelKeyDeletion", []kmsDeletionFault{
			admission, initialRead,
			{name: "remove deadline", action: sqlite3.AUTH_DELETE, table: pending},
			{name: "read back retained key", after: pending, action: sqlite3.AUTH_READ, table: keys, column: "material"},
			{name: "commit verified cancellation", after: pending, action: sqlite3.AUTH_TRANSACTION},
		}},
	}
	for _, tc := range cases {
		for _, fault := range tc.faults {
			t.Run(tc.op+"/"+fault.name, func(t *testing.T) {
				f := newKMSDeletionTeam(t)
				g := &Gateway{KMS: f.store}
				args := map[string]any{"KeyId": f.target.Arn}
				duration := 7 * 24 * time.Hour
				expectedDue := float64(f.now.Add(duration).UnixMilli()) / 1000
				if tc.op == "ScheduleKeyDeletion" {
					args["PendingWindowInDays"] = 7
				} else {
					if _, err := f.store.scheduleDeletion(t.Context(), kmsDeletionRegion, f.target.KeyID, 7); err != nil {
						t.Fatal(err)
					}
					f.now = f.now.Add(duration - time.Millisecond)
				}
				before := kmsAliasDatabaseFrame(t, f.store.db)
				repair := fault.install(t, f.store.db)
				denied := kmsGrantCall(t, g, tc.op, args, 500, "KMSInternalException")
				repair()
				if denied["KeyId"] != nil || denied["DeletionDate"] != nil || kmsAliasDatabaseFrame(t, f.store.db) != before {
					t.Fatal("failed request returned success metadata or changed the team's stored state")
				}
				ok := kmsGrantCall(t, g, tc.op, args, 200, "")
				if string(ok["KeyId"]) != `"`+f.target.Arn+`"` {
					t.Fatal("retry acknowledged a different key")
				}
				var count int
				if err := f.store.db.QueryRow("SELECT count(*) FROM aws_kms_pending_deletions WHERE region=? AND key_id=?", kmsDeletionRegion, f.target.KeyID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if tc.op == "CancelKeyDeletion" {
					f.now = f.now.Add(time.Second)
				}
				actual, err := f.store.DescribeKey(t.Context(), kmsDeletionRegion, f.target.KeyID)
				if err != nil {
					t.Fatal(err)
				}
				if tc.op == "ScheduleKeyDeletion" {
					if count != 1 || actual.Enabled || actual.KeyState != "PendingDeletion" || actual.DeletionDate != expectedDue {
						t.Fatal("verified schedule did not persist the original seven-day deadline")
					}
				} else if count != 0 || actual.Enabled || actual.KeyState != "Disabled" || actual.DeletionDate != 0 {
					t.Fatal("verified cancellation did not preserve the disabled key past its old deadline")
				}
				f.checkDependencies(t, f.target, "alias/deletion-target", f.targetGrant, true)
				f.checkDependencies(t, f.peer, "alias/deletion-peer", f.peerGrant, true)
			})
		}
	}
}

func TestKMSDeletionExpiryRollsBackEveryDependencyOnStorageFailure(t *testing.T) {
	const keys, pending = "aws_kms_keys", "aws_kms_pending_deletions"
	const aliases, revision, grants = "aws_kms_aliases", "aws_kms_alias_state", "aws_kms_grants"
	for _, fault := range []kmsDeletionFault{
		{name: "read alias schema", action: sqlite3.AUTH_READ, table: "sqlite_master", column: "type"},
		{name: "read expired key", action: sqlite3.AUTH_READ, table: keys, column: "material"},
		{name: "count linked aliases", action: sqlite3.AUTH_READ, table: aliases, column: "key_id"},
		{name: "remove linked aliases", action: sqlite3.AUTH_DELETE, table: aliases},
		{name: "verify deleted alias count", after: aliases, action: sqlite3.AUTH_FUNCTION, column: "total_changes"},
		{name: "read alias revision", after: aliases, action: sqlite3.AUTH_READ, table: revision, column: "revision"},
		{name: "advance alias revision", after: aliases, action: sqlite3.AUTH_UPDATE, table: revision, column: "revision"},
		{name: "remove linked grants", after: revision, action: sqlite3.AUTH_DELETE, table: grants},
		{name: "remove deadline", after: grants, action: sqlite3.AUTH_DELETE, table: pending},
		{name: "remove expired key", after: pending, action: sqlite3.AUTH_DELETE, table: keys},
		{name: "commit entire expiry", after: keys, action: sqlite3.AUTH_TRANSACTION},
	} {
		t.Run(fault.name, func(t *testing.T) {
			f := newKMSDeletionTeam(t)
			if _, err := f.store.scheduleDeletion(t.Context(), kmsDeletionRegion, f.target.KeyID, 7); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(7 * 24 * time.Hour)
			before := kmsAliasDatabaseFrame(t, f.store.db)
			repair := fault.install(t, f.store.db)
			// An ordinary read of an unrelated live key must wait for a successful
			// cleanup. A failed cleanup cannot publish an incomplete peer response.
			failed, err := f.store.DescribeKey(t.Context(), kmsDeletionRegion, f.peer.Arn)
			repair()
			if err == nil || failed.KeyID != "" || kmsAliasDatabaseFrame(t, f.store.db) != before {
				t.Fatal("failed expiry published metadata or partially changed the team")
			}
			peer, err := f.store.DescribeKey(t.Context(), kmsDeletionRegion, f.peer.Arn)
			if err != nil || !reflect.DeepEqual(peer, f.peer) {
				t.Fatal("peer changed after expiry retry", err)
			}
			_, err = f.store.DescribeKey(t.Context(), kmsDeletionRegion, f.target.Arn)
			kmsWantCode(t, err, "NotFoundException")
			f.checkDependencies(t, f.target, "alias/deletion-target", f.targetGrant, false)
			f.checkDependencies(t, f.peer, "alias/deletion-peer", f.peerGrant, true)
			var count int
			if err := f.store.db.QueryRow("SELECT count(*) FROM aws_kms_pending_deletions").Scan(&count); err != nil || count != 0 {
				t.Fatal("successful expiry retained its deadline", err)
			}
		})
	}
}

// Abort during an actual SQLite write to this table. Counting writes allows a
// multi-key cleanup to reach its second key before the request is interrupted.
func kmsCancelDeletionAfter(t *testing.T, db *sql.DB, change sqlite3.AuthorizerActionCode, table string, target int) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	observed := 0
	kmsDeletionConnection(t, db, func(conn *sqlite3.Conn) error {
		conn.UpdateHook(func(action sqlite3.AuthorizerActionCode, _, changedTable string, _ int64) {
			if action == change && changedTable == table {
				observed++
				if observed == target {
					cancel()
				}
			}
		})
		return nil
	})
	return ctx, func() {
		t.Helper()
		kmsDeletionConnection(t, db, func(conn *sqlite3.Conn) error { conn.UpdateHook(nil); return nil })
		if observed != target || ctx.Err() != context.Canceled {
			t.Fatalf("expected cancellation at change %d of %s; observed %d", target, table, observed)
		}
	}
}

// Abort only after SQLite has applied the addressed row change. The test
// uses the provider entry points, so the request owns the whole transaction.
func TestKMSDeletionCanceledAfterDeadlineWrite(t *testing.T) {
	for _, op := range []string{"schedule", "cancel"} {
		t.Run(op, func(t *testing.T) {
			f := newKMSDeletionTeam(t)
			change := sqlite3.AUTH_INSERT
			if op == "cancel" {
				if _, err := f.store.scheduleDeletion(t.Context(), kmsDeletionRegion, f.target.Arn, 7); err != nil {
					t.Fatal(err)
				}
				change = sqlite3.AUTH_DELETE
			}
			before := kmsAliasDatabaseFrame(t, f.store.db)
			ctx, verifyCancellation := kmsCancelDeletionAfter(t, f.store.db, change, "aws_kms_pending_deletions", 1)
			var acknowledged string
			var err error
			if op == "schedule" {
				var out kmsScheduledDeletion
				out, err = f.store.scheduleDeletion(ctx, kmsDeletionRegion, f.target.Arn, 7)
				acknowledged = out.KeyID
			} else {
				acknowledged, err = f.store.cancelDeletion(ctx, kmsDeletionRegion, f.target.Arn)
			}
			verifyCancellation()
			if err == nil || acknowledged != "" || kmsAliasDatabaseFrame(t, f.store.db) != before {
				t.Fatal("request canceled after the deadline row changed returned success or persisted partial state")
			}
			if op == "schedule" {
				out, err := f.store.scheduleDeletion(t.Context(), kmsDeletionRegion, f.target.Arn, 7)
				if err != nil || out.KeyID != f.target.Arn || out.KeyState != "PendingDeletion" {
					t.Fatal("schedule retry failed", err)
				}
			} else {
				arn, err := f.store.cancelDeletion(t.Context(), kmsDeletionRegion, f.target.Arn)
				if err != nil || arn != f.target.Arn {
					t.Fatal("cancel retry failed", err)
				}
			}
		})
	}
}

func TestKMSDeletionCanceledDuringSecondKeyExpiry(t *testing.T) {
	f := newKMSDeletionTeam(t)
	bare, err := f.store.CreateKey(t.Context(), kmsDeletionRegion, "same deadline, no aliases or grants")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []KMSKeyMetadata{f.target, bare} {
		if _, err := f.store.scheduleDeletion(t.Context(), kmsDeletionRegion, key.Arn, 7); err != nil {
			t.Fatal(err)
		}
	}
	before := kmsAliasDatabaseFrame(t, f.store.db)
	var oldRevision int64
	if err := f.store.db.QueryRow("SELECT revision FROM aws_kms_alias_state WHERE region=?", kmsDeletionRegion).Scan(&oldRevision); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(7 * 24 * time.Hour)
	ctx, verifyCancellation := kmsCancelDeletionAfter(t, f.store.db, sqlite3.AUTH_DELETE, "aws_kms_keys", 2)
	peer, err := f.store.DescribeKey(ctx, kmsDeletionRegion, f.peer.Arn)
	verifyCancellation()
	if err == nil || peer.KeyID != "" || kmsAliasDatabaseFrame(t, f.store.db) != before {
		t.Fatal("cancellation during the second expiry left a partially deleted batch")
	}
	peer, err = f.store.DescribeKey(t.Context(), kmsDeletionRegion, f.peer.Arn)
	if err != nil || !reflect.DeepEqual(peer, f.peer) {
		t.Fatal("could not retry expiry for both keys", err)
	}
	for _, key := range []KMSKeyMetadata{f.target, bare} {
		_, err := f.store.DescribeKey(t.Context(), kmsDeletionRegion, key.Arn)
		kmsWantCode(t, err, "NotFoundException")
	}
	f.checkDependencies(t, f.target, "alias/deletion-target", f.targetGrant, false)
	f.checkDependencies(t, f.peer, "alias/deletion-peer", f.peerGrant, true)
	var revision, deadlines int64
	if err := f.store.db.QueryRow(`SELECT revision, (SELECT COUNT(*) FROM aws_kms_pending_deletions)
		FROM aws_kms_alias_state WHERE region=?`, kmsDeletionRegion).Scan(&revision, &deadlines); err != nil || deadlines != 0 || revision != oldRevision+1 {
		t.Fatal("retry should clear both deadlines and advance the alias revision only for the key with an alias", err)
	}
}
