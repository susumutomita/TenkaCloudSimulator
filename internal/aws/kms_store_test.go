package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"path/filepath"
	"strings"
	"testing"
)

func kmsTestStore(t *testing.T, path string) (*core.Store, *KMSStore) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewKMSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return db, s
}
func kmsWantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e kmsError
	if !errors.As(err, &e) || e.code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
func TestKMSRoundtripStatePersistenceAndTeamIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "team-a.db")
	db, s := kmsTestStore(t, path)
	key, err := s.CreateKey(ctx, "us-east-1", "Test key")
	if err != nil {
		t.Fatal(err)
	}
	if key.KeyState != "Enabled" || !key.Enabled || key.MultiRegion || key.Arn != kmsARN("us-east-1", key.KeyID) {
		t.Fatal(key)
	}
	contextMap := map[string]string{"team": "A", "document": "résumé"}
	plaintext := []byte("private local example")
	blob, arn, err := s.Encrypt(ctx, "us-east-1", key.KeyID, contextMap, plaintext)
	if err != nil || arn != key.Arn {
		t.Fatal(arn, err)
	}
	blob2, _, err := s.Encrypt(ctx, "us-east-1", key.KeyID, contextMap, plaintext)
	if err != nil || bytes.Equal(blob, blob2) {
		t.Fatal("randomized authenticated ciphertext", err)
	}
	out, _, err := s.Decrypt(ctx, "us-east-1", key.Arn, map[string]string{"document": "résumé", "team": "A"}, blob)
	if err != nil || !bytes.Equal(out, plaintext) {
		t.Fatal(string(out), err)
	}
	other, err := s.CreateKey(ctx, "us-east-1", "")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Decrypt(ctx, "us-east-1", other.KeyID, contextMap, blob)
	kmsWantCode(t, err, "IncorrectKeyException")
	for _, bad := range []map[string]string{nil, {"team": "a", "document": "résumé"}, {"team": "A"}, {"team": "A", "document": "résumé", "extra": ""}} {
		_, _, err = s.Decrypt(ctx, "us-east-1", "", bad, blob)
		kmsWantCode(t, err, "InvalidCiphertextException")
	}
	tampered := bytes.Clone(blob)
	tampered[len(tampered)-1] ^= 1
	_, _, err = s.Decrypt(ctx, "us-east-1", "", contextMap, tampered)
	kmsWantCode(t, err, "InvalidCiphertextException")
	_, _, err = s.Decrypt(ctx, "us-west-2", "", contextMap, blob)
	kmsWantCode(t, err, "InvalidCiphertextException")
	for range 2 {
		if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, false); err != nil {
			t.Fatal(err)
		}
	}
	disabled, err := s.DescribeKey(ctx, "us-east-1", key.KeyID)
	if err != nil || disabled.Enabled || disabled.KeyState != "Disabled" {
		t.Fatal(disabled, err)
	}
	_, _, err = s.Encrypt(ctx, "us-east-1", key.KeyID, nil, plaintext)
	kmsWantCode(t, err, "DisabledException")
	_, _, err = s.Decrypt(ctx, "us-east-1", "", contextMap, blob)
	kmsWantCode(t, err, "DisabledException")
	for range 2 {
		if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, true); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := json.Marshal(key)
	if err != nil || bytes.Contains(encoded, plaintext) || bytes.Contains(encoded, []byte("material")) {
		t.Fatal("private material in metadata", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	_, restarted := kmsTestStore(t, path)
	out, _, err = restarted.Decrypt(ctx, "us-east-1", "", contextMap, blob)
	if err != nil || !bytes.Equal(out, plaintext) {
		t.Fatal("restart", err)
	}
	_, teamB := kmsTestStore(t, filepath.Join(t.TempDir(), "team-b.db"))
	_, _, err = teamB.Decrypt(ctx, "us-east-1", "", contextMap, blob)
	kmsWantCode(t, err, "NotFoundException")
	list, err := teamB.ListKeys(ctx, "us-east-1", 100, "")
	if err != nil || len(list.Keys) != 0 {
		t.Fatal(list, err)
	}
}
func TestKMSBoundsListAndFailClosedPersistence(t *testing.T) {
	ctx := context.Background()
	db, s := kmsTestStore(t, "")
	keys := []KMSKeyMetadata{}
	for range 3 {
		k, err := s.CreateKey(ctx, "us-east-1", "")
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, k)
	}
	if _, err := s.CreateKey(ctx, "us-west-2", ""); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	marker := ""
	for {
		page, err := s.ListKeys(ctx, "us-east-1", 1, marker)
		if err != nil || len(page.Keys) != 1 || seen[page.Keys[0].KeyID] {
			t.Fatal(page, err)
		}
		seen[page.Keys[0].KeyID] = true
		if !page.Truncated {
			break
		}
		marker = page.NextMarker
	}
	if len(seen) != 3 {
		t.Fatal(seen)
	}
	for _, bad := range []string{"garbage", marker + "!"} {
		_, err := s.ListKeys(ctx, "us-east-1", 1, bad)
		kmsWantCode(t, err, "ValidationException")
	}
	_, err := s.ListKeys(ctx, "us-west-2", 1, marker)
	kmsWantCode(t, err, "ValidationException")
	for _, limit := range []int{0, 1001} {
		_, err = s.ListKeys(ctx, "us-east-1", limit, "")
		kmsWantCode(t, err, "ValidationException")
	}
	key := keys[0]
	for _, input := range [][]byte{nil, {}, make([]byte, 4097)} {
		_, _, err = s.Encrypt(ctx, "us-east-1", key.KeyID, nil, input)
		kmsWantCode(t, err, "ValidationException")
	}
	if _, _, err = s.Encrypt(ctx, "us-east-1", key.KeyID, nil, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	empty, _, err := s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Decrypt(ctx, "us-east-1", "", map[string]string{}, empty); err != nil {
		t.Fatal("empty context equivalence", err)
	}
	_, _, err = s.Encrypt(ctx, "us-east-1", key.KeyID, map[string]string{"k": strings.Repeat("x", 8193)}, []byte("x"))
	kmsWantCode(t, err, "ValidationException")
	for _, bad := range []string{"alias/test", "arn:aws:kms:us-west-2:000000000000:key/" + key.KeyID, "arn:aws:kms:us-east-1:111111111111:key/" + key.KeyID, "", "uppercase"} {
		_, err = s.DescribeKey(ctx, "us-east-1", bad)
		kmsWantCode(t, err, "ValidationException")
	}
	missing := "00000000-0000-4000-8000-000000000000"
	_, err = s.DescribeKey(ctx, "us-east-1", missing)
	kmsWantCode(t, err, "NotFoundException")
	err = s.SetEnabled(ctx, "us-east-1", missing, true)
	kmsWantCode(t, err, "NotFoundException")
	if _, err = db.Database().Exec("UPDATE aws_kms_keys SET encryptions=? WHERE key_id=?", kmsMaxEncryptions, key.KeyID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Encrypt(ctx, "us-east-1", key.KeyID, nil, []byte("x"))
	kmsWantCode(t, err, "LimitExceededException")
	if _, err = db.Database().Exec("UPDATE aws_kms_keys SET policy='corrupt' WHERE key_id=?", key.KeyID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DescribeKey(ctx, "us-east-1", key.KeyID); err == nil {
		t.Fatal("corrupt policy accepted")
	}
	if err = s.SetEnabled(ctx, "us-east-1", key.KeyID, true); err == nil {
		t.Fatal("corrupt state enabled")
	}
	if _, _, err = s.Decrypt(ctx, "us-east-1", "", nil, empty); err == nil {
		t.Fatal("corrupt state decrypted")
	}
}
func TestKMSBlobAndContextBindings(t *testing.T) {
	material := bytes.Repeat([]byte{7}, 32)
	id := "00000000-0000-4000-8000-000000000000"
	blob, err := kmsSeal(material, "us-east-1", id, map[string]string{"a": "bc"}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	region, parsed, encrypted, err := kmsBlob(blob)
	if err != nil || region != "us-east-1" || parsed != id {
		t.Fatal(region, parsed, err)
	}
	for _, contextMap := range []map[string]string{{"ab": "c"}, {"A": "bc"}} {
		_, err = kmsOpen(material, region, id, contextMap, encrypted)
		kmsWantCode(t, err, "InvalidCiphertextException")
	}
	_, err = kmsOpen(material, "us-west-2", id, map[string]string{"a": "bc"}, encrypted)
	kmsWantCode(t, err, "InvalidCiphertextException")
	_, err = kmsOpen(material, region, "00000000-0000-4000-8000-000000000001", map[string]string{"a": "bc"}, encrypted)
	kmsWantCode(t, err, "InvalidCiphertextException")
	for _, bad := range [][]byte{nil, []byte("x"), []byte(kmsBlobPrefix), append([]byte(kmsBlobPrefix), 0), make([]byte, 6145), blob[:len(blob)-1]} {
		_, _, _, err = kmsBlob(bad)
		kmsWantCode(t, err, "InvalidCiphertextException")
	}
	_, err = kmsSeal(nil, region, id, nil, []byte("x"))
	kmsWantCode(t, err, "KMSInternalException")
	_, err = kmsOpen(nil, region, id, nil, encrypted)
	kmsWantCode(t, err, "KMSInternalException")
}
