package simpledb

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) CreateDomain(ctx context.Context, a, r, d string) error {
	if ctx.Err() != nil {
		return storeError("Cancelled")
	}
	if err := domainScope(a, r, d); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO aws_simpledb_domains(account,region,name) VALUES(?,?,?) ON CONFLICT(account,region,name) DO NOTHING`, a, r, d); err != nil {
			return err
		}
		return requireDomain(ctx, tx, a, r, d)
	})
}
func (s *Store) DeleteDomain(ctx context.Context, a, r, d string) error {
	if ctx.Err() != nil {
		return storeError("Cancelled")
	}
	if err := domainScope(a, r, d); err != nil {
		return err
	}
	return s.write(ctx, func(tx *sql.Tx) error {
		// Explicit deletion also preserves cascade semantics on caller connections
		// where SQLite foreign-key enforcement has not been enabled.
		if _, err := tx.ExecContext(ctx, `DELETE FROM aws_simpledb_items WHERE account=? AND region=? AND domain=?`, a, r, d); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM aws_simpledb_domains WHERE account=? AND region=? AND name=?`, a, r, d); err != nil {
			return err
		}
		var remaining int
		err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM aws_simpledb_domains WHERE account=? AND region=? AND name=?)+(SELECT count(*) FROM aws_simpledb_items WHERE account=? AND region=? AND domain=?)`, a, r, d, a, r, d).Scan(&remaining)
		if err != nil {
			return err
		}
		if remaining != 0 {
			return errors.New("SimpleDB deletion did not persist")
		}
		return nil
	})
}
