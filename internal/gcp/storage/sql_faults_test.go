package storage

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func sqlExec(t *testing.T, db *core.Store, statement string) {
	t.Helper()
	if _, err := db.Database().Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func TestStorageMutationSQLFaultsRollbackCompleteFrames(t *testing.T) {
	db, s := fixture(t, ":memory:", "sql-faults", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "fault-empty")
	create(t, s, "project-one", "fault-data")
	upload(t, s, "project-one", "fault-data", "key", "original", "0")
	cases := []struct{ trigger, method, path, body string }{
		{`CREATE TRIGGER fail BEFORE INSERT ON gcp_storage_buckets BEGIN SELECT RAISE(ABORT,'private bucket insert'); END`, "POST", "/storage/v1/b?project=project-one", `{"name":"never-created"}`},
		{`CREATE TRIGGER fail BEFORE DELETE ON gcp_storage_buckets BEGIN SELECT RAISE(ABORT,'private bucket delete'); END`, "DELETE", "/storage/v1/b/fault-empty", ""},
		{`CREATE TRIGGER fail BEFORE UPDATE ON gcp_storage_sequence BEGIN SELECT RAISE(ABORT,'private sequence'); END`, "POST", "/upload/storage/v1/b/fault-data/o?uploadType=media&name=key", "replacement"},
		{`CREATE TRIGGER fail BEFORE DELETE ON gcp_storage_objects BEGIN SELECT RAISE(ABORT,'private object delete'); END`, "DELETE", "/storage/v1/b/fault-data/o/key", ""},
		{`CREATE TRIGGER fail BEFORE DELETE ON gcp_storage_objects BEGIN DELETE FROM gcp_storage_objects WHERE environment=OLD.environment AND bucket=OLD.bucket AND name=OLD.name; SELECT RAISE(IGNORE); END`, "DELETE", "/storage/v1/b/fault-data/o/key", ""},
	}
	for _, tc := range cases {
		before := storageSnapshot(t, db)
		sqlExec(t, db, tc.trigger)
		w := request(s, "project-one", tc.method, tc.path, tc.body, "application/json")
		mustStatus(t, w, 500)
		if strings.Contains(w.Body.String(), "private") {
			t.Fatal("SQL detail leaked")
		}
		sqlExec(t, db, "DROP TRIGGER fail")
		if after := storageSnapshot(t, db); after != before {
			t.Fatal("SQL mutation failure changed complete persisted frame")
		}
	}
	// A real deferred foreign-key failure occurs at Commit, after the native
	// object and sequence writes; both must roll back, including audit rows.
	sqlExec(t, db, `CREATE TABLE host_fault_audit(environment TEXT,project TEXT,FOREIGN KEY(environment,project) REFERENCES gcp_storage_projects(environment,id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail AFTER UPDATE ON gcp_storage_objects BEGIN INSERT INTO host_fault_audit VALUES(NEW.environment,'missing-project'); END`)
	before := storageSnapshot(t, db)
	mustStatus(t, request(s, "project-one", "POST", "/upload/storage/v1/b/fault-data/o?uploadType=media&name=key", "replacement", "text/plain"), 500)
	sqlExec(t, db, "DROP TRIGGER fail")
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("commit failure retained partial object/sequence")
	}
	var count int
	if err := db.Database().QueryRow("SELECT count(*) FROM host_fault_audit").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed commit retained child rows", count, err)
	}
}

