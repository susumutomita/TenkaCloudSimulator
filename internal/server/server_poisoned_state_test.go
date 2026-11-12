package server

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// A state file in which a simulator-owned table name is occupied by a foreign
// object (here a view) must not be silently adopted: composition either
// refuses the file, releasing everything it opened, or never touched that name
// during startup. Every refusal must leave the file reopenable once repaired.
func TestCompositionRefusesStateWithForeignObjectsInPlaceOfTables(t *testing.T) {
	base := filepath.Join(t.TempDir(), "reference.db")
	_, closeAll, err := NewWithProviders(base, DefaultProviderConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err = closeAll(); err != nil {
		t.Fatal(err)
	}
	reference, err := sql.Open("sqlite3", "file:"+base)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := reference.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('buckets','objects','identity_policies') ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	rows.Close()
	reference.Close()
	if len(names) < 40 {
		t.Fatalf("reference composition created only %d tables", len(names))
	}
	refused := 0
	for _, name := range names {
		path := filepath.Join(t.TempDir(), "poisoned.db")
		state, err := core.New(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = state.Database().Exec(`CREATE VIEW "` + name + `" AS SELECT 1 AS foreign_object`); err != nil {
			t.Fatal(err)
		}
		if err = state.Close(); err != nil {
			t.Fatal(err)
		}
		h, closeState, err := NewWithProviders(path, DefaultProviderConfig())
		if err == nil {
			if err = closeState(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		refused++
		if h != nil || closeState != nil {
			t.Fatalf("refused composition for %s still returned a handler", name)
		}
		// The refusal released the database: it can be repaired and composed.
		repair, err := sql.Open("sqlite3", "file:"+path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repair.Exec(`DROP VIEW "` + name + `"`); err != nil {
			t.Fatalf("refused state for %s is still held open: %v", name, err)
		}
		repair.Close()
		_, closeState, err = NewWithProviders(path, DefaultProviderConfig())
		if err != nil {
			t.Fatalf("repaired state for %s refused: %v", name, err)
		}
		if err = closeState(); err != nil {
			t.Fatal(err)
		}
	}
	if refused < len(names)/3 {
		t.Fatalf("only %d of %d foreign objects were refused at startup", refused, len(names))
	}
}
