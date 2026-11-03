package core

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}
func TestRelationshipAndAtomicConditions(t *testing.T) {
	s := testStore(t)
	if err := s.Create("test", "region"); err != nil {
		t.Fatal(err)
	}
	var successful atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Put("test", "one", "text/plain", []byte("winner"), "", "*")
			if err == nil {
				successful.Add(1)
			} else if Code(err) != "PreconditionFailed" {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if successful.Load() != 1 {
		t.Fatalf("conditional writes succeeded %d times", successful.Load())
	}
	if err := s.DeleteBucket("test"); Code(err) != "BucketNotEmpty" {
		t.Fatalf("nonempty deletion: %v", err)
	}
	if err := s.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
	// The database itself also rejects an orphan even if a future adapter bypasses Store.
	if _, err := s.db.Exec("INSERT INTO objects VALUES('absent','key','text/plain','etag','now',X'')"); err == nil {
		t.Fatal("foreign key did not reject orphan")
	}
	o, err := s.Get("test", "one")
	if err != nil || string(o.Data) != "winner" {
		t.Fatalf("winner lost: %+v %v", o, err)
	}
	if _, err = s.Put("test", "one", "text/plain", []byte("changed"), `"wrong"`, ""); Code(err) != "PreconditionFailed" {
		t.Fatalf("failed overwrite accepted: %v", err)
	}
	if err = s.Delete("test", "one"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBucket("test"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get("test", "one"); Code(err) != "NoSuchBucket" {
		t.Fatal(err)
	}
}
func TestPersistenceIsolationAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Create("persistent", "region"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Put("persistent", "zero", "application/octet-stream", nil, "", ""); err != nil {
		t.Fatal(err)
	}
	ids := NewIdentities(s)
	p, err := ParsePolicy([]byte(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"read","Resource":"*"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = ids.Set("reader", p); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if o, e := s.Get("persistent", "zero"); e != nil || len(o.Data) != 0 {
		t.Fatalf("persisted zero object: %v %v", o, e)
	}
	if d, e := NewIdentities(s).Decision("reader", "read", "any"); e != nil || d != "allowed" {
		t.Fatalf("policy did not persist: %s %v", d, e)
	}
	isolated := testStore(t)
	if b, e := isolated.Buckets(); e != nil || len(b) != 0 {
		t.Fatalf("state leaked: %v %v", b, e)
	}
	if _, e := s.Put("persistent", "huge", "text/plain", make([]byte, MaxObjectBytes+1), "", ""); Code(e) != "EntityTooLarge" {
		t.Fatal(e)
	}
	data := make([]byte, MaxObjectBytes)
	for i := 0; i < 8; i++ {
		if _, e := s.Put("persistent", string(rune('a'+i)), "application/octet-stream", data, "", ""); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Put("persistent", "over-limit", "text/plain", []byte{1}, "", ""); Code(e) != "ServiceUnavailable" {
		t.Fatal(e)
	}
	if _, e := s.Put("persistent", "a", "text/plain", []byte{1}, "", ""); e != nil {
		t.Fatal(e)
	}
}
func TestClosedDatabaseFailsExplicitly(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err = s.Buckets(); err == nil {
		t.Fatal("closed DB became empty success")
	}
	if _, err = NewIdentities(s).Decision("reader", "read", "any"); err == nil {
		t.Fatal("closed DB policy became implicit deny")
	}
}
func TestSchemaVersionsAndIndependentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Create("shared", "region"); err != nil {
		t.Fatal(err)
	}
	other, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	var wg sync.WaitGroup
	for _, store := range []*Store{s, other} {
		wg.Add(1)
		go func(store *Store) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := store.Put("shared", "key", "text/plain", []byte("data"), "", ""); err != nil {
					t.Error(err)
				}
			}
		}(store)
	}
	wg.Wait()
	if err = s.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("PRAGMA user_version=99"); err != nil {
		t.Fatal(err)
	}
	if bad, err := New(path); err == nil {
		bad.Close()
		t.Fatal("accepted future schema")
	}
	if bad, err := New("file:untrusted.db?mode=ro"); err == nil {
		bad.Close()
		t.Fatal("accepted URI instead of state path")
	}
}
