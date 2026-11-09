package aws

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestMultipartRecreatedBucketRejectedInsideTransaction(t *testing.T) {
	for _, region := range []string{"us-east-1", "us-west-2"} {
		t.Run(region, func(t *testing.T) {
			db, s := s3TestState(t)
			body := &s3PausedBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader("")}
			r := httptest.NewRequest("POST", "/versions-test/key?uploads", body)
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); (&Gateway{Store: db, S3: s}).ServeHTTP(w, r) }()
			select {
			case <-body.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("request never reached body read")
			}
			if e := s.DeleteBucket("versions-test"); e != nil {
				t.Fatal(e)
			}
			if e := s.Create("versions-test", region); e != nil {
				t.Fatal(e)
			}
			close(body.release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("request did not finish")
			}
			want := 409
			if region != "us-east-1" {
				want = 400
			}
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			uploads, e := s.Uploads(context.Background(), "versions-test")
			if e != nil || len(uploads) != 0 {
				t.Fatal("replacement bucket was mutated", uploads, e)
			}
		})
	}
}
func TestMultipartLegacyIgnoredImportRollsBackAndChronologicalList(t *testing.T) {
	db, s := s3TestState(t)
	ctx := context.Background()
	if _, e := db.Put("versions-test", "legacy", "text/plain", []byte("legacy"), "", ""); e != nil {
		t.Fatal(e)
	}
	s3Enable(t, s, "Enabled")
	u := mpStart(t, s, "legacy")
	p := mpPart(t, s, u, 1, []byte("replacement"))
	s3SQL(t, db, `CREATE TRIGGER selective_ignore BEFORE INSERT ON aws_s3_versions WHEN NEW.version='null' BEGIN SELECT RAISE(IGNORE); END`)
	if _, e := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p}); core.Code(e) != "InternalError" {
		t.Fatal("selective failure acknowledged", e)
	}
	if v, e := db.Get("versions-test", "legacy"); e != nil || string(v.Data) != "legacy" {
		t.Fatal(e)
	}
	parts, e := s.Parts(ctx, "versions-test", u.Key, u.ID)
	if e != nil || len(parts) != 1 {
		t.Fatal(e)
	}
	s3SQL(t, db, "DROP TRIGGER selective_ignore")
	if _, e = s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p}); e != nil {
		t.Fatal(e)
	}
	versions, e := s.Versions("versions-test")
	if e != nil || len(versions) != 2 {
		t.Fatal(versions, e)
	}
	old, e := s.Get("versions-test", "legacy", "null")
	if e != nil || string(old.Data) != "legacy" {
		t.Fatal("lost legacy history", e)
	}
	a := mpStart(t, s, "order")
	b := mpStart(t, s, "order")
	s3SQL(t, db, `UPDATE aws_s3_uploads SET initiated='2026-01-01T00:00:00.100000000Z',id='ffffffffffffffffffffffffffffffff' WHERE id='`+a.ID+`'`)
	s3SQL(t, db, `UPDATE aws_s3_uploads SET initiated='2026-01-01T00:00:00.200000000Z',id='00000000000000000000000000000000' WHERE id='`+b.ID+`'`)
	rows, e := s.Uploads(ctx, "versions-test")
	if e != nil || len(rows) != 2 || rows[0].ID != "ffffffffffffffffffffffffffffffff" {
		t.Fatal(rows, e)
	}
}
func TestMultipartQuotasAndUnreadableStateFailClosed(t *testing.T) {
	db, s := s3TestState(t)
	ctx := context.Background()
	u := mpStart(t, s, "fault")
	s3SQL(t, db, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<999) INSERT INTO aws_s3_uploads(id,bucket,key,content_type,initiated) SELECT printf('%032d',x),'versions-test','quota','', '2026-01-01T00:00:00Z' FROM n`)
	if _, e := s.CreateUpload(ctx, "versions-test", "over", ""); core.Code(e) != "ServiceUnavailable" {
		t.Fatal(e)
	}
	s3SQL(t, db, `DELETE FROM aws_s3_uploads WHERE key='quota'`)
	s3SQL(t, db, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO aws_s3_parts(upload,number,etag,modified,data) SELECT '`+u.ID+`',x,'"d41d8cd98f00b204e9800998ecf8427e"','2026-01-01T00:00:00Z',X'' FROM n`)
	if _, e := s.Put("versions-test", "over", "", nil, "", ""); core.Code(e) != "ServiceUnavailable" {
		t.Fatal(e)
	}
	p, e := s.UploadPart(ctx, "versions-test", u.Key, u.ID, 1, []byte("replace"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p}); e != nil {
		t.Fatal("completion did not free omitted rows", e)
	}
	u = mpStart(t, s, "corrupt")
	mpPart(t, s, u, 1, []byte("x"))
	s3SQL(t, db, `UPDATE aws_s3_uploads SET initiated='corrupt' WHERE id='`+u.ID+`'`)
	if _, e = s.Uploads(ctx, "versions-test"); e == nil {
		t.Fatal("invalid timestamp returned")
	}
	if _, e = s.Parts(ctx, "versions-test", u.Key, u.ID); e == nil {
		t.Fatal("invalid upload accepted")
	}
	s3SQL(t, db, `UPDATE aws_s3_uploads SET initiated='2026-01-01T00:00:00Z'; UPDATE aws_s3_parts SET modified='corrupt'`)
	if _, e = s.Parts(ctx, "versions-test", u.Key, u.ID); e == nil {
		t.Fatal("invalid part timestamp returned")
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	operations := []func() error{func() error { _, e := s.CreateUpload(ctx, "versions-test", "closed", ""); return e }, func() error { _, e := s.Uploads(ctx, "versions-test"); return e }, func() error { _, e := s.Parts(ctx, "versions-test", u.Key, u.ID); return e }, func() error { _, e := s.UploadPart(ctx, "versions-test", u.Key, u.ID, 1, nil); return e }, func() error { return s.AbortUpload(ctx, "versions-test", u.Key, u.ID) }, func() error { _, e := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p}); return e }}
	for _, op := range operations {
		if op() == nil {
			t.Fatal("closed state succeeded")
		}
	}
}
