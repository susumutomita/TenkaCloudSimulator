package aws

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func kmsAliasAllow(context.Context, *sql.Tx, string, string) (bool, error) { return true, nil }
func kmsAliasFixture(t *testing.T) (*KMSStore, *KMSAliasStore, KMSKeyMetadata, KMSKeyMetadata) {
	t.Helper()
	_, keys := kmsTestStore(t, filepath.Join(t.TempDir(), "aliases.db"))
	ctx := context.Background()
	first, err := keys.CreateKey(ctx, "us-east-1", "first")
	if err != nil {
		t.Fatal(err)
	}
	aliases, err := NewKMSAliasStore(keys, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := keys.CreateKey(ctx, "us-east-1", "second")
	if err != nil {
		t.Fatal(err)
	}
	return keys, aliases, first, second
}

// Capture actual alias/key/cursor state, without printing key material or cursor
// secrets on failures. Every refusal below compares the complete saved frame.
func kmsAliasFrame(t *testing.T, keys *KMSStore) map[string][][]any {
	t.Helper()
	frame := map[string][][]any{}
	for _, table := range []string{"aws_kms_keys", "aws_kms_aliases", "aws_kms_alias_state"} {
		rows, err := keys.db.Query(`SELECT * FROM ` + table + ` ORDER BY 1,2`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, v := range values {
				if b, ok := v.([]byte); ok {
					values[i] = bytes.Clone(b)
				}
			}
			frame[table] = append(frame[table], values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return frame
}
func kmsAliasUnchanged(t *testing.T, keys *KMSStore, before map[string][][]any) {
	t.Helper()
	if !reflect.DeepEqual(before, kmsAliasFrame(t, keys)) {
		t.Fatal("refused operation changed alias/key/authority state")
	}
}

func TestKMSAliasesNativeLifecycleAndConcreteTarget(t *testing.T) {
	keys, s, first, second := kmsAliasFixture(t)
	ctx := context.Background()
	if !s.UsesDatabase(keys.db) || s.UsesDatabase(nil) {
		t.Fatal("aliases do not borrow the actual key DB")
	}
	if err := s.CreateAlias(ctx, "alias/App", first.Arn, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 1 || page.Truncated {
		t.Fatal("create/list", err)
	}
	a := page.Aliases[0]
	if a.AliasName != "alias/App" || a.AliasArn != "arn:aws:kms:us-east-1:000000000000:alias/App" || a.TargetKeyId != first.KeyID || a.CreationDate <= 0 || a.LastUpdatedDate < a.CreationDate {
		t.Fatal("alias native shape")
	}
	if err = keys.SetEnabled(ctx, "us-east-1", second.KeyID, false); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateAlias(ctx, "alias/App", second.KeyID, kmsAliasAllow); err != nil {
		t.Fatal("disabled key still supports alias management", err)
	}
	page, err = s.ListAliases(ctx, second.Arn, 50, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 1 || page.Aliases[0].CreationDate != a.CreationDate || page.Aliases[0].LastUpdatedDate < a.LastUpdatedDate {
		t.Fatal("retarget metadata/filter", err)
	}
	tx, err := keys.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.ResolveAliasTx(ctx, tx, a.AliasArn)
	if err != nil || id != second.KeyID {
		tx.Rollback()
		t.Fatal("same-tx resolver", err)
	}
	if _, err = tx.Exec(`SELECT 1`); err != nil {
		tx.Rollback()
		t.Fatal("resolver closed caller tx", err)
	}
	tx.Rollback()
	_, _, err = keys.Encrypt(ctx, "us-east-1", id, nil, []byte("dummy"))
	kmsWantCode(t, err, "DisabledException")
	if err = s.DeleteAlias(ctx, "alias/App", kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	page, err = s.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 0 {
		t.Fatal("delete", err)
	}
	if _, err = keys.DescribeKey(ctx, "us-east-1", first.KeyID); err != nil {
		t.Fatal("alias deletion removed key", err)
	}
	if _, err = keys.DescribeKey(ctx, "us-east-1", second.KeyID); err != nil {
		t.Fatal("alias deletion removed disabled target", err)
	}
}
func TestKMSAliasesPermissionResourcesAreIndependentLiterals(t *testing.T) {
	keys, s, first, second := kmsAliasFixture(t)
	ctx := context.Background()
	name := "alias/Permissions"
	aliasARN := "arn:aws:kms:us-east-1:000000000000:alias/Permissions"
	firstARN := "arn:aws:kms:us-east-1:000000000000:key/" + first.KeyID
	secondARN := "arn:aws:kms:us-east-1:000000000000:key/" + second.KeyID
	capture := func(wantAction string, want []string) KMSAliasAuthorizer {
		at := 0
		t.Cleanup(func() {
			if at != len(want) {
				t.Error("missing required permission/resource check", wantAction)
			}
		})
		return func(_ context.Context, tx *sql.Tx, action, resource string) (bool, error) {
			if tx == nil || action != wantAction || at >= len(want) || resource != want[at] {
				t.Fatal("wrong action/resource or nontransactional authorization", action, resource)
			}
			at++
			return true, nil
		}
	}
	for deny := 0; deny < 2; deny++ {
		before := kmsAliasFrame(t, keys)
		at := 0
		err := s.CreateAlias(ctx, name, first.KeyID, func(context.Context, *sql.Tx, string, string) (bool, error) { ok := at != deny; at++; return ok, nil })
		kmsWantCode(t, err, "AccessDeniedException")
		kmsAliasUnchanged(t, keys, before)
	}
	if err := s.CreateAlias(ctx, name, first.KeyID, capture("kms:CreateAlias", []string{aliasARN, firstARN})); err != nil {
		t.Fatal(err)
	}
	for deny := 0; deny < 3; deny++ {
		before := kmsAliasFrame(t, keys)
		at := 0
		err := s.UpdateAlias(ctx, name, second.KeyID, func(context.Context, *sql.Tx, string, string) (bool, error) { ok := at != deny; at++; return ok, nil })
		kmsWantCode(t, err, "AccessDeniedException")
		kmsAliasUnchanged(t, keys, before)
	}
	if err := s.UpdateAlias(ctx, name, second.KeyID, capture("kms:UpdateAlias", []string{aliasARN, firstARN, secondARN})); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListAliases(ctx, "", 50, "", capture("kms:ListAliases", []string{"*"})); err != nil {
		t.Fatal(err)
	}
	for deny := 0; deny < 2; deny++ {
		before := kmsAliasFrame(t, keys)
		at := 0
		err := s.DeleteAlias(ctx, name, func(context.Context, *sql.Tx, string, string) (bool, error) { ok := at != deny; at++; return ok, nil })
		kmsWantCode(t, err, "AccessDeniedException")
		kmsAliasUnchanged(t, keys, before)
	}
	if err := s.DeleteAlias(ctx, name, capture("kms:DeleteAlias", []string{aliasARN, secondARN})); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	kmsWantCode(t, s.CreateAlias(ctx, name, first.KeyID, nil), "AccessDeniedException")
	kmsAliasUnchanged(t, keys, before)
	infra := errors.New("injected authorization storage outage")
	if err := s.CreateAlias(ctx, name, first.KeyID, func(context.Context, *sql.Tx, string, string) (bool, error) { return false, infra }); !errors.Is(err, infra) {
		t.Fatal("authorization outage masked", err)
	}
	kmsAliasUnchanged(t, keys, before)
}
func TestKMSAliasesRejectInvalidDuplicateCrossRegionAndRename(t *testing.T) {
	keys, s, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	if err := s.CreateAlias(ctx, "alias/Original", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	for _, name := range []string{"", "alias/", "App", "alias/aws/s3", "alias/日本語", "alias/with space", "arn:aws:kms:us-east-1:000000000000:alias/Other", "alias/" + strings.Repeat("a", 251)} {
		kmsWantCode(t, s.CreateAlias(ctx, name, key.KeyID, kmsAliasAllow), "ValidationException")
		kmsAliasUnchanged(t, keys, before)
	}
	kmsWantCode(t, s.CreateAlias(ctx, "alias/Original", key.KeyID, kmsAliasAllow), "AlreadyExistsException")
	kmsWantCode(t, s.UpdateAlias(ctx, "alias/Renamed", key.KeyID, kmsAliasAllow), "NotFoundException")
	kmsWantCode(t, s.UpdateAlias(ctx, "alias/Original", "alias/Original", kmsAliasAllow), "ValidationException")
	kmsWantCode(t, s.CreateAlias(ctx, "alias/Cross", "arn:aws:kms:us-west-2:000000000000:key/"+key.KeyID, kmsAliasAllow), "ValidationException")
	kmsWantCode(t, s.CreateAlias(ctx, "alias/Cross", "arn:aws:kms:us-east-1:111111111111:key/"+key.KeyID, kmsAliasAllow), "ValidationException")
	kmsWantCode(t, s.DeleteAlias(ctx, "alias/Missing", kmsAliasAllow), "NotFoundException")
	kmsAliasUnchanged(t, keys, before)
}
func TestKMSAliasesMarkersPersistenceAndScope(t *testing.T) {
	keys, s, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	for _, name := range []string{"alias/c", "alias/a", "alias/b"} {
		if err := s.CreateAlias(ctx, name, key.KeyID, kmsAliasAllow); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListAliases(ctx, "", 1, "", kmsAliasAllow)
	if err != nil || !first.Truncated || first.Aliases[0].AliasName != "alias/a" || first.NextMarker == "" {
		t.Fatal("first page", err)
	}
	reopened, err := NewKMSAliasStore(keys, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.ListAliases(ctx, "", 1, first.NextMarker, kmsAliasAllow)
	if err != nil || second.Aliases[0].AliasName != "alias/b" {
		t.Fatal("marker did not survive reopening", err)
	}
	before := kmsAliasFrame(t, keys)
	for _, input := range []struct {
		filter string
		limit  int
		marker string
	}{{"", 2, first.NextMarker}, {key.KeyID, 1, first.NextMarker}, {"", 1, first.NextMarker + "x"}, {"", 1, "junk"}} {
		_, err = s.ListAliases(ctx, input.filter, input.limit, input.marker, kmsAliasAllow)
		kmsWantCode(t, err, "InvalidMarkerException")
		kmsAliasUnchanged(t, keys, before)
	}
	west, err := NewKMSAliasStore(keys, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = west.ListAliases(ctx, "", 1, first.NextMarker, kmsAliasAllow)
	kmsWantCode(t, err, "InvalidMarkerException")
	_, other := kmsTestStore(t, filepath.Join(t.TempDir(), "peer.db"))
	peer, err := NewKMSAliasStore(other, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = peer.ListAliases(ctx, "", 1, first.NextMarker, kmsAliasAllow)
	kmsWantCode(t, err, "InvalidMarkerException")
	if err = s.UpdateAlias(ctx, "alias/a", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	_, err = s.ListAliases(ctx, "", 1, first.NextMarker, kmsAliasAllow)
	kmsWantCode(t, err, "InvalidMarkerException")
	for _, limit := range []int{0, -1, 101} {
		_, err = s.ListAliases(ctx, "", limit, "", kmsAliasAllow)
		kmsWantCode(t, err, "ValidationException")
	}
}
func TestKMSAliasesCheckedWritesRollbackAndRetry(t *testing.T) {
	for _, operation := range []string{"Create", "Update", "Delete", "Revision"} {
		t.Run(operation, func(t *testing.T) {
			keys, s, first, second := kmsAliasFixture(t)
			ctx := context.Background()
			name := "alias/Atomic"
			if operation != "Create" {
				if err := s.CreateAlias(ctx, name, first.KeyID, kmsAliasAllow); err != nil {
					t.Fatal(err)
				}
			}
			before := kmsAliasFrame(t, keys)
			var sqlText string
			switch operation {
			case "Create":
				sqlText = `CREATE TRIGGER fail_alias BEFORE INSERT ON aws_kms_aliases BEGIN SELECT RAISE(IGNORE); END;`
			case "Update":
				sqlText = `CREATE TRIGGER fail_alias BEFORE UPDATE ON aws_kms_aliases BEGIN SELECT RAISE(IGNORE); END;`
			case "Delete":
				sqlText = `CREATE TRIGGER fail_alias BEFORE DELETE ON aws_kms_aliases BEGIN SELECT RAISE(IGNORE); END;`
			case "Revision":
				sqlText = `CREATE TRIGGER fail_alias BEFORE UPDATE ON aws_kms_alias_state BEGIN SELECT RAISE(IGNORE); END;`
			}
			if _, err := keys.db.Exec(sqlText); err != nil {
				t.Fatal(err)
			}
			mutate := func() error {
				switch operation {
				case "Create":
					return s.CreateAlias(ctx, name, first.KeyID, kmsAliasAllow)
				case "Update", "Revision":
					return s.UpdateAlias(ctx, name, second.KeyID, kmsAliasAllow)
				default:
					return s.DeleteAlias(ctx, name, kmsAliasAllow)
				}
			}
			if err := mutate(); err == nil {
				t.Fatal("ignored write returned success")
			}
			kmsAliasUnchanged(t, keys, before)
			if _, err := keys.db.Exec(`DROP TRIGGER fail_alias`); err != nil {
				t.Fatal(err)
			}
			if err := mutate(); err != nil {
				t.Fatal("retry", err)
			}
		})
	}
}
func TestKMSAliasesLimitsAndConcurrentLastSlot(t *testing.T) {
	keys, s, key, other := kmsAliasFixture(t)
	ctx := context.Background()
	for i := 0; i < 49; i++ {
		if err := s.CreateAlias(ctx, fmt.Sprintf("alias/cap%02d", i), key.KeyID, kmsAliasAllow); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, name := range []string{"alias/lastA", "alias/lastB"} {
		wg.Add(1)
		go func(name string) { defer wg.Done(); errs <- s.CreateAlias(ctx, name, key.KeyID, kmsAliasAllow) }(name)
	}
	wg.Wait()
	close(errs)
	success, refused := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else {
			kmsWantCode(t, err, "LimitExceededException")
			refused++
		}
	}
	if success != 1 || refused != 1 {
		t.Fatal("last slot overcommit or no admitted writer", success, refused)
	}
	page, err := s.ListAliases(ctx, key.KeyID, 100, "", kmsAliasAllow)
	if err != nil || len(page.Aliases) != 50 {
		t.Fatal("per-key cap", err)
	}
	if err = s.CreateAlias(ctx, "alias/other", other.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	kmsWantCode(t, s.UpdateAlias(ctx, "alias/other", key.KeyID, kmsAliasAllow), "LimitExceededException")
	kmsAliasUnchanged(t, keys, before)
}
func TestKMSAliasesParentSnapshotForeignKeysAndReadFailure(t *testing.T) {
	keys, s, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	if err := s.CreateAlias(ctx, "alias/Bound", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	if _, err := keys.db.Exec(`DELETE FROM aws_kms_keys WHERE region=? AND key_id=?`, "us-east-1", key.KeyID); err == nil {
		t.Fatal("referenced key was deleted")
	}
	kmsAliasUnchanged(t, keys, before)
	if _, err := keys.db.Exec(`UPDATE aws_kms_aliases SET target_created=target_created+1 WHERE name='alias/Bound'`); err != nil {
		t.Fatal(err)
	}
	damaged := kmsAliasFrame(t, keys)
	page, err := s.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err == nil || len(page.Aliases) != 0 {
		t.Fatal("corrupt target snapshot disclosed partial aliases")
	}
	kmsAliasUnchanged(t, keys, damaged)
	tx, err := keys.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveAliasTx(ctx, tx, "alias/Bound"); err == nil {
		tx.Rollback()
		t.Fatal("drifted target adopted")
	}
	tx.Rollback()
	if _, err = keys.db.Exec(`UPDATE aws_kms_aliases SET target_created=? WHERE name='alias/Bound'`, key.CreationDate); err != nil {
		t.Fatal(err)
	}
	kmsAliasUnchanged(t, keys, before)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.ListAliases(canceled, "", 50, "", kmsAliasAllow); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled read", err)
	}
	kmsAliasUnchanged(t, keys, before)
}

func TestKMSAliasesActualRestartTwoTeamsAndTwoRegions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	db, keys := kmsTestStore(t, path)
	eastKey, err := keys.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	westKey, err := keys.CreateKey(ctx, "us-west-2", "")
	if err != nil {
		t.Fatal(err)
	}
	east, err := NewKMSAliasStore(keys, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	west, err := NewKMSAliasStore(keys, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		s  *KMSAliasStore
		id string
	}{{east, eastKey.KeyID}, {west, westKey.KeyID}} {
		if err = fixture.s.CreateAlias(ctx, "alias/Same", fixture.id, kmsAliasAllow); err != nil {
			t.Fatal(err)
		}
	}
	eastPage, err := east.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	_, reopenedKeys := kmsTestStore(t, path)
	reopened, err := NewKMSAliasStore(reopenedKeys, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err != nil || !reflect.DeepEqual(eastPage, after) {
		t.Fatal("durable aliases/timestamps changed", err)
	}
	west, err = NewKMSAliasStore(reopenedKeys, "us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	westPage, err := west.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err != nil || len(westPage.Aliases) != 1 || westPage.Aliases[0].TargetKeyId != westKey.KeyID {
		t.Fatal("region namespace", err)
	}
	_, peerKeys := kmsTestStore(t, filepath.Join(t.TempDir(), "peer.db"))
	peerKey, err := peerKeys.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := NewKMSAliasStore(peerKeys, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = peer.CreateAlias(ctx, "alias/Same", peerKey.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	peerBefore := kmsAliasFrame(t, peerKeys)
	if err = reopened.DeleteAlias(ctx, "alias/Same", kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	kmsAliasUnchanged(t, peerKeys, peerBefore)
	westAfter, err := west.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err != nil || !reflect.DeepEqual(westPage, westAfter) {
		t.Fatal("east cleanup affected west", err)
	}
}
func TestKMSAliasesGlobalBudgetAndMissingStateRefuseSuccess(t *testing.T) {
	keys, s, key, other := kmsAliasFixture(t)
	ctx := context.Background()
	// Physical rows include all regions, even a legacy over-per-key state. New
	// admission must not evade the global storage budget through another target.
	if _, err := keys.db.Exec(`WITH RECURSIVE n(x) AS(SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO aws_kms_aliases SELECT 'us-east-1','alias/legacy'||x,?,?,1,1 FROM n`, key.KeyID, key.CreationDate); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	kmsWantCode(t, s.CreateAlias(ctx, "alias/overflow", other.KeyID, kmsAliasAllow), "LimitExceededException")
	kmsAliasUnchanged(t, keys, before)
	if err := s.DeleteAlias(ctx, "alias/legacy1", kmsAliasAllow); err != nil {
		t.Fatal("cleanup must remain possible at cap", err)
	}
	if err := s.CreateAlias(ctx, "alias/admitted", other.KeyID, kmsAliasAllow); err != nil {
		t.Fatal("released physical slot", err)
	}
	if _, err := keys.db.Exec(`ALTER TABLE aws_kms_aliases RENAME COLUMN target_created TO unavailable_target`); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListAliases(ctx, "", 50, "", kmsAliasAllow)
	if err == nil || len(page.Aliases) != 0 {
		t.Fatal("damaged schema returned a partial successful page")
	}
	if _, err = keys.db.Exec(`ALTER TABLE aws_kms_aliases RENAME COLUMN unavailable_target TO target_created`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListAliases(ctx, "", 50, "", kmsAliasAllow); err != nil {
		t.Fatal("repair/retry", err)
	}
}

func TestKMSAliasesCryptoUsesConcreteKeyAndRetargetedCiphertextGuard(t *testing.T) {
	keys, s, first, second := kmsAliasFixture(t)
	ctx := context.Background()
	name := "alias/Crypto"
	if err := s.CreateAlias(ctx, name, first.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	plaintext := []byte{0, 1, 2, 255, 17}
	contextMap := map[string]string{"exercise": "local"}
	authorize := func(wantAction, wantID string) KMSAliasAuthorizer {
		return func(ctx context.Context, tx *sql.Tx, action, resource string) (bool, error) {
			if action != wantAction || resource != "arn:aws:kms:us-east-1:000000000000:key/"+wantID {
				t.Fatal("crypto authorization did not use concrete key/action")
			}
			// This real read on the supplied tx is safe with the one-connection provider
			// DB; nested public methods/another transaction would deadlock instead.
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_kms_aliases WHERE region='us-east-1' AND name='alias/Crypto'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("not the caller alias transaction", err)
			}
			return true, nil
		}
	}
	blob, arn, err := s.Encrypt(ctx, name, contextMap, plaintext, authorize("kms:Encrypt", first.KeyID))
	if err != nil || arn != first.Arn {
		t.Fatal("alias encrypt", err)
	}
	blobRegion, blobID, _, err := kmsBlob(blob)
	if err != nil || blobRegion != "us-east-1" || blobID != first.KeyID {
		t.Fatal("alias changed native ciphertext identity", err)
	}
	got, arn, err := s.Decrypt(ctx, "arn:aws:kms:us-east-1:000000000000:alias/Crypto", contextMap, blob, authorize("kms:Decrypt", first.KeyID))
	if err != nil || arn != first.Arn || !bytes.Equal(got, plaintext) {
		t.Fatal("alias decrypt", err)
	}
	if err = s.UpdateAlias(ctx, name, second.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	called := false
	got, arn, err = s.Decrypt(ctx, name, contextMap, blob, func(context.Context, *sql.Tx, string, string) (bool, error) { called = true; return true, nil })
	kmsWantCode(t, err, "IncorrectKeyException")
	if len(got) != 0 || arn != "" || called {
		t.Fatal("retargeted alias selected old ciphertext or authorized wrong key")
	}
	kmsAliasUnchanged(t, keys, before)
	got, arn, err = s.Decrypt(ctx, "", contextMap, blob, authorize("kms:Decrypt", first.KeyID))
	if err != nil || arn != first.Arn || !bytes.Equal(got, plaintext) {
		t.Fatal("omitted KeyId no longer follows ciphertext UUID", err)
	}
	newBlob, arn, err := s.Encrypt(ctx, name, contextMap, plaintext, authorize("kms:Encrypt", second.KeyID))
	if err != nil || arn != second.Arn {
		t.Fatal("retarget new encryption", err)
	}
	_, id, _, err := kmsBlob(newBlob)
	if err != nil || id != second.KeyID {
		t.Fatal("new alias did not select retarget", err)
	}
	// Direct concrete wrappers remain interoperable with alias-generated blobs.
	got, arn, err = keys.Decrypt(ctx, "us-east-1", second.KeyID, contextMap, newBlob)
	if err != nil || arn != second.Arn || !bytes.Equal(got, plaintext) {
		t.Fatal("legacy concrete wrapper changed", err)
	}
}
func TestKMSAliasesCryptoDenialsAndIgnoredCounterPreserveFullFrame(t *testing.T) {
	keys, s, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	if err := s.CreateAlias(ctx, "alias/Bound", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	blob, _, err := keys.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	for _, authorize := range []KMSAliasAuthorizer{nil, func(context.Context, *sql.Tx, string, string) (bool, error) { return false, nil }} {
		out, arn, err := s.Encrypt(ctx, "alias/Bound", nil, []byte("dummy"), authorize)
		kmsWantCode(t, err, "AccessDeniedException")
		if len(out) != 0 || arn != "" {
			t.Fatal("denied encryption leaked success")
		}
		kmsAliasUnchanged(t, keys, before)
		out, arn, err = s.Decrypt(ctx, "", nil, blob, authorize)
		kmsWantCode(t, err, "AccessDeniedException")
		if len(out) != 0 || arn != "" {
			t.Fatal("denied decrypt leaked plaintext")
		}
		kmsAliasUnchanged(t, keys, before)
	}
	if _, err = keys.db.Exec(`CREATE TRIGGER ignore_alias_crypto BEFORE UPDATE OF encryptions ON aws_kms_keys BEGIN SELECT RAISE(IGNORE); END;`); err != nil {
		t.Fatal(err)
	}
	out, arn, err := s.Encrypt(ctx, "alias/Bound", nil, []byte("dummy"), kmsAliasAllow)
	if err == nil || len(out) != 0 || arn != "" {
		t.Fatal("ignored counter returned alias ciphertext")
	}
	kmsAliasUnchanged(t, keys, before)
	if _, err = keys.db.Exec(`DROP TRIGGER ignore_alias_crypto`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Encrypt(ctx, "alias/Bound", nil, []byte("dummy"), kmsAliasAllow); err != nil {
		t.Fatal("retry", err)
	}
	row, err := kmsRead(ctx, keys.db, "us-east-1", key.KeyID)
	if err != nil || row.encryptions != 2 {
		t.Fatal("failed admission consumed a counter", err)
	}
}
func TestKMSCallerTxCryptoHelpersNeverCommitOrRollbackCaller(t *testing.T) {
	keys, _, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	before := kmsAliasFrame(t, keys)
	tx, err := keys.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	blob, arn, err := kmsEncryptTx(ctx, tx, "us-east-1", key.KeyID, nil, []byte("transaction fixture"))
	if err != nil || arn != key.Arn {
		tx.Rollback()
		t.Fatal(err)
	}
	_, id, encrypted, err := kmsBlob(blob)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	plaintext, arn, err := kmsDecryptTx(ctx, tx, "us-east-1", id, nil, encrypted)
	if err != nil || arn != key.Arn || string(plaintext) != "transaction fixture" {
		tx.Rollback()
		t.Fatal("same uncommitted key transaction", err)
	}
	if _, err = tx.Exec(`UPDATE aws_kms_keys SET description='caller mutation' WHERE region='us-east-1' AND key_id=?`, key.KeyID); err != nil {
		tx.Rollback()
		t.Fatal("helper closed caller transaction", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	kmsAliasUnchanged(t, keys, before)
}
func TestKMSAliasesCryptoCommitFailurePublishesNoCiphertext(t *testing.T) {
	keys, s, key, _ := kmsAliasFixture(t)
	ctx := context.Background()
	if err := s.CreateAlias(ctx, "alias/Commit", key.KeyID, kmsAliasAllow); err != nil {
		t.Fatal(err)
	}
	if _, err := keys.db.Exec(`CREATE TABLE alias_crypto_parent(id INTEGER PRIMARY KEY);
 CREATE TABLE alias_crypto_child(parent INTEGER REFERENCES alias_crypto_parent(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER fail_alias_crypto_commit AFTER UPDATE OF encryptions ON aws_kms_keys BEGIN INSERT INTO alias_crypto_child VALUES(1); END;`); err != nil {
		t.Fatal(err)
	}
	before := kmsAliasFrame(t, keys)
	blob, arn, err := s.Encrypt(ctx, "alias/Commit", nil, []byte("dummy"), kmsAliasAllow)
	if err == nil || len(blob) != 0 || arn != "" {
		t.Fatal("failed COMMIT published ciphertext")
	}
	kmsAliasUnchanged(t, keys, before)
	var rows int
	if err = keys.db.QueryRow(`SELECT count(*) FROM alias_crypto_child`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("failed commit retained deferred child", err)
	}
	if _, err = keys.db.Exec(`DROP TRIGGER fail_alias_crypto_commit`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Encrypt(ctx, "alias/Commit", nil, []byte("dummy"), kmsAliasAllow); err != nil {
		t.Fatal("commit-failure retry", err)
	}
}
