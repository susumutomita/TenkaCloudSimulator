package core

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreConnectionTurnover(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"default", ""}, {"memory", ":memory:"}, {"file", filepath.Join(t.TempDir(), "state #1.db")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := New(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Error(err)
				}
			})
			db := store.Database()
			if err := store.Create("bucket", "region"); err != nil {
				t.Fatal(err)
			}
			// Providers also define their own tables on the shared database.
			if _, err := db.Exec("CREATE TABLE provider_sample (id INTEGER PRIMARY KEY, value TEXT); INSERT INTO provider_sample VALUES (1, 'committed')"); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec("UPDATE provider_sample SET value='canceled'; INSERT INTO provider_sample VALUES (2, 'canceled')"); err != nil {
				t.Fatal(err)
			}
			// Let database/sql roll back the canceled transaction and release
			// its connection. With MaxOpenConns=1 the next query waits for it.
			cancel()
			readCtx, stop := context.WithTimeout(t.Context(), 10*time.Second)
			defer stop()
			var count int
			var value string
			if err := db.QueryRowContext(readCtx, "SELECT count(*),min(value) FROM provider_sample").Scan(&count, &value); err != nil || count != 1 || value != "committed" {
				t.Fatalf("after cancellation: count=%d value=%q err=%v", count, value, err)
			}

			// Explicitly retire a physical connection, even if a future driver
			// allows database/sql to reuse it following cancellation.
			conn, err := db.Conn(readCtx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
				t.Fatal(err)
			}

			var version, fk, timeout int
			if err := db.QueryRowContext(readCtx, "PRAGMA user_version").Scan(&version); err != nil || version != 1 {
				t.Fatalf("schema version after reconnect: %d, %v", version, err)
			}
			if err := db.QueryRowContext(readCtx, "PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("foreign keys after reconnect: %d, %v", fk, err)
			}
			if err := db.QueryRowContext(readCtx, "PRAGMA busy_timeout").Scan(&timeout); err != nil || timeout != 5000 {
				t.Fatalf("busy timeout after reconnect: %d, %v", timeout, err)
			}
			if _, err := db.ExecContext(readCtx, "INSERT INTO objects VALUES ('absent','k','type','etag','now',X'')"); err == nil {
				t.Fatal("replacement connection accepted an orphan object")
			}
			if _, err := store.BucketContext(readCtx, "bucket"); err != nil {
				t.Fatalf("committed core row disappeared: %v", err)
			}
			if err := db.QueryRowContext(readCtx, "SELECT value FROM provider_sample WHERE id=1").Scan(&value); err != nil || value != "committed" {
				t.Fatalf("provider row after reconnect: value=%q err=%v", value, err)
			}
		})
	}
}

func TestMemoryStoreOwnsItsBackingUntilClose(t *testing.T) {
	first := testStore(t)
	second := testStore(t)
	for region, store := range map[string]*Store{"first": first, "second": second} {
		if err := store.Create("same-bucket", region); err != nil {
			t.Fatal(err)
		}
	}
	// A read-only memdb open cannot create an absent backing database.
	// Make a fresh physical connection each time, so no test handle
	// extends its lifetime over Close.
	probe, err := sql.Open("sqlite3", "file:/"+first.memoryName+"?vfs=memdb&mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	probe.SetMaxIdleConns(0)
	var region string
	if err := probe.QueryRow("SELECT region FROM buckets WHERE name='same-bucket'").Scan(&region); err != nil || region != "first" {
		t.Fatalf("unable to read first Store's backing: %q, %v", region, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := probe.Ping(); err == nil {
		t.Fatal("closed memory Store's backing remained accessible")
	}
	bucket, err := second.Bucket("same-bucket")
	if err != nil || bucket.Region != "second" {
		t.Fatalf("closing first Store affected the second: %+v, %v", bucket, err)
	}
	next := testStore(t)
	if _, err := next.Bucket("same-bucket"); Code(err) != "NoSuchBucket" {
		t.Fatalf("fresh memory Store reused previous state: %v", err)
	}
}
