package blob

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

type Service struct {
	db        *sql.DB
	config    Config
	authorize Authorizer
	cursorKey []byte
	requests  atomic.Uint64
}

// New provisions only a host-owned synthetic account fixture, not an Azure ARM
// resource. Existing ownership/secret state is never silently overwritten.
func New(db *sql.DB, cfg Config, authorize Authorizer) (*Service, error) {
	if db == nil || authorize == nil || !accountName.MatchString(cfg.Account) || !validLabel(cfg.Owner, 256) || !validLabel(cfg.Environment, 128) {
		return nil, errors.New("Azure Blob requires database, trusted account/owner/environment and authorization")
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var fk int
	if err = tx.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		return nil, errors.New("Azure Blob requires enforced foreign keys")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS azure_blob_accounts(environment TEXT NOT NULL,account TEXT NOT NULL,owner TEXT NOT NULL,cursor_key BLOB NOT NULL,PRIMARY KEY(environment,account));
CREATE TABLE IF NOT EXISTS azure_blob_containers(environment TEXT NOT NULL,account TEXT NOT NULL,name TEXT NOT NULL,etag TEXT NOT NULL,modified TEXT NOT NULL,metadata TEXT NOT NULL,PRIMARY KEY(environment,account,name),FOREIGN KEY(environment,account) REFERENCES azure_blob_accounts(environment,account) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS azure_blob_objects(environment TEXT NOT NULL,account TEXT NOT NULL,container TEXT NOT NULL,name TEXT NOT NULL,body BLOB NOT NULL,etag TEXT NOT NULL,modified TEXT NOT NULL,metadata TEXT NOT NULL,content_type TEXT NOT NULL,md5 TEXT NOT NULL,PRIMARY KEY(environment,account,container,name),FOREIGN KEY(environment,account,container) REFERENCES azure_blob_containers(environment,account,name) ON DELETE CASCADE);`)
	if err != nil {
		return nil, err
	}
	// Check both ownership edges before inserting any synthetic account. Otherwise
	// creating a missing owner row could silently adopt pre-existing containers.
	var dangling int
	if err = tx.QueryRow(`SELECT (SELECT count(*) FROM azure_blob_containers c WHERE NOT EXISTS(SELECT 1 FROM azure_blob_accounts a WHERE a.environment=c.environment AND a.account=c.account))+(SELECT count(*) FROM azure_blob_objects b WHERE NOT EXISTS(SELECT 1 FROM azure_blob_containers c WHERE c.environment=b.environment AND c.account=b.account AND c.name=b.container))`).Scan(&dangling); err != nil {
		return nil, err
	}
	if dangling != 0 {
		return nil, errors.New("orphan Azure account/container state preserved; no automatic adoption")
	}
	var owner string
	var key []byte
	err = tx.QueryRow("SELECT owner,cursor_key FROM azure_blob_accounts WHERE environment=? AND account=?", cfg.Environment, cfg.Account).Scan(&owner, &key)
	if errors.Is(err, sql.ErrNoRows) {
		var count int
		if err = tx.QueryRow("SELECT count(*) FROM azure_blob_accounts").Scan(&count); err != nil {
			return nil, err
		}
		if count >= 1000 {
			return nil, errors.New("local Azure account fixture limit reached")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		result, e := tx.Exec("INSERT INTO azure_blob_accounts VALUES(?,?,?,?)", cfg.Environment, cfg.Account, cfg.Owner, key)
		if e != nil {
			return nil, e
		}
		if err = affected(result, 1); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if owner != cfg.Owner || len(key) != 32 {
		return nil, errors.New("Azure account ownership or cursor secret drift; preserved for explicit repair")
	}
	var orphans int
	if err = tx.QueryRow(`SELECT count(*) FROM azure_blob_objects b WHERE NOT EXISTS(SELECT 1 FROM azure_blob_containers c WHERE c.environment=b.environment AND c.account=b.account AND c.name=b.container)`).Scan(&orphans); err != nil {
		return nil, err
	}
	if orphans != 0 {
		return nil, errors.New("orphan Azure Blob state preserved for repair")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &Service{db: db, config: cfg, authorize: authorize, cursorKey: append([]byte(nil), key...)}, nil
}
func affected(result sql.Result, want int64) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != want {
		return errInternal
	}
	return nil
}
func (s *Service) begin(ctx context.Context, op Operation, container, key string) (*sql.Tx, error) {
	permission := permissionFor(op)
	if permission.Name == "" || permission.Kind == "" {
		return nil, fail(501, "NotImplemented", "Unsupported native operation.")
	}
	actor, ok := ActorFromContext(ctx)
	if !ok || actor.Environment != s.config.Environment {
		return nil, fail(403, "AuthorizationPermissionMismatch", "Trusted Azure actor binding is required.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = tx.Rollback()
		}
	}()
	var owner string
	var secret []byte
	if err = tx.QueryRowContext(ctx, "SELECT owner,cursor_key FROM azure_blob_accounts WHERE environment=? AND account=?", s.config.Environment, s.config.Account).Scan(&owner, &secret); err != nil {
		return nil, err
	}
	if owner != s.config.Owner || !hmac.Equal(secret, s.cursorKey) {
		return nil, errInternal
	}
	request := Authorization{Actor: actor, Operation: op, Permission: permission, Resource: Resource{Provider: "azure", Environment: s.config.Environment, Account: s.config.Account, Container: container, Blob: key, Owner: owner}}
	allowed, err := s.authorize(ctx, tx, request)
	if err != nil {
		return nil, errInternal
	}
	if !allowed {
		return nil, fail(403, "AuthorizationPermissionMismatch", "The actor is not authorized for this operation.")
	}
	success = true
	return tx, nil
}
func metadata(input map[string]string) (map[string]string, string, error) {
	if len(input) > 32 {
		return nil, "", fail(400, "MetadataTooLarge", "Local metadata field limit exceeded.")
	}
	out := map[string]string{}
	for k, v := range input {
		if !metadataName.MatchString(k) {
			return nil, "", fail(400, "InvalidMetadata", "Invalid metadata name.")
		}
		k = strings.ToLower(k)
		if _, ok := out[k]; ok {
			return nil, "", fail(400, "InvalidMetadata", "Duplicate metadata name.")
		}
		for _, c := range v {
			if c < 0x20 || c > 0x7e {
				return nil, "", fail(400, "InvalidMetadata", "Metadata values require visible ASCII.")
			}
		}
		out[k] = v
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) > MaxMetadataBytes {
		return nil, "", fail(400, "MetadataTooLarge", "Local encoded metadata limit exceeded.")
	}
	return out, string(encoded), nil
}
func decodeMetadata(raw string) (map[string]string, error) {
	var m map[string]string
	if len(raw) > MaxMetadataBytes || json.Unmarshal([]byte(raw), &m) != nil || m == nil {
		return nil, errInternal
	}
	_, canonical, err := metadata(m)
	if err != nil || canonical != raw {
		return nil, errInternal
	}
	return m, nil
}
func newTag() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return `"0x` + hex.EncodeToString(b[:]) + `"`, nil
}
func (s *Service) parent(tx *sql.Tx, name string) (entity, error) {
	var e entity
	var raw string
	err := tx.QueryRow("SELECT name,etag,modified,metadata FROM azure_blob_containers WHERE environment=? AND account=? AND name=?", s.config.Environment, s.config.Account, name).Scan(&e.Name, &e.ETag, &e.Modified, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return e, fail(404, "ContainerNotFound", "The specified container does not exist.")
	}
	if err != nil {
		return e, err
	}
	if !validContainer(e.Name) || !storedTag.MatchString(e.ETag) {
		return e, errInternal
	}
	if _, err = http.ParseTime(e.Modified); err != nil {
		return e, errInternal
	}
	e.Metadata, err = decodeMetadata(raw)
	return e, err
}
func (s *Service) container(ctx context.Context, op Operation, name string, meta map[string]string) (entity, error) {
	tx, err := s.begin(ctx, op, name, "")
	if err != nil {
		return entity{}, err
	}
	defer tx.Rollback()
	var out entity
	switch op {
	case CreateContainer:
		_, raw, e := metadata(meta)
		if e != nil {
			return out, e
		}
		var n int
		if e = tx.QueryRow("SELECT count(*) FROM azure_blob_containers WHERE environment=? AND account=? AND name=?", s.config.Environment, s.config.Account, name).Scan(&n); e != nil {
			return out, e
		}
		if n != 0 {
			return out, fail(409, "ContainerAlreadyExists", "The container already exists.")
		}
		if e = tx.QueryRow("SELECT count(*) FROM azure_blob_containers").Scan(&n); e != nil {
			return out, e
		}
		if n >= MaxContainers {
			return out, fail(409, "SimulatorQuotaExceeded", "Local container budget exceeded.")
		}
		if e = s.budget(tx, 0, int64(len(name)+len(raw)), 0); e != nil {
			return out, e
		}
		tag, e := newTag()
		if e != nil {
			return out, e
		}
		out = entity{Name: name, ETag: tag, Modified: time.Now().UTC().Format(http.TimeFormat), Metadata: meta}
		result, e := tx.Exec("INSERT INTO azure_blob_containers VALUES(?,?,?,?,?,?)", s.config.Environment, s.config.Account, name, tag, out.Modified, raw)
		if e != nil {
			return out, e
		}
		if e = affected(result, 1); e != nil {
			return out, e
		}
	case GetContainerProperties:
		out, err = s.parent(tx, name)
		if err != nil {
			return out, err
		}
	case DeleteContainer:
		out, err = s.parent(tx, name)
		if err != nil {
			return out, err
		}
		var n int64
		if err = tx.QueryRow("SELECT count(*) FROM azure_blob_objects WHERE environment=? AND account=? AND container=?", s.config.Environment, s.config.Account, name).Scan(&n); err != nil {
			return out, err
		}
		result, e := tx.Exec("DELETE FROM azure_blob_objects WHERE environment=? AND account=? AND container=?", s.config.Environment, s.config.Account, name)
		if e != nil {
			return out, e
		}
		if e = affected(result, n); e != nil {
			return out, e
		}
		result, e = tx.Exec("DELETE FROM azure_blob_containers WHERE environment=? AND account=? AND name=?", s.config.Environment, s.config.Account, name)
		if e != nil {
			return out, e
		}
		if e = affected(result, 1); e != nil {
			return out, e
		}
	default:
		return out, errInternal
	}
	return out, tx.Commit()
}
func (s *Service) budget(tx *sql.Tx, deltaCount, deltaBytes, oldBytes int64) error {
	var count, total int64
	err := tx.QueryRow(`SELECT count(*),COALESCE(SUM(length(body)+length(CAST(metadata AS BLOB))+length(CAST(name AS BLOB))+length(CAST(content_type AS BLOB))),0)+(SELECT COALESCE(SUM(length(CAST(name AS BLOB))+length(CAST(metadata AS BLOB))),0) FROM azure_blob_containers) FROM azure_blob_objects`).Scan(&count, &total)
	if err != nil {
		return err
	}
	if count+deltaCount > MaxBlobs || total-oldBytes+deltaBytes > MaxStoredBytes {
		return fail(409, "SimulatorQuotaExceeded", "Local physical Azure Blob storage budget exceeded.")
	}
	return nil
}
func (s *Service) load(tx *sql.Tx, container, key string, body bool) (entity, bool, error) {
	var out entity
	var raw string
	row := tx.QueryRow("SELECT name,etag,modified,metadata,content_type,md5,length(body) FROM azure_blob_objects WHERE environment=? AND account=? AND container=? AND name=?", s.config.Environment, s.config.Account, container, key)
	err := row.Scan(&out.Name, &out.ETag, &out.Modified, &raw, &out.ContentType, &out.MD5, &out.Length)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if out.Length < 0 || out.Length > MaxBlobBytes || !validBlob(out.Name) || !validLabel(out.ContentType, 256) || !storedTag.MatchString(out.ETag) {
		return out, false, errInternal
	}
	if _, err = http.ParseTime(out.Modified); err != nil {
		return out, false, errInternal
	}
	hash, err := base64.StdEncoding.DecodeString(out.MD5)
	if err != nil || len(hash) != md5.Size {
		return out, false, errInternal
	}
	out.Metadata, err = decodeMetadata(raw)
	if err != nil {
		return out, false, err
	}
	if body {
		if err = tx.QueryRow("SELECT body FROM azure_blob_objects WHERE environment=? AND account=? AND container=? AND name=?", s.config.Environment, s.config.Account, container, key).Scan(&out.Body); err != nil {
			return out, false, err
		}
		sum := md5.Sum(out.Body)
		if int64(len(out.Body)) != out.Length || !hmac.Equal(sum[:], hash) {
			return out, false, errInternal
		}
	}
	return out, true, nil
}

func (s *Service) object(ctx context.Context, op Operation, container, key string, cond condition, data []byte, meta map[string]string, contentType string) (entity, error) {
	tx, err := s.begin(ctx, op, container, key)
	if err != nil {
		return entity{}, err
	}
	defer tx.Rollback()
	if _, err = s.parent(tx, container); err != nil {
		return entity{}, err
	}
	out, exists, err := s.load(tx, container, key, op == GetBlob)
	if err != nil {
		return out, err
	}
	if op != PutBlob && !exists {
		return out, fail(404, "BlobNotFound", "The specified blob does not exist.")
	}
	if err = cond.check(out.ETag, exists, op == GetBlob || op == GetBlobProperties); err != nil {
		return out, err
	}
	switch op {
	case PutBlob:
		if data == nil {
			data = []byte{}
		}
		if len(data) > MaxBlobBytes {
			return out, fail(413, "RequestBodyTooLarge", "Local blob size limit exceeded.")
		}
		normalized, raw, e := metadata(meta)
		if e != nil {
			return out, e
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if !validLabel(contentType, 256) {
			return out, fail(400, "InvalidHeaderValue", "Invalid content type.")
		}
		oldBytes := int64(0)
		deltaCount := int64(1)
		if exists {
			oldMeta, _ := json.Marshal(out.Metadata)
			oldBytes = out.Length + int64(len(out.Name)+len(out.ContentType)+len(oldMeta))
			deltaCount = 0
		}
		if e = s.budget(tx, deltaCount, int64(len(data)+len(raw)+len(key)+len(contentType)), oldBytes); e != nil {
			return out, e
		}
		tag, e := newTag()
		if e != nil {
			return out, e
		}
		sum := md5.Sum(data)
		out = entity{Name: key, ETag: tag, Modified: time.Now().UTC().Format(http.TimeFormat), Metadata: normalized, ContentType: contentType, MD5: base64.StdEncoding.EncodeToString(sum[:]), Length: int64(len(data))}
		result, e := tx.Exec(`INSERT INTO azure_blob_objects VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(environment,account,container,name) DO UPDATE SET body=excluded.body,etag=excluded.etag,modified=excluded.modified,metadata=excluded.metadata,content_type=excluded.content_type,md5=excluded.md5`, s.config.Environment, s.config.Account, container, key, data, tag, out.Modified, raw, contentType, out.MD5)
		if e != nil {
			return out, e
		}
		if e = affected(result, 1); e != nil {
			return out, e
		}
	case DeleteBlob:
		result, e := tx.Exec("DELETE FROM azure_blob_objects WHERE environment=? AND account=? AND container=? AND name=?", s.config.Environment, s.config.Account, container, key)
		if e != nil {
			return out, e
		}
		if e = affected(result, 1); e != nil {
			return out, e
		}
	case GetBlob, GetBlobProperties:
	default:
		return out, fmt.Errorf("unknown Azure Blob operation")
	}
	return out, tx.Commit()
}
