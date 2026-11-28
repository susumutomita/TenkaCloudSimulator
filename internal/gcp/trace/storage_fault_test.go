package trace

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func faultStore(t *testing.T) fixture {
	t.Helper()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	return openTrace(t, filepath.Join(t.TempDir(), "trace.db"), &now)
}

func traceSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// Include expired observations and all durable fields. A failed append must
// also roll back its preceding retention sweep.
func storedObservations(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), `SELECT json_array(sequence,project,trace_id,observed,
		start_time,end_time,document) FROM main.gcp_trace_spans ORDER BY sequence`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var saved []string
	for rows.Next() {
		var observation string
		if err := rows.Scan(&observation); err != nil {
			t.Fatal(err)
		}
		saved = append(saved, observation)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestTraceConstructionIsTransactional(t *testing.T) {
	owner := Config{Project: project, Owner: "owner", Subject: func(context.Context) (string, bool) { return "owner", true }}
	for _, tc := range []struct {
		name, prepare string
	}{
		{"incompatible stored span schema", "CREATE TABLE gcp_trace_spans(unrelated TEXT)"},
		{"refused owner binding", `CREATE TABLE gcp_trace_projects(project TEXT PRIMARY KEY, owner TEXT NOT NULL);
			CREATE TRIGGER stop BEFORE INSERT ON gcp_trace_projects BEGIN SELECT RAISE(ABORT,'refused'); END`},
		{"ignored owner binding", `CREATE TABLE gcp_trace_projects(project TEXT PRIMARY KEY, owner TEXT NOT NULL);
			CREATE TRIGGER stop BEFORE INSERT ON gcp_trace_projects BEGIN SELECT RAISE(IGNORE); END`},
		{"deferred constraint at commit", `CREATE TABLE gcp_trace_projects(project TEXT PRIMARY KEY, owner TEXT NOT NULL);
			CREATE TABLE parents(id INTEGER PRIMARY KEY);
			CREATE TABLE children(parent INTEGER REFERENCES parents(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER stop AFTER INSERT ON gcp_trace_projects BEGIN INSERT INTO children VALUES(1); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := core.New(filepath.Join(t.TempDir(), "startup.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			db := store.Database()
			traceSQL(t, db, tc.prepare)
			var schemaBefore, schemaAfter int
			if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE 'gcp_trace_%'").Scan(&schemaBefore); err != nil {
				t.Fatal(err)
			}
			if e, err := New(db, owner); err == nil || e != nil {
				t.Fatal("opened a failed or unbound trace store")
			}
			if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name LIKE 'gcp_trace_%'").Scan(&schemaAfter); err != nil || schemaAfter != schemaBefore {
				t.Fatalf("partially initialized schema: before=%d after=%d err=%v", schemaBefore, schemaAfter, err)
			}
			if tc.name != "incompatible stored span schema" {
				var count int
				if err := db.QueryRow("SELECT count(*) FROM gcp_trace_projects").Scan(&count); err != nil || count != 0 {
					t.Fatalf("failed construction retained owner: count=%d err=%v", count, err)
				}
			}
		})
	}
	f := faultStore(t)
	for _, cfg := range []Config{{}, {Project: project, Owner: "owner"}, {Project: project, Owner: strings.Repeat("a", 257), Subject: owner.Subject}} {
		if e, err := New(f.db.Database(), cfg); err == nil || e != nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	if e, err := New(nil, owner); err == nil || e != nil {
		t.Fatal("accepted nil database")
	}
	db := f.db.Database()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	if e, err := New(db, owner); err == nil || e != nil {
		t.Fatal("opened closed database")
	}
}

type traceAuthorizer func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode

func setTraceAuthorizer(t *testing.T, db *sql.DB, policy traceAuthorizer) {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(raw any) error { return raw.(driver.Conn).Raw().SetAuthorizer(policy) }); err != nil {
		t.Fatal(err)
	}
}

func traceFailure(t *testing.T, f fixture, method, path, body string, status int) {
	t.Helper()
	raw := f.send(t, t.Context(), "owner", method, path, body, status)
	reply := parse[map[string]any](t, raw)
	e, ok := reply["error"].(map[string]any)
	want := "INTERNAL"
	if status == 403 {
		want = "PERMISSION_DENIED"
	}
	if len(reply) != 1 || !ok || e["status"] != want || e["code"] != float64(status) || strings.Contains(string(raw), "retained-private-span") {
		t.Fatalf("unexpected failure envelope: %s", raw)
	}
}

func TestTraceStorageFailurePreservesPriorObservations(t *testing.T) {
	for _, tc := range []struct {
		name, statement string
		countQuery      int
	}{
		{"retention refused", `CREATE TEMP TRIGGER fault BEFORE DELETE ON gcp_trace_spans BEGIN SELECT RAISE(ABORT,'retention failed'); END`, 0},
		{"project budget unavailable", "", 1},
		{"trace budget unavailable", "", 2},
		{"refused insertion", `CREATE TEMP TRIGGER fault BEFORE INSERT ON gcp_trace_spans BEGIN SELECT RAISE(ABORT,'write failed'); END`, 0},
		{"ignored insertion", `CREATE TEMP TRIGGER fault BEFORE INSERT ON gcp_trace_spans BEGIN SELECT RAISE(IGNORE); END`, 0},
		{"final project count unavailable", "", 3},
		{"final receipt unavailable", "", 4},
		{"removed inserted observation", `CREATE TEMP TRIGGER fault AFTER INSERT ON gcp_trace_spans BEGIN DELETE FROM gcp_trace_spans WHERE sequence=NEW.sequence; END`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := faultStore(t)
			db := f.db.Database()
			s := makeSpan("7", "retained-private-span", "")
			f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, s), traceAt(idC, s)), 200)
			traceSQL(t, db, "UPDATE gcp_trace_spans SET observed=0 WHERE trace_id=?", idC)
			before := storedObservations(t, db)
			// SQLite prepares COUNT in admission, then again for final readback.
			// Deny the selected real aggregate at its corresponding transaction phase.
			countQueries := 0
			if tc.countQuery > 0 {
				setTraceAuthorizer(t, db, func(action sqlite3.AuthorizerActionCode, _, column, _, _ string) sqlite3.AuthorizerReturnCode {
					if action == sqlite3.AUTH_FUNCTION && column == "count" {
						countQueries++
						if countQueries == tc.countQuery {
							return sqlite3.AUTH_DENY
						}
					}
					return sqlite3.AUTH_OK
				})
			} else {
				traceSQL(t, db, tc.statement)
			}
			traceFailure(t, f, http.MethodPatch, collection, payload(t, traceAt(idB, s)), 500)
			if tc.countQuery > 0 {
				setTraceAuthorizer(t, db, nil)
				if countQueries != tc.countQuery {
					t.Fatalf("failed at count %d, want %d", countQueries, tc.countQuery)
				}
			}
			if !slices.Equal(storedObservations(t, db), before) {
				t.Fatal("failed write changed prior observations or retained a partial batch")
			}
			f.send(t, t.Context(), "owner", "GET", collection+"/"+idB, "", 404)
		})
	}
}

func TestTraceReadsRefuseCorruptObservations(t *testing.T) {
	for _, tc := range []struct{ name, damage string }{
		{"malformed stored JSON", "UPDATE gcp_trace_spans SET document='broken' WHERE trace_id=?"},
		{"inconsistent span timestamp", "UPDATE gcp_trace_spans SET start_time='2026-11-28T11:59:00.000000000Z' WHERE trace_id=?"},
		{"too many stored observations", `WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<128)
			INSERT INTO gcp_trace_spans(project,trace_id,observed,start_time,end_time,document)
			SELECT project,trace_id,observed,start_time,end_time,document FROM gcp_trace_spans,n WHERE trace_id=?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := faultStore(t)
			s := makeSpan("23", "retained-private-span", "")
			f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, s)), 200)
			db := f.db.Database()
			traceSQL(t, db, tc.damage, idA)
			before := storedObservations(t, db)
			traceFailure(t, f, http.MethodGet, collection+"/"+idA, "", 500)
			traceFailure(t, f, http.MethodGet, listAt("COMPLETE"), "", 500)
			if !slices.Equal(storedObservations(t, db), before) {
				t.Fatal("reading repaired or removed a damaged observation")
			}
		})
	}
}

