package aws

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

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// errInjectedStorage is returned by the fault driver at the armed operation.
var errInjectedStorage = errors.New("injected storage fault")

// faultCounter fails exactly the armed driver operation (BEGIN, statement
// preparation, direct execution, or COMMIT). Zero disarms it.
type faultCounter struct {
	seen  atomic.Int64
	armed atomic.Int64
}

func (f *faultCounter) step() error {
	if f.armed.Load() == 0 {
		return nil
	}
	if f.seen.Add(1) == f.armed.Load() {
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

var faultDrivers sync.Map // DSN -> *faultCounter

func (d faultDriver) Open(name string) (driver.Conn, error) {
	counter, ok := faultDrivers.Load(name)
	if !ok {
		return nil, errors.New("unregistered fault DSN")
	}
	c, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &faultConn{base: c, faults: counter.(*faultCounter)}, nil
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
	tx, err := c.base.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &faultTx{base: tx, faults: c.faults}, nil
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

var registerFaultDriver sync.Once

// faultDB is a real file-backed simulator database whose driver can fail one
// chosen operation.
type faultDB struct {
	*sql.DB
	faults *faultCounter
}

func newFaultDB(t *testing.T) *faultDB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	seed, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = seed.Close(); err != nil {
		t.Fatal(err)
	}
	registerFaultDriver.Do(func() {
		probe, err := sql.Open("sqlite3", "file::memory:")
		if err != nil {
			panic(err)
		}
		sql.Register("sqlite3-storage-faults", faultDriver{base: probe.Driver()})
		_ = probe.Close()
	})
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_txlock=immediate"}).String()
	counter := &faultCounter{}
	faultDrivers.Store(dsn, counter)
	t.Cleanup(func() { faultDrivers.Delete(dsn) })
	db, err := sql.Open("sqlite3-storage-faults", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		t.Fatal(err)
	}
	return &faultDB{DB: db, faults: counter}
}

func (f *faultDB) arm(at int64) { f.faults.seen.Store(0); f.faults.armed.Store(at) }
func (f *faultDB) disarm()      { f.faults.armed.Store(0) }

// dump renders every user table in a stable order so a failed operation can be
// proven to have left no partial write behind.
func (f *faultDB) dump(t *testing.T) string {
	t.Helper()
	f.disarm()
	rows, err := f.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
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
	rows.Close()
	var out strings.Builder
	for _, table := range tables {
		r, err := f.Query(`SELECT * FROM "` + table + `" ORDER BY rowid`)
		if err != nil {
			r, err = f.Query(`SELECT * FROM "` + table + `"`)
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
		r.Close()
	}
	return out.String()
}

// sweepStorageFaults counts the driver operations of a fault-free run of op,
// then reruns op once per operation on a fresh database prepared by setup,
// failing a different operation each time. A failed run must report the error
// and, when atomic, leave every table unchanged. It returns how many runs
// failed; at least one must.
func sweepStorageFaults(t *testing.T, atomic bool, setup func(t *testing.T, db *sql.DB) func(context.Context) error) int {
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
			continue // a best-effort step absorbed the fault
		}
		failed++
		if atomic {
			if after := db.dump(t); after != before {
				t.Fatalf("fault at operation %d/%d (%v) left a partial write:\nbefore:\n%s\nafter:\n%s", at, total, err, before, after)
			}
		}
	}
	if failed == 0 {
		t.Fatal("no injected storage fault was reported")
	}
	return failed
}
