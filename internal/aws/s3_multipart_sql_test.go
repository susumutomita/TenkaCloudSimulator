package aws

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Compare persisted rows, including peer buckets and existing object/history,
// rather than treating an error response as evidence of a successful rollback.
func mpSQLSnapshot(t *testing.T, db *core.Store) map[string][][]any {
	t.Helper()
	state := make(map[string][][]any)
	for table, order := range map[string]string{
		"buckets": "name", "objects": "bucket,key", "aws_s3_versions": "seq",
		"aws_s3_uploads": "id", "aws_s3_parts": "upload,number",
	} {
		state[table] = [][]any{}
		rows, err := db.Database().Query("SELECT * FROM " + table + " ORDER BY " + order)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if data, ok := value.([]byte); ok {
					values[i] = append([]byte{}, data...)
				}
			}
			state[table] = append(state[table], values)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func mpSQLUnchanged(t *testing.T, db *core.Store, before map[string][][]any) {
	t.Helper()
	after := mpSQLSnapshot(t, db)
	for table, rows := range before {
		if !reflect.DeepEqual(rows, after[table]) {
			t.Fatalf("failed multipart operation changed %s", table)
		}
	}
}

func mpSQLFixture(t *testing.T) (*core.Store, *S3Store, S3Upload, S3Part) {
	t.Helper()
	db, s := s3TestState(t)
	if err := s.Create("multipart-peer", "us-west-2"); err != nil {
		t.Fatal(err)
	}
	for _, bucket := range []string{"versions-test", "multipart-peer"} {
		if _, err := s.Put(bucket, "evidence", "text/plain", []byte("old "+bucket), "", ""); err != nil {
			t.Fatal(err)
		}
		u, err := s.CreateUpload(context.Background(), bucket, "neighbor", "text/plain")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.UploadPart(context.Background(), bucket, u.Key, u.ID, 1, []byte(bucket)); err != nil {
			t.Fatal(err)
		}
	}
	u := mpStart(t, s, "evidence")
	return db, s, u, mpPart(t, s, u, 1, []byte("replacement"))
}

func TestMultipartSQLRejectedCreatePreservesExistingScopes(t *testing.T) {
	for _, raise := range []string{"ABORT,'create refused'", "IGNORE"} {
		t.Run(raise, func(t *testing.T) {
			db, s, _, _ := mpSQLFixture(t)
			before := mpSQLSnapshot(t, db)
			s3SQL(t, db, fmt.Sprintf(`CREATE TRIGGER mp_sql_fail BEFORE INSERT ON aws_s3_uploads WHEN NEW.key='candidate' BEGIN SELECT RAISE(%s); END`, raise))
			if _, err := s.CreateUpload(context.Background(), "versions-test", "candidate", "text/plain"); err == nil {
				t.Fatal("unacknowledged upload creation succeeded")
			}
			mpSQLUnchanged(t, db, before)
			s3SQL(t, db, "DROP TRIGGER mp_sql_fail")
			mpStart(t, s, "candidate")
		})
	}
}

func TestMultipartSQLDeleteAndCascadeFailuresRollBack(t *testing.T) {
	for _, operation := range []string{"replace", "abort", "complete"} {
		for _, raise := range []string{"ABORT,'delete refused'", "IGNORE"} {
			t.Run(operation+"/"+raise, func(t *testing.T) {
				db, s, u, p := mpSQLFixture(t)
				before := mpSQLSnapshot(t, db)
				// Fault the child deletion, including the implicit ON DELETE CASCADE.
				s3SQL(t, db, fmt.Sprintf(`CREATE TRIGGER mp_sql_fail BEFORE DELETE ON aws_s3_parts WHEN OLD.upload='%s' BEGIN SELECT RAISE(%s); END`, u.ID, raise))
				perform := func() error {
					switch operation {
					case "replace":
						_, err := s.UploadPart(context.Background(), "versions-test", u.Key, u.ID, 1, []byte("new part"))
						return err
					case "abort":
						return s.AbortUpload(context.Background(), "versions-test", u.Key, u.ID)
					default:
						_, err := s.CompleteUpload(context.Background(), "versions-test", u.Key, u.ID, []S3Part{p})
						return err
					}
				}
				if err := perform(); err == nil {
					t.Fatal("refused part deletion was acknowledged")
				}
				mpSQLUnchanged(t, db, before)
				s3SQL(t, db, "DROP TRIGGER mp_sql_fail")
				if err := perform(); err != nil {
					t.Fatal("operation did not recover after removing fault", err)
				}
			})
		}
	}
}

func TestMultipartSQLQueryFailuresPreserveStateAndRecover(t *testing.T) {
	for _, operation := range []string{"bucket", "count", "upload", "uploads", "parts", "complete", "quota"} {
		t.Run(operation, func(t *testing.T) {
			db, s, u, p := mpSQLFixture(t)
			before := mpSQLSnapshot(t, db)
			ctx := context.Background()
			var table, column string
			var perform func() error
			switch operation {
			case "bucket":
				table, column = "buckets", "region"
				perform = func() error { _, err := s.CreateUpload(ctx, "versions-test", "candidate", ""); return err }
			case "count":
				table = "aws_s3_uploads"
				perform = func() error { _, err := s.CreateUpload(ctx, "versions-test", "candidate", ""); return err }
			case "upload":
				table, column = "aws_s3_uploads", "content_type"
				perform = func() error { return s.AbortUpload(ctx, "versions-test", u.Key, u.ID) }
			case "uploads":
				table, column = "aws_s3_uploads", "key"
				perform = func() error { _, err := s.Uploads(ctx, "versions-test"); return err }
			case "parts":
				table, column = "aws_s3_parts", "modified"
				perform = func() error { _, err := s.Parts(ctx, "versions-test", u.Key, u.ID); return err }
			case "complete":
				table, column = "aws_s3_parts", "etag"
				perform = func() error { _, err := s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p}); return err }
			case "quota":
				// The old part has already been deleted when quota's history query fails.
				table = "aws_s3_versions"
				perform = func() error { _, err := s.UploadPart(ctx, "versions-test", u.Key, u.ID, 1, []byte("new")); return err }
			}
			var damage, repair string
			if column == "" {
				damage = "ALTER TABLE " + table + " RENAME TO mp_sql_unavailable"
				repair = "ALTER TABLE mp_sql_unavailable RENAME TO " + table
			} else {
				damage = "ALTER TABLE " + table + " RENAME COLUMN " + column + " TO mp_sql_unavailable"
				repair = "ALTER TABLE " + table + " RENAME COLUMN mp_sql_unavailable TO " + column
			}
			s3SQL(t, db, damage)
			if err := perform(); err == nil {
				t.Fatal("unreadable SQL state acknowledged")
			}
			s3SQL(t, db, repair)
			mpSQLUnchanged(t, db, before)
			if err := perform(); err != nil {
				t.Fatal("operation did not recover after restoring schema", err)
			}
		})
	}
}

