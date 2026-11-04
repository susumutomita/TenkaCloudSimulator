package blob

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestAzureBlobConstructorFailureDoesNotAcquireOrRepairAuthority(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	put(t, s, "box", "saved", "private-peer-bytes", nil, 201)
	before := fullBlobSnapshot(t, s)
	for _, cfg := range []Config{{Account: "BAD", Owner: "owner", Environment: "env"}, {Account: "accountone", Owner: "\t", Environment: "env"}, {Account: "accountone", Owner: "owner", Environment: ""}} {
		if _, err := New(s.db, cfg, permitted); err == nil {
			t.Fatal("invalid constructor config accepted")
		}
		if fullBlobSnapshot(t, s) != before {
			t.Fatal("invalid constructor changed persisted state")
		}
	}
	execSQL(t, s, "PRAGMA foreign_keys=OFF")
	if _, err := New(s.db, config("accounttwo", "owner"), permitted); err == nil {
		t.Fatal("unenforced foreign keys accepted")
	}
	execSQL(t, s, "PRAGMA foreign_keys=ON")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("foreign-key refusal changed state")
	}
	for _, trigger := range []string{"CREATE TRIGGER constructor_fault BEFORE INSERT ON azure_blob_accounts BEGIN SELECT RAISE(ABORT,'injected constructor failure'); END", "CREATE TRIGGER constructor_fault BEFORE INSERT ON azure_blob_accounts BEGIN SELECT RAISE(IGNORE); END"} {
		execSQL(t, s, trigger)
		if _, err := New(s.db, config("accounttwo", "owner"), permitted); err == nil {
			t.Fatal("failed account insert acknowledged")
		}
		if fullBlobSnapshot(t, s) != before {
			t.Fatal("failed constructor acquired authority or modified peer")
		}
		execSQL(t, s, "DROP TRIGGER constructor_fault")
	}
	execSQL(t, s, "UPDATE azure_blob_accounts SET cursor_key=x'01' WHERE account='accountone'")
	drift := fullBlobSnapshot(t, s)
	if _, err := New(s.db, config("accountone", "owner"), permitted); err == nil {
		t.Fatal("corrupt cursor authority adopted")
	}
	assertNativeError(t, boundedWire(s, "GET", "/accountone/box/saved", nil, nil), 500, "InternalError")
	if fullBlobSnapshot(t, s) != drift {
		t.Fatal("cursor drift repaired silently")
	}
}
func TestAzureBlobOrphanObjectAndAccountFixtureQuotaArePreserved(t *testing.T) {
	t.Run("orphan-object", func(t *testing.T) {
		_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
		create(t, s, "box")
		put(t, s, "box", "saved", "private-peer-bytes", nil, 201)
		execSQL(t, s, "PRAGMA foreign_keys=OFF")
		execSQL(t, s, "INSERT INTO azure_blob_objects SELECT environment,account,'absent',name,body,etag,modified,metadata,content_type,md5 FROM azure_blob_objects WHERE container='box'")
		execSQL(t, s, "PRAGMA foreign_keys=ON")
		before := fullBlobSnapshot(t, s)
		if _, err := New(s.db, config("accounttwo", "owner"), permitted); err == nil {
			t.Fatal("orphan object adopted")
		}
		if fullBlobSnapshot(t, s) != before {
			t.Fatal("orphan evidence deleted or new authority created")
		}
	})
	t.Run("account-cap", func(t *testing.T) {
		_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
		execSQL(t, s, "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO azure_blob_accounts SELECT 'fixture-'||x,'accountone','owner',zeroblob(32) FROM n")
		before := fullBlobSnapshot(t, s)
		if _, err := New(s.db, config("accounttwo", "owner"), permitted); err == nil {
			t.Fatal("account cap ignored")
		}
		if fullBlobSnapshot(t, s) != before {
			t.Fatal("account cap rejection changed state")
		}
	})
}
func TestAzureBlobContainerMutationFaultsAndCanceledReadPreserveGraph(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	create(t, s, "peer")
	put(t, s, "box", "saved", "saved", nil, 201)
	put(t, s, "peer", "saved", "private-peer-bytes", nil, 201)
	before := fullBlobSnapshot(t, s)
	execSQL(t, s, "CREATE TRIGGER container_insert_ignore BEFORE INSERT ON azure_blob_containers BEGIN SELECT RAISE(IGNORE); END")
	assertNativeError(t, boundedWire(s, "PUT", "/accountone/new?restype=container", nil, nil), 500, "InternalError")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("ignored container insertion changed graph")
	}
	execSQL(t, s, "DROP TRIGGER container_insert_ignore")
	for _, action := range []string{"SELECT RAISE(ABORT,'parent delete failure');", "SELECT RAISE(IGNORE);"} {
		execSQL(t, s, "CREATE TRIGGER container_delete_fault BEFORE DELETE ON azure_blob_containers WHEN OLD.name='box' BEGIN "+action+" END")
		assertNativeError(t, boundedWire(s, "DELETE", "/accountone/box?restype=container", nil, nil), 500, "InternalError")
		if fullBlobSnapshot(t, s) != before {
			t.Fatal("parent deletion failure committed earlier child deletion")
		}
		execSQL(t, s, "DROP TRIGGER container_delete_fault")
	}
	ctx, cancel := context.WithCancel(WithActor(context.Background(), *learner()))
	cancel()
	r := httptest.NewRequest("GET", "/accountone/box/saved", nil).WithContext(ctx)
	r.Header.Set("x-ms-version", APIVersion)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	assertNativeError(t, w, 500, "InternalError")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("canceled operation changed state")
	}
	execSQL(t, s, "ALTER TABLE azure_blob_objects RENAME COLUMN body TO blocked_body")
	faulted := fullBlobSnapshot(t, s)
	assertNativeError(t, boundedWire(s, "GET", "/accountone/box?restype=container&comp=list", nil, nil), 500, "InternalError")
	assertNativeError(t, boundedWire(s, "GET", "/accountone/box/saved", nil, nil), 500, "InternalError")
	if fullBlobSnapshot(t, s) != faulted {
		t.Fatal("missing-column query failure changed graph")
	}
	execSQL(t, s, "ALTER TABLE azure_blob_objects RENAME COLUMN blocked_body TO body")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("storage schema recovery changed data")
	}
}
func TestAzureBlobClosedDatabaseAndIncompatibleSchemaConstructorRefuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	db, s := fixture(t, path, config("accountone", "owner"), permitted)
	create(t, s, "box")
	put(t, s, "box", "saved", "saved", nil, 201)
	before := fullBlobSnapshot(t, s)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(db.Database(), config("accounttwo", "owner"), permitted); err == nil {
		t.Fatal("closed database accepted")
	}
	reopened, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := New(reopened.Database(), config("accountone", "owner"), permitted)
	if err != nil {
		t.Fatal(err)
	}
	if fullBlobSnapshot(t, again) != before {
		t.Fatal("failed closed-db constructor changed saved graph")
	}
	incompatible, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer incompatible.Close()
	if _, err = incompatible.Database().Exec("CREATE TABLE azure_blob_accounts(unrelated TEXT)"); err != nil {
		t.Fatal(err)
	}
	if _, err = New(incompatible.Database(), config("accountone", "owner"), permitted); err == nil {
		t.Fatal("incompatible authority table accepted")
	}
	var containers int
	if err = incompatible.Database().QueryRow("SELECT count(*) FROM sqlite_master WHERE name='azure_blob_containers'").Scan(&containers); err != nil || containers != 0 {
		t.Fatal("failed constructor leaked table creation", containers, err)
	}
}

