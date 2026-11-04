package aws

import (
	"context"
	"database/sql"
)

// s3Query allows shared transactional helpers to use either a normal CFN
// transaction or a request-bound native operation without losing cancellation.
type s3Query interface {
	Exec(string, ...any) (sql.Result, error)
	QueryRow(string, ...any) *sql.Row
}
type s3RequestTx struct {
	*sql.Tx
	context context.Context
}

func beginS3Request(ctx context.Context, db *sql.DB) (*s3RequestTx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &s3RequestTx{Tx: tx, context: ctx}, nil
}
func (tx *s3RequestTx) Exec(query string, args ...any) (sql.Result, error) {
	return tx.Tx.ExecContext(tx.context, query, args...)
}
func (tx *s3RequestTx) QueryRow(query string, args ...any) *sql.Row {
	return tx.Tx.QueryRowContext(tx.context, query, args...)
}