func TestMultipartSQLCorruptRowsFailWithoutMutatingOtherScopes(t *testing.T) {
	db, s, u, _ := mpSQLFixture(t)
	ctx := context.Background()
	before := mpSQLSnapshot(t, db)
	// SQLite permits NULL in a non-integer PRIMARY KEY in an ordinary table.
	s3SQL(t, db, `INSERT INTO aws_s3_uploads(id,bucket,key,content_type,initiated) VALUES(NULL,'versions-test','corrupt','','2026-01-01T00:00:00Z')`)
	corrupt := mpSQLSnapshot(t, db)
	if _, err := s.Uploads(ctx, "versions-test"); err == nil {
		t.Fatal("NULL upload ID returned as a valid upload")
	}
	if rows, err := s.Uploads(ctx, "multipart-peer"); err != nil || len(rows) != 1 {
		t.Fatal("corrupt owner row affected peer list", rows, err)
	}
	mpSQLUnchanged(t, db, corrupt)
	s3SQL(t, db, "DELETE FROM aws_s3_uploads WHERE id IS NULL")
	// An on-disk row outside the numeric domain must fail Scan, not become part 0.
	s3SQL(t, db, `PRAGMA ignore_check_constraints=ON`)
	if _, err := db.Database().Exec("UPDATE aws_s3_parts SET number='corrupt' WHERE upload=?", u.ID); err != nil {
		t.Fatal(err)
	}
	corrupt = mpSQLSnapshot(t, db)
	if _, err := s.Parts(ctx, "versions-test", u.Key, u.ID); err == nil {
		t.Fatal("nonnumeric part number returned as valid")
	}
	mpSQLUnchanged(t, db, corrupt)
	if _, err := db.Database().Exec("UPDATE aws_s3_parts SET number=1 WHERE upload=?", u.ID); err != nil {
		t.Fatal(err)
	}
	s3SQL(t, db, `PRAGMA ignore_check_constraints=OFF`)
	mpSQLUnchanged(t, db, before)
	if parts, err := s.Parts(ctx, "versions-test", u.Key, u.ID); err != nil || len(parts) != 1 || parts[0].Number != 1 {
		t.Fatal("repair did not restore readable parts", parts, err)
	}
}

