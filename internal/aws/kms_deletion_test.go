package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
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

func TestKMSPendingKeyRetainsReadGrantsAndRefusesCrypto(t *testing.T) {
	db, store := kmsTestStore(t, "")
	ctx := t.Context()
	key, err := store.CreateKey(ctx, "us-east-1", "private practice key")
	if err != nil {
		t.Fatal(err)
	}
	roles, err := NewIAMRoleStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	role, err := roles.Create(ctx, "pending-reader", stsRootTrust, "", 3600)
	if err != nil {
		t.Fatal(err)
	}
	gateway := &Gateway{KMS: store}
	req := map[string]any{"KeyId": key.Arn, "GranteePrincipal": role.Arn, "Operations": []string{"DescribeKey"}}
	grant := kmsGrantCall(t, gateway, "CreateGrant", req, 200, "")
	var token string
	if err = json.Unmarshal(grant["GrantToken"], &token); err != nil {
		t.Fatal(err)
	}
	const alias = "alias/pending"
	kmsGrantCall(t, gateway, "CreateAlias", map[string]any{"AliasName": alias, "TargetKeyId": key.KeyID}, 200, "")
	plaintext := []byte("retained before cancellation")
	encrypted := kmsGrantCall(t, gateway, "Encrypt", map[string]any{"KeyId": alias, "Plaintext": plaintext}, 200, "")
	encryptedBody := map[string]any{"KeyId": alias, "CiphertextBlob": encrypted["CiphertextBlob"]}
	schedule := kmsGrantCall(t, gateway, "ScheduleKeyDeletion", map[string]any{"KeyId": key.KeyID, "PendingWindowInDays": 14}, 200, "")
	before := kmsStateFingerprint(t, store)
	for _, id := range []string{key.KeyID, alias} {
		kmsGrantCall(t, gateway, "Encrypt", map[string]any{"KeyId": id, "Plaintext": plaintext}, 400, "KMSInvalidStateException")
		kmsGrantCall(t, gateway, "Decrypt", map[string]any{"KeyId": id, "CiphertextBlob": encrypted["CiphertextBlob"]}, 400, "KMSInvalidStateException")
	}
	rejected := kmsGrantCall(t, gateway, "CreateGrant", req, 400, "KMSInvalidStateException")
	if rejected["GrantId"] != nil || rejected["GrantToken"] != nil || kmsStateFingerprint(t, store) != before {
		t.Fatal("pending write changed key material or acknowledged authority")
	}
	grants := kmsGrantCall(t, gateway, "ListGrants", map[string]any{"KeyId": key.Arn}, 200, "")
	var entries []kmsGrantEntry
	if err = json.Unmarshal(grants["Grants"], &entries); err != nil || len(entries) != 1 || entries[0].GranteePrincipal != role.Arn {
		t.Fatal("pending grant changed", err)
	}
	// Exercise provider authorization after the trusted SDK layer has bound this
	// role generation. The standalone SDK fixture additionally checks SigV4.
	actor := SDKIdentity{Principal: role.RoleName, ARN: "arn:aws:sts::" + kmsAccount + ":assumed-role/" + role.RoleName + "/check", Account: kmsAccount, UserID: role.RoleID + ":check", Expires: time.Now().Add(time.Hour)}
	request := httptest.NewRequest("POST", "/", nil).WithContext(context.WithValue(context.WithValue(ctx, sdkIdentityContextKey{}, actor), sdkPolicyContextKey{}, core.Policy{}))
	tx, err := store.begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := kmsDescribeAsRole(httptest.NewRecorder(), request, tx, "us-east-1", key.KeyID, kmsRequest{GrantTokens: []string{token}})
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	data := out.(map[string]any)["KeyMetadata"].(KMSKeyMetadata)
	var date float64
	if err = json.Unmarshal(schedule["DeletionDate"], &date); err != nil || data.KeyState != "PendingDeletion" || data.Enabled || data.DeletionDate != date {
		t.Fatal("grant did not report saved deadline", err)
	}
	kmsGrantCall(t, gateway, "CancelKeyDeletion", map[string]any{"KeyId": key.Arn}, 200, "")
	kmsGrantCall(t, gateway, "Decrypt", encryptedBody, 400, "DisabledException")
	kmsGrantCall(t, gateway, "EnableKey", map[string]any{"KeyId": key.Arn}, 200, "")
	recovered := kmsGrantCall(t, gateway, "Decrypt", encryptedBody, 200, "")
	var plain []byte
	if err = json.Unmarshal(recovered["Plaintext"], &plain); err != nil || !bytes.Equal(plain, plaintext) {
		t.Fatal("old ciphertext lost", err)
	}
}
