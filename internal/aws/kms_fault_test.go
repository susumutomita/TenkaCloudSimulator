package aws

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestKMSAtomicCounterCeilingAndMutationRollback(t *testing.T) {
	ctx := context.Background()
	db, s := kmsTestStore(t, "")
	key, err := s.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("UPDATE aws_kms_keys SET encryptions=? WHERE key_id=?", kmsMaxEncryptions-1, key.KeyID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, e := s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
			out <- e
		}()
	}
	wg.Wait()
	close(out)
	success, limited := 0, 0
	for e := range out {
		if e == nil {
			success++
		} else {
			var failure kmsError
			if errors.As(e, &failure) && failure.code == "LimitExceededException" {
				limited++
			} else {
				t.Fatal(e)
			}
		}
	}
	if success != 1 || limited != 1 {
		t.Fatal(success, limited)
	}
	if _, err = db.Database().Exec("UPDATE aws_kms_keys SET encryptions=0 WHERE key_id=?", key.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER fail_kms_update BEFORE UPDATE ON aws_kms_keys BEGIN SELECT RAISE(ABORT,'fixture failure'); END;`); err != nil {
		t.Fatal(err)
	}
	blob, arn, err := s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err == nil || blob != nil || arn != "" {
		t.Fatal("failed transaction returned ciphertext")
	}
	if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, false); err == nil {
		t.Fatal("failed Disable succeeded")
	}
	row, err := kmsRead(ctx, db.Database(), "us-east-1", key.KeyID)
	if err != nil || row.encryptions != 0 || !row.metadata.Enabled {
		t.Fatal("partial mutation", err)
	}
	if _, err = db.Database().Exec(`DROP TRIGGER fail_kms_update; CREATE TRIGGER fail_kms_insert BEFORE INSERT ON aws_kms_keys BEGIN SELECT RAISE(ABORT,'fixture failure'); END;`); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateKey(ctx, "us-east-1", "")
	if err == nil || created.KeyID != "" {
		t.Fatal("failed Create returned key")
	}
	list, err := s.ListKeys(ctx, "us-east-1", 100, "")
	if err != nil || len(list.Keys) != 1 {
		t.Fatal("partial create", err)
	}
}
func TestKMSDatabaseFailuresAreNotSuccessfulCrypto(t *testing.T) {
	ctx := context.Background()
	db, s := kmsTestStore(t, "")
	key, err := s.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err = s.Decrypt(canceled, "us-east-1", "", nil, blob); err == nil {
		t.Fatal("canceled operation succeeded")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = NewKMSStore(db.Database()); err == nil {
		t.Fatal("closed schema accepted")
	}
	if _, err = s.CreateKey(ctx, "us-east-1", ""); err == nil {
		t.Fatal("closed Create succeeded")
	}
	if _, err = s.DescribeKey(ctx, "us-east-1", key.KeyID); err == nil {
		t.Fatal("closed Describe succeeded")
	}
	if _, err = s.ListKeys(ctx, "us-east-1", 100, ""); err == nil {
		t.Fatal("closed List succeeded")
	}
	if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, false); err == nil {
		t.Fatal("closed Disable succeeded")
	}
	if _, _, err = s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy")); err == nil {
		t.Fatal("closed Encrypt succeeded")
	}
	if _, _, err = s.Decrypt(ctx, "us-east-1", "", nil, blob); err == nil {
		t.Fatal("closed Decrypt succeeded")
	}
	w, _ := kmsHTTP(t, &Gateway{KMS: s}, "CreateKey", `{}`, "")
	if w.Code != 500 || !strings.Contains(w.Body.String(), "KMSInternalException") {
		t.Fatal(w.Code, w.Body.String())
	}
	w, _ = kmsHTTP(t, &Gateway{KMS: s}, "ListKeys", `{}`, "unbound")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestKMSCorruptMaterialAndContextLimitsFailClosed(t *testing.T) {
	ctx := context.Background()
	db, s := kmsTestStore(t, "")
	key, err := s.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("PRAGMA ignore_check_constraints=ON; UPDATE aws_kms_keys SET material=x'01'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DescribeKey(ctx, "us-east-1", key.KeyID); err == nil {
		t.Fatal("corrupt material described")
	}
	if _, _, err = s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy")); err == nil {
		t.Fatal("corrupt material encrypted")
	}
	if _, _, err = s.Decrypt(ctx, "us-east-1", "", nil, blob); err == nil {
		t.Fatal("corrupt material decrypted")
	}
	huge := map[string]string{}
	for i := 0; i < 101; i++ {
		huge[string(rune(i+32))] = "x"
	}
	for _, m := range []map[string]string{huge, {"": "x"}, {"x": string([]byte{255})}} {
		_, err = kmsContext(m)
		kmsWantCode(t, err, "ValidationException")
	}
	if _, err = s.CreateKey(ctx, "bad", ""); err == nil {
		t.Fatal("invalid region")
	}
	if _, err = s.CreateKey(ctx, "us-east-1", strings.Repeat("界", 8193)); err == nil {
		t.Fatal("description limit")
	}
	if err = s.SetEnabled(ctx, "bad", key.KeyID, true); err == nil {
		t.Fatal("invalid region mutation")
	}
	if _, _, err = s.Encrypt(ctx, "bad", key.KeyID, nil, []byte("x")); err == nil {
		t.Fatal("invalid region crypto")
	}
	request := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	request.Header.Set("X-Simulator-Service", "kms")
	request.Header.Set("X-Amz-Target", "ListKeys")
	request.Header.Set("Content-Type", "application/x-amz-json-1.1")
	w := httptest.NewRecorder()
	(&Gateway{KMS: s}).ServeHTTP(w, request)
	if w.Code != 400 {
		t.Fatal("wrong wire target accepted", w.Code)
	}
}

// Deferred constraints exercise a real COMMIT failure after successful crypto or
// insertion, not just an early SQL validation error. No result may escape.
func TestKMSFailedCommitReturnsNoKeyOrCiphertext(t *testing.T) {
	ctx := context.Background()
	db, s := kmsTestStore(t, "")
	_, err := db.Database().Exec(`CREATE TABLE kms_fixture_parent(id INTEGER PRIMARY KEY);
 CREATE TABLE kms_fixture_child(parent INTEGER REFERENCES kms_fixture_parent(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER kms_commit_insert AFTER INSERT ON aws_kms_keys BEGIN INSERT INTO kms_fixture_child VALUES(1); END;`)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.CreateKey(ctx, "us-east-1", "")
	if err == nil || key.KeyID != "" {
		t.Fatal("commit failure returned key")
	}
	keys, err := s.ListKeys(ctx, "us-east-1", 100, "")
	if err != nil || len(keys.Keys) != 0 {
		t.Fatal("failed create left state", err)
	}
	if _, err = db.Database().Exec("DROP TRIGGER kms_commit_insert"); err != nil {
		t.Fatal(err)
	}
	key, err = s.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec(`CREATE TRIGGER kms_commit_update AFTER UPDATE ON aws_kms_keys BEGIN INSERT INTO kms_fixture_child VALUES(1); END;`); err != nil {
		t.Fatal(err)
	}
	blob, arn, err := s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("dummy"))
	if err == nil || blob != nil || arn != "" {
		t.Fatal("commit failure returned ciphertext")
	}
	row, err := kmsRead(ctx, db.Database(), "us-east-1", key.KeyID)
	if err != nil || row.encryptions != 0 {
		t.Fatal("failed encryption consumed state", err)
	}
	if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, false); err == nil {
		t.Fatal("failed commit disabled key")
	}
	row, err = kmsRead(ctx, db.Database(), "us-east-1", key.KeyID)
	if err != nil || !row.metadata.Enabled {
		t.Fatal("failed disable changed state", err)
	}
}
func TestKMSKeyQuotaCorruptListAndMissingSchema(t *testing.T) {
	ctx := context.Background()
	db, s := kmsTestStore(t, "")
	_, err := db.Database().Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<1000)
 INSERT INTO aws_kms_keys(region,key_id,description,created,state,material,encryptions,policy)
 SELECT 'us-east-1',printf('%08x-0000-4000-8000-%012x',x,x),'',1,'Enabled',zeroblob(32),0,? FROM n`, kmsDefaultPolicy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateKey(ctx, "us-east-1", "")
	kmsWantCode(t, err, "LimitExceededException")
	if _, err = db.Database().Exec("DELETE FROM aws_kms_keys WHERE key_id NOT IN (SELECT key_id FROM aws_kms_keys LIMIT 1); UPDATE aws_kms_keys SET key_id='corrupt'"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListKeys(ctx, "us-east-1", 100, ""); err == nil {
		t.Fatal("corrupt identifier listed as a key")
	}
	if _, err = db.Database().Exec("DROP TABLE aws_kms_keys"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateKey(ctx, "us-east-1", ""); err == nil {
		t.Fatal("missing schema accepted Create")
	}
}
func TestKMSTypedInternalErrorAndRedaction(t *testing.T) {
	w := httptest.NewRecorder()
	kmsWriteError(w, kmsInternal())
	if w.Code != 500 || !strings.Contains(w.Body.String(), "KMSInternalException") {
		t.Fatal(w.Code, w.Body.String())
	}
	_, s := kmsTestStore(t, "")
	ctx := context.Background()
	key, err := s.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	blob, _, err := s.Encrypt(ctx, "us-east-1", key.KeyID, map[string]string{"context": "private-value"}, []byte("private-plaintext"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Decrypt(ctx, "us-east-1", "", nil, blob)
	if err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatal("error disclosed request data")
	}
}
