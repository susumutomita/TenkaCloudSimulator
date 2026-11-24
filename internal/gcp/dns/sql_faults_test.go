package dns

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDNSIgnoredWritesCannotReportSuccess(t *testing.T) {
	cases := []struct {
		name, statement, method, path string
		body                          any
	}{
		{"record-insert", "BEFORE INSERT ON gcp_dns_rrsets", "POST", "/zone-one/changes", factoryChange([]any{factoryA("new.example.test.", "192.0.2.2")}, nil)},
		{"record-delete", "BEFORE DELETE ON gcp_dns_rrsets", "POST", "/zone-one/changes", factoryChange(nil, []any{factoryA("old.example.test.", "192.0.2.1")})},
		{"receipt-insert", "BEFORE INSERT ON gcp_dns_history", "POST", "/zone-one/changes", factoryChange([]any{factoryA("new.example.test.", "192.0.2.2")}, nil)},
		{"zone-insert", "BEFORE INSERT ON gcp_dns_zones", "POST", "", map[string]any{"name": "new-zone", "dnsName": "example.test.", "description": "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, db := leafFixture(t)
			f.create("zone-one")
			f.call("POST", f.root()+"/zone-one/changes", factoryChange([]any{factoryA("old.example.test.", "192.0.2.1")}, nil), 200)
			before := f.snapshot("zone-one")
			if _, err := db.Database().Exec("CREATE TRIGGER ignore_write " + tc.statement + " BEGIN SELECT RAISE(IGNORE); END"); err != nil {
				t.Fatal(err)
			}
			f.call(tc.method, f.root()+tc.path, tc.body, 500)
			if f.snapshot("zone-one") != before {
				t.Fatal("ignored SQL write left partial state or receipt")
			}
		})
	}
	t.Run("zone-delete", func(t *testing.T) {
		f, db := leafFixture(t)
		f.create("zone-one")
		before := f.snapshot("zone-one")
		if _, err := db.Database().Exec("CREATE TRIGGER ignore_delete BEFORE DELETE ON gcp_dns_zones BEGIN SELECT RAISE(IGNORE); END"); err != nil {
			t.Fatal(err)
		}
		f.call("DELETE", f.root()+"/zone-one", nil, 500)
		if f.snapshot("zone-one") != before {
			t.Fatal("ignored delete changed zone")
		}
	})
}
func TestDNSCorruptStateSurfacesAsError(t *testing.T) {
	cases := []struct{ sql, path string }{
		{"UPDATE gcp_dns_rrsets SET data='not-json'", "/zone-one/rrsets"},
		{"UPDATE gcp_dns_history SET document='not-json'", "/zone-one/changes"},
		{"UPDATE gcp_dns_history SET document='not-json'", "/zone-one/changes/1"},
		{"DROP TABLE gcp_dns_rrsets", "/zone-one/rrsets"},
		{"DROP TABLE gcp_dns_history", "/zone-one/changes"},
	}
	for _, tc := range cases {
		t.Run(tc.path+tc.sql, func(t *testing.T) {
			f, db := leafFixture(t)
			f.create("zone-one")
			f.call("POST", f.root()+"/zone-one/changes", factoryChange([]any{factoryA("old.example.test.", "192.0.2.1")}, nil), 200)
			if _, err := db.Database().Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			out := f.call("GET", f.root()+tc.path, nil, 500)
			if strings.Contains(factoryCanonical(out), "not-json") {
				t.Fatal("internal state leaked")
			}
		})
	}
}
func TestDNSInitializeFailuresRollbackAndBindingsStayBounded(t *testing.T) {
	cfg := Config{Project: "project-one", Owner: "owner", Subject: func(context.Context) (string, bool) { return "owner", true }}
	db, fault := factoryFaultDB(t)
	fault.count.Store(0)
	fault.at.Store(1)
	if _, err := New(db, cfg); err == nil {
		t.Fatal("injected initialization failure was hidden")
	}
	fault.at.Store(0)
	if _, err := New(db, cfg); err != nil {
		t.Fatal("initialization retry failed", err)
	}
	for i := 0; i < 63; i++ {
		cfg.Project = fmt.Sprintf("project-%d", i)
		if _, err := New(db, cfg); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Project = "one-too-many"
	if _, err := New(db, cfg); err == nil {
		t.Fatal("unbounded configured projects")
	}
	if _, err := db.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		t.Fatal(err)
	}
	cfg.Project = "project-one"
	if _, err := New(db, cfg); err == nil {
		t.Fatal("disabled foreign keys accepted")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db, cfg); err == nil {
		t.Fatal("closed DB accepted")
	}
}