func TestStorageReadSchemaFaultsNeverDisclosePartialResults(t *testing.T) {
	db, s := fixture(t, ":memory:", "schema-faults", projectPolicy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "a-bucket")
	create(t, s, "project-one", "z-bucket")
	upload(t, s, "project-one", "a-bucket", "key", "private-media", "0")
	before := storageSnapshot(t, db)
	// A malformed persisted schema, as after an unsupported migration, fails
	// each distinct protocol path before returning bytes or performing cleanup.
	for _, tc := range []struct{ table, column, method, path string }{
		{"gcp_storage_buckets", "project", "POST", "/storage/v1/b?project=project-one"},
		{"gcp_storage_buckets", "location", "GET", "/storage/v1/b?project=project-one"},
		{"gcp_storage_buckets", "location", "GET", "/storage/v1/b/a-bucket"},
		{"gcp_storage_objects", "environment", "DELETE", "/storage/v1/b/z-bucket"},
		{"gcp_storage_objects", "content_type", "GET", "/storage/v1/b/a-bucket/o/key?alt=media"},
		{"gcp_storage_objects", "name", "GET", "/storage/v1/b/a-bucket/o"},
		{"gcp_storage_sequence", "next_generation", "POST", "/upload/storage/v1/b/a-bucket/o?uploadType=media&name=key"},
	} {
		sqlExec(t, db, "ALTER TABLE "+tc.table+" RENAME COLUMN "+tc.column+" TO unavailable_column")
		body := "new"
		if strings.Contains(tc.path, "project=") {
			body = `{"name":"new-bucket"}`
		}
		w := request(s, "project-one", tc.method, tc.path, body, "application/json")
		mustStatus(t, w, 500)
		if strings.Contains(w.Body.String(), "private-media") {
			t.Fatal("schema failure disclosed data")
		}
		sqlExec(t, db, "ALTER TABLE "+tc.table+" RENAME COLUMN unavailable_column TO "+tc.column)
		if after := storageSnapshot(t, db); after != before {
			t.Fatal("schema failure mutated retained rows")
		}
	}
	// A row-level evaluation error on the second bucket tests rows.Err, not
	// merely Query's preparation error, and must not expose the first result.
	sqlExec(t, db, `ALTER TABLE gcp_storage_buckets RENAME TO saved_buckets; CREATE VIEW gcp_storage_buckets AS SELECT environment,name,project,CASE WHEN name='z-bucket' THEN abs(-9223372036854775808) ELSE location END AS location,created FROM saved_buckets`)
	w := request(s, "project-one", "GET", "/storage/v1/b?project=project-one", "", "")
	mustStatus(t, w, 500)
	if strings.Contains(w.Body.String(), "a-bucket") {
		t.Fatal("late SQL failure disclosed partial listing")
	}
	sqlExec(t, db, "DROP VIEW gcp_storage_buckets; ALTER TABLE saved_buckets RENAME TO gcp_storage_buckets")
	if after := storageSnapshot(t, db); after != before {
		t.Fatal("late SQL failure changed state")
	}
	// Non-null schema drift is realistic corrupt data; failed string scans
	// must not become a successful partial constructor or bucket list.
	sqlExec(t, db, `ALTER TABLE gcp_storage_buckets RENAME TO saved_buckets; CREATE VIEW gcp_storage_buckets AS SELECT environment,name,NULL AS project,location,created FROM saved_buckets`)
	_, err := New(db.Database(), Config{Environment: "schema-faults", Projects: []Project{{"project-one", "1001"}, {"project-two", "1002"}}, AuthorizeTx: projectPolicy})
	if err == nil {
		t.Fatal("constructor accepted corrupt owner scan")
	}
	mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b/a-bucket", "", ""), 500)
	sqlExec(t, db, "DROP VIEW gcp_storage_buckets; ALTER TABLE saved_buckets RENAME TO gcp_storage_buckets")
	sqlExec(t, db, `ALTER TABLE gcp_storage_buckets RENAME TO saved_buckets; CREATE VIEW gcp_storage_buckets AS SELECT environment,name,project,NULL AS location,created FROM saved_buckets`)
	mustStatus(t, request(s, "project-one", "GET", "/storage/v1/b?project=project-one", "", ""), 500)
	sqlExec(t, db, "DROP VIEW gcp_storage_buckets; ALTER TABLE saved_buckets RENAME TO gcp_storage_buckets")
}

