package iaas

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

var testConfig = Config{"practice", "project-a", "is1a", "learner"}

func fixture(t *testing.T) (*DiskStore, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	base, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { base.Close() })
	s, e := NewDiskStore(base.Database(), testConfig)
	if e != nil {
		t.Fatal(e)
	}
	return s, base.Database(), path
}
func input(name string) CreateDiskInput {
	return CreateDiskInput{name, "local metadata", 20480, 4, "virtio"}
}
func mustCreate(t *testing.T, s *DiskStore, name string) Disk {
	t.Helper()
	d, e := s.Create(context.Background(), input(name))
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func frame(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out []any
	for _, q := range []string{"SELECT * FROM sakura_disk_scope ORDER BY singleton", "SELECT * FROM sakura_disks ORDER BY id"} {
		rows, e := db.Query(q)
		if e != nil {
			t.Fatal(e)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			v := make([]any, len(cols))
			p := make([]any, len(cols))
			for i := range v {
				p[i] = &v[i]
			}
			if e = rows.Scan(p...); e != nil {
				t.Fatal(e)
			}
			out = append(out, v)
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		rows.Close()
	}
	b, _ := json.Marshal(out)
	return string(b)
}
func exec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, e := db.Exec(q); e != nil {
		t.Fatal(e)
	}
}
func TestDiskLifecyclePersistenceAndPeer(t *testing.T) {
	ctx := context.Background()
	s, db, path := fixture(t)
	peer, pdb, _ := fixture(t)
	pd := mustCreate(t, peer, "peer")
	pf := frame(t, pdb)
	a := mustCreate(t, s, "同名")
	b := mustCreate(t, s, "同名")
	if a.ID == b.ID || a.Generation == b.Generation {
		t.Fatal("identities reused")
	}
	p, e := s.ListFromCount(ctx, 1, 1)
	if e != nil || p.Total != 2 || len(p.Disks) != 1 || p.Disks[0] != b {
		t.Fatalf("page %#v %v", p, e)
	}
	empty := ""
	name := "new"
	u, e := s.UpdateNameDesc(ctx, a.ID, DiskPatch{&name, &empty}, a.Generation)
	if e != nil || u.Description != "" || u.Name != "new" {
		t.Fatal(u, e)
	}
	before := frame(t, db)
	if _, e = s.UpdateNameDesc(ctx, a.ID, DiskPatch{&name, nil}, "wrong"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if frame(t, db) != before {
		t.Fatal("stale mutation")
	}
	if e = s.Delete(ctx, a.ID, a.Generation); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, a.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	reopened, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	again, e := NewDiskStore(reopened.Database(), testConfig)
	if e != nil {
		t.Fatal(e)
	}
	c := mustCreate(t, again, "after restart")
	if c.ID <= b.ID {
		t.Fatal("issued id reused")
	}
	if _, e = peer.Get(ctx, pd.ID); e != nil || frame(t, pdb) != pf {
		t.Fatal("peer affected", e)
	}
	for _, cfg := range []Config{{"other", testConfig.Project, testConfig.Zone, testConfig.Owner}, {testConfig.Environment, "other", testConfig.Zone, testConfig.Owner}, {testConfig.Environment, testConfig.Project, "other", testConfig.Owner}, {testConfig.Environment, testConfig.Project, testConfig.Zone, "other"}} {
		f := frame(t, db)
		if _, e = NewDiskStore(db, cfg); !errors.Is(e, ErrScope) {
			t.Fatal(e)
		}
		if frame(t, db) != f {
			t.Fatal("scope adopted")
		}
	}
}
func TestDiskIgnoredWritesRollbackAndRepair(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"insert", "allocator", "update", "delete"} {
		t.Run(kind, func(t *testing.T) {
			s, db, _ := fixture(t)
			d := mustCreate(t, s, "existing")
			before := frame(t, db)
			triggers := map[string]string{"insert": "BEFORE INSERT ON sakura_disks", "allocator": "BEFORE UPDATE ON sakura_disk_scope", "update": "BEFORE UPDATE ON sakura_disks", "delete": "BEFORE DELETE ON sakura_disks"}
			exec(t, db, "CREATE TRIGGER ignore_write "+triggers[kind]+" BEGIN SELECT RAISE(IGNORE); END")
			name := "changed"
			var e error
			switch kind {
			case "insert", "allocator":
				_, e = s.Create(ctx, input("next"))
			case "update":
				_, e = s.UpdateNameDesc(ctx, d.ID, DiskPatch{Name: &name})
			case "delete":
				e = s.Delete(ctx, d.ID)
			}
			if !errors.Is(e, ErrCorrupt) {
				t.Fatalf("false acknowledgement: %v", e)
			}
			if frame(t, db) != before {
				t.Fatal("partial commit")
			}
			exec(t, db, "DROP TRIGGER ignore_write")
			switch kind {
			case "insert", "allocator":
				n := mustCreate(t, s, "repair")
				if n.ID != d.ID+1 {
					t.Fatal("failed allocation committed")
				}
			case "update":
				if _, e = s.UpdateNameDesc(ctx, d.ID, DiskPatch{Name: &name}); e != nil {
					t.Fatal(e)
				}
			case "delete":
				if e = s.Delete(ctx, d.ID); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestDiskValidationQuotaAndCancellation(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	for _, bad := range []CreateDiskInput{input(""), input(strings.Repeat("字", 65)), input("x\x00"), input(string([]byte{255})), {"x", strings.Repeat("x", 513), 20480, 4, "virtio"}, {"x", "", 20481, 4, "virtio"}, {"x", "", 20480, 2, "virtio"}, {"x", "", 20480, 4, "ide"}} {
		f := frame(t, db)
		if _, e := s.Create(ctx, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if frame(t, db) != f {
			t.Fatal("invalid request changed state")
		}
	}
	in := input(strings.Repeat("字", 64))
	in.Description = strings.Repeat("字", 512)
	first, e := s.Create(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	f := frame(t, db)
	_, e = s.Create(canceled, input("cancel"))
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, e = s.Get(canceled, first.ID)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, e = s.ListFromCount(canceled, 0, 1)
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	_, e = s.UpdateNameDesc(canceled, first.ID, DiskPatch{})
	if !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if e = s.Delete(canceled, first.ID); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if frame(t, db) != f {
		t.Fatal("canceled write")
	}
	for i := 1; i < 100; i++ {
		mustCreate(t, s, "bounded")
	}
	f = frame(t, db)
	if _, e = s.Create(ctx, input("over quota")); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	if frame(t, db) != f {
		t.Fatal("quota changed state")
	}
	if e = s.Delete(ctx, first.ID); e != nil {
		t.Fatal(e)
	}
	last := mustCreate(t, s, "freed slot")
	if last.ID != 101 {
		t.Fatal(last.ID)
	}
}
func TestDiskCorruptionScopeAndUnboundRefusal(t *testing.T) {
	ctx := context.Background()
	s, db, _ := fixture(t)
	d := mustCreate(t, s, "disk")
	exec(t, db, "UPDATE sakura_disks SET modified='broken'")
	f := frame(t, db)
	if _, e := s.Get(ctx, d.ID); !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
	if _, e := s.ListFromCount(ctx, 0, 10); !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
	if e := s.Delete(ctx, d.ID); !errors.Is(e, ErrCorrupt) {
		t.Fatal(e)
	}
	if frame(t, db) != f {
		t.Fatal("corrupt data discarded")
	}
	if _, e := db.Exec("UPDATE sakura_disks SET modified=?", d.Modified.Format("2006-01-02T15:04:05.999999999Z07:00")); e != nil {
		t.Fatal(e)
	}
	exec(t, db, "UPDATE sakura_disk_scope SET owner='other'")
	f = frame(t, db)
	if _, e := s.Get(ctx, d.ID); !errors.Is(e, ErrScope) {
		t.Fatal(e)
	}
	if _, e := s.Create(ctx, input("no")); !errors.Is(e, ErrScope) {
		t.Fatal(e)
	}
	if frame(t, db) != f {
		t.Fatal("drift adopted")
	}
	exec(t, db, "DELETE FROM sakura_disk_scope")
	f = frame(t, db)
	if _, e := NewDiskStore(db, testConfig); !errors.Is(e, ErrScope) {
		t.Fatal(e)
	}
	if frame(t, db) != f {
		t.Fatal("unbound data adopted")
	}
	if _, e := NewDiskStore(nil, testConfig); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}

func TestDiskUnboundEmptyNamespaceAndClosedDatabase(t *testing.T) {
	s, db, _ := fixture(t)
	d := mustCreate(t, s, "issued")
	if e := s.Delete(context.Background(), d.ID); e != nil {
		t.Fatal(e)
	}
	exec(t, db, "DELETE FROM sakura_disk_scope")
	before := frame(t, db)
	if _, e := NewDiskStore(db, testConfig); !errors.Is(e, ErrScope) {
		t.Fatalf("empty namespace adopted: %v", e)
	}
	if frame(t, db) != before {
		t.Fatal("allocator was recreated")
	}
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := NewDiskStore(db, testConfig); e == nil {
		t.Fatal("closed DB accepted")
	}
	if _, e := s.Get(context.Background(), d.ID); e == nil {
		t.Fatal("closed DB read acknowledged")
	}
}

func TestDiskSelectorsAndNoopPreserveState(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	d := mustCreate(t, s, "disk")
	before := frame(t, db)
	if same, e := s.UpdateNameDesc(ctx, d.ID, DiskPatch{}, d.Generation); e != nil || same != d {
		t.Fatal(same, e)
	}
	for _, page := range []struct {
		from  int64
		count int
	}{{-1, 1}, {0, 0}, {0, 101}} {
		if _, e := s.ListFromCount(ctx, page.from, page.count); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, e := s.Get(ctx, 0); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e := s.Get(ctx, d.ID+1); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	name := "new"
	if _, e := s.UpdateNameDesc(ctx, d.ID, DiskPatch{Name: &name}, ""); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if e := s.Delete(ctx, d.ID, "old"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e := s.Delete(ctx, d.ID, d.Generation, d.Generation); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, e := s.Get(nil, d.ID); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if frame(t, db) != before {
		t.Fatal("selector/noop changed metadata")
	}
}

func TestDiskPartialNamespaceRefused(t *testing.T) {
	_, db, _ := fixture(t)
	exec(t, db, "DROP TABLE sakura_disks")
	var before int
	if e := db.QueryRow("SELECT next_id FROM sakura_disk_scope").Scan(&before); e != nil {
		t.Fatal(e)
	}
	if _, e := NewDiskStore(db, testConfig); !errors.Is(e, ErrScope) {
		t.Fatal(e)
	}
	var tables, after int
	if e := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sakura_disks'").Scan(&tables); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow("SELECT next_id FROM sakura_disk_scope").Scan(&after); e != nil {
		t.Fatal(e)
	}
	if tables != 0 || after != before {
		t.Fatal("partial namespace repaired by adoption")
	}
}
func TestDiskConcurrentLastSlot(t *testing.T) {
	s, db, path := fixture(t)
	for i := 0; i < 99; i++ {
		mustCreate(t, s, "retained")
	}
	before, e := s.ListFromCount(context.Background(), 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	other, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	second, e := NewDiskStore(other.Database(), testConfig)
	if e != nil {
		t.Fatal(e)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, store := range []*DiskStore{s, second} {
		go func(st *DiskStore) { <-start; _, err := st.Create(context.Background(), input("last")); results <- err }(store)
	}
	close(start)
	success, quota := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrQuota):
			quota++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || quota != 1 {
		t.Fatal(success, quota)
	}
	after, e := s.ListFromCount(context.Background(), 0, 100)
	if e != nil || after.Total != 100 {
		t.Fatal(after.Total, e)
	}
	for i, d := range before.Disks {
		if after.Disks[i] != d {
			t.Fatal("existing disk mutated")
		}
	}
	var next int64
	if e = db.QueryRow("SELECT next_id FROM sakura_disk_scope").Scan(&next); e != nil || next != 101 {
		t.Fatal(next, e)
	}
}

func TestDiskScopeSchemaCounterAndForeignKeyRefusals(t *testing.T) {
	s, db, _ := fixture(t)
	d := mustCreate(t, s, "retained")
	before := frame(t, db)
	for _, cfg := range []Config{{}, {" practice", testConfig.Project, testConfig.Zone, testConfig.Owner}, {testConfig.Environment, testConfig.Project, testConfig.Zone, "bad\nowner"}} {
		if _, e := NewDiskStore(db, cfg); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
		if frame(t, db) != before {
			t.Fatal("invalid constructor adopted state")
		}
	}
	exec(t, db, "PRAGMA foreign_keys=OFF")
	if _, e := NewDiskStore(db, testConfig); !errors.Is(e, ErrScope) {
		t.Fatal(e)
	}
	exec(t, db, "PRAGMA foreign_keys=ON")
	if frame(t, db) != before {
		t.Fatal("foreign key refusal changed state")
	}
	// Missing binding columns are infrastructure faults, not caller denial or adoption.
	exec(t, db, "ALTER TABLE sakura_disk_scope RENAME COLUMN owner TO missing_owner")
	if _, e := NewDiskStore(db, testConfig); e == nil || errors.Is(e, ErrNotFound) || errors.Is(e, ErrScope) {
		t.Fatal("schema fault misclassified", e)
	}
	if _, e := s.Get(context.Background(), d.ID); e == nil {
		t.Fatal("missing binding schema accepted")
	}
	exec(t, db, "ALTER TABLE sakura_disk_scope RENAME COLUMN missing_owner TO owner")
	if frame(t, db) != before {
		t.Fatal("binding fault changed retained state")
	}
	exec(t, db, "UPDATE sakura_disk_scope SET version=2")
	bad := frame(t, db)
	if _, e := NewDiskStore(db, testConfig); !errors.Is(e, ErrScope) {
		t.Fatal(e)
	}
	if frame(t, db) != bad {
		t.Fatal("unknown version silently migrated")
	}
	exec(t, db, "UPDATE sakura_disk_scope SET version=1")
	for _, next := range []int64{0, d.ID} {
		if _, e := db.Exec("UPDATE sakura_disk_scope SET next_id=?", next); e != nil {
			t.Fatal(e)
		}
		bad = frame(t, db)
		if _, e := s.Create(context.Background(), input("forbidden")); !errors.Is(e, ErrCorrupt) {
			t.Fatal(e)
		}
		if _, e := NewDiskStore(db, testConfig); !errors.Is(e, ErrCorrupt) {
			t.Fatal(e)
		}
		if frame(t, db) != bad {
			t.Fatal("allocator corruption repaired by adoption")
		}
	}
	if _, e := db.Exec("UPDATE sakura_disk_scope SET next_id=?", int64(math.MaxInt64)); e != nil {
		t.Fatal(e)
	}
	bad = frame(t, db)
	if _, e := s.Create(context.Background(), input("overflow")); !errors.Is(e, ErrQuota) {
		t.Fatal(e)
	}
	if frame(t, db) != bad {
		t.Fatal("exhausted ID counter changed")
	}
	exec(t, db, "UPDATE sakura_disk_scope SET next_id=2")
	exec(t, db, "ALTER TABLE sakura_disks RENAME COLUMN id TO absent_id")
	if _, e := s.Get(context.Background(), d.ID); e == nil || errors.Is(e, ErrNotFound) {
		t.Fatal("allocator read fault masked", e)
	}
	exec(t, db, "ALTER TABLE sakura_disks RENAME COLUMN absent_id TO id")
	if frame(t, db) != before {
		t.Fatal("failed allocator read changed frame")
	}
	if _, e := NewDiskStore(db, testConfig); e != nil {
		t.Fatal(e)
	}
	if got, e := s.Get(context.Background(), d.ID); e != nil || got != d {
		t.Fatal("repair did not recover exact saved disk", e)
	}
}
func TestDiskPersistedProjectionFailuresRollbackAndRepair(t *testing.T) {
	// These are explicit SQLite integrity-fault fixtures, not claims that remote
	// callers can create triggers or that arbitrary host DB tampering is contained.
	for _, kind := range []string{"create-rewrite", "create-corrupt-time", "create-scope-drift", "allocator-rewrite", "update-rewrite", "update-corrupt-time", "delete-reinsert"} {
		t.Run(kind, func(t *testing.T) {
			s, db, _ := fixture(t)
			d := mustCreate(t, s, "retained")
			before := frame(t, db)
			trigger := map[string]string{
				"create-rewrite":      `AFTER INSERT ON sakura_disks BEGIN UPDATE sakura_disks SET description='unexpected' WHERE id=NEW.id; END`,
				"create-corrupt-time": `AFTER INSERT ON sakura_disks BEGIN UPDATE sakura_disks SET created='bad-time' WHERE id=NEW.id; END`,
				"create-scope-drift":  `AFTER INSERT ON sakura_disks BEGIN UPDATE sakura_disk_scope SET owner='unexpected'; END`,
				"allocator-rewrite":   `AFTER UPDATE ON sakura_disk_scope BEGIN UPDATE sakura_disk_scope SET next_id=NEW.next_id+1; END`,
				"update-rewrite":      `AFTER UPDATE ON sakura_disks BEGIN UPDATE sakura_disks SET description='unexpected' WHERE id=NEW.id; END`,
				"update-corrupt-time": `AFTER UPDATE ON sakura_disks BEGIN UPDATE sakura_disks SET modified='bad-time' WHERE id=NEW.id; END`,
				"delete-reinsert":     `AFTER DELETE ON sakura_disks BEGIN INSERT INTO sakura_disks VALUES(OLD.id,OLD.generation,OLD.name,OLD.description,OLD.size_mb,OLD.plan_id,OLD.connection,OLD.created,OLD.modified); END`,
			}[kind]
			exec(t, db, "CREATE TRIGGER projection_fault "+trigger)
			var e error
			name := "revised"
			switch {
			case strings.HasPrefix(kind, "update"):
				_, e = s.UpdateNameDesc(context.Background(), d.ID, DiskPatch{Name: &name})
			case kind == "delete-reinsert":
				e = s.Delete(context.Background(), d.ID)
			default:
				_, e = s.Create(context.Background(), input("new"))
			}
			if !errors.Is(e, ErrCorrupt) && !(kind == "create-scope-drift" && errors.Is(e, ErrScope)) {
				t.Fatal("inconsistent saved projection acknowledged", e)
			}
			if frame(t, db) != before {
				t.Fatal("post-write failure partially committed resource or allocator")
			}
			exec(t, db, "DROP TRIGGER projection_fault")
			switch {
			case strings.HasPrefix(kind, "update"):
				if _, e = s.UpdateNameDesc(context.Background(), d.ID, DiskPatch{Name: &name}); e != nil {
					t.Fatal(e)
				}
			case kind == "delete-reinsert":
				if e = s.Delete(context.Background(), d.ID); e != nil {
					t.Fatal(e)
				}
			default:
				newDisk := mustCreate(t, s, "repaired")
				if newDisk.ID != d.ID+1 {
					t.Fatal("failed transaction issued an ID")
				}
			}
		})
	}
}
func TestDiskSQLProjectionReadFailuresAndMonotonicModified(t *testing.T) {
	s, db, _ := fixture(t)
	d := mustCreate(t, s, "retained")
	before := frame(t, db)
	exec(t, db, "ALTER TABLE sakura_disks RENAME COLUMN description TO missing_description")
	// A schema failure during Create must leave disk data and the allocator intact;
	// failed list/read operations must not produce empty successes. The separate
	// trigger fixtures above verify post-write projection rollback explicitly.
	for _, operation := range []func() error{
		func() error { _, e := s.Create(context.Background(), input("must rollback")); return e },
		func() error { _, e := s.ListFromCount(context.Background(), 0, 100); return e },
		func() error { _, e := s.Get(context.Background(), d.ID); return e },
		func() error {
			v := "changed"
			_, e := s.UpdateNameDesc(context.Background(), d.ID, DiskPatch{Name: &v})
			return e
		},
		func() error { return s.Delete(context.Background(), d.ID) },
	} {
		if e := operation(); e == nil || errors.Is(e, ErrNotFound) {
			t.Fatal("SQL schema failure masked", e)
		}
		if frame(t, db) != before {
			t.Fatal("schema read failure changed data/allocator")
		}
	}
	exec(t, db, "ALTER TABLE sakura_disks RENAME COLUMN missing_description TO description")
	if got, e := s.Get(context.Background(), d.ID); e != nil || got != d {
		t.Fatal(e)
	}
	future := d.Modified.Add(24 * time.Hour)
	if _, e := db.Exec("UPDATE sakura_disks SET modified=? WHERE id=?", future.Format(time.RFC3339Nano), d.ID); e != nil {
		t.Fatal(e)
	}
	name := "revised"
	updated, e := s.UpdateNameDesc(context.Background(), d.ID, DiskPatch{Name: &name})
	if e != nil || !updated.Modified.Equal(future) || updated.Name != name {
		t.Fatal("modified time regressed", updated, e)
	}
}
