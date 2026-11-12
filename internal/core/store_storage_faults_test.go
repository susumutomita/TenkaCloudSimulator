package core

import (
	"context"
	"database/sql"
	"testing"
)

// faultStore is a Store over a fault-injecting database carrying the exact
// simulator schema produced by New.
func faultStore(t *testing.T, db *sql.DB) *Store {
	t.Helper()
	seed, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	rows, err := seed.db.Query(`SELECT sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var ddl []string
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		ddl = append(ddl, s)
	}
	rows.Close()
	for _, s := range append(ddl, "PRAGMA user_version=1") {
		if _, err = db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return &Store{db: db}
}

// Every bucket and object operation reports a storage failure as the
// simulator's InternalError rather than a success or a client error.
func TestStoreReportsEveryStorageFault(t *testing.T) {
	sweepFaultsMode(t, faultReported, func(t *testing.T, db *sql.DB) func(context.Context) error {
		s := faultStore(t, db)
		return func(ctx context.Context) error {
			check := func(err error) error {
				if err != nil && Code(err) != "InternalError" {
					t.Fatalf("storage fault reported as %s: %v", Code(err), err)
				}
				return err
			}
			if err := check(s.Create("bucket", "us-east-1")); err != nil {
				return err
			}
			first, err := s.Put("bucket", "a.txt", "text/plain", []byte("one"), "", "*")
			if err = check(err); err != nil {
				return err
			}
			if _, err = s.Put("bucket", "a.txt", "text/plain", []byte("two"), first.ETag, ""); check(err) != nil {
				return err
			}
			if got, err := s.Get("bucket", "a.txt"); check(err) != nil {
				return err
			} else if string(got.Data) != "two" {
				t.Fatalf("object %q", got.Data)
			}
			if b, err := s.BucketContext(ctx, "bucket"); check(err) != nil {
				return err
			} else if len(b.Objects) != 1 {
				t.Fatalf("bucket objects %v", b.Objects)
			}
			if list, err := s.Buckets(); check(err) != nil {
				return err
			} else if len(list) != 1 {
				t.Fatalf("buckets %v", list)
			}
			if err = check(s.Delete("bucket", "a.txt")); err != nil {
				return err
			}
			return check(s.DeleteBucket("bucket"))
		}
	})
}

func TestStoreForeignKeyCheckFindsOrphans(t *testing.T) {
	s, err := New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`PRAGMA foreign_keys=OFF; INSERT INTO objects(bucket,key,content_type,etag,modified,data) VALUES('ghost','k','','""','2026-01-01T00:00:00Z',x''); PRAGMA foreign_keys=ON;`); err != nil {
		t.Fatal(err)
	}
	if err = s.ForeignKeyCheck(); err == nil {
		t.Fatal("orphan object passed the foreign key check")
	}
}
