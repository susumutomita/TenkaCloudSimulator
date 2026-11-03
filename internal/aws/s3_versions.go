package aws

import (
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// S3Store owns version history. objects remains the shared current-object view;
// history and that view change in the same immediate SQLite transaction.
type S3Store struct{ db *sql.DB }
type S3Version struct {
	core.Object
	ID       string
	Marker   bool
	Sequence int64
	Latest   bool
}

func NewS3Store(db *sql.DB) (*S3Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_s3_config(bucket TEXT PRIMARY KEY REFERENCES buckets(name) ON DELETE CASCADE,status TEXT NOT NULL CHECK(status IN ('','Enabled','Suspended')),public_access TEXT);
 CREATE TABLE IF NOT EXISTS aws_s3_versions(seq INTEGER PRIMARY KEY AUTOINCREMENT,bucket TEXT NOT NULL REFERENCES buckets(name) ON DELETE RESTRICT,key TEXT NOT NULL,version TEXT NOT NULL,marker INTEGER NOT NULL CHECK(marker IN (0,1)),content_type TEXT NOT NULL,etag TEXT NOT NULL,modified TEXT NOT NULL,data BLOB NOT NULL,UNIQUE(bucket,key,version));
 CREATE INDEX IF NOT EXISTS aws_s3_version_keys ON aws_s3_versions(bucket,key,seq DESC);`)
	return &S3Store{db}, err
}
func s3BucketExists(tx *sql.Tx, bucket string) error {
	var n int
	err := tx.QueryRow("SELECT 1 FROM buckets WHERE name=?", bucket).Scan(&n)
	if err == sql.ErrNoRows {
		return core.Fail("NoSuchBucket", "The specified bucket does not exist.")
	}
	return err
}
func s3State(tx *sql.Tx, bucket string) (string, error) {
	if err := s3BucketExists(tx, bucket); err != nil {
		return "", err
	}
	var state string
	err := tx.QueryRow("SELECT status FROM aws_s3_config WHERE bucket=?", bucket).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return state, err
}

// Import the original unversioned object lazily, preserving existing state files.
func s3Import(tx *sql.Tx, bucket, key string) error {
	_, err := tx.Exec(`INSERT INTO aws_s3_versions(bucket,key,version,marker,content_type,etag,modified,data)
 SELECT bucket,key,'null',0,content_type,etag,modified,data FROM objects o WHERE bucket=? AND key=? AND NOT EXISTS(SELECT 1 FROM aws_s3_versions v WHERE v.bucket=o.bucket AND v.key=o.key)`, bucket, key)
	return err
}
func s3Scan(row interface{ Scan(...any) error }, key string) (S3Version, error) {
	v := S3Version{Object: core.Object{Key: key}}
	var modified string
	err := row.Scan(&v.Sequence, &v.ID, &v.Marker, &v.ContentType, &v.ETag, &modified, &v.Data)
	if err != nil {
		return v, err
	}
	v.Size = len(v.Data)
	v.Modified, err = time.Parse(time.RFC3339Nano, modified)
	return v, err
}

const s3VersionColumns = "seq,version,marker,content_type,etag,modified,data"

func s3Latest(tx *sql.Tx, bucket, key string) (S3Version, error) {
	return s3Scan(tx.QueryRow("SELECT "+s3VersionColumns+" FROM aws_s3_versions WHERE bucket=? AND key=? ORDER BY seq DESC LIMIT 1", bucket, key), key)
}
func s3Refresh(tx *sql.Tx, bucket, key string) error {
	v, err := s3Latest(tx, bucket, key)
	if err == sql.ErrNoRows || (err == nil && v.Marker) {
		_, err = tx.Exec("DELETE FROM objects WHERE bucket=? AND key=?", bucket, key)
		return err
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO objects(bucket,key,content_type,etag,modified,data) VALUES(?,?,?,?,?,?) ON CONFLICT(bucket,key) DO UPDATE SET content_type=excluded.content_type,etag=excluded.etag,modified=excluded.modified,data=excluded.data`, bucket, key, v.ContentType, v.ETag, v.Modified.Format(time.RFC3339Nano), v.Data)
	return err
}

