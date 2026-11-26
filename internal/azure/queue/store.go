package queue

import (
	"database/sql"
	"errors"
)

const schema = `CREATE TABLE IF NOT EXISTS azure_queue_scope(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),account TEXT NOT NULL,environment TEXT NOT NULL,owner TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS azure_queues(name TEXT PRIMARY KEY);
CREATE TABLE IF NOT EXISTS azure_queue_messages(
 seq INTEGER PRIMARY KEY AUTOINCREMENT,queue TEXT NOT NULL REFERENCES azure_queues(name) ON DELETE CASCADE,
 id TEXT NOT NULL UNIQUE,text TEXT NOT NULL,inserted INTEGER NOT NULL,expires INTEGER NOT NULL,
 visible INTEGER NOT NULL,receipt TEXT NOT NULL,dequeues INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS azure_queue_ready ON azure_queue_messages(queue,visible,seq);`

func initialize(db *sql.DB, cfg Config) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var fk int
	if err = tx.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		return err
	}
	if fk != 1 {
		return errors.New("Queue Storage requires SQLite foreign keys")
	}
	if _, err = tx.Exec(schema); err != nil {
		return err
	}
	var a, e, o string
	err = tx.QueryRow("SELECT account,environment,owner FROM azure_queue_scope WHERE singleton=1").Scan(&a, &e, &o)
	if errors.Is(err, sql.ErrNoRows) {
		var n int
		if err = tx.QueryRow("SELECT (SELECT count(*) FROM azure_queues)+(SELECT count(*) FROM azure_queue_messages)").Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return errors.New("Queue scope missing with existing data; explicit repair required")
		}
		err = checkedExec(tx, 1, "INSERT INTO azure_queue_scope VALUES(1,?,?,?)", cfg.Account, cfg.Environment, cfg.Owner)
		if err == nil {
			err = tx.QueryRow("SELECT account,environment,owner FROM azure_queue_scope WHERE singleton=1").Scan(&a, &e, &o)
			if err == nil && (a != cfg.Account || e != cfg.Environment || o != cfg.Owner) {
				err = errors.New("Queue scope insert postcondition failed")
			}
		}
	} else if err == nil && (a != cfg.Account || e != cfg.Environment || o != cfg.Owner) {
		return errors.New("Queue account/environment/owner drift")
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

type storedMessage struct {
	ID, Text, Receipt                 string
	Inserted, Expires, Visible, Count int64
}

func (m storedMessage) wire(payload, receipt bool) wireMessage {
	w := wireMessage{ID: m.ID, Inserted: stamp(m.Inserted), Expires: stamp(m.Expires), Count: m.Count}
	if payload {
		v := m.Text
		w.Text = &v
	}
	if receipt {
		w.Receipt = m.Receipt
		w.Visible = stamp(m.Visible)
	}
	return w
}
func queueExists(tx *sql.Tx, name string) error {
	var found string
	err := tx.QueryRow("SELECT name FROM azure_queues WHERE name=?", name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return problem(404, "QueueNotFound")
	}
	return err
}
func liveMessage(tx *sql.Tx, queue, id, receipt string, now int64) (storedMessage, error) {
	var m storedMessage
	err := tx.QueryRow("SELECT id,text,receipt,inserted,expires,visible,dequeues FROM azure_queue_messages WHERE queue=? AND id=? AND expires>?", queue, id, now).Scan(&m.ID, &m.Text, &m.Receipt, &m.Inserted, &m.Expires, &m.Visible, &m.Count)
	if errors.Is(err, sql.ErrNoRows) {
		return m, problem(404, "MessageNotFound")
	}
	if err != nil {
		return m, err
	}
	if receipt != m.Receipt {
		return m, problem(400, "PopReceiptMismatch")
	}
	return m, nil
}

// checkedExec treats suppressed writes as storage failures, never successful
// protocol operations. The surrounding transaction must remain uncommitted.
func checkedExec(tx *sql.Tx, expected int64, query string, args ...any) error {
	result, err := tx.Exec(query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != expected {
		return errors.New("Queue mutation affected unexpected rows")
	}
	return nil
}
func checkedDelete(tx *sql.Tx, table, where string, args ...any) error {
	// table/where are fixed implementation literals, never request expressions.
	var before, after int64
	if err := tx.QueryRow("SELECT count(*) FROM "+table+" WHERE "+where, args...).Scan(&before); err != nil {
		return err
	}
	if err := checkedExec(tx, before, "DELETE FROM "+table+" WHERE "+where, args...); err != nil {
		return err
	}
	if err := tx.QueryRow("SELECT count(*) FROM "+table+" WHERE "+where, args...).Scan(&after); err != nil {
		return err
	}
	if after != 0 {
		return errors.New("Queue deletion postcondition failed")
	}
	return nil
}
func verifyMessage(tx *sql.Tx, queue string, m storedMessage) error {
	var got storedMessage
	err := tx.QueryRow("SELECT id,text,receipt,inserted,expires,visible,dequeues FROM azure_queue_messages WHERE queue=? AND id=?", queue, m.ID).Scan(&got.ID, &got.Text, &got.Receipt, &got.Inserted, &got.Expires, &got.Visible, &got.Count)
	if err != nil {
		return err
	}
	if got != m {
		return errors.New("Queue message mutation postcondition failed")
	}
	return nil
}