func TestTraceOwnerAndReadTransactionFailures(t *testing.T) {
	f := faultStore(t)
	db := f.db.Database()
	s := makeSpan("5", "retained-private-span", "")
	body := payload(t, traceAt(idA, s))
	f.send(t, t.Context(), "owner", "PATCH", collection, body, 200)
	before := storedObservations(t, db)
	traceSQL(t, db, "UPDATE gcp_trace_projects SET owner='other' WHERE project=?", project)
	traceFailure(t, f, http.MethodGet, collection+"/"+idA, "", 403)
	traceFailure(t, f, http.MethodPatch, collection, body, 403)
	traceSQL(t, db, "UPDATE gcp_trace_projects SET owner='owner' WHERE project=?", project)
	for _, tc := range []struct {
		name, column, path string
		action             sqlite3.AuthorizerActionCode
	}{
		{"owner read refused", "owner", collection + "/" + idA, sqlite3.AUTH_READ},
		{"high water unavailable", "max", listAt("COMPLETE"), sqlite3.AUTH_FUNCTION},
		{"keyset unavailable", "start_time", listAt("MINIMAL"), sqlite3.AUTH_READ},
		{"span data unavailable", "document", collection + "/" + idA, sqlite3.AUTH_READ},
		{"read commit refused", "COMMIT", collection + "/" + idA, sqlite3.AUTH_TRANSACTION},
	} {
		t.Run(tc.name, func(t *testing.T) {
			denied := false
			setTraceAuthorizer(t, db, func(action sqlite3.AuthorizerActionCode, a, b, _, _ string) sqlite3.AuthorizerReturnCode {
				if action == tc.action && (b == tc.column || (action == sqlite3.AUTH_TRANSACTION && a == tc.column)) {
					denied = true
					return sqlite3.AUTH_DENY
				}
				return sqlite3.AUTH_OK
			})
			traceFailure(t, f, http.MethodGet, tc.path, "", 500)
			setTraceAuthorizer(t, db, nil)
			if !denied || !slices.Equal(storedObservations(t, db), before) {
				t.Fatal("read fault did not preserve original observation")
			}
		})
	}
	traceSQL(t, db, "UPDATE gcp_trace_spans SET trace_id='invalid-id'")
	traceFailure(t, f, http.MethodGet, listAt("MINIMAL"), "", 500)
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	traceFailure(t, f, http.MethodGet, collection+"/"+idA, "", 500)
	traceFailure(t, f, http.MethodPatch, collection, body, 500)
}

