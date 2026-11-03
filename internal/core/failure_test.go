package core

import (
	"errors"
	"path/filepath"
	"testing"
)

func execSQL(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestClosedStoreMutationsFail(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	for name, run := range map[string]func() error{
		"create":         func() error { return s.Create("bucket", "region") },
		"bucket":         func() error { _, e := s.Bucket("bucket"); return e },
		"delete bucket":  func() error { return s.DeleteBucket("bucket") },
		"put":            func() error { _, e := s.Put("bucket", "key", "text/plain", nil, "", ""); return e },
		"get":            func() error { _, e := s.Get("bucket", "key"); return e },
		"delete object":  func() error { return s.Delete("bucket", "key") },
		"foreign keys":   s.ForeignKeyCheck,
		"identity write": func() error { return NewIdentities(s).Set("reader", Policy{}) },
		"identity list":  func() error { _, e := NewIdentities(s).Names(); return e },
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("closed database reported success")
			}
		})
	}
	if Code(errors.New("unexpected")) != "InternalError" {
		t.Fatal("unknown error was not internal")
	}
}

func TestCorruptStoredTimestampsFail(t *testing.T) {
	s := testStore(t)
	if err := s.Create("bucket", "region"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("bucket", "key", "text/plain", []byte("original"), "", ""); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s, "UPDATE buckets SET created='invalid'")
	if _, err := s.Buckets(); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Bucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	execSQL(t, s, "UPDATE buckets SET created='2026-11-03T00:00:00Z'")
	execSQL(t, s, "UPDATE objects SET modified='invalid'")
	if _, err := s.Bucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Get("bucket", "key"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
}

func TestSchemaDamageFailsWithoutSuccessfulEmptyResults(t *testing.T) {
	s := testStore(t)
	if err := s.Create("bucket", "region"); err != nil {
		t.Fatal(err)
	}
	execSQL(t, s, "DROP TABLE objects")
	if _, err := s.Bucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if err := s.DeleteBucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Put("bucket", "key", "text/plain", nil, "", ""); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Get("bucket", "key"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if err := s.Delete("bucket", "key"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	execSQL(t, s, "DROP TABLE buckets")
	if err := s.Create("bucket", "region"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Bucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if err := s.DeleteBucket("bucket"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Put("bucket", "key", "text/plain", nil, "", ""); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if _, err := s.Get("bucket", "key"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
	if err := s.Delete("bucket", "key"); Code(err) != "InternalError" {
		t.Fatal(err)
	}
}

func TestUnversionedNonemptyDatabaseIsNotAdopted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s, "PRAGMA user_version=0")
	s.Close()
	if bad, err := New(path); err == nil {
		bad.Close()
		t.Fatal("adopted nonempty unversioned database")
	}
	if bad, err := New(filepath.Join(t.TempDir(), "absent", "state.db")); err == nil {
		bad.Close()
		t.Fatal("opened missing parent")
	}
}

func TestPrincipalQuotaAllowsUpdatesAtCapacity(t *testing.T) {
	s := testStore(t)
	ids := NewIdentities(s)
	p, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"read","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, s, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO identity_policies SELECT 'reader'||x, ? FROM n`, `{"Version":"2012-10-17","Statement":{"Effect":"Deny","Action":"read","Resource":"*"}}`)
	if err = ids.Set("new", p); err == nil {
		t.Fatal("exceeded quota")
	}
	if err = ids.Set("reader1", p); err != nil {
		t.Fatal(err)
	}
	if d, err := ids.Decision("reader1", "read", "any"); err != nil || d != "allowed" {
		t.Fatal(d, err)
	}
	if err = ids.Set(Administrator, p); err == nil {
		t.Fatal("overwrote administrator")
	}
	execSQL(t, s, "DROP TABLE identity_policies")
	if err = ids.Set("reader", p); err == nil {
		t.Fatal("missing table reported success")
	}
}

func TestBucketAndObjectCountLimits(t *testing.T) {
	s := testStore(t)
	execSQL(t, s, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO buckets SELECT 'bucket'||x,'region','2026-11-03T00:00:00Z' FROM n`)
	if err := s.Create("overflow", "region"); Code(err) != "ServiceUnavailable" {
		t.Fatal(err)
	}
	execSQL(t, s, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO objects SELECT 'bucket1','key'||x,'text/plain','etag','2026-11-03T00:00:00Z',X'' FROM n`)
	if _, err := s.Put("bucket1", "new", "text/plain", nil, "", ""); Code(err) != "ServiceUnavailable" {
		t.Fatal(err)
	}
	if _, err := s.Put("bucket1", "key1", "text/plain", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("missing", "key"); Code(err) != "NoSuchBucket" {
		t.Fatal(err)
	}
	if err := s.Delete("missing", "key"); Code(err) != "NoSuchBucket" {
		t.Fatal(err)
	}
	if err := s.DeleteBucket("missing"); Code(err) != "NoSuchBucket" {
		t.Fatal(err)
	}
}
