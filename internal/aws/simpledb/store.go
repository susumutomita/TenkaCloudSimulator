// Package simpledb provides bounded local SimpleDB storage, not authentication
// or an HTTP API. The caller supplies trusted account/region scope and owns DB.
package simpledb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
)

type Store struct {
	db       *sql.DB
	writer   chan struct{}
	tokenKey [32]byte
}
type storeError string

func (e storeError) Error() string { return string(e) }
func (e storeError) Code() string  { return string(e) }
func failure(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return storeError("Cancelled")
	}
	return err
}
func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("SimpleDB requires caller-owned database")
	}
	s := &Store{db: db, writer: make(chan struct{}, 1)}
	if _, err := rand.Read(s.tokenKey[:]); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS aws_simpledb_domains (account TEXT NOT NULL, region TEXT NOT NULL, name TEXT NOT NULL, PRIMARY KEY(account,region,name))`,
		`CREATE TABLE IF NOT EXISTS aws_simpledb_items (account TEXT NOT NULL, region TEXT NOT NULL, domain TEXT NOT NULL, name TEXT NOT NULL, attributes TEXT NOT NULL, PRIMARY KEY(account,region,domain,name), FOREIGN KEY(account,region,domain) REFERENCES aws_simpledb_domains(account,region,name) ON DELETE CASCADE)`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) write(ctx context.Context, fn func(*sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return failure(ctx, err)
	}
	// Keep one writer per store, but do not leave canceled requests
	// waiting for an unrelated transaction to finish.
	select {
	case s.writer <- struct{}{}:
	case <-ctx.Done():
		return failure(ctx, ctx.Err())
	}
	defer func() { <-s.writer }()
	// Cancellation and acquisition can become ready at the same time.
	if err := ctx.Err(); err != nil {
		return failure(ctx, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return failure(ctx, err)
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return failure(ctx, err)
	}
	if err = ctx.Err(); err != nil {
		return failure(ctx, err)
	}
	return failure(ctx, tx.Commit())
}
func requireDomain(ctx context.Context, tx *sql.Tx, a, r, d string) error {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM aws_simpledb_domains WHERE account=? AND region=? AND name=?`, a, r, d).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return storeError("NoSuchDomain")
	}
	return err
}