func TestDNSInvalidPersistedAuthorityIsRefused(t *testing.T) {
	_, db := leafFixture(t)
	if _, err := db.Database().Exec("UPDATE gcp_dns_authority SET key=x'00'"); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db.Database(), Config{Project: "project-one", Owner: "owner", Subject: func(context.Context) (string, bool) { return "owner", true }}); err == nil {
		t.Fatal("corrupt token authority accepted")
	}
}

// AFTER triggers still report one affected row. These tests require the actual
// HTTP mutation to refuse final-state drift, restore the complete old API view,
// and permit a correct retry after removing the test-only trigger.
func TestDNSAfterTriggerDriftRollsBackWholeChange(t *testing.T) {
	old := factoryA("old.example.test.", "192.0.2.1")
	sentinel := factoryA("sentinel.example.test.", "192.0.2.9")
	added := factoryA("new.example.test.", "192.0.2.2")
	cases := []struct {
		name, trigger string
		add, del      []any
	}{
		{"delete-reinserts-old", `AFTER DELETE ON gcp_dns_rrsets BEGIN INSERT INTO gcp_dns_rrsets(zone,name,type,ttl,data) VALUES(OLD.zone,OLD.name,OLD.type,OLD.ttl,OLD.data); END`, nil, []any{old}},
		{"insert-rewrites-rdata", `AFTER INSERT ON gcp_dns_rrsets BEGIN UPDATE gcp_dns_rrsets SET data='["192.0.2.222"]' WHERE zone=NEW.zone AND name=NEW.name AND type=NEW.type; END`, []any{added}, nil},
		{"insert-rewrites-ttl", `AFTER INSERT ON gcp_dns_rrsets BEGIN UPDATE gcp_dns_rrsets SET ttl=1 WHERE zone=NEW.zone AND name=NEW.name AND type=NEW.type; END`, []any{added}, nil},
		{"receipt-disappears", `AFTER INSERT ON gcp_dns_history BEGIN DELETE FROM gcp_dns_history WHERE zone=NEW.zone AND seq=NEW.seq; END`, []any{added}, nil},
		{"receipt-rewrites-prior-history", `AFTER INSERT ON gcp_dns_history BEGIN UPDATE gcp_dns_history SET document='{}' WHERE zone=NEW.zone AND seq<>NEW.seq; END`, []any{added}, nil},
		{"receipt-moves-sequence", `AFTER INSERT ON gcp_dns_history BEGIN UPDATE gcp_dns_history SET seq=seq+1000 WHERE zone=NEW.zone AND seq=NEW.seq; END`, []any{added}, nil},
		{"receipt-rewrites-sentinel", `AFTER INSERT ON gcp_dns_history BEGIN UPDATE gcp_dns_rrsets SET ttl=1 WHERE zone=NEW.zone AND name='sentinel.example.test.'; END`, []any{added}, nil},
		{"record-insert-removes-prior-history", `AFTER INSERT ON gcp_dns_rrsets BEGIN DELETE FROM gcp_dns_history WHERE zone=NEW.zone; END`, []any{added}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, db := leafFixture(t)
			f.create("zone-one")
			base := f.root() + "/zone-one"
			initial := factoryItems(t, f.call("GET", base+"/rrsets", nil, 200), "rrsets")
			seed := f.call("POST", base+"/changes", factoryChange([]any{old, sentinel}, nil), 200)
			before := factoryObservation{List: append(append([]any{}, initial...), old, sentinel), Gets: []any{old, sentinel}, Changes: []any{seed}}
			f.requireObservation(base, before)
			snapshot := f.snapshot("zone-one")
			if _, err := db.Database().Exec("CREATE TRIGGER drift " + tc.trigger); err != nil {
				t.Fatal(err)
			}
			out := f.call("POST", base+"/changes", factoryChange(tc.add, tc.del), 500)
			if strings.Contains(factoryCanonical(out), "192.0.2.222") {
				t.Fatal("internal drift leaked in error")
			}
			f.requireObservation(base, before)
			factoryEqual(t, f.call("GET", base+"/changes/"+seed["id"].(string), nil, 200), seed)
			if f.snapshot("zone-one") != snapshot {
				t.Fatal("AFTER-trigger refusal did not restore old state")
			}
			if _, err := db.Database().Exec("DROP TRIGGER drift"); err != nil {
				t.Fatal(err)
			}
			receipt := f.call("POST", base+"/changes", factoryChange(tc.add, tc.del), 200)
			expected := append(append([]any{}, initial...), sentinel)
			gets := []any{sentinel}
			if len(tc.del) == 0 {
				expected = append(expected, old)
				gets = append(gets, old)
			}
			expected = append(expected, tc.add...)
			gets = append(gets, tc.add...)
			f.requireObservation(base, factoryObservation{List: expected, Gets: gets, Changes: []any{seed, receipt}})
			if len(tc.del) != 0 {
				f.call("GET", base+"/rrsets/old.example.test./A", nil, 404)
			}
		})
	}
}
