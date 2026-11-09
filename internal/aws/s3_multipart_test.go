package aws

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func mpStart(t *testing.T, s *S3Store, key string) S3Upload {
	t.Helper()
	u, e := s.CreateUpload(context.Background(), "versions-test", key, "text/plain")
	if e != nil {
		t.Fatal(e)
	}
	return u
}
func mpPart(t *testing.T, s *S3Store, u S3Upload, n int, b []byte) S3Part {
	t.Helper()
	p, e := s.UploadPart(context.Background(), "versions-test", u.Key, u.ID, n, b)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestMultipartAtomicVersionAndCleanup(t *testing.T) {
	db, s := s3TestState(t)
	ctx := context.Background()
	old := s3Put(t, s, "old")
	s3Enable(t, s, "Enabled")
	u := mpStart(t, s, "evidence")
	a := bytes.Repeat([]byte("a"), s3MinPart)
	p := mpPart(t, s, u, 1, a)
	q := mpPart(t, s, u, 3, []byte("end"))
	if err := s.DeleteBucket("versions-test"); core.Code(err) != "BucketNotEmpty" {
		t.Fatal(err)
	}
	for _, bad := range [][]S3Part{nil, {q, p}, {p, p}, {{Number: 1, ETag: `"00000000000000000000000000000000"`}}, {p, {Number: 2, ETag: q.ETag}}} {
		if _, err := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, bad); err == nil {
			t.Fatal("invalid completion succeeded")
		}
		if v, e := s.Get("versions-test", "evidence", ""); e != nil || string(v.Data) != "old" {
			t.Fatal("failed completion changed object", e)
		}
	}
	if _, err := s.CompleteUpload(ctx, "versions-test", "neighbor", u.ID, []S3Part{q}); core.Code(err) != "NoSuchUpload" {
		t.Fatal(err)
	}
	s3SQL(t, db, `CREATE TRIGGER mp_fail BEFORE INSERT ON objects BEGIN SELECT RAISE(IGNORE); END`)
	if _, err := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p, q}); core.Code(err) != "InternalError" {
		t.Fatal("ignored current write acknowledged", err)
	}
	parts, e := s.Parts(ctx, "versions-test", u.Key, u.ID)
	if e != nil || len(parts) != 2 {
		t.Fatal("rollback lost parts", e)
	}
	s3SQL(t, db, "DROP TRIGGER mp_fail")
	v, e := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p, q})
	if e != nil || !bytes.Equal(v.Data, append(a, []byte("end")...)) || !strings.HasSuffix(v.ETag, `-2"`) || v.ID == "" || v.ID == old.ID {
		t.Fatal("completion", e, v.ID, v.ETag)
	}
	got, e := db.Get("versions-test", "evidence")
	if e != nil || !bytes.Equal(got.Data, v.Data) || got.ETag != v.ETag {
		t.Fatal("current view differs", e)
	}
	if _, e = s.Parts(ctx, "versions-test", u.Key, u.ID); core.Code(e) != "NoSuchUpload" {
		t.Fatal(e)
	}
	history, e := s.Versions("versions-test")
	if e != nil || len(history) != 2 {
		t.Fatal(history, e)
	}
	for _, version := range history {
		if _, e = s.Delete("versions-test", version.Key, version.ID); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.DeleteBucket("versions-test"); e != nil {
		t.Fatal(e)
	}
}
func TestMultipartLimitsReplacementIntegrityAndAbort(t *testing.T) {
	db, s := s3TestState(t)
	ctx := context.Background()
	u := mpStart(t, s, "limits")
	p := mpPart(t, s, u, 1, []byte("short"))
	q := mpPart(t, s, u, 2, []byte("last"))
	if _, e := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p, q}); core.Code(e) != "EntityTooSmall" {
		t.Fatal(e)
	}
	for _, n := range []int{0, 10001} {
		if _, e := s.UploadPart(ctx, "versions-test", u.Key, u.ID, n, nil); core.Code(e) != "InvalidArgument" {
			t.Fatal(e)
		}
	}
	if _, e := s.UploadPart(ctx, "versions-test", u.Key, u.ID, 1, make([]byte, core.MaxObjectBytes+1)); core.Code(e) != "EntityTooLarge" {
		t.Fatal(e)
	}
	large := mpPart(t, s, u, 1, make([]byte, core.MaxObjectBytes))
	if _, e := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{large, q}); core.Code(e) != "EntityTooLarge" {
		t.Fatal(e)
	}
	// A normal PUT must include multipart bytes in its aggregate quota.
	s3SQL(t, db, `UPDATE aws_s3_parts SET data=zeroblob(33554432) WHERE number=2`)
	second := mpStart(t, s, "other")
	mpPart(t, s, second, 1, make([]byte, core.MaxObjectBytes))
	mpPart(t, s, second, 2, make([]byte, core.MaxObjectBytes))
	mpPart(t, s, second, 3, make([]byte, core.MaxObjectBytes))
	if _, e := s.Put("versions-test", "blocked", "", []byte("x"), "", ""); core.Code(e) != "ServiceUnavailable" {
		t.Fatal("PUT ignored parts budget", e)
	}
	if _, e := s.UploadPart(ctx, "versions-test", u.Key, u.ID, 1, make([]byte, core.MaxObjectBytes)); e != nil {
		t.Fatal("same-size replacement failed at budget", e)
	}
	if _, e := s.UploadPart(ctx, "versions-test", u.Key, u.ID, 3, []byte("x")); core.Code(e) != "ServiceUnavailable" {
		t.Fatal(e)
	}
	s3SQL(t, db, `UPDATE aws_s3_parts SET data=X'01' WHERE upload='`+u.ID+`' AND number=1`)
	if _, e := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{large}); core.Code(e) != "InternalError" {
		t.Fatal("corrupt data accepted", e)
	}
	if e := s.AbortUpload(ctx, "versions-test", u.Key, u.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.AbortUpload(ctx, "versions-test", u.Key, u.ID); core.Code(e) != "NoSuchUpload" {
		t.Fatal(e)
	}
	if e := s.AbortUpload(ctx, "versions-test", second.Key, second.ID); e != nil {
		t.Fatal(e)
	}
	uploads, e := s.Uploads(ctx, "versions-test")
	if e != nil || len(uploads) != 0 {
		t.Fatal(uploads, e)
	}
	if _, e := s.Put("versions-test", "unblocked", "", []byte("x"), "", ""); e != nil {
		t.Fatal(e)
	}
}
func TestMultipartPersistenceConcurrencyAndStorageFailures(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "state.db")
	db, e := core.New(file)
	if e != nil {
		t.Fatal(e)
	}
	s, e := NewS3Store(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Create("versions-test", "us-east-1"); e != nil {
		t.Fatal(e)
	}
	u := mpStart(t, s, "persist")
	p := mpPart(t, s, u, 1, []byte("persistent"))
	db.Close()
	db, e = core.New(file)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s, e = NewS3Store(db.Database())
	if e != nil {
		t.Fatal(e)
	}
	parts, e := s.Parts(ctx, "versions-test", u.Key, u.ID)
	if e != nil || len(parts) != 1 || parts[0].ETag != p.ETag {
		t.Fatal(parts, e)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, missing := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if core.Code(err) == "NoSuchUpload" {
			missing++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || missing != 1 {
		t.Fatal(success, missing)
	}
	u = mpStart(t, s, "fault")
	p = mpPart(t, s, u, 1, []byte("keep"))
	for _, table := range []string{"aws_s3_uploads", "aws_s3_parts", "aws_s3_versions", "objects"} {
		for _, action := range []string{"ABORT", "IGNORE"} {
			target, event := table, "INSERT"
			if table == "aws_s3_uploads" {
				event = "DELETE"
			}
			s3SQL(t, db, fmt.Sprintf("CREATE TRIGGER mp_fail BEFORE %s ON %s BEGIN SELECT RAISE(%s%s); END", event, target, action, map[string]string{"ABORT": ",'refused'"}[action]))
			var err error
			if table == "aws_s3_parts" {
				_, err = s.UploadPart(ctx, "versions-test", u.Key, u.ID, 1, []byte("replacement"))
			} else {
				_, err = s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p})
			}
			if err == nil {
				t.Fatal("fault acknowledged", table, action)
			}
			s3SQL(t, db, "DROP TRIGGER mp_fail")
			parts, err = s.Parts(ctx, "versions-test", u.Key, u.ID)
			if err != nil || len(parts) != 1 || parts[0].ETag != p.ETag {
				t.Fatal("fault changed parts", err)
			}
		}
	}
	cancel, c := context.WithCancel(ctx)
	c()
	if _, e = s.CreateUpload(cancel, "versions-test", "cancel", ""); e == nil {
		t.Fatal("cancelled mutation succeeded")
	}
	for _, key := range []string{"", strings.Repeat("x", 1025), "\x00"} {
		if _, e = s.CreateUpload(ctx, "versions-test", key, ""); core.Code(e) != "InvalidArgument" {
			t.Fatal(e)
		}
	}
	if _, e = s.CreateUpload(ctx, "versions-test", "ok", strings.Repeat("x", 1025)); core.Code(e) != "InvalidArgument" {
		t.Fatal(e)
	}
	if _, e = s.Uploads(ctx, "missing"); core.Code(e) != "NoSuchBucket" {
		t.Fatal(e)
	}
	if _, e = s.Parts(ctx, "versions-test", u.Key, "missing"); core.Code(e) != "NoSuchUpload" {
		t.Fatal(e)
	}
}
