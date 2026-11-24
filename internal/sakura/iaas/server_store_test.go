package iaas

import (
	"context"
	"database/sql"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"path/filepath"
	"testing"
)

func TestServerStoreReopenScopeAndDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	db, e := core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewServerStore(db.Database(), testConfig)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	row, e := s.Create(ctx, CreateServerInput{"server", "before", 2, 4096})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db, e = core.New(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e = NewServerStore(db.Database(), testConfig)
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.Get(ctx, row.ID)
	if e != nil || got != row {
		t.Fatal(got, e)
	}
	changed := testConfig
	changed.Environment = "other"
	if _, e = NewServerStore(db.Database(), changed); !errors.Is(e, ErrScope) {
		t.Fatal("adopted foreign scope", e)
	}
	if e = s.Delete(ctx, row.ID, "wrong-generation"); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.Delete(ctx, row.ID, row.Generation); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(ctx, row.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	next, e := s.Create(ctx, CreateServerInput{"server", "", 2, 4096})
	if e != nil || next.ID <= row.ID || next.Generation == row.Generation {
		t.Fatal(next, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e = s.Delete(cancelled, next.ID); e == nil {
		t.Fatal("cancelled delete succeeded")
	}
	if _, e = s.Get(ctx, next.ID); e != nil {
		t.Fatal(e)
	}
}
func TestServerStoreIgnoredWritesRollback(t *testing.T) {
	_, db, _ := fixture(t)
	s, e := NewServerStore(db, testConfig)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	row, e := s.Create(ctx, CreateServerInput{"kept", "desc", 2, 4096})
	if e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		name, sql string
		run       func() error
	}{
		{"create", "CREATE TRIGGER ignore_server BEFORE INSERT ON sakura_servers BEGIN SELECT RAISE(IGNORE); END", func() error { _, e := s.Create(ctx, CreateServerInput{"lost", "", 2, 4096}); return e }},
		{"allocator", "CREATE TRIGGER ignore_server BEFORE UPDATE ON sakura_server_scope BEGIN SELECT RAISE(IGNORE); END", func() error { _, e := s.Create(ctx, CreateServerInput{"lost", "", 2, 4096}); return e }},
		{"update", "CREATE TRIGGER ignore_server BEFORE UPDATE ON sakura_servers BEGIN SELECT RAISE(IGNORE); END", func() error {
			name := "lost"
			_, e := s.UpdateNameDesc(ctx, row.ID, ServerPatch{Name: &name})
			return e
		}},
		{"delete", "CREATE TRIGGER ignore_server BEFORE DELETE ON sakura_servers BEGIN SELECT RAISE(IGNORE); END", func() error { return s.Delete(ctx, row.ID) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := db.Exec(tc.sql); e != nil {
				t.Fatal(e)
			}
			if e := tc.run(); e == nil {
				t.Fatal("ignored write acknowledged")
			}
			if _, e := db.Exec("DROP TRIGGER ignore_server"); e != nil {
				t.Fatal(e)
			}
			got, e := s.Get(ctx, row.ID)
			if e != nil || got != row {
				t.Fatal("state changed", got, e)
			}
			page, e := s.ListFromCount(ctx, 0, 100)
			if e != nil || page.Total != 1 {
				t.Fatal(page, e)
			}
		})
	}
	if _, e := db.Exec("UPDATE sakura_server_scope SET owner='foreign'"); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Get(ctx, row.ID); !errors.Is(e, ErrScope) {
		t.Fatal("scope drift accepted", e)
	}
}
func TestServerStoreStorageFaults(t *testing.T) {
	sweepFaultsMode(t, faultRecover, func(t *testing.T, db *sql.DB) func(context.Context) error {
		return func(ctx context.Context) error {
			s, e := NewServerStore(db, testConfig)
			if e != nil {
				return e
			}
			row, e := s.Create(ctx, CreateServerInput{"server", "", 2, 4096})
			if e != nil {
				return e
			}
			if _, e = s.Get(ctx, row.ID); e != nil {
				return e
			}
			if _, e = s.ListFromCount(ctx, 0, 10); e != nil {
				return e
			}
			name := "updated"
			if _, e = s.UpdateNameDesc(ctx, row.ID, ServerPatch{Name: &name}); e != nil {
				return e
			}
			return s.Delete(ctx, row.ID)
		}
	})
}