// Logical quota counts every retained version/marker and untouched legacy object,
// excluding the duplicate current-object view. Replacement frees its null version.
func s3Quota(tx *sql.Tx, bytes, count int) error {
	var stored, versions int
	err := tx.QueryRow(`SELECT
 (SELECT coalesce(sum(length(data)),0) FROM aws_s3_versions)+(SELECT coalesce(sum(length(data)),0) FROM objects o WHERE NOT EXISTS(SELECT 1 FROM aws_s3_versions v WHERE v.bucket=o.bucket AND v.key=o.key)),
 (SELECT count(*) FROM aws_s3_versions)+(SELECT count(*) FROM objects o WHERE NOT EXISTS(SELECT 1 FROM aws_s3_versions v WHERE v.bucket=o.bucket AND v.key=o.key))`).Scan(&stored, &versions)
	if err != nil {
		return err
	}
	if stored+bytes > core.MaxStoredBytes || versions+count > core.MaxObjects {
		return core.Fail("ServiceUnavailable", "Simulator retained-version storage limit reached.")
	}
	return nil
}
func s3NewID() (string, error) {
	var b [16]byte
	_, err := rand.Read(b[:])
	return hex.EncodeToString(b[:]), err
}
func (s *S3Store) Put(bucket, key, contentType string, data []byte, match, none string) (S3Version, error) {
	if len(contentType) > 1024 {
		return S3Version{}, core.Fail("InvalidArgument", "Content-Type exceeds the 1024-byte simulator limit.")
	}
	if len(data) > core.MaxObjectBytes {
		return S3Version{}, core.Fail("EntityTooLarge", "Object exceeds the 8 MiB simulator limit.")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return S3Version{}, err
	}
	defer tx.Rollback()
	state, err := s3State(tx, bucket)
	if err != nil {
		return S3Version{}, err
	}
	if err = s3Import(tx, bucket, key); err != nil {
		return S3Version{}, err
	}
	old, err := s3Latest(tx, bucket, key)
	found := err == nil && !old.Marker
	if err != nil && err != sql.ErrNoRows {
		return S3Version{}, err
	}
	if (match != "" && (!found || (match != "*" && match != old.ETag))) || (none != "" && found && (none == "*" || none == old.ETag)) {
		return S3Version{}, core.Fail("PreconditionFailed", "An object precondition failed.")
	}
	id := "null"
	if state == "Enabled" {
		id, err = s3NewID()
		if err != nil {
			return S3Version{}, err
		}
	} else {
		if _, err = tx.Exec("DELETE FROM aws_s3_versions WHERE bucket=? AND key=? AND version='null'", bucket, key); err != nil {
			return S3Version{}, err
		}
		// Remove the replaced current view before quota accounting; otherwise a
		// last null version looks like an untouched legacy object and is counted
		// twice. The transaction restores both tables if the new write fails.
		if _, err = tx.Exec("DELETE FROM objects WHERE bucket=? AND key=?", bucket, key); err != nil {
			return S3Version{}, err
		}
	}
	if err = s3Quota(tx, len(data), 1); err != nil {
		return S3Version{}, err
	}
	sum := md5.Sum(data)
	v := S3Version{Object: core.Object{Key: key, Size: len(data), ETag: `"` + hex.EncodeToString(sum[:]) + `"`, Modified: time.Now().UTC(), ContentType: contentType, Data: data}, ID: id, Latest: true}
	if v.Data == nil {
		v.Data = []byte{}
	}
	if _, err = tx.Exec("INSERT INTO aws_s3_versions(bucket,key,version,marker,content_type,etag,modified,data) VALUES(?,?,?,0,?,?,?,?)", bucket, key, id, v.ContentType, v.ETag, v.Modified.Format(time.RFC3339Nano), v.Data); err != nil {
		return S3Version{}, err
	}
	if err = s3Refresh(tx, bucket, key); err != nil {
		return S3Version{}, err
	}
	if err = tx.Commit(); err != nil {
		return S3Version{}, err
	}
	if state == "" {
		v.ID = ""
	}
	return v, nil
}
func (s *S3Store) Get(bucket, key, id string) (S3Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return S3Version{}, err
	}
	defer tx.Rollback()
	state, err := s3State(tx, bucket)
	if err != nil {
		return S3Version{}, err
	}
	if err = s3Import(tx, bucket, key); err != nil {
		return S3Version{}, err
	}
	var v S3Version
	if id == "" {
		v, err = s3Latest(tx, bucket, key)
	} else {
		v, err = s3Scan(tx.QueryRow("SELECT "+s3VersionColumns+" FROM aws_s3_versions WHERE bucket=? AND key=? AND version=?", bucket, key, id), key)
	}
	if err == sql.ErrNoRows {
		code := "NoSuchKey"
		if id != "" {
			code = "NoSuchVersion"
		}
		return v, core.Fail(code, "The specified object or version does not exist.")
	}
	if err != nil {
		return v, err
	}
	if err = tx.Commit(); err != nil {
		return S3Version{}, err
	}
	if state == "" && id == "" {
		v.ID = ""
	}
	return v, nil
}
func (s *S3Store) Delete(bucket, key, id string) (S3Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return S3Version{}, err
	}
	defer tx.Rollback()
	state, err := s3State(tx, bucket)
	if err != nil {
		return S3Version{}, err
	}
	if err = s3Import(tx, bucket, key); err != nil {
		return S3Version{}, err
	}
	v := S3Version{ID: id}
	if id != "" {
		v, err = s3Scan(tx.QueryRow("SELECT "+s3VersionColumns+" FROM aws_s3_versions WHERE bucket=? AND key=? AND version=?", bucket, key, id), key)
		if err == sql.ErrNoRows {
			v = S3Version{ID: id}
		} else if err != nil {
			return v, err
		}
		if _, err = tx.Exec("DELETE FROM aws_s3_versions WHERE bucket=? AND key=? AND version=?", bucket, key, id); err != nil {
			return v, err
		}
	} else if state == "" {
		if _, err = tx.Exec("DELETE FROM aws_s3_versions WHERE bucket=? AND key=?", bucket, key); err != nil {
			return v, err
		}
	} else {
		v.ID = "null"
		v.Marker = true
		if state == "Enabled" {
			v.ID, err = s3NewID()
			if err != nil {
				return v, err
			}
		} else {
			if _, err = tx.Exec("DELETE FROM aws_s3_versions WHERE bucket=? AND key=? AND version='null'", bucket, key); err != nil {
				return v, err
			}
			if _, err = tx.Exec("DELETE FROM objects WHERE bucket=? AND key=?", bucket, key); err != nil {
				return v, err
			}
		}
		if err = s3Quota(tx, 0, 1); err != nil {
			return v, err
		}
		_, err = tx.Exec("INSERT INTO aws_s3_versions(bucket,key,version,marker,content_type,etag,modified,data) VALUES(?,?,?,1,'','',?,X'')", bucket, key, v.ID, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return v, err
		}
	}
	if err = s3Refresh(tx, bucket, key); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (s *S3Store) DeleteBucket(bucket string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s3BucketExists(tx, bucket); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRow("SELECT (SELECT count(*) FROM objects WHERE bucket=?)+(SELECT count(*) FROM aws_s3_versions WHERE bucket=?)", bucket, bucket).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return core.Fail("BucketNotEmpty", "Delete all objects, versions and delete markers first.")
	}
	if _, err = tx.Exec("DELETE FROM buckets WHERE name=?", bucket); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *S3Store) Versions(bucket string) ([]S3Version, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = s3BucketExists(tx, bucket); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO aws_s3_versions(bucket,key,version,marker,content_type,etag,modified,data) SELECT bucket,key,'null',0,content_type,etag,modified,data FROM objects o WHERE bucket=? AND NOT EXISTS(SELECT 1 FROM aws_s3_versions v WHERE v.bucket=o.bucket AND v.key=o.key)`, bucket); err != nil {
		return nil, err
	}
	rows, err := tx.Query("SELECT key,seq,version,marker,content_type,etag,modified,length(data) FROM aws_s3_versions WHERE bucket=? ORDER BY key,seq DESC", bucket)
	if err != nil {
		return nil, err
	}
	result := []S3Version{}
	last := ""
	for rows.Next() {
		v := S3Version{}
		var modified string
		if err = rows.Scan(&v.Key, &v.Sequence, &v.ID, &v.Marker, &v.ContentType, &v.ETag, &modified, &v.Size); err != nil {
			_ = rows.Close()
			return nil, err
		}
		v.Modified, err = time.Parse(time.RFC3339Nano, modified)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		v.Latest = len(result) == 0 || v.Key != last
		last = v.Key
		result = append(result, v)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (s *S3Store) Create(bucket, region string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT region FROM buckets WHERE name=?", bucket).Scan(&existing)
	if err == nil {
		if existing == region {
			return core.Fail("BucketAlreadyOwnedByYou", "The bucket already exists in this simulator.")
		}
		return core.Fail("BucketAlreadyExists", "A bucket with this name already exists.")
	}
	if err != sql.ErrNoRows {
		return err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM buckets").Scan(&count); err != nil {
		return err
	}
	if count >= core.MaxBuckets {
		return core.Fail("ServiceUnavailable", "Simulator bucket limit reached.")
	}
	if _, err = tx.Exec("INSERT INTO buckets VALUES(?,?,?)", bucket, region, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO aws_s3_config VALUES(?,'','{"BlockPublicAcls":true,"IgnorePublicAcls":true,"BlockPublicPolicy":true,"RestrictPublicBuckets":true}')`, bucket); err != nil {
		return err
	}
	return tx.Commit()
}
