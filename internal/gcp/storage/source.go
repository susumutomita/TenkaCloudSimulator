package storage

import (
	"context"
	"database/sql"
	"errors"
)

// Errors of ReadProjectObject.
var (
	ErrObjectNotFound = errors.New("Cloud Storage object not found in the project")
	ErrObjectTooLarge = errors.New("Cloud Storage object exceeds the read limit")
)

// ReadProjectObject reads one object of a bucket that the given synthetic
// project owns, for a trusted same-project provider that copies deployment
// sources (App Engine). It runs in its own transaction, never
// creates, adopts or changes Storage state and is not a JSON API path. A
// bucket of another project reads as not found.
func ReadProjectObject(ctx context.Context, db *sql.DB, environment, project, bucket, object string, limit int64) ([]byte, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var size int64
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT b.project, length(o.data) FROM gcp_storage_buckets b JOIN gcp_storage_objects o ON o.environment = b.environment AND o.bucket = b.name
 WHERE b.environment = ? AND b.name = ? AND o.name = ?`, environment, bucket, object).Scan(&owner, &size)
	if errors.Is(err, sql.ErrNoRows) || err == nil && owner != project {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	if size > limit {
		return nil, ErrObjectTooLarge
	}
	var data []byte
	if err = tx.QueryRowContext(ctx, "SELECT data FROM gcp_storage_objects WHERE environment = ? AND bucket = ? AND name = ?", environment, bucket, object).Scan(&data); err != nil {
		return nil, err
	}
	return data, nil
}
