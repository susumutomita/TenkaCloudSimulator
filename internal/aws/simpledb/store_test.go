package simpledb

import (
	"context"
	"database/sql"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, e := sql.Open("sqlite3", filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := db.Close(); e != nil {
			t.Error(e)
		}
	})
	s, e := NewStore(db)
	if e != nil {
		t.Fatal(e)
	}
	return s, db
}
func TestCallerOwnedDBAndNil(t *testing.T) {
	if _, e := NewStore(nil); e == nil {
		t.Fatal("nil accepted")
	}
	s, db := fixture(t)
	if e := s.CreateDomain(context.Background(), "a", "r", "domain"); e != nil {
		t.Fatal(e)
	}
	if _, e := NewStore(db); e != nil {
		t.Fatal(e)
	}
	if e := db.Ping(); e != nil {
		t.Fatal(e)
	}
}
func TestIgnoredPutAndDeleteRollback(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	if e := s.CreateDomain(ctx, "a", "r", "domain"); e != nil {
		t.Fatal(e)
	}
	if e := s.Put(ctx, "a", "r", "domain", "item", map[string][]string{"x": {"old"}}, nil); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`CREATE TRIGGER ignore_update BEFORE UPDATE ON aws_simpledb_items BEGIN SELECT RAISE(IGNORE); END`); e != nil {
		t.Fatal(e)
	}
	if e := s.Put(ctx, "a", "r", "domain", "item", map[string][]string{"x": {"new"}}, map[string]bool{"x": true}); e == nil {
		t.Fatal("ignored update succeeded")
	}
	got, e := s.Get(ctx, "a", "r", "domain", "item", nil)
	if e != nil || len(got["x"]) != 1 || got["x"][0] != "old" {
		t.Fatalf("state changed %v %v", got, e)
	}
	if _, e := db.Exec(`CREATE TRIGGER ignore_domain_delete BEFORE DELETE ON aws_simpledb_domains BEGIN SELECT RAISE(IGNORE); END`); e != nil {
		t.Fatal(e)
	}
	if e := s.DeleteDomain(ctx, "a", "r", "domain"); e == nil {
		t.Fatal("ignored delete succeeded")
	}
	got, e = s.Get(ctx, "a", "r", "domain", "item", nil)
	if e != nil || len(got["x"]) != 1 || got["x"][0] != "old" {
		t.Fatalf("delete not rolled back %v %v", got, e)
	}
}
func TestTokenScopeAndCancellation(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	for _, d := range []string{"aaa", "bbb"} {
		if e := s.CreateDomain(ctx, "a", "r", d); e != nil {
			t.Fatal(e)
		}
	}
	_, token, e := s.ListDomains(ctx, "a", "r", 1, "")
	if e != nil || token == "" {
		t.Fatal(e)
	}
	if _, _, e = s.ListDomains(ctx, "b", "r", 1, token); e != storeError("InvalidNextToken") {
		t.Fatal(e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e = s.DeleteDomain(cancelled, "a", "r", "aaa"); e != storeError("Cancelled") {
		t.Fatal(e)
	}
	names, _, e := s.ListDomains(ctx, "a", "r", 100, "")
	if e != nil || len(names) != 2 {
		t.Fatal(names, e)
	}
}
