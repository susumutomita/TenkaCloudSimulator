package cognitoidp

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestUserPoolStorageRejectsInvalidScopesBeforeUsingState(t *testing.T) {
	s, _ := poolFixture(t)
	ctx := t.Context()
	scope := Scope{"012345678901", "us-east-1"}
	invalidAccount := Scope{"untrusted", scope.Region}
	actor := Actor{"credential-a", "exercise-a"}
	seed, err := s.create(ctx, scope, "existing pool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.create(ctx, invalidAccount, "must not persist"); !errors.Is(err, errInvalid) {
		t.Fatal("invalid scope admitted a create:", err)
	}
	if _, err := s.describe(ctx, scope, "us-west-2_fixture"); !errors.Is(err, errInvalid) {
		t.Fatal("wrong region pool ID admitted a describe:", err)
	}
	if err := s.delete(ctx, invalidAccount, seed.ID); !errors.Is(err, errInvalid) {
		t.Fatal("invalid scope did not refuse delete:", err)
	}
	if rows, cursor, err := s.list(ctx, scope, Actor{}, 1, ""); !errors.Is(err, errInvalid) || rows != nil || cursor != "" {
		t.Fatal("invalid cursor identity exposed pool metadata or issued a cursor:", err)
	}
	if rows, cursor, err := s.list(ctx, scope, actor, 10, ""); err != nil || len(rows) != 1 || rows[0] != seed || cursor != "" {
		t.Fatal("invalid calls affected existing state:", err)
	}
}

var errPoolReadbackFault = errors.New("private fixture: storage observation interrupted")

// The test DB delegates SQL and row data to real SQLite. Independently inject
// the database/sql observation errors the store handles: loss of a write ACK
// or a row-stream error after one usable row. This is not an engine failure
// that the current SQLite build itself has been observed to emit.
type poolReadbackFault struct {
	write, page bool
	failed      int
}
type poolFaultConnector struct {
	base  driver.Driver
	dsn   string
	fault *poolReadbackFault
}

func (c poolFaultConnector) Driver() driver.Driver { return c.base }
func (c poolFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return poolFaultConnection{conn, c.fault}, nil
}

type poolFaultConnection struct {
	driver.Conn
	fault *poolReadbackFault
}

func (c poolFaultConnection) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	stmt, err := c.Conn.(driver.ConnPrepareContext).PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return poolFaultStatement{stmt, c.fault}, nil
}

type poolFaultStatement struct {
	driver.Stmt
	fault *poolReadbackFault
}

func (s poolFaultStatement) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	res, err := s.Stmt.(driver.StmtExecContext).ExecContext(ctx, args)
	if err == nil && s.fault.write {
		return poolFaultResult{res, s.fault}, nil
	}
	return res, err
}

func (s poolFaultStatement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := s.Stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
	if err == nil && s.fault.page {
		return &poolFaultRows{Rows: rows, fault: s.fault}, nil
	}
	return rows, err
}

type poolFaultResult struct {
	driver.Result
	fault *poolReadbackFault
}

func (r poolFaultResult) RowsAffected() (int64, error) {
	r.fault.failed++
	return 0, errPoolReadbackFault
}

type poolFaultRows struct {
	driver.Rows
	fault   *poolReadbackFault
	started bool
}

func (r *poolFaultRows) Next(dest []driver.Value) error {
	if r.started {
		r.fault.failed++
		return errPoolReadbackFault
	}
	err := r.Rows.Next(dest)
	r.started = err == nil
	return err
}

func TestUserPoolUnconfirmedStorageDoesNotPublishSuccessOrPartialPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pools.sqlite")
	c, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	actual, err := NewStore(c.Database())
	if err != nil {
		t.Fatal(err)
	}
	fault := &poolReadbackFault{}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_txlock=immediate"}).String()
	db := sql.OpenDB(poolFaultConnector{c.Database().Driver(), dsn, fault})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	observed, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	scope := Scope{"012345678901", "us-east-1"}
	actor := Actor{"credential-a", "exercise-a"}
	h := NewHTTPHandler(observed, scope, actor, func(context.Context, string, string) bool { return true })
	requireOpaqueFailure := func(t *testing.T, operation, body string) {
		t.Helper()
		w := poolRequest(t, h, operation, body, http.StatusInternalServerError, "InternalErrorException")
		var fields map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil || len(fields) != 2 ||
			fields["__type"] != "InternalErrorException" || fields["message"] != "Local user pool state operation failed." {
			t.Fatal("unconfirmed storage exposed internal data, a successful write, or a partial page")
		}
	}
	fault.write = true
	requireOpaqueFailure(t, "CreateUserPool", `{"PoolName":"recoverable pool"}`)
	fault.write = false
	// The actual SQLite mutation ran. A missing ACK must not be presented
	// as a success or assumed to prove rollback by the HTTP caller.
	rows, cursor, err := actual.list(ctx, scope, actor, 10, "")
	if err != nil || len(rows) != 1 || rows[0].Name != "recoverable pool" || cursor != "" || fault.failed != 1 {
		t.Fatal("expected exactly one persisted create and one lost acknowledgment:", err)
	}
	first := rows[0]
	second, err := actual.create(ctx, scope, "second pool")
	if err != nil {
		t.Fatal(err)
	}
	fault.page = true
	requireOpaqueFailure(t, "ListUserPools", `{"MaxResults":1}`)
	fault.page = false
	if fault.failed != 2 {
		t.Fatal("the listing did not discard an interrupted row stream")
	}
	if page, next, err := observed.list(ctx, scope, actor, 1, ""); err != nil || len(page) != 1 || next == "" {
		t.Fatal("a fresh healthy listing could not establish a continuation:", err)
	}

	input, err := json.Marshal(map[string]string{"UserPoolId": first.ID})
	if err != nil {
		t.Fatal(err)
	}
	fault.write = true
	requireOpaqueFailure(t, "DeleteUserPool", string(input))
	fault.write = false
	if rows, cursor, err := actual.list(ctx, scope, actor, 10, ""); err != nil || len(rows) != 1 || rows[0] != second || cursor != "" || fault.failed != 3 {
		t.Fatal("the unconfirmed delete did not remove exactly the selected pool:", err)
	}
}
