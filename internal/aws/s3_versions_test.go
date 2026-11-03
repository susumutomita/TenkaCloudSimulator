package aws

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func s3TestState(t *testing.T) (*core.Store, *S3Store) {
	t.Helper()
	db, err := core.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := NewS3Store(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Create("versions-test", "us-east-1"); err != nil {
		t.Fatal(err)
	}
	return db, s
}
func s3Enable(t *testing.T, s *S3Store, status string) {
	t.Helper()
	if _, err := s.db.Exec("UPDATE aws_s3_config SET status=? WHERE bucket='versions-test'", status); err != nil {
		t.Fatal(err)
	}
}
func s3Put(t *testing.T, s *S3Store, value string) S3Version {
	t.Helper()
	v, err := s.Put("versions-test", "evidence", "text/plain", []byte(value), "", "")
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestS3VersionLifecycleAndSharedCurrentView(t *testing.T) {
	db, s := s3TestState(t)
	legacy := s3Put(t, s, "original")
	if legacy.ID != "" {
		t.Fatal("unversioned response has version", legacy.ID)
	}
	s3Enable(t, s, "Enabled")
	first := s3Put(t, s, "first")
	second := s3Put(t, s, "second")
	if first.ID == "" || first.ID == "null" || first.ID == second.ID {
		t.Fatal("versions not unique")
	}
	versions, err := s.Versions("versions-test")
	if err != nil || len(versions) != 3 || versions[2].ID != "null" || !versions[0].Latest || versions[1].Latest {
		t.Fatal(versions, err)
	}
	old, err := s.Get("versions-test", "evidence", "null")
	if err != nil || string(old.Data) != "original" {
		t.Fatal(old, err)
	}
	marker, err := s.Delete("versions-test", "evidence", "")
	if err != nil || !marker.Marker || marker.ID == "null" {
		t.Fatal(marker, err)
	}
	if _, err = db.Get("versions-test", "evidence"); core.Code(err) != "NoSuchKey" {
		t.Fatal("shared current view retained deleted object", err)
	}
	current, err := s.Get("versions-test", "evidence", "")
	if err != nil || !current.Marker || current.ID != marker.ID {
		t.Fatal(current, err)
	}
	if err = s.DeleteBucket("versions-test"); core.Code(err) != "BucketNotEmpty" {
		t.Fatal("history bucket deleted", err)
	}
	removed, err := s.Delete("versions-test", "evidence", marker.ID)
	if err != nil || !removed.Marker {
		t.Fatal(removed, err)
	}
	view, err := db.Get("versions-test", "evidence")
	if err != nil || string(view.Data) != "second" {
		t.Fatal("marker removal did not restore shared current view", view, err)
	}
	_, err = s.Delete("versions-test", "evidence", second.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err = db.Get("versions-test", "evidence")
	if err != nil || string(view.Data) != "first" {
		t.Fatal(view, err)
	}
	if _, err = s.Get("versions-test", "evidence", second.ID); core.Code(err) != "NoSuchVersion" {
		t.Fatal("removed version returned", err)
	}
	for _, id := range []string{first.ID, "null"} {
		if _, err = s.Delete("versions-test", "evidence", id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Delete("versions-test", "missing", "absent-version"); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteBucket("versions-test"); err != nil {
		t.Fatal(err)
	}
	if err = db.ForeignKeyCheck(); err != nil {
		t.Fatal(err)
	}
}
func TestS3SuspensionReplacesNullAndRetainsVersions(t *testing.T) {
	_, s := s3TestState(t)
	s3Enable(t, s, "Enabled")
	kept := s3Put(t, s, "kept")
	s3Enable(t, s, "Suspended")
	one := s3Put(t, s, "one")
	two := s3Put(t, s, "two")
	if one.ID != "null" || two.ID != "null" {
		t.Fatal("suspension returned numbered versions")
	}
	versions, err := s.Versions("versions-test")
	if err != nil || len(versions) != 2 || versions[1].ID != kept.ID {
		t.Fatal(versions, err)
	}
	marker, err := s.Delete("versions-test", "evidence", "")
	if err != nil || marker.ID != "null" || !marker.Marker {
		t.Fatal(marker, err)
	}
	if _, err = s.Delete("versions-test", "evidence", ""); err != nil {
		t.Fatal(err)
	}
	versions, err = s.Versions("versions-test")
	if err != nil || len(versions) != 2 {
		t.Fatal(versions, err)
	}
	s3Enable(t, s, "Enabled")
	s3Put(t, s, "enabled again")
	v, err := s.Get("versions-test", "evidence", "null")
	if err != nil || !v.Marker {
		t.Fatal("null marker lost when enabled", v, err)
	}
}
func TestS3VersionTransactionsAndConditionalWrites(t *testing.T) {
	db, s := s3TestState(t)
	s3Enable(t, s, "Enabled")
	original := s3Put(t, s, "unchanged")
	for _, condition := range [][2]string{{"\"wrong\"", ""}, {"", "*"}, {"", original.ETag}} {
		if _, err := s.Put("versions-test", "evidence", "text/plain", []byte("bad"), condition[0], condition[1]); core.Code(err) != "PreconditionFailed" {
			t.Fatal(err)
		}
	}
	if _, err := s.Put("versions-test", "absent", "text/plain", nil, "*", ""); core.Code(err) != "PreconditionFailed" {
		t.Fatal(err)
	}
	for _, trigger := range []string{
		`CREATE TRIGGER reject_history BEFORE INSERT ON aws_s3_versions BEGIN SELECT RAISE(ABORT,'write rejected'); END;`,
		`CREATE TRIGGER reject_view BEFORE UPDATE ON objects BEGIN SELECT RAISE(ABORT,'write rejected'); END;`,
	} {
		if _, err := db.Database().Exec(trigger); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put("versions-test", "evidence", "text/plain", []byte("must rollback"), original.ETag, ""); err == nil {
			t.Fatal("rejected write succeeded")
		}
		versions, err := s.Versions("versions-test")
		if err != nil || len(versions) != 1 {
			t.Fatal("partial history committed", versions, err)
		}
		view, err := db.Get("versions-test", "evidence")
		if err != nil || string(view.Data) != "unchanged" {
			t.Fatal(view, err)
		}
		if _, err = db.Database().Exec("DROP TRIGGER IF EXISTS reject_history; DROP TRIGGER IF EXISTS reject_view"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Database().Exec(`CREATE TRIGGER reject_delete BEFORE DELETE ON objects BEGIN SELECT RAISE(ABORT,'delete rejected'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("versions-test", "evidence", ""); err == nil {
		t.Fatal("failed marker/view transaction succeeded")
	}
	versions, err := s.Versions("versions-test")
	if err != nil || len(versions) != 1 {
		t.Fatal("partial delete marker persisted", versions, err)
	}
}
func TestS3QuotaCountsHistoryAndAllowsReplacement(t *testing.T) {
	_, s := s3TestState(t)
	s3Enable(t, s, "Enabled")
	body := bytes.Repeat([]byte("x"), core.MaxObjectBytes)
	ids := []string{}
	for range 8 {
		v, err := s.Put("versions-test", "evidence", "application/octet-stream", body, "", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v.ID)
	}
	if _, err := s.Put("versions-test", "other", "text/plain", []byte("x"), "", ""); core.Code(err) != "ServiceUnavailable" {
		t.Fatal("historical bytes bypassed quota", err)
	}
	if _, err := s.Delete("versions-test", "evidence", ids[0]); err != nil {
		t.Fatal(err)
	}
	s3Enable(t, s, "Suspended")
	if _, err := s.Put("versions-test", "evidence", "text/plain", body, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("versions-test", "evidence", "text/plain", []byte("replacement"), "", ""); err != nil {
		t.Fatal("replacement quota did not release null version", err)
	}
	if _, err := s.Put("versions-test", "evidence", "text/plain", make([]byte, core.MaxObjectBytes+1), "", ""); core.Code(err) != "EntityTooLarge" {
		t.Fatal(err)
	}
}
func TestS3LegacyPersistenceAndConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Create("versions-test", "us-east-1"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Put("versions-test", "evidence", "text/plain", []byte("legacy"), "", ""); err != nil {
		t.Fatal(err)
	}
	s, err := NewS3Store(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Database().Exec("INSERT INTO aws_s3_config(bucket,status) VALUES('versions-test','Enabled')"); err != nil {
		t.Fatal(err)
	}
	second, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := NewS3Store(second.Database())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			target := s
			if i%2 == 0 {
				target = s2
			}
			_, e := target.Put("versions-test", "evidence", "text/plain", []byte("new"), "", "")
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err = NewS3Store(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	versions, err := s.Versions("versions-test")
	if err != nil || len(versions) != 21 {
		t.Fatal(len(versions), err)
	}
	old, err := s.Get("versions-test", "evidence", "null")
	if err != nil || string(old.Data) != "legacy" {
		t.Fatal(old, err)
	}
	for _, op := range []func() error{
		func() error { _, e := s.Get("absent", "key", ""); return e }, func() error { _, e := s.Put("absent", "key", "", nil, "", ""); return e }, func() error { _, e := s.Delete("absent", "key", ""); return e }, func() error { _, e := s.Versions("absent"); return e }, func() error { return s.DeleteBucket("absent") },
	} {
		if core.Code(op()) != "NoSuchBucket" {
			t.Fatal("missing bucket accepted")
		}
	}
}

func TestS3NullReplacementReleasesFullQuotaAtomically(t *testing.T) {
	_, s := s3TestState(t)
	body := make([]byte, core.MaxObjectBytes)
	for i := range 8 {
		if _, err := s.Put("versions-test", fmt.Sprint(i), "application/octet-stream", body, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	// With no other version remaining, the current view must not be mistaken for
	// a second legacy object when the null history row is replaced.
	if _, err := s.Put("versions-test", "0", "text/plain", body, "", ""); err != nil {
		t.Fatal("full unversioned replacement rejected", err)
	}
	s3Enable(t, s, "Suspended")
	if _, err := s.Delete("versions-test", "0", ""); err != nil {
		t.Fatal("suspended null deletion did not release quota", err)
	}
	if _, err := s.Put("versions-test", "new", "text/plain", body, "", ""); err != nil {
		t.Fatal("removed null bytes still counted", err)
	}
}
func TestS3MarkerCountQuotaAndFailedReplacement(t *testing.T) {
	db, s := s3TestState(t)
	s3Enable(t, s, "Enabled")
	if _, err := db.Database().Exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO aws_s3_versions(bucket,key,version,marker,content_type,etag,modified,data) SELECT 'versions-test','evidence',printf('%d',x),1,'','','2026-11-03T00:00:00Z',X'' FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete("versions-test", "evidence", ""); core.Code(err) != "ServiceUnavailable" {
		t.Fatal("markers bypassed count quota", err)
	}
	if _, err := s.Put("versions-test", "evidence", "text/plain", nil, "", ""); core.Code(err) != "ServiceUnavailable" {
		t.Fatal("version count quota ignored", err)
	}
	versions, err := s.Versions("versions-test")
	if err != nil || len(versions) != 10000 {
		t.Fatal(len(versions), err)
	}
	if _, err = s.Delete("versions-test", "evidence", "10000"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delete("versions-test", "evidence", ""); err != nil {
		t.Fatal("erased marker did not release quota", err)
	}
}
