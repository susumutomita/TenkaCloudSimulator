// Test-only copy of the storage fault driver used by the aws package sweeps:
// it fails exactly one chosen BEGIN, statement preparation, direct execution,
// or COMMIT so every storage error path can be shown to surface.
package appengine

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// errInjectedStorage is returned at the armed operation.
var errInjectedStorage = errors.New("injected storage fault")

type faultCounter struct {
	seen  atomic.Int64
	armed atomic.Int64
}

func (c *faultCounter) step() error {
	if c.armed.Load() == 0 {
		return nil
	}
	if c.seen.Add(1) == c.armed.Load() {
		return errInjectedStorage
	}
	return nil
}

type faultDriver struct{ base driver.Driver }
type faultConn struct {
	base   driver.Conn
	faults *faultCounter
}
type faultTx struct {
	base   driver.Tx
	faults *faultCounter
}

var faultCounters sync.Map // DSN -> *faultCounter
var registerFaultDriver sync.Once

const faultDriverName = "sqlite3-appengine-faults"

func (d faultDriver) Open(name string) (driver.Conn, error) {
	c, ok := faultCounters.Load(name)
	if !ok {
		return nil, errors.New("unregistered fault DSN")
	}
	base, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &faultConn{base: base, faults: c.(*faultCounter)}, nil
}
func (c *faultConn) Prepare(q string) (driver.Stmt, error) {
	return c.PrepareContext(context.Background(), q)
}
func (c *faultConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	if err := c.faults.step(); err != nil {
		return nil, err
	}
	return c.base.(driver.ConnPrepareContext).PrepareContext(ctx, q)
}
func (c *faultConn) Close() error { return c.base.Close() }
func (c *faultConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *faultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := c.faults.step(); err != nil {
		return nil, err
	}
	t, err := c.base.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &faultTx{base: t, faults: c.faults}, nil
}
func (c *faultConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if len(args) != 0 {
		return nil, driver.ErrSkip // database/sql prepares instead; counted there
	}
	if err := c.faults.step(); err != nil {
		return nil, err
	}
	return c.base.(driver.ExecerContext).ExecContext(ctx, q, args)
}
func (c *faultConn) CheckNamedValue(v *driver.NamedValue) error {
	return c.base.(driver.NamedValueChecker).CheckNamedValue(v)
}
func (t *faultTx) Commit() error {
	if err := t.faults.step(); err != nil {
		_ = t.base.Rollback()
		return err
	}
	return t.base.Commit()
}
func (t *faultTx) Rollback() error { return t.base.Rollback() }

// faultDB is a real file-backed SQLite database whose driver can fail one chosen
// operation. It is configured like the simulator's own state connection.
type faultDB struct {
	*sql.DB
	faults *faultCounter
}

// newFaultDB opens a fresh database file in a test temporary directory.
func newFaultDB(t testing.TB) *faultDB {
	t.Helper()
	registerFaultDriver.Do(func() {
		probe, err := sql.Open("sqlite3", "file::memory:")
		if err != nil {
			panic(err)
		}
		sql.Register(faultDriverName, faultDriver{base: probe.Driver()})
		_ = probe.Close()
	})
	path := filepath.Join(t.TempDir(), "state.db")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_txlock=immediate"}).String()
	c := &faultCounter{}
	faultCounters.Store(dsn, c)
	t.Cleanup(func() { faultCounters.Delete(dsn) })
	db, err := sql.Open(faultDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err = db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		t.Fatal(err)
	}
	return &faultDB{DB: db, faults: c}
}

// arm fails the at-th operation from now on; disarm stops injecting.
func (d *faultDB) arm(at int64) { d.faults.seen.Store(0); d.faults.armed.Store(at) }
func (d *faultDB) disarm()      { d.faults.armed.Store(0) }

// dump renders every user table in a stable order so a failed operation can be
// proven to have left no partial write behind.
func (d *faultDB) dump(t testing.TB) string {
	t.Helper()
	d.disarm()
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	_ = rows.Close()
	var out strings.Builder
	for _, table := range tables {
		r, err := d.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
		if err != nil {
			r, err = d.Query(`SELECT * FROM "` + table + `"`)
		}
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := r.Columns()
		for r.Next() {
			values := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = r.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&out, "%s %v\n", table, values)
		}
		_ = r.Close()
	}
	return out.String()
}

// Mode selects what a failed run must preserve.
type faultMode int

const (
	// faultReported only requires the injected failure to surface as an error.
	faultReported faultMode = iota
	// faultAtomic also requires every table to be unchanged after a failed run.
	faultAtomic
	// faultRecover requires a fault-free retry of the same operation to succeed.
	faultRecover
)

// sweepFaultsMode counts the driver operations of a fault-free run of the operation
// returned by setup, then reruns it on a fresh database once per operation,
// failing a different one each time. A run may absorb a best-effort fault, but
// at least one run must fail. It returns the number of failed runs.
func sweepFaultsMode(t *testing.T, mode faultMode, setup func(t *testing.T, db *sql.DB) func(context.Context) error) int {
	t.Helper()
	clean := newFaultDB(t)
	op := setup(t, clean.DB)
	clean.arm(1 << 62)
	if err := op(context.Background()); err != nil {
		t.Fatal("fault-free run failed:", err)
	}
	total := clean.faults.seen.Load()
	clean.disarm()
	if total == 0 {
		t.Fatal("operation performed no storage work")
	}
	failed := 0
	for at := int64(1); at <= total; at++ {
		db := newFaultDB(t)
		op := setup(t, db.DB)
		before := db.dump(t)
		db.arm(at)
		err := op(context.Background())
		db.disarm()
		if err == nil {
			continue
		}
		failed++
		if mode == faultAtomic {
			if after := db.dump(t); after != before {
				t.Fatalf("fault at operation %d/%d (%v) left a partial write:\nbefore:\n%s\nafter:\n%s", at, total, err, before, after)
			}
		}
		if mode == faultRecover {
			if err := op(context.Background()); err != nil {
				t.Fatalf("retry after fault at operation %d/%d did not recover: %v", at, total, err)
			}
		}
	}
	if failed == 0 {
		t.Fatal("no injected storage fault was reported")
	}
	return failed
}
