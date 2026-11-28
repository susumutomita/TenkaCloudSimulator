package aws

import (
	"context"
	"database/sql"
	"time"
)

const kmsDayMillis = int64(24 * time.Hour / time.Millisecond)

const kmsDeletionSchema = `CREATE TABLE IF NOT EXISTS aws_kms_pending_deletions (
 region TEXT NOT NULL, key_id TEXT NOT NULL,
 scheduled_ms INTEGER NOT NULL CHECK(typeof(scheduled_ms)='integer' AND scheduled_ms>0),
 window_days INTEGER NOT NULL CHECK(typeof(window_days)='integer' AND window_days BETWEEN 7 AND 30),
 due_ms INTEGER NOT NULL CHECK(typeof(due_ms)='integer' AND due_ms=scheduled_ms+window_days*86400000),
 PRIMARY KEY(region,key_id), FOREIGN KEY(region,key_id) REFERENCES aws_kms_keys(region,key_id));
 CREATE INDEX IF NOT EXISTS aws_kms_deletion_deadlines ON aws_kms_pending_deletions(due_ms);`

func kmsInvalidState() error {
	return kmsFailure("KMSInvalidStateException", "The operation is incompatible with the KMS key state.")
}

// Reap in a separate committed transaction before the triggering operation.
// A subsequent NotFound, denied grant, or rejected Cancel must not restore an
// expired key. Recheck after each commit so no request observes a gap.
func (s *KMSStore) beginAt(ctx context.Context) (*sql.Tx, int64, error) {
	for {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, 0, err
		}
		now := s.now().UTC().UnixMilli()
		changed, err := kmsReapDue(ctx, tx, now)
		if err != nil {
			tx.Rollback()
			return nil, 0, err
		}
		if !changed {
			return tx, now, nil
		}
		if err = tx.Commit(); err != nil {
			return nil, 0, err
		}
	}
}

func (s *KMSStore) begin(ctx context.Context) (*sql.Tx, error) {
	tx, _, err := s.beginAt(ctx)
	return tx, err
}

func kmsReapDue(ctx context.Context, tx *sql.Tx, now int64) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT region,key_id FROM aws_kms_pending_deletions WHERE due_ms<=? ORDER BY due_ms,region,key_id`, now)
	if err != nil {
		return false, err
	}
	type target struct{ region, id string }
	var keys []target
	for rows.Next() {
		var key target
		if err = rows.Scan(&key.region, &key.id); err != nil {
			rows.Close()
			return false, err
		}
		keys = append(keys, key)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	if err = rows.Close(); err != nil {
		return false, err
	}
	if len(keys) == 0 {
		return false, nil
	}
	// Alias schema is initialized on the first alias request. Never create it
	// as a side effect of expiring a key without any customer aliases.
	var haveAliases bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='aws_kms_aliases')`).Scan(&haveAliases); err != nil {
		return false, err
	}
	for _, key := range keys {
		record, err := kmsRead(ctx, tx, key.region, key.id)
		if err != nil {
			return false, err
		}
		if record.metadata.KeyState != "PendingDeletion" {
			return false, kmsCorrupt("pending deletion")
		}
		if haveAliases {
			n, err := kmsDeleteChildren(ctx, tx, "aws_kms_aliases", key.region, key.id)
			if err != nil {
				return false, err
			}
			if n > 0 {
				aliases := KMSAliasStore{region: key.region}
				if _, _, err = aliases.stateTx(ctx, tx); err != nil {
					return false, err
				}
				if err = kmsGrantWrite(ctx, tx, `UPDATE aws_kms_alias_state SET revision=revision+1 WHERE region=?`, key.region); err != nil {
					return false, err
				}
			}
		}
		if _, err = kmsDeleteChildren(ctx, tx, "aws_kms_grants", key.region, key.id); err != nil {
			return false, err
		}
		if err = kmsGrantWrite(ctx, tx, `DELETE FROM aws_kms_pending_deletions WHERE region=? AND key_id=? AND due_ms<=?`, key.region, key.id, now); err != nil {
			return false, err
		}
		if err = kmsGrantWrite(ctx, tx, `DELETE FROM aws_kms_keys WHERE region=? AND key_id=?`, key.region, key.id); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Only provider-owned table constants reach this helper. Count the exact set
// before the deletion and include trigger side effects when verifying its frame.
func kmsDeleteChildren(ctx context.Context, tx *sql.Tx, table, region, id string) (int64, error) {
	var count, before, after int64
	where := " WHERE region=? AND key_id=?"
	if err := tx.QueryRowContext(ctx, "SELECT count(*), total_changes() FROM "+table+where, region, id).Scan(&count, &before); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+table+where, region, id)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
		return 0, err
	}
	if n != count || after-before != count {
		return 0, kmsInternal()
	}
	return n, nil
}

type kmsScheduledDeletion struct {
	KeyID               string `json:"KeyId"`
	DeletionDate        float64
	KeyState            string
	PendingWindowInDays int
}

func (s *KMSStore) scheduleDeletion(ctx context.Context, region, key string, days int) (kmsScheduledDeletion, error) {
	var result kmsScheduledDeletion
	id, err := kmsID(region, key)
	if err != nil || days < 7 || days > 30 {
		return result, kmsInvalid()
	}
	tx, now, err := s.beginAt(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	row, err := kmsRead(ctx, tx, region, id)
	if err != nil {
		return result, err
	}
	if row.metadata.KeyState == "PendingDeletion" {
		return result, kmsInvalidState()
	}
	due := now + int64(days)*kmsDayMillis
	if err = kmsGrantWrite(ctx, tx, `INSERT INTO aws_kms_pending_deletions(region,key_id,scheduled_ms,window_days,due_ms) VALUES(?,?,?,?,?)`, region, id, now, days, due); err != nil {
		return result, err
	}
	if err = kmsGrantWrite(ctx, tx, `UPDATE aws_kms_keys SET state='Disabled' WHERE region=? AND key_id=?`, region, id); err != nil {
		return result, err
	}
	saved, err := kmsRead(ctx, tx, region, id)
	if err != nil {
		return result, err
	}
	date := float64(due) / 1000
	if saved.metadata.KeyState != "PendingDeletion" || saved.metadata.DeletionDate != date {
		return result, kmsInternal()
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return kmsScheduledDeletion{saved.metadata.Arn, date, "PendingDeletion", days}, nil
}

func (s *KMSStore) cancelDeletion(ctx context.Context, region, key string) (string, error) {
	id, err := kmsID(region, key)
	if err != nil {
		return "", err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	row, err := kmsRead(ctx, tx, region, id)
	if err != nil {
		return "", err
	}
	if row.metadata.KeyState != "PendingDeletion" {
		return "", kmsInvalidState()
	}
	if err = kmsGrantWrite(ctx, tx, `DELETE FROM aws_kms_pending_deletions WHERE region=? AND key_id=?`, region, id); err != nil {
		return "", err
	}
	saved, err := kmsRead(ctx, tx, region, id)
	if err != nil {
		return "", err
	}
	if saved.metadata.KeyState != "Disabled" || saved.metadata.DeletionDate != 0 {
		return "", kmsInternal()
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return saved.metadata.Arn, nil
}
