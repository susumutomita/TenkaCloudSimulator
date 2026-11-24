package dns

// Test-only transport wrapper around real SQLite. No provider table names or
// runtime implementation details are used to select failures. Every write
// statement boundary and COMMIT of one accepted change is tested on fresh DBs.
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

var factoryInjected = errors.New("Cloud DNS oracle injected SQL failure")

type factoryFault struct{ count, at atomic.Int64 }

func (f *factoryFault) step() error {
	if f.count.Add(1) == f.at.Load() {
		return factoryInjected
	}
	return nil
}

type factoryDriver struct{ base driver.Driver }
type factoryConn struct {
	base  driver.Conn
	fault *factoryFault
}
type factoryStmt struct {
	base  driver.Stmt
	fault *factoryFault
	write bool
}
type factoryTx struct {
	base  driver.Tx
	fault *factoryFault
}

var factoryDrivers sync.Once
var factoryFaults sync.Map

func (d factoryDriver) Open(name string) (driver.Conn, error) {
	b, e := d.base.Open(name)
	if e != nil {
		return nil, e
	}
	f, ok := factoryFaults.Load(name)
	if !ok {
		_ = b.Close()
		return nil, errors.New("unregistered factory database")
	}
	return &factoryConn{b, f.(*factoryFault)}, nil
}
func (c *factoryConn) Prepare(q string) (driver.Stmt, error) {
	s, e := c.base.Prepare(q)
	if e != nil {
		return nil, e
	}
	first := strings.ToUpper(strings.Fields(q)[0])
	return &factoryStmt{s, c.fault, first == "INSERT" || first == "UPDATE" || first == "DELETE" || first == "REPLACE"}, nil
}
func (c *factoryConn) Close() error { return c.base.Close() }
func (c *factoryConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *factoryConn) BeginTx(ctx context.Context, o driver.TxOptions) (driver.Tx, error) {
	tx, e := c.base.(driver.ConnBeginTx).BeginTx(ctx, o)
	if e != nil {
		return nil, e
	}
	return &factoryTx{tx, c.fault}, nil
}
func (c *factoryConn) CheckNamedValue(v *driver.NamedValue) error {
	return c.base.(driver.NamedValueChecker).CheckNamedValue(v)
}
func (s *factoryStmt) Close() error  { return s.base.Close() }
func (s *factoryStmt) NumInput() int { return s.base.NumInput() }
func (s *factoryStmt) Exec(a []driver.Value) (driver.Result, error) {
	if s.write {
		if e := s.fault.step(); e != nil {
			return nil, e
		}
	}
	return s.base.Exec(a)
}
func (s *factoryStmt) Query(a []driver.Value) (driver.Rows, error) {
	if s.write {
		if e := s.fault.step(); e != nil {
			return nil, e
		}
	}
	return s.base.Query(a)
}
func (tx *factoryTx) Commit() error {
	if e := tx.fault.step(); e != nil {
		_ = tx.base.Rollback()
		return e
	}
	return tx.base.Commit()
}
func (tx *factoryTx) Rollback() error { return tx.base.Rollback() }
func factoryFaultDB(t *testing.T) (*sql.DB, *factoryFault) {
	t.Helper()
	factoryDrivers.Do(func() {
		p, e := sql.Open("sqlite3", "file::memory:")
		if e != nil {
			panic(e)
		}
		sql.Register("sqlite3-dns-oracle", factoryDriver{p.Driver()})
		_ = p.Close()
	})
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "fault.db"), RawQuery: "_txlock=immediate"}).String()
	fault := &factoryFault{}
	factoryFaults.Store(dsn, fault)
	t.Cleanup(func() { factoryFaults.Delete(dsn) })
	db, e := sql.Open("sqlite3-dns-oracle", dsn)
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, e = db.Exec("PRAGMA foreign_keys=ON"); e != nil {
		t.Fatal(e)
	}
	return db, fault
}
func factoryRollback(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "additions-only"
		if replacement {
			name = "delete-and-replace"
		}
		t.Run(name, func(t *testing.T) {
			oldA, oldB := factoryA("first.example.test.", "192.0.2.1"), factoryA("second.example.test.", "192.0.2.2")
			add := []any{factoryA("third.example.test.", "192.0.2.3"), factoryA("fourth.example.test.", "192.0.2.4")}
			var del []any
			if replacement {
				del = []any{oldA, oldB}
				add = []any{factoryA("first.example.test.", "192.0.2.11"), factoryA("second.example.test.", "192.0.2.22")}
			}
			setup := func(t *testing.T) (factoryDNS, *factoryFault, string, factoryObservation, []any) {
				db, fault := factoryFaultDB(t)
				f := factoryEngine(t, db, "project-one")
				f.create("zone-one")
				base := f.root() + "/zone-one"
				initial := factoryItems(t, f.call("GET", base+"/rrsets", nil, 200), "rrsets")
				seed := f.call("POST", base+"/changes", factoryChange([]any{oldA, oldB}, nil), 200)
				before := factoryObservation{List: append(append([]any{}, initial...), oldA, oldB), Gets: []any{oldA, oldB}, Changes: []any{seed}}
				f.requireObservation(base, before)
				return f, fault, base, before, initial
			}
			// Count writes of this particular change; injection includes COMMIT and
			// uses fresh preseeded databases for every boundary, including replacement.
			f, fault, base, _, _ := setup(t)
			fault.count.Store(0)
			f.call("POST", base+"/changes", factoryChange(add, del), 200)
			total := fault.count.Load()
			if total < 2 {
				t.Fatal("oracle did not observe a write and COMMIT")
			}
			for at := int64(1); at <= total; at++ {
				t.Run(fmt.Sprintf("boundary-%d", at), func(t *testing.T) {
					f, fault, base, before, initial := setup(t)
					snapshot := f.snapshot("zone-one")
					fault.count.Store(0)
					fault.at.Store(at)
					f.call("POST", base+"/changes", factoryChange(add, del), 500)
					fault.at.Store(0)
					// Full history and both exact old RRsets must survive, not just counts.
					f.requireObservation(base, before)
					if f.snapshot("zone-one") != snapshot {
						t.Fatal("failed change altered metadata, records or receipts")
					}
					receipt := f.call("POST", base+"/changes", factoryChange(add, del), 200)
					factoryEqual(t, factoryRecordValues(factoryItems(t, receipt, "additions")), factoryRecordValues(add))
					factoryEqual(t, factoryRecordValues(factoryItems(t, receipt, "deletions")), factoryRecordValues(del))
					expected := append(append([]any{}, initial...), add...)
					gets := append([]any{}, add...)
					if !replacement {
						expected = append(expected, oldA, oldB)
						gets = append(gets, oldA, oldB)
					}
					f.requireObservation(base, factoryObservation{List: expected, Gets: gets, Changes: append(append([]any{}, before.Changes...), receipt)})
				})
			}
		})
	}
}