func TestStorageFactoryPolicyCorruptionAndInitializationFaults(t *testing.T) {
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := projectFactoryConfig("factory-faults")
	h, err := NewProjectHandler(db.Database(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []string{"", " bad", "bad\n", strings.Repeat("x", 1025)} {
		bad := projectFactoryConfig("factory-faults")
		bad.Projects[0].OwnerSubject = subject
		if _, err = NewProjectHandler(db.Database(), bad); err == nil {
			t.Fatal("invalid owner admitted")
		}
	}
	bad := projectFactoryConfig("factory-faults")
	bad.Projects = append(bad.Projects, bad.Projects[0])
	if _, err = NewProjectHandler(db.Database(), bad); err == nil {
		t.Fatal("duplicate project owner admitted")
	}
	owner := factoryServer(h, "exercise-one")
	defer owner.Close()
	mustStatus(t, actualRequest(t, owner, "POST", "/storage/v1/b?project=project-one", `{"name":"factory-private"}`, nil), 200)
	before := factorySnapshot(t, db)
	sqlExec(t, db, "UPDATE gcp_storage_project_owners SET subject='wrong-owner' WHERE project='project-one'")
	w := actualRequest(t, owner, "GET", "/storage/v1/b/factory-private", "", nil)
	mustStatus(t, w, 500)
	if strings.Contains(w.Body.String(), "wrong-owner") {
		t.Fatal("authority drift disclosed")
	}
	sqlExec(t, db, "UPDATE gcp_storage_project_owners SET subject='exercise-one' WHERE project='project-one'")
	if factorySnapshot(t, db) != before {
		t.Fatal("authority drift request mutated state")
	}
	sqlExec(t, db, "ALTER TABLE gcp_storage_project_owners RENAME COLUMN subject TO unavailable_column")
	mustStatus(t, actualRequest(t, owner, "GET", "/storage/v1/b/factory-private", "", nil), 500)
	if _, err = NewProjectHandler(db.Database(), cfg); err == nil {
		t.Fatal("malformed owner schema accepted")
	}
	sqlExec(t, db, "ALTER TABLE gcp_storage_project_owners RENAME COLUMN unavailable_column TO subject")
	mustStatus(t, actualRequest(t, owner, "GET", "/storage/v1/b/missing-bucket", "", nil), 403)
	// Owner insertion fault while initializing another environment must roll
	// back that environment's project and sequence rows as well.
	sqlExec(t, db, `CREATE TRIGGER fail BEFORE INSERT ON gcp_storage_project_owners BEGIN SELECT RAISE(ABORT,'private owner insert'); END`)
	before = factorySnapshot(t, db)
	if _, err = NewProjectHandler(db.Database(), projectFactoryConfig("new-environment")); err == nil {
		t.Fatal("owner insert fault accepted")
	}
	sqlExec(t, db, "DROP TRIGGER fail")
	if factorySnapshot(t, db) != before {
		t.Fatal("failed owner insert retained constructor state")
	}
	// Deferred policy FK failure specifically exercises constructor Commit.
	sqlExec(t, db, `CREATE TABLE host_owner_audit(environment TEXT,project TEXT,FOREIGN KEY(environment,project) REFERENCES gcp_storage_projects(environment,id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER fail AFTER INSERT ON gcp_storage_project_owners BEGIN INSERT INTO host_owner_audit VALUES(NEW.environment,'missing-project'); END`)
	before = factorySnapshot(t, db)
	if _, err = NewProjectHandler(db.Database(), projectFactoryConfig("commit-failure")); err == nil {
		t.Fatal("deferred constructor constraint accepted")
	}
	sqlExec(t, db, "DROP TRIGGER fail")
	if factorySnapshot(t, db) != before {
		t.Fatal("failed constructor commit retained authority")
	}
	// A failed neutral extraction cannot fall back to a prepopulated leaf actor.
	r := httptest.NewRequest("GET", "/storage/v1/b/factory-private", nil).WithContext(WithActor(context.Background(), Actor{"exercise-one", "factory-faults"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	mustStatus(t, rec, 403)
}

func TestStoragePerBucketListingDenialIsNotPartialDisclosure(t *testing.T) {
	policy := func(ctx context.Context, tx *sql.Tx, a Actor, p string, r Resource) error {
		if p == "storage.buckets.list" && r.Bucket == "private-list" {
			return ErrDenied
		}
		return projectPolicy(ctx, tx, a, p, r)
	}
	db, s := fixture(t, ":memory:", "list-denial", policy, Limits{})
	defer db.Close()
	create(t, s, "project-one", "a-public-list")
	create(t, s, "project-one", "private-list")
	before := storageSnapshot(t, db)
	w := request(s, "project-one", "GET", "/storage/v1/b?project=project-one", "", "")
	mustStatus(t, w, 403)
	if strings.Contains(w.Body.String(), "a-public-list") || strings.Contains(w.Body.String(), "private-list") {
		t.Fatal("partial listing disclosed on policy refusal")
	}
	if storageSnapshot(t, db) != before {
		t.Fatal("denied listing mutated state")
	}
}