func TestAzureBlobSchemaReadAndBudgetFailuresNeverPublishMutation(t *testing.T) {
	cases := []struct{ name, table, column, method, path string }{
		{"owner-query", "azure_blob_accounts", "owner", "GET", "/accountone/box/saved"},
		{"parent-query", "azure_blob_containers", "etag", "GET", "/accountone/box/saved"},
		{"container-existence", "azure_blob_containers", "name", "PUT", "/accountone/new?restype=container"},
		{"container-budget", "azure_blob_objects", "metadata", "PUT", "/accountone/new?restype=container"},
		{"delete-child-count", "azure_blob_objects", "container", "DELETE", "/accountone/box?restype=container"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
			create(t, s, "box")
			create(t, s, "peer")
			put(t, s, "box", "saved", "saved", nil, 201)
			put(t, s, "peer", "saved", "private-peer-bytes", nil, 201)
			execSQL(t, s, "ALTER TABLE "+c.table+" RENAME COLUMN "+c.column+" TO unavailable_column")
			before := fullBlobSnapshot(t, s)
			assertNativeError(t, boundedWire(s, c.method, c.path, nil, nil), 500, "InternalError")
			if fullBlobSnapshot(t, s) != before {
				t.Fatal("query failure committed mutation")
			}
			if c.name == "owner-query" {
				if _, err := New(s.db, s.config, permitted); err == nil {
					t.Fatal("incompatible saved authority accepted")
				}
				if fullBlobSnapshot(t, s) != before {
					t.Fatal("incompatible authority constructor changed data")
				}
			}
			execSQL(t, s, "ALTER TABLE "+c.table+" RENAME COLUMN unavailable_column TO "+c.column)
			if w := boundedWire(s, "GET", "/accountone/peer/saved", nil, nil); w.Code != 200 || w.Body.String() != "private-peer-bytes" {
				t.Fatal("restored schema lost peer")
			}
		})
	}
}
func TestAzureBlobContainerCapAndWriteErrorsPreservePeer(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	create(t, s, "peer")
	put(t, s, "peer", "saved", "private-peer-bytes", nil, 201)
	execSQL(t, s, "CREATE TRIGGER container_insert_abort BEFORE INSERT ON azure_blob_containers BEGIN SELECT RAISE(ABORT,'private host storage error'); END")
	before := fullBlobSnapshot(t, s)
	assertNativeError(t, boundedWire(s, "PUT", "/accountone/new?restype=container", nil, nil), 500, "InternalError")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("container write error changed graph")
	}
	execSQL(t, s, "DROP TRIGGER container_insert_abort")
	put(t, s, "box", "saved", "saved", nil, 201)
	execSQL(t, s, "CREATE TRIGGER child_delete_ignore BEFORE DELETE ON azure_blob_objects WHEN OLD.container='box' BEGIN SELECT RAISE(IGNORE); END")
	before = fullBlobSnapshot(t, s)
	assertNativeError(t, boundedWire(s, "DELETE", "/accountone/box?restype=container", nil, nil), 500, "InternalError")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("ignored child deletion changed graph")
	}
	execSQL(t, s, "DROP TRIGGER child_delete_ignore")
	execSQL(t, s, "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<998) INSERT INTO azure_blob_containers SELECT environment,account,printf('fixture%04d',x),etag,modified,metadata FROM azure_blob_containers,n WHERE name='box'")
	before = fullBlobSnapshot(t, s)
	assertNativeError(t, boundedWire(s, "PUT", "/accountone/extra?restype=container", nil, nil), 409, "SimulatorQuotaExceeded")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("container quota refusal changed graph")
	}
}
func TestAzureBlobMissingAuthorityAndMissingContainerFrames(t *testing.T) {
	_, s := fixture(t, ":memory:", config("accountone", "owner"), permitted)
	create(t, s, "box")
	put(t, s, "box", "saved", "saved", nil, 201)
	for _, method := range []string{"HEAD", "DELETE"} {
		before := fullBlobSnapshot(t, s)
		w := boundedWire(s, method, "/accountone/missing?restype=container", nil, nil)
		if w.Code != 404 || w.Header().Get("x-ms-error-code") != "ContainerNotFound" {
			t.Fatal("missing container error", w.Code, w.Header())
		}
		if fullBlobSnapshot(t, s) != before {
			t.Fatal("missing container refusal changed state")
		}
	}
	execSQL(t, s, "PRAGMA foreign_keys=OFF")
	execSQL(t, s, "DELETE FROM azure_blob_accounts")
	execSQL(t, s, "PRAGMA foreign_keys=ON")
	before := fullBlobSnapshot(t, s)
	assertNativeError(t, boundedWire(s, "GET", "/accountone/box/saved", nil, nil), 500, "InternalError")
	if fullBlobSnapshot(t, s) != before {
		t.Fatal("missing authority silently adopted/deleted graph")
	}
}

func TestAzureBlobReadOnlyConstructorCannotPartiallyCreateSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.sqlite")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	readonly, err := sql.Open("sqlite3", "file:"+url.PathEscape(path)+"?mode=ro&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer readonly.Close()
	readonly.SetMaxOpenConns(1)
	if _, err = New(readonly, config("accountone", "owner"), permitted); err == nil {
		t.Fatal("read-only schema provision accepted")
	}
	var count int
	if err = readonly.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'azure_blob_%'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed readonly constructor leaked schema", count, err)
	}
}
