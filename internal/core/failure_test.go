package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClosedStateNeverBecomesEmptySuccess(t *testing.T) {
	s, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	operations := []func() error{
		func() error { return s.Create("bucket", "region") }, func() error { _, e := s.Bucket("bucket"); return e },
		func() error { return s.DeleteBucket("bucket") }, func() error { _, e := s.Put("bucket", "key", "text/plain", nil, "", ""); return e },
		func() error { _, e := s.Get("bucket", "key"); return e }, func() error { return s.Delete("bucket", "key") }, s.ForeignKeyCheck,
	}
	for i, operation := range operations {
		if err := operation(); err == nil {
			t.Fatalf("closed state operation %d became success", i)
		}
	}
	identities := NewIdentities(s)
	if _, err := identities.Names(); err == nil {
		t.Fatal("closed identities returned empty success")
	}
	if err := identities.Set("reader", Policy{}); err == nil {
		t.Fatal("closed identity write succeeded")
	}
	if err := identities.Delete("reader"); err == nil {
		t.Fatal("closed identity delete succeeded")
	}
	if Code(errors.New("unknown error")) != "InternalError" {
		t.Fatal("unexpected error became a domain success")
	}
}
func TestCorruptAndUnversionedStateRejected(t *testing.T) {
	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corrupt, []byte("not a SQLite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := New(corrupt); err == nil {
		s.Close()
		t.Fatal("corrupt database accepted")
	}
	path := filepath.Join(t.TempDir(), "unversioned.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=0"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = New(path); err == nil {
		s.Close()
		t.Fatal("unversioned existing tables accepted")
	}
}
func TestDatabaseFaultsRollbackMutations(t *testing.T) {
	s := testStore(t)
	if err := s.Create("stable", "region"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("stable", "file", "text/plain", []byte("before"), "", ""); err != nil {
		t.Fatal(err)
	}
	// Real SQLite triggers model disk/storage write rejection without a mock fallback.
	for _, tc := range []struct {
		sql       string
		operation func() error
		verify    func()
	}{
		{"CREATE TRIGGER reject_object_update BEFORE UPDATE ON objects BEGIN SELECT RAISE(ABORT,'storage rejected write'); END;", func() error { _, e := s.Put("stable", "file", "text/plain", []byte("after"), "", ""); return e }, func() {
			o, e := s.Get("stable", "file")
			if e != nil || string(o.Data) != "before" {
				t.Fatal("failed update lost original", e)
			}
		}},
		{"CREATE TRIGGER reject_object_delete BEFORE DELETE ON objects BEGIN SELECT RAISE(ABORT,'storage rejected delete'); END;", func() error { return s.Delete("stable", "file") }, func() {
			if _, e := s.Get("stable", "file"); e != nil {
				t.Fatal("failed deletion removed object", e)
			}
		}},
		{"CREATE TRIGGER reject_bucket_insert BEFORE INSERT ON buckets BEGIN SELECT RAISE(ABORT,'storage rejected bucket'); END;", func() error { return s.Create("new", "region") }, func() {
			if _, e := s.Bucket("new"); Code(e) != "NoSuchBucket" {
				t.Fatal("failed creation left a bucket", e)
			}
		}},
	} {
		if _, err := s.db.Exec(tc.sql); err != nil {
			t.Fatal(err)
		}
		if err := tc.operation(); Code(err) != "InternalError" {
			t.Fatal(err)
		}
		tc.verify()
	}
}
func TestMissingTablesAndCorruptTimestampsFailExplicitly(t *testing.T) {
	s := testStore(t)
	if err := s.Create("bucket", "region"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("bucket", "key", "text/plain", []byte("value"), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE objects SET modified='invalid'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("bucket", "key"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Bucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE buckets SET created='invalid'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Buckets(); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Bucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE objects; UPDATE buckets SET created='2026-11-03T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{func() error { _, e := s.Bucket("bucket"); return e }, func() error { return s.DeleteBucket("bucket") }, func() error { _, e := s.Put("bucket", "key", "text/plain", nil, "", ""); return e }, func() error { _, e := s.Get("bucket", "key"); return e }, func() error { return s.Delete("bucket", "key") }} {
		if e := f(); Code(e) != "InternalError" {
			t.Fatal(e)
		}
	}
	if _, err := s.db.Exec("DROP TABLE buckets"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("bucket", "region"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
}
func TestIdentityLimitsAndMalformedDocuments(t *testing.T) {
	s := testStore(t)
	ids := NewIdentities(s)
	p, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"*","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = ids.Set(Administrator, p); err == nil {
		t.Fatal("reserved administrator policy changed")
	}
	if err = ids.Set("reader", p); err != nil {
		t.Fatal(err)
	}
	if names, e := ids.Names(); e != nil || len(names) != 2 {
		t.Fatalf("names %v %v", names, e)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 1000; i++ {
		if _, err = tx.Exec("INSERT INTO identity_policies(name,document) VALUES(?,?)", strings.Repeat("p", i%10+1)+string(rune(0x1000+i)), `{"Version":"2012-10-17","Statement":{"Effect":"Deny","Action":"*","Resource":"*"}}`); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = ids.Set("over-limit", p); err == nil {
		t.Fatal("principal limit bypassed")
	}
	if err = ids.Set("reader", p); err != nil {
		t.Fatal("limit prevented updating existing identity", err)
	}
	for _, document := range []string{`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":17,"Resource":"*"}}`, `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"","Resource":"*"}}`} {
		if _, err = ParsePolicy([]byte(document)); err == nil {
			t.Fatal("accepted malformed identity document")
		}
	}
	if _, err = s.db.Exec("DROP TABLE identity_policies"); err != nil {
		t.Fatal(err)
	}
	if err = ids.Set("reader", p); err == nil {
		t.Fatal("missing identity table write succeeded")
	}
}

func TestGlobalBucketQuotaAndDeleteRollback(t *testing.T) {
	s := testStore(t)
	if err := s.Create("existing", "region-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Create("existing", "region-a"); Code(err) != "BucketAlreadyOwnedByYou" {
		t.Fatal(err)
	}
	if err := s.Create("existing", "region-b"); Code(err) != "BucketAlreadyExists" {
		t.Fatal(err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < MaxBuckets; i++ {
		if _, err = tx.Exec("INSERT INTO buckets VALUES(?,?,?)", fmt.Sprintf("bucket-%04d", i), "region-a", "2026-11-03T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = s.Create("exceeds-limit", "region-a"); Code(err) != "ServiceUnavailable" {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("CREATE TRIGGER reject_bucket_delete BEFORE DELETE ON buckets BEGIN SELECT RAISE(ABORT,'storage rejected bucket deletion'); END;"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBucket("existing"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err = s.Bucket("existing"); err != nil {
		t.Fatal("rejected deletion lost bucket", err)
	}
}
