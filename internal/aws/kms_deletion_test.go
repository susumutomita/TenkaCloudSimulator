package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestKMSDeletionDeadlineAndDependencies(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	path := filepath.Join(t.TempDir(), "team.db")
	open := func() (*core.Store, *KMSStore) {
		t.Helper()
		db, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		store, err := newKMSStore(db.Database(), func() time.Time { return now })
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		return db, store
	}
	db, s := open()
	const region = "us-east-1"
	key, err := s.CreateKey(ctx, region, "expire")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.CreateKey(ctx, region, "retain")
	if err != nil {
		t.Fatal(err)
	}
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(ctx, "reader", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{KMS: s}
	grant := func(key KMSKeyMetadata) string {
		t.Helper()
		var id string
		values := kmsGrantCall(t, g, "CreateGrant", map[string]any{"KeyId": key.Arn, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}}, 200, "")
		if err := json.Unmarshal(values["GrantId"], &id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	targetGrant, peerGrant := grant(key), grant(peer)
	aliases, err := NewKMSAliasStore(s, region)
	if err != nil {
		t.Fatal(err)
	}
	allow := func(context.Context, *sql.Tx, string, string) (bool, error) { return true, nil }
	for _, pair := range [][2]string{{"alias/expire", key.Arn}, {"alias/move", key.Arn}, {"alias/keep", peer.Arn}} {
		if err = aliases.CreateAlias(ctx, pair[0], pair[1], allow); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.scheduleDeletion(ctx, region, key.Arn, 7)
	if err != nil || result.KeyID != key.Arn || result.PendingWindowInDays != 7 || result.KeyState != "PendingDeletion" || result.DeletionDate != float64(now.Add(7*24*time.Hour).UnixMilli())/1000 {
		t.Fatal("schedule mismatch", err)
	}
	due := now.Add(7 * 24 * time.Hour)
	if _, err = s.scheduleDeletion(ctx, region, key.KeyID, 30); err == nil {
		t.Fatal("replaced pending window")
	} else {
		kmsWantCode(t, err, "KMSInvalidStateException")
	}
	kmsWantCode(t, aliases.UpdateAlias(ctx, "alias/keep", key.Arn, allow), "KMSInvalidStateException")
	kmsWantCode(t, aliases.CreateAlias(ctx, "alias/extra", key.Arn, allow), "KMSInvalidStateException")
	if err = aliases.UpdateAlias(ctx, "alias/move", peer.Arn, allow); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	now = due.Add(-time.Millisecond)
	db, s = open()
	metadata, err := s.DescribeKey(ctx, region, key.Arn)
	if err != nil || metadata.Enabled || metadata.KeyState != "PendingDeletion" || metadata.DeletionDate != result.DeletionDate {
		t.Fatal("one tick before deletion", err)
	}
	aliases, err = NewKMSAliasStore(s, region)
	if err != nil {
		t.Fatal(err)
	}
	page, err := aliases.ListAliases(ctx, "", 1, "", allow)
	if err != nil || page.NextMarker == "" {
		t.Fatal("expected marker", err)
	}
	snapshot := func() string {
		t.Helper()
		var v string
		err := db.Database().QueryRow(`SELECT json_array(
		 (SELECT group_concat(quote(region)||quote(key_id)||quote(material)||quote(policy)||quote(state)||quote(encryptions)) FROM aws_kms_keys ORDER BY key_id),
		 (SELECT group_concat(quote(name)||quote(key_id)||quote(target_created)) FROM aws_kms_aliases ORDER BY name),
		 (SELECT group_concat(quote(region)||quote(revision)||quote(secret)) FROM aws_kms_alias_state ORDER BY region),
		 (SELECT group_concat(quote(key_id)||quote(grant_id)||quote(role_id)||quote(created)) FROM aws_kms_grants ORDER BY grant_id),
		 (SELECT group_concat(quote(key_id)||quote(due_ms)) FROM aws_kms_pending_deletions ORDER BY key_id))`).Scan(&v)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snapshot()
	if _, err = db.Database().Exec(`CREATE TEMP TRIGGER retain_grant BEFORE DELETE ON aws_kms_grants BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	now = due
	if _, err = s.DescribeKey(ctx, region, key.KeyID); err == nil || snapshot() != before {
		t.Fatal("partial expiry after dependency fault")
	}
	if _, err = db.Database().Exec("DROP TRIGGER retain_grant"); err != nil {
		t.Fatal(err)
	}
	// Equal to the persisted deadline: cancellation cannot recover the key.
	_, err = s.cancelDeletion(ctx, region, key.KeyID)
	kmsWantCode(t, err, "NotFoundException")
	if _, err = aliases.ListAliases(ctx, "", 1, page.NextMarker, allow); err == nil {
		t.Fatal("accepted pre-expiry alias marker")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	now = due.Add(-time.Hour)
	db, s = open()
	_, err = s.DescribeKey(ctx, region, key.KeyID)
	kmsWantCode(t, err, "NotFoundException")
	retained, err := s.DescribeKey(ctx, region, peer.KeyID)
	if err != nil || retained.Arn != peer.Arn || !retained.Enabled {
		t.Fatal("peer lost", err)
	}
	rows, err := db.Database().Query("SELECT name,key_id FROM aws_kms_aliases ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name, id string
		if err = rows.Scan(&name, &id); err != nil || id != peer.KeyID {
			t.Fatal("wrong surviving alias", err)
		}
		names = append(names, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(names) != 2 || names[0] != "alias/keep" || names[1] != "alias/move" {
		t.Fatal("wrong surviving aliases", names)
	}
	var id string
	if err = db.Database().QueryRow("SELECT grant_id FROM aws_kms_grants WHERE key_id=?", peer.KeyID).Scan(&id); err != nil || id != peerGrant {
		t.Fatal("peer grant lost", err)
	}
	var count int
	if err = db.Database().QueryRow("SELECT count(*) FROM aws_kms_grants WHERE grant_id=?", targetGrant).Scan(&count); err != nil || count != 0 {
		t.Fatal("expired grant survived", err)
	}
}

func TestKMSDeletionCancelAndValidation(t *testing.T) {
	db, s := kmsTestStore(t, "")
	g := &Gateway{KMS: s}
	ctx := t.Context()
	key, err := s.CreateKey(ctx, "us-east-1", "cancel")
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"KeyId": key.Arn}
	for _, days := range []any{0, 6, 31, 7.5, "7", nil} {
		kmsGrantCall(t, g, "ScheduleKeyDeletion", map[string]any{"KeyId": key.Arn, "PendingWindowInDays": days}, 400, "ValidationException")
	}
	kmsGrantCall(t, g, "ScheduleKeyDeletion", map[string]any{"KeyId": "alias/example"}, 400, "ValidationException")
	kmsGrantCall(t, g, "ScheduleKeyDeletion", map[string]any{"KeyId": key.Arn, "Unknown": true}, 400, "ValidationException")
	kmsGrantCall(t, g, "CancelKeyDeletion", body, 400, "KMSInvalidStateException")
	start := time.Now().UTC().Truncate(time.Millisecond)
	now := start
	s.now = func() time.Time { return now }
	for _, enabled := range []bool{true, false} {
		if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, enabled); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Database().Exec("CREATE TEMP TRIGGER drop_schedule AFTER INSERT ON aws_kms_pending_deletions BEGIN DELETE FROM aws_kms_pending_deletions WHERE key_id=NEW.key_id; END"); err != nil {
			t.Fatal(err)
		}
		kmsGrantCall(t, g, "ScheduleKeyDeletion", body, 500, "KMSInternalException")
		state, err := s.DescribeKey(ctx, "us-east-1", key.KeyID)
		if err != nil || state.Enabled != enabled || state.DeletionDate != 0 {
			t.Fatal("failed scheduling changed key", err)
		}
		if _, err = db.Database().Exec("DROP TRIGGER drop_schedule"); err != nil {
			t.Fatal(err)
		}
		out := kmsGrantCall(t, g, "ScheduleKeyDeletion", body, 200, "")
		if string(out["PendingWindowInDays"]) != "30" || string(out["KeyState"]) != `"PendingDeletion"` {
			t.Fatal("default window")
		}
		for _, op := range []string{"EnableKey", "DisableKey", "ScheduleKeyDeletion"} {
			kmsGrantCall(t, g, op, body, 400, "KMSInvalidStateException")
		}
		for _, op := range []string{"DescribeKey", "GetKeyPolicy", "ListGrants"} {
			kmsGrantCall(t, g, op, body, 200, "")
		}
		now = now.Add(30*24*time.Hour - time.Millisecond)
		out = kmsGrantCall(t, g, "CancelKeyDeletion", body, 200, "")
		if len(out) != 1 || string(out["KeyId"]) != `"`+key.Arn+`"` {
			t.Fatal("cancel response")
		}
		now = now.Add(time.Second)
		state, err = s.DescribeKey(ctx, "us-east-1", key.KeyID)
		if err != nil || state.Enabled || state.KeyState != "Disabled" || state.DeletionDate != 0 {
			t.Fatal("accepted cancellation did not persist", err)
		}
		kmsGrantCall(t, g, "CancelKeyDeletion", body, 400, "KMSInvalidStateException")
	}
}
