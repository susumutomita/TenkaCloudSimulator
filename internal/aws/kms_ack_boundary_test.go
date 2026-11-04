package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func kmsStateFingerprint(t *testing.T, s *KMSStore) string {
	t.Helper()
	rows, err := s.db.Query("SELECT region,key_id,description,created,state,material,encryptions,policy FROM aws_kms_keys ORDER BY region,key_id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	all := [][]any{}
	for rows.Next() {
		values := make([]any, 8)
		dest := make([]any, 8)
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		all = append(all, values)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func kmsNativeInternalFailure(t *testing.T, w *httptest.ResponseRecorder, result map[string]json.RawMessage) {
	t.Helper()
	if w.Code != 500 || string(result["__type"]) != `"KMSInternalException"` || w.Header().Get("X-Amzn-Errortype") != "KMSInternalException" {
		t.Fatal("expected sanitized native storage failure", w.Code, w.Body.String())
	}
	for _, field := range []string{"KeyMetadata", "KeyId", "CiphertextBlob", "Plaintext"} {
		if _, ok := result[field]; ok {
			t.Fatal("failed mutation acknowledged secret or resource", field)
		}
	}
	if strings.Contains(w.Body.String(), "private-kms") || strings.Contains(w.Body.String(), "IGNORE") {
		t.Fatal("storage failure disclosed caller or host state")
	}
}
func TestKMSIgnoredWritesCannotAcknowledgeNativeSuccess(t *testing.T) {
	_, s := kmsTestStore(t, "")
	g := &Gateway{KMS: s}
	ctx := context.Background()
	key, err := s.CreateKey(ctx, "us-east-1", "private-kms-original")
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.CreateKey(ctx, "us-west-2", "private-kms-peer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("CREATE TRIGGER kms_ignore_insert BEFORE INSERT ON aws_kms_keys BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	before := kmsStateFingerprint(t, s)
	w, out := kmsHTTP(t, g, "CreateKey", `{"Description":"private-kms-new"}`, "")
	kmsNativeInternalFailure(t, w, out)
	if kmsStateFingerprint(t, s) != before {
		t.Fatal("ignored creation changed key ledger")
	}
	if _, err = s.db.Exec("DROP TRIGGER kms_ignore_insert"); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"DisableKey", "EnableKey", "Encrypt"} {
		if op == "EnableKey" {
			if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, false); err != nil {
				t.Fatal(err)
			}
		} else {
			if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, true); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = s.db.Exec("CREATE TRIGGER kms_ignore_update BEFORE UPDATE ON aws_kms_keys WHEN OLD.region='us-east-1' BEGIN SELECT RAISE(IGNORE); END"); err != nil {
			t.Fatal(err)
		}
		before = kmsStateFingerprint(t, s)
		body := `{"KeyId":"` + key.KeyID + `"}`
		if op == "Encrypt" {
			body = `{"KeyId":"` + key.KeyID + `","Plaintext":"cHJpdmF0ZS1rbXM=","EncryptionContext":{"scope":"private-kms"}}`
		}
		w, out = kmsHTTP(t, g, op, body, "")
		kmsNativeInternalFailure(t, w, out)
		if kmsStateFingerprint(t, s) != before {
			t.Fatal("ignored mutation changed state/counter/peer", op)
		}
		if _, err = s.db.Exec("DROP TRIGGER kms_ignore_update"); err != nil {
			t.Fatal(err)
		}
		w, out = kmsHTTP(t, g, op, body, "")
		if w.Code != 200 {
			t.Fatal("clean retry failed", op, w.Code, w.Body.String())
		}
		if op == "Encrypt" && len(out["CiphertextBlob"]) == 0 {
			t.Fatal("accepted encryption missing ciphertext")
		}
	}
	peerAfter, err := s.DescribeKey(ctx, "us-west-2", peer.KeyID)
	if err != nil || peerAfter.Description != "private-kms-peer" || !peerAfter.Enabled {
		t.Fatal("failed operations affected regional peer")
	}
	if _, err = NewKMSStore(nil); err == nil {
		t.Fatal("nil database accepted")
	}
}
func TestKMSNativeMediaTypeAndHeaderMultiplicityAreBounded(t *testing.T) {
	_, s := kmsTestStore(t, "")
	key, err := s.CreateKey(context.Background(), "us-east-1", "private-kms-original")
	if err != nil {
		t.Fatal(err)
	}
	before := kmsStateFingerprint(t, s)
	for _, media := range []string{"application/x-amz-json-1.1evil", "application/x-amz-json-1.1; broken", "application/x-amz-json-1.1; charset=latin1", "application/x-amz-json-1.1; ignored=option"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{"KeyId":"`+key.KeyID+`"}`))
		r.Header.Set("X-Amz-Target", "TrentService.DisableKey")
		r.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r, "us-east-1", func(string, string) bool { return true })
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"ValidationException"`) {
			t.Fatal("invalid media accepted", media, w.Code)
		}
	}
	for _, header := range []string{"Content-Type", "X-Amz-Target"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		r.Header.Set("X-Amz-Target", "TrentService.CreateKey")
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Add(header, r.Header.Get(header))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r, "us-east-1", func(string, string) bool { return true })
		if w.Code != 400 {
			t.Fatal("repeated native routing header accepted", header)
		}
	}
	if kmsStateFingerprint(t, s) != before {
		t.Fatal("invalid transport changed existing key ledger")
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"KeyId":"`+key.KeyID+`"}`))
	r.Header.Set("X-Amz-Target", "TrentService.DescribeKey")
	r.Header.Set("Content-Type", "application/x-amz-json-1.1; charset=UTF-8")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r, "us-east-1", func(string, string) bool { return true })
	if w.Code != 200 {
		t.Fatal("supported UTF-8 media parameter rejected", w.Code, w.Body.String())
	}
	if kmsStateFingerprint(t, s) != before {
		t.Fatal("metadata read changed ledger")
	}
}