func TestMultipartSQLCancelledAndMissingParentDoNotMutate(t *testing.T) {
	db, s, u, p := mpSQLFixture(t)
	before := mpSQLSnapshot(t, db)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, operation := range []string{"part", "parts", "abort", "complete"} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprint(operation, "/missing=", missing), func(t *testing.T) {
				ctx, bucket := cancelled, "versions-test"
				if missing {
					ctx, bucket = context.Background(), "missing-parent"
				}
				var err error
				switch operation {
				case "part":
					_, err = s.UploadPart(ctx, bucket, u.Key, u.ID, 1, []byte("new"))
				case "parts":
					_, err = s.Parts(ctx, bucket, u.Key, u.ID)
				case "abort":
					err = s.AbortUpload(ctx, bucket, u.Key, u.ID)
				case "complete":
					_, err = s.CompleteUpload(ctx, bucket, u.Key, u.ID, []S3Part{p})
				}
				if missing && core.Code(err) != "NoSuchBucket" || !missing && !errors.Is(err, context.Canceled) {
					t.Fatal("unexpected refusal", err)
				}
				mpSQLUnchanged(t, db, before)
			})
		}
	}
}

func TestMultipartSQLSharedRowQuotaAcrossBuckets(t *testing.T) {
	db, s, u, p := mpSQLFixture(t)
	ctx := context.Background()
	s3Enable(t, s, "Enabled")
	peers, err := s.Uploads(ctx, "multipart-peer")
	if err != nil || len(peers) != 1 {
		t.Fatal(peers, err)
	}
	peer := peers[0]
	// Two retained versions and 9,998 parts share the process-wide row budget.
	if _, err = db.Database().Exec(`WITH RECURSIVE n(x) AS (VALUES(2) UNION ALL SELECT x+1 FROM n WHERE x<9996)
 INSERT INTO aws_s3_parts(upload,number,etag,modified,data)
 SELECT ?,x,'"d41d8cd98f00b204e9800998ecf8427e"','2026-01-01T00:00:00Z',X'' FROM n`, peer.ID); err != nil {
		t.Fatal(err)
	}
	before := mpSQLSnapshot(t, db)
	if _, err = s.UploadPart(ctx, "versions-test", u.Key, u.ID, 2, nil); core.Code(err) != "ServiceUnavailable" {
		t.Fatal("peer bucket's parts did not consume shared quota", err)
	}
	mpSQLUnchanged(t, db, before)
	peerParts, err := s.Parts(ctx, "multipart-peer", peer.Key, peer.ID)
	if err != nil || len(peerParts) != 9996 {
		t.Fatal("peer parts unavailable at quota", err)
	}
	// Completion exchanges one part for one version, retaining the old version.
	if _, err = s.CompleteUpload(ctx, "versions-test", u.Key, u.ID, []S3Part{p}); err != nil {
		t.Fatal("completion could not exchange rows at quota", err)
	}
	if after, err := s.Parts(ctx, "multipart-peer", peer.Key, peer.ID); err != nil || !reflect.DeepEqual(after, peerParts) {
		t.Fatal("completion changed another bucket's parts", err)
	}
	next := mpStart(t, s, "next")
	if _, err = s.UploadPart(ctx, "versions-test", next.Key, next.ID, 1, nil); core.Code(err) != "ServiceUnavailable" {
		t.Fatal("completion released unrelated quota", err)
	}
	if err = s.AbortUpload(ctx, "multipart-peer", peer.Key, peer.ID); err != nil {
		t.Fatal(err)
	}
	mpPart(t, s, next, 1, nil)
	if v, err := s.Get("multipart-peer", "evidence", ""); err != nil || string(v.Data) != "old multipart-peer" {
		t.Fatal("releasing peer parts changed its object", err)
	}
}
