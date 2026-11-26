package simpledb

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

func TestCancelledConditionalPutAfterStagingRollsBack(t *testing.T) {
	ctx := t.Context()
	file := filepath.Join(t.TempDir(), "staged.db")
	open := func() (*Store, *sql.DB) {
		t.Helper()
		db, err := sql.Open("sqlite3", file)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		// The TEMP trigger is local to the only fixture connection.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		store, err := NewStore(db)
		if err != nil {
			t.Fatal(err)
		}
		return store, db
	}
	s, db := open()
	const account, region, domain, item = "local", "us-east-1", "domain", "row"
	before := map[string][]string{"version": {"v1"}, "payload": {"before"}}
	after := map[string][]string{"version": {"v2"}, "payload": {"after"}}
	if err := s.CreateDomain(ctx, account, region, domain); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, account, region, domain, item, before, nil); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(ctx)
	defer cancel()
	var captured [][2]string
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(raw any) error {
		return raw.(sqlitedriver.Conn).Raw().CreateFunction("sdb_test_cancel_after_stage", 2, 0,
			func(result sqlite3.Context, args ...sqlite3.Value) {
				captured = append(captured, [2]string{args[0].Text(), args[1].Text()})
				cancel()
				result.ResultNull()
			})
	}); err != nil {
		t.Fatal(err)
	}
	// The same transaction sees the staged row in the table, not just the
	// candidate NEW value. The hook has no way to run in production or after
	// reopening the fixture database.
	if _, err := conn.ExecContext(ctx, `CREATE TEMP TRIGGER sdb_test_cancel_after_stage
		AFTER UPDATE OF attributes ON aws_simpledb_items
		BEGIN
			SELECT sdb_test_cancel_after_stage(OLD.attributes,
				(SELECT attributes FROM aws_simpledb_items
				WHERE account = NEW.account AND region = NEW.region AND domain = NEW.domain AND name = NEW.name));
		END`); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	handler := func(s *Store) http.Handler {
		return NewHTTPHandler(s, Scope{account, region}, func(_ context.Context, action, resource string) bool {
			return action == "sdb:PutAttributes" && resource == "arn:aws:sdb:us-east-1:local:domain/domain"
		})
	}
	form := params("PutAttributes", map[string]string{
		"DomainName": domain, "ItemName": item, "Expected.Name": "version", "Expected.Value": "v1",
		"Attribute.1.Name": "version", "Attribute.1.Value": "v2", "Attribute.1.Replace": "true",
		"Attribute.2.Name": "payload", "Attribute.2.Value": "after", "Attribute.2.Replace": "true",
	})
	req := httptest.NewRequestWithContext(cancelled, http.MethodPost, "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reply := httptest.NewRecorder()
	handler(s).ServeHTTP(reply, req)
	// Internal handler contract only. A disconnected client need not receive
	// this response; do not turn cancellation into a new public wire status.
	assertQueryError(t, reply, http.StatusInternalServerError, "InternalError")
	if err := cancelled.Err(); err != context.Canceled || len(captured) != 1 {
		t.Fatalf("request did not cancel exactly at the staged row: %v (hooks=%d)", err, len(captured))
	}
	for index, want := range []map[string][]string{before, after} {
		var got map[string][]string
		if err := json.Unmarshal([]byte(captured[0][index]), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("transaction row %d: %v (%v), want %v", index, got, err, want)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	fresh, _ := open()
	verify := func(want map[string][]string) {
		t.Helper()
		got, err := fresh.Get(ctx, account, region, domain, item, nil)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("reopened state = %v (%v), want %v", got, err, want)
		}
	}
	verify(before)
	// This also proves the rollback retained the prior version condition.
	if again := callHTTP(handler(fresh), form); again.Code != http.StatusOK {
		t.Fatalf("same conditional write after rollback: %d %s", again.Code, again.Body.String())
	}
	verify(after)
}
