// Package core owns provider-independent relational state and identity decisions.
package core

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const MaxObjectBytes = 8 << 20
const MaxStoredBytes = 64 << 20
const MaxBuckets = 1000
const MaxObjects = 10000

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string        { return e.Message }
func Fail(code, message string) error { return &Error{code, message} }
func Code(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "InternalError"
}
func storageError(err error) error {
	if err == nil {
		return nil
	}
	slog.Error("simulator state operation failed", "error", err)
	return Fail("InternalError", "Simulator state operation failed.")
}

type Object struct {
	Key         string    `json:"key"`
	Size        int       `json:"size"`
	ETag        string    `json:"etag"`
	Modified    time.Time `json:"modified"`
	ContentType string    `json:"contentType"`
	Data        []byte    `json:"-"`
}
type Bucket struct {
	Name    string    `json:"name"`
	Region  string    `json:"region"`
	Created time.Time `json:"created"`
	Objects []Object  `json:"objects"`
}
type Store struct{ db *sql.DB }

func New(path string) (*Store, error) {
	if path == "" {
		path = ":memory:"
	}
	if strings.HasPrefix(path, "file:") {
		return nil, fmt.Errorf("--state accepts a file path, not a SQLite URI")
	}
	if path != ":memory:" {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return nil, e
		}
		if e = f.Close(); e != nil {
			return nil, e
		}
	}
	dsn := "file::memory:?_txlock=immediate"
	if path != ":memory:" {
		absolute, e := filepath.Abs(path)
		if e != nil {
			return nil, e
		}
		dsn = (&url.URL{Scheme: "file", Path: absolute, RawQuery: "_txlock=immediate"}).String()
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// A single connection serializes this process; immediate transactions and a busy
	// timeout also serialize independent processes that intentionally share a DB file.
	if _, err = db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		_ = db.Close()
		return nil, err
	}
	if version != 0 && version != 1 {
		_ = db.Close()
		return nil, fmt.Errorf("unsupported simulator schema version %d", version)
	}
	if version == 0 {
		var tables int
		if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			_ = db.Close()
			return nil, err
		}
		if tables != 0 {
			_ = db.Close()
			return nil, fmt.Errorf("state file is not an empty or versioned simulator database")
		}
		tx, e := db.Begin()
		if e != nil {
			_ = db.Close()
			return nil, e
		}
		_, err = tx.Exec(`CREATE TABLE buckets(name TEXT PRIMARY KEY, region TEXT NOT NULL, created TEXT NOT NULL);
 CREATE TABLE objects(bucket TEXT NOT NULL REFERENCES buckets(name) ON DELETE RESTRICT, key TEXT NOT NULL, content_type TEXT NOT NULL, etag TEXT NOT NULL, modified TEXT NOT NULL, data BLOB NOT NULL, PRIMARY KEY(bucket,key));
 CREATE TABLE identity_policies(name TEXT PRIMARY KEY, document TEXT NOT NULL);
 PRAGMA user_version=1;`)
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &Store{db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Create(name, region string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow("SELECT region FROM buckets WHERE name=?", name).Scan(&existing)
	if err == nil {
		if existing == region {
			return Fail("BucketAlreadyOwnedByYou", "The bucket already exists in this simulator.")
		}
		return Fail("BucketAlreadyExists", "A bucket with this name already exists.")
	}
	if err != sql.ErrNoRows {
		return storageError(err)
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM buckets").Scan(&count); err != nil {
		return storageError(err)
	}
	if count >= MaxBuckets {
		return Fail("ServiceUnavailable", "Simulator bucket limit reached.")
	}
	if _, err = tx.Exec("INSERT INTO buckets VALUES(?,?,?)", name, region, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit())
}
func (s *Store) Buckets() ([]Bucket, error) {
	rows, err := s.db.Query("SELECT name,region,created FROM buckets ORDER BY name")
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	result := []Bucket{}
	for rows.Next() {
		var b Bucket
		var created string
		if err = rows.Scan(&b.Name, &b.Region, &created); err != nil {
			return nil, storageError(err)
		}
		b.Created, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, storageError(err)
		}
		b.Objects = []Object{}
		result = append(result, b)
	}
	return result, storageError(rows.Err())
}
func (s *Store) Bucket(name string) (Bucket, error) {
	return s.BucketContext(context.Background(), name)
}
func (s *Store) BucketContext(ctx context.Context, name string) (Bucket, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bucket{}, storageError(err)
	}
	defer tx.Rollback()
	b := Bucket{Name: name, Objects: []Object{}}
	var created string
	err = tx.QueryRowContext(ctx, "SELECT region,created FROM buckets WHERE name=?", name).Scan(&b.Region, &created)
	if err == sql.ErrNoRows {
		return b, Fail("NoSuchBucket", "The specified bucket does not exist.")
	}
	if err != nil {
		return b, storageError(err)
	}
	b.Created, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return b, storageError(err)
	}
	rows, err := tx.QueryContext(ctx, "SELECT key,length(data),etag,modified,content_type FROM objects WHERE bucket=? ORDER BY key", name)
	if err != nil {
		return b, storageError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var o Object
		var modified string
		if err = rows.Scan(&o.Key, &o.Size, &o.ETag, &modified, &o.ContentType); err != nil {
			return b, storageError(err)
		}
		o.Modified, err = time.Parse(time.RFC3339Nano, modified)
		if err != nil {
			return b, storageError(err)
		}
		b.Objects = append(b.Objects, o)
	}
	if err = rows.Err(); err != nil {
		return b, storageError(err)
	}
	if err = rows.Close(); err != nil {
		return b, storageError(err)
	}
	return b, storageError(tx.Commit())
}
func exists(tx *sql.Tx, name string) error {
	var n int
	err := tx.QueryRow("SELECT 1 FROM buckets WHERE name=?", name).Scan(&n)
	if err == sql.ErrNoRows {
		return Fail("NoSuchBucket", "The specified bucket does not exist.")
	}
	return storageError(err)
}
func (s *Store) DeleteBucket(name string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	if err = exists(tx, name); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRow("SELECT count(*) FROM objects WHERE bucket=?", name).Scan(&n); err != nil {
		return storageError(err)
	}
	if n != 0 {
		return Fail("BucketNotEmpty", "Delete all objects before deleting this bucket.")
	}
	if _, err = tx.Exec("DELETE FROM buckets WHERE name=?", name); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit())
}
func (s *Store) Put(name, key, contentType string, data []byte, match, none string) (Object, error) {
	if len(data) > MaxObjectBytes {
		return Object{}, Fail("EntityTooLarge", "Object exceeds the 8 MiB simulator limit.")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Object{}, storageError(err)
	}
	defer tx.Rollback()
	if err = exists(tx, name); err != nil {
		return Object{}, err
	}
	var oldETag string
	var oldSize int
	err = tx.QueryRow("SELECT etag,length(data) FROM objects WHERE bucket=? AND key=?", name, key).Scan(&oldETag, &oldSize)
	found := err == nil
	if err != nil && err != sql.ErrNoRows {
		return Object{}, storageError(err)
	}
	if (match != "" && (!found || (match != "*" && match != oldETag))) || (none != "" && found && (none == "*" || none == oldETag)) {
		return Object{}, Fail("PreconditionFailed", "An object precondition failed.")
	}
	var total, count int
	if err = tx.QueryRow("SELECT coalesce(sum(length(data)),0),count(*) FROM objects").Scan(&total, &count); err != nil {
		return Object{}, storageError(err)
	}
	if total-oldSize+len(data) > MaxStoredBytes || (!found && count >= MaxObjects) {
		return Object{}, Fail("ServiceUnavailable", "Simulator storage limit reached.")
	}
	sum := md5.Sum(data)
	obj := Object{Key: key, Size: len(data), ETag: `"` + hex.EncodeToString(sum[:]) + `"`, Modified: time.Now().UTC(), ContentType: contentType}
	if data == nil {
		data = []byte{}
	}
	_, err = tx.Exec("INSERT INTO objects(bucket,key,content_type,etag,modified,data) VALUES(?,?,?,?,?,?) ON CONFLICT(bucket,key) DO UPDATE SET content_type=excluded.content_type,etag=excluded.etag,modified=excluded.modified,data=excluded.data", name, key, contentType, obj.ETag, obj.Modified.Format(time.RFC3339Nano), data)
	if err != nil {
		return Object{}, storageError(err)
	}
	return obj, storageError(tx.Commit())
}
func (s *Store) Get(name, key string) (Object, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Object{}, storageError(err)
	}
	defer tx.Rollback()
	if err = exists(tx, name); err != nil {
		return Object{}, err
	}
	var o Object
	var modified string
	o.Key = key
	err = tx.QueryRow("SELECT length(data),etag,modified,content_type,data FROM objects WHERE bucket=? AND key=?", name, key).Scan(&o.Size, &o.ETag, &modified, &o.ContentType, &o.Data)
	if err == sql.ErrNoRows {
		return o, Fail("NoSuchKey", "The specified key does not exist.")
	}
	if err != nil {
		return o, storageError(err)
	}
	o.Modified, err = time.Parse(time.RFC3339Nano, modified)
	if err != nil {
		return o, storageError(err)
	}
	return o, storageError(tx.Commit())
}
func (s *Store) Delete(name, key string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback()
	if err = exists(tx, name); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM objects WHERE bucket=? AND key=?", name, key); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit())
}
func (s *Store) ForeignKeyCheck() error {
	rows, err := s.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign key invariant violated")
	}
	return rows.Err()
}

// Database is shared only with trusted in-process providers. Each provider owns
// its tables and validation; this does not grant a remote SQL API.
func (s *Store) Database() *sql.DB { return s.db }