func TestTraceUnreadableRowsDoNotPublishAPartialResponse(t *testing.T) {
	for _, tc := range []struct{ name, traceID, document, path string }{
		{"null document", "trace_id", "NULL", collection + "/" + idA},
		{"error on later span", "trace_id", "CASE WHEN sequence=2 THEN json_extract('invalid','$') ELSE document END", collection + "/" + idA},
		{"unreadable list key", "CASE WHEN sequence=2 THEN json_extract('invalid','$') ELSE trace_id END", "document", listAt("MINIMAL")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := faultStore(t)
			s := makeSpan("9", "retained-private-span", "")
			f.send(t, t.Context(), "owner", "PATCH", collection, payload(t, traceAt(idA, s, s)), 200)
			db := f.db.Database()
			before := storedObservations(t, db)
			// A connection-local view preserves the real backing rows, but makes the
			// returned column inconsistent or the second SQLite step fail.
			traceSQL(t, db, `CREATE TEMP VIEW gcp_trace_spans AS SELECT sequence,project,`+tc.traceID+` AS trace_id,
				observed,start_time,end_time,`+tc.document+` AS document FROM main.gcp_trace_spans`)
			traceFailure(t, f, http.MethodGet, tc.path, "", 500)
			traceSQL(t, db, "DROP VIEW temp.gcp_trace_spans")
			if !slices.Equal(storedObservations(t, db), before) {
				t.Fatal("failed read changed backing rows")
			}
			got := parse[traceRecord](t, f.send(t, t.Context(), "owner", "GET", collection+"/"+idA, "", 200))
			if len(got.Spans) != 2 || got.Spans[0].Name != s.Name || got.Spans[1].Name != s.Name {
				t.Fatal("lost one of the two original observations")
			}
		})
	}
}
