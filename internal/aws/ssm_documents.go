package aws

// Document management stores definitions only. No method executes document content.
import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

const ssmDocumentLimit = 100
const ssmDocumentVersionLimit = 100
const ssmDocumentTotalBytes = 8 << 20

type SSMDocumentStore struct{ db *sql.DB }
type SSMDocument struct {
	Name            string
	DocumentVersion string
	DefaultVersion  string
	LatestVersion   string
	Content         string
	CreatedDate     float64
	Hash            string
}

// NewSSMDocumentStore uses the existing host-owned database. Callers must perform
// IAM admission before every operation; this internal store grants no authority.
func NewSSMDocumentStore(db *sql.DB) (*SSMDocumentStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_ssm_documents(region TEXT NOT NULL,name TEXT NOT NULL,latest INTEGER NOT NULL CHECK(latest>0),PRIMARY KEY(region,name));
 CREATE TABLE IF NOT EXISTS aws_ssm_document_versions(region TEXT NOT NULL,name TEXT NOT NULL,version INTEGER NOT NULL CHECK(version>0),content TEXT NOT NULL,created REAL NOT NULL,PRIMARY KEY(region,name,version),FOREIGN KEY(region,name) REFERENCES aws_ssm_documents(region,name) ON DELETE CASCADE);`)
	if err != nil {
		return nil, err
	}
	return &SSMDocumentStore{db: db}, nil
}

func documentFailure(code, message string) error { return ssmFailure{code, message} }

// Save appends immutable versions. Default version remains 1. Empty expected is
// create; updates require an exact latest number or $LATEST. No overwrite API.
func (s *SSMDocumentStore) Save(ctx context.Context, region, name, content, expected string) (SSMDocument, error) {
	if err := validateSSMDocument(region, name, content); err != nil {
		return SSMDocument{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSMDocument{}, err
	}
	defer tx.Rollback()
	var latest int
	err = tx.QueryRowContext(ctx, `SELECT latest FROM aws_ssm_documents WHERE region=? AND name=?`, region, name).Scan(&latest)
	if err != nil && err != sql.ErrNoRows {
		return SSMDocument{}, err
	}
	exists := err == nil
	if expected == "" && exists {
		return SSMDocument{}, documentFailure("DocumentAlreadyExists", "Document already exists.")
	}
	if expected != "" && !exists {
		return SSMDocument{}, documentFailure("InvalidDocument", "Document does not exist.")
	}
	if expected != "" && expected != "$LATEST" && expected != strconv.Itoa(latest) {
		return SSMDocument{}, documentFailure("InvalidDocumentVersion", "Only the latest version can be updated.")
	}
	if latest >= ssmDocumentVersionLimit {
		return SSMDocument{}, documentFailure("DocumentVersionLimitExceeded", "Local document version limit reached.")
	}
	if exists {
		var prior string
		if err = tx.QueryRowContext(ctx, `SELECT content FROM aws_ssm_document_versions WHERE region=? AND name=? AND version=?`, region, name, latest).Scan(&prior); err != nil {
			return SSMDocument{}, err
		}
		if prior == content {
			return SSMDocument{}, documentFailure("DuplicateDocumentContent", "Document content has not changed.")
		}
	}
	var count, total int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ssm_documents`).Scan(&count); err != nil {
		return SSMDocument{}, err
	}
	if !exists && count >= ssmDocumentLimit {
		return SSMDocument{}, documentFailure("DocumentLimitExceeded", "Local document count limit reached.")
	}
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(sum(length(CAST(content AS BLOB))),0) FROM aws_ssm_document_versions`).Scan(&total); err != nil {
		return SSMDocument{}, err
	}
	if total+len(content) > ssmDocumentTotalBytes {
		return SSMDocument{}, documentFailure("DocumentLimitExceeded", "Local stored document byte limit reached.")
	}
	latest++
	if !exists {
		_, err = tx.ExecContext(ctx, `INSERT INTO aws_ssm_documents(region,name,latest) VALUES(?,?,?)`, region, name, latest)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE aws_ssm_documents SET latest=? WHERE region=? AND name=?`, latest, region, name)
	}
	if err != nil {
		return SSMDocument{}, err
	}
	created := float64(time.Now().UnixMilli()) / 1000
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ssm_document_versions(region,name,version,content,created) VALUES(?,?,?,?,?)`, region, name, latest, content, created)
	if err != nil {
		return SSMDocument{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return SSMDocument{}, err
	}
	if n != 1 {
		return SSMDocument{}, fmt.Errorf("document insert did not persist")
	}
	// Read back the complete invariant before acknowledging a commit, including
	// triggers/fault injection which may silently ignore a write.
	var storedLatest int
	var stored string
	if err = tx.QueryRowContext(ctx, `SELECT d.latest,v.content FROM aws_ssm_documents d JOIN aws_ssm_document_versions v USING(region,name) WHERE d.region=? AND d.name=? AND v.version=?`, region, name, latest).Scan(&storedLatest, &stored); err != nil {
		return SSMDocument{}, err
	}
	if storedLatest != latest || stored != content {
		return SSMDocument{}, fmt.Errorf("document write invariant failed")
	}
	if err = tx.Commit(); err != nil {
		return SSMDocument{}, err
	}
	return makeSSMDocument(name, latest, latest, content, created), nil
}
func makeSSMDocument(name string, version, latest int, content string, created float64) SSMDocument {
	hash := sha256.Sum256([]byte(content))
	return SSMDocument{Name: name, DocumentVersion: strconv.Itoa(version), DefaultVersion: "1", LatestVersion: strconv.Itoa(latest), Content: content, CreatedDate: created, Hash: hex.EncodeToString(hash[:])}
}
func (s *SSMDocumentStore) Get(ctx context.Context, region, name, selector string) (SSMDocument, error) {
	if !validSSMDocumentAddress(region, name) {
		return SSMDocument{}, documentFailure("ValidationException", "Invalid local document address.")
	}
	// Use the same transaction mode as the project stores. The pinned driver
	// read-only reset fails on this database configuration and leaves query_only
	// enabled; this path performs SELECTs only and still reads one snapshot.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSMDocument{}, err
	}
	defer tx.Rollback()
	var latest int
	if err = tx.QueryRowContext(ctx, `SELECT latest FROM aws_ssm_documents WHERE region=? AND name=?`, region, name).Scan(&latest); err == sql.ErrNoRows {
		return SSMDocument{}, documentFailure("InvalidDocument", "Document does not exist.")
	} else if err != nil {
		return SSMDocument{}, err
	}
	version := 1
	switch selector {
	case "", "$DEFAULT":
	case "$LATEST":
		version = latest
	default:
		version, err = strconv.Atoi(selector)
		if err != nil || version < 1 || strconv.Itoa(version) != selector {
			return SSMDocument{}, documentFailure("InvalidDocumentVersion", "Invalid version.")
		}
	}
	var content string
	var created float64
	if err = tx.QueryRowContext(ctx, `SELECT content,created FROM aws_ssm_document_versions WHERE region=? AND name=? AND version=?`, region, name, version).Scan(&content, &created); err == sql.ErrNoRows {
		return SSMDocument{}, documentFailure("InvalidDocumentVersion", "Version does not exist.")
	} else if err != nil {
		return SSMDocument{}, err
	}
	if err = tx.Commit(); err != nil {
		return SSMDocument{}, err
	}
	return makeSSMDocument(name, version, latest, content, created), nil
}
func (s *SSMDocumentStore) Delete(ctx context.Context, region, name string) error {
	if !validSSMDocumentAddress(region, name) {
		return documentFailure("ValidationException", "Invalid local document address.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM aws_ssm_documents WHERE region=? AND name=?`, region, name)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return documentFailure("InvalidDocument", "Document does not exist.")
	}
	var count int
	if err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM aws_ssm_document_versions WHERE region=? AND name=?) + (SELECT count(*) FROM aws_ssm_documents WHERE region=? AND name=?)`, region, name, region, name).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("document cleanup incomplete")
	}
	return tx.Commit()
}

// List returns a bounded complete regional view. Native pagination is applied by
// the API adapter; it must bind cursors to region and current immutable contents.
func (s *SSMDocumentStore) List(ctx context.Context, region string) ([]SSMDocument, error) {
	if !validSSMDocumentRegion(region) {
		return nil, documentFailure("ValidationException", "Invalid region.")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.name,d.latest,v.content,v.created FROM aws_ssm_documents d JOIN aws_ssm_document_versions v ON d.region=v.region AND d.name=v.name AND v.version=1 WHERE d.region=? ORDER BY d.name`, region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SSMDocument{}
	for rows.Next() {
		var name, content string
		var latest int
		var created float64
		if err = rows.Scan(&name, &latest, &content, &created); err != nil {
			return nil, err
		}
		out = append(out, makeSSMDocument(name, 1, latest, content, created))
		if len(out) > ssmDocumentLimit {
			return nil, fmt.Errorf("document count invariant failed")
		}
	}
	return out, rows.Err()
}
