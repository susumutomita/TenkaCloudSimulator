package aws

import (
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func s3SQL(t *testing.T, db *core.Store, sql string) {
	t.Helper()
	if _, err := db.Database().Exec(sql); err != nil {
		t.Fatal(err)
	}
}
func TestS3LegacyImportFailurePreservesOriginal(t *testing.T) {
	db, s := s3TestState(t)
	if _, err := db.Put("versions-test", "evidence", "text/plain", []byte("legacy bytes"), "", ""); err != nil {
		t.Fatal(err)
	}
	s3SQL(t, db, `CREATE TRIGGER deny_import BEFORE INSERT ON aws_s3_versions BEGIN SELECT RAISE(ABORT,'import denied'); END;`)
	operations := []func() error{
		func() error { _, e := s.Put("versions-test", "evidence", "text/plain", nil, "", ""); return e }, func() error { _, e := s.Get("versions-test", "evidence", ""); return e }, func() error { _, e := s.Delete("versions-test", "evidence", ""); return e }, func() error { _, e := s.Versions("versions-test"); return e },
	}
	for _, op := range operations {
		if op() == nil {
			t.Fatal("failed history import succeeded")
		}
		current, err := db.Get("versions-test", "evidence")
		if err != nil || string(current.Data) != "legacy bytes" {
			t.Fatal("failed import changed original bytes", current, err)
		}
	}
}
func TestS3FailedNullMutationPreservesHistoryAndCurrent(t *testing.T) {
	db, s := s3TestState(t)
	s3Put(t, s, "original")
	s3SQL(t, db, `CREATE TRIGGER deny_history_delete BEFORE DELETE ON aws_s3_versions BEGIN SELECT RAISE(ABORT,'delete denied'); END;`)
	operations := []func() error{
		func() error { _, e := s.Put("versions-test", "evidence", "text/plain", nil, "", ""); return e }, func() error { _, e := s.Delete("versions-test", "evidence", ""); return e }, func() error { _, e := s.Delete("versions-test", "evidence", "null"); return e },
	}
	for _, op := range operations {
		if op() == nil {
			t.Fatal("rejected null mutation succeeded")
		}
	}
	s3Enable(t, s, "Suspended")
	if _, err := s.Delete("versions-test", "evidence", ""); err == nil {
		t.Fatal("suspended history failure succeeded")
	}
	s3SQL(t, db, `DROP TRIGGER deny_history_delete; CREATE TRIGGER deny_current_delete BEFORE DELETE ON objects BEGIN SELECT RAISE(ABORT,'current deletion denied'); END;`)
	if _, err := s.Put("versions-test", "evidence", "text/plain", nil, "", ""); err == nil {
		t.Fatal("partial null replacement succeeded")
	}
	if _, err := s.Delete("versions-test", "evidence", ""); err == nil {
		t.Fatal("partial suspended deletion succeeded")
	}
	current, err := db.Get("versions-test", "evidence")
	if err != nil || string(current.Data) != "original" {
		t.Fatal(current, err)
	}
	versions, err := s.Versions("versions-test")
	if err != nil || len(versions) != 1 || versions[0].Marker {
		t.Fatal(versions, err)
	}
	s3SQL(t, db, `DROP TRIGGER deny_current_delete; CREATE TRIGGER deny_marker BEFORE INSERT ON aws_s3_versions WHEN NEW.marker=1 BEGIN SELECT RAISE(ABORT,'marker denied'); END;`)
	s3Enable(t, s, "Enabled")
	if _, err = s.Delete("versions-test", "new-key", ""); err == nil {
		t.Fatal("rejected marker succeeded")
	}
	if _, err = s.Get("versions-test", "new-key", ""); core.Code(err) != "NoSuchKey" {
		t.Fatal("failed marker changed state", err)
	}
}
func TestS3CreateAndDeleteAreAtomicUnderStorageFailures(t *testing.T) {
	db, s := s3TestState(t)
	if err := s.Create("versions-test", "us-east-1"); core.Code(err) != "BucketAlreadyOwnedByYou" {
		t.Fatal(err)
	}
	if err := s.Create("versions-test", "eu-west-1"); core.Code(err) != "BucketAlreadyExists" {
		t.Fatal(err)
	}
	for _, trigger := range []string{
		`CREATE TRIGGER deny_create BEFORE INSERT ON buckets BEGIN SELECT RAISE(ABORT,'bucket denied'); END;`,
		`CREATE TRIGGER deny_config BEFORE INSERT ON aws_s3_config BEGIN SELECT RAISE(ABORT,'configuration denied'); END;`,
	} {
		s3SQL(t, db, trigger)
		if err := s.Create("failed-bucket", "us-east-1"); err == nil {
			t.Fatal("partial bucket create succeeded")
		}
		if _, err := db.Bucket("failed-bucket"); core.Code(err) != "NoSuchBucket" {
			t.Fatal("orphan bucket from failed create", err)
		}
		s3SQL(t, db, "DROP TRIGGER IF EXISTS deny_create;DROP TRIGGER IF EXISTS deny_config")
	}
	s3SQL(t, db, `CREATE TRIGGER deny_bucket_delete BEFORE DELETE ON buckets BEGIN SELECT RAISE(ABORT,'bucket deletion denied'); END;`)
	if err := s.DeleteBucket("versions-test"); err == nil {
		t.Fatal("failed bucket deletion succeeded")
	}
	if _, err := db.Bucket("versions-test"); err != nil {
		t.Fatal("rejected delete lost bucket", err)
	}
	s3SQL(t, db, "DROP TRIGGER deny_bucket_delete; DROP TABLE aws_s3_versions")
	if err := s.DeleteBucket("versions-test"); err == nil {
		t.Fatal("unreadable history treated as empty")
	}
}
func TestS3CorruptHistoryFailsClosed(t *testing.T) {
	for _, corruption := range []string{`UPDATE aws_s3_versions SET modified='not a timestamp'`, `PRAGMA ignore_check_constraints=ON; UPDATE aws_s3_versions SET marker='invalid boolean'`} {
		t.Run(corruption, func(t *testing.T) {
			db, s := s3TestState(t)
			s3Put(t, s, "preserved")
			s3SQL(t, db, corruption)
			for _, op := range []func() error{
				func() error { _, e := s.Get("versions-test", "evidence", ""); return e }, func() error { _, e := s.Get("versions-test", "evidence", "null"); return e }, func() error { _, e := s.Delete("versions-test", "evidence", "null"); return e }, func() error { _, e := s.Put("versions-test", "evidence", "text/plain", nil, "", ""); return e }, func() error { _, e := s.Versions("versions-test"); return e },
			} {
				if op() == nil {
					t.Fatal("corrupt history accepted")
				}
			}
			current, err := db.Get("versions-test", "evidence")
			if err != nil || string(current.Data) != "preserved" {
				t.Fatal("corrupt history changed current bytes", current, err)
			}
		})
	}
}
func s3Wire(t *testing.T, g *Gateway, method, path, body string, want int) {
	t.Helper()
	w := httptest.NewRecorder()
	g.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
	if w.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	if w.Code == 500 && (strings.Contains(w.Body.String(), "SELECT") || strings.Contains(w.Body.String(), "sqlite")) {
		t.Fatal("internal state details leaked")
	}
}
func TestS3ConfigurationFailureAndUnavailableProvider(t *testing.T) {
	db, s := s3TestState(t)
	g := &Gateway{Store: db, S3: s}
	s3SQL(t, db, `UPDATE aws_s3_config SET public_access='broken JSON'`)
	s3Wire(t, g, "GET", "/versions-test?publicAccessBlock", "", 500)
	s3SQL(t, db, `CREATE TRIGGER deny_config_update BEFORE UPDATE ON aws_s3_config BEGIN SELECT RAISE(ABORT,'private error'); END;`)
	s3Wire(t, g, "DELETE", "/versions-test?publicAccessBlock", "", 500)
	s3Wire(t, g, "PUT", "/versions-test?publicAccessBlock", `<PublicAccessBlockConfiguration/>`, 500)
	s3Wire(t, g, "PUT", "/versions-test?versioning", `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`, 500)
	s3SQL(t, db, `DROP TRIGGER deny_config_update;ALTER TABLE aws_s3_config RENAME COLUMN public_access TO unreadable`)
	s3Wire(t, g, "GET", "/versions-test?publicAccessBlock", "", 500)
	if err := db.Create("legacy-bucket", "us-east-1"); err != nil {
		t.Fatal(err)
	}
	s3Wire(t, g, "GET", "/legacy-bucket?versioning", "", 200)
	g.S3 = nil
	for _, path := range []string{"/versions-test/key", "/versions-test?versioning", "/versions-test?versions"} {
		s3Wire(t, g, "GET", path, "", 500)
	}
}
func TestS3BucketLimitUsesNativeServiceUnavailable(t *testing.T) {
	db, s := s3TestState(t)
	g := &Gateway{Store: db, S3: s}
	s3SQL(t, db, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO buckets SELECT printf('quota-%04d',x),'us-east-1','2026-11-03T00:00:00Z' FROM n`)
	s3Wire(t, g, "PUT", "/over-limit", "", 503)
	if _, err := db.Bucket("over-limit"); core.Code(err) != "NoSuchBucket" {
		t.Fatal("limit created a bucket", err)
	}
}

func TestS3EmptyObjectAndStrictXMLTokens(t *testing.T) {
	_, s := s3TestState(t)
	v, err := s.Put("versions-test", "empty", "application/octet-stream", nil, "", "")
	if err != nil || v.Size != 0 {
		t.Fatal(v, err)
	}
	got, err := s.Get("versions-test", "empty", "")
	if err != nil || len(got.Data) != 0 {
		t.Fatal(got, err)
	}
	allowed := map[string]bool{"Status": true}
	valid := []string{`<?xml version="1.0"?><!-- configuration --><VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`, `<VersioningConfiguration><!-- setting --><Status>Enabled</Status></VersioningConfiguration>`}
	for _, body := range valid {
		fields, e := s3XMLFields([]byte(body), "VersioningConfiguration", allowed)
		if e != nil || fields["Status"] != "Enabled" {
			t.Fatal(fields, e)
		}
	}
	invalid := []string{
		``, `<`, `<VersioningConfiguration>`, `<VersioningConfiguration><Status>`,
		`<VersioningConfiguration><Status>Enabled</Bad></VersioningConfiguration>`,
		`prefix<VersioningConfiguration/>`, `<VersioningConfiguration>ignored text</VersioningConfiguration>`,
		`<?unsafe data?><VersioningConfiguration/>`, `<VersioningConfiguration><?xml version="1.0"?></VersioningConfiguration>`,
		`<!DOCTYPE VersioningConfiguration><VersioningConfiguration/>`,
		`<VersioningConfiguration xmlns="https://unrecognized.invalid"><Status>Enabled</Status></VersioningConfiguration>`,
		`<Other/>`, `<VersioningConfiguration><Status extra="ignored">Enabled</Status></VersioningConfiguration>`,
		`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration><?xml version="1.0"?>`,
	}
	for _, body := range invalid {
		if _, e := s3XMLFields([]byte(body), "VersioningConfiguration", allowed); e == nil {
			t.Fatalf("unsafe/malformed XML accepted: %s", body)
		}
	}
	db, s := s3TestState(t)
	g := &Gateway{Store: db, S3: s}
	s3Wire(t, g, "PUT", "/versions-test?versioning", strings.Repeat("x", 4097), 400)
	s3Wire(t, g, "GET", "/versions-test?versions&prefix=%01", "", 400)
}

type s3PausedBody struct {
	entered, release chan struct{}
	reader           *strings.Reader
	once             sync.Once
}

func (b *s3PausedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.reader.Read(p)
}
func TestS3SlowConfigurationBodyDoesNotBlockState(t *testing.T) {
	db, s := s3TestState(t)
	g := &Gateway{Store: db, S3: s}
	body := &s3PausedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)}
	var release sync.Once
	unblock := func() { release.Do(func() { close(body.release) }) }
	defer unblock()
	w := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { g.ServeHTTP(w, httptest.NewRequest("PUT", "/versions-test?versioning", body)); close(finished) }()
	<-body.entered
	read := make(chan error, 1)
	go func() { _, err := db.Buckets(); read <- err }()
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		unblock()
		<-finished
		t.Fatal("slow request body held the shared state connection")
	}
	unblock()
	<-finished
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
