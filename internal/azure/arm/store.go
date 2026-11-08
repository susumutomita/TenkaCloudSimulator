package arm

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const (
	maxDocumentBytes = 64 << 10
	maxStoredBytes   = 32 << 20
	maxResources     = 10000
	defaultPageSize  = 100
	maxPageSize      = 1000
)

type row struct {
	Key        string
	ID         string
	Parent     string
	Type       string
	Document   map[string]any
	Seq        int64
	Generation int64
}

type store struct{ db *sql.DB }

// newStore creates the provider-owned tables and binds the environment to its
// subscription, tenant and owner. A persisted binding is never reassigned.
func newStore(db *sql.DB, cfg Config) (*store, error) {
	if db == nil {
		return nil, errors.New("azure ARM requires a provider database")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE IF NOT EXISTS azure_arm_scopes(
 environment TEXT NOT NULL PRIMARY KEY, subscription TEXT NOT NULL, tenant TEXT NOT NULL, owner TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS azure_arm_resources(
 key TEXT NOT NULL PRIMARY KEY, id TEXT NOT NULL, subscription TEXT NOT NULL, parent TEXT NOT NULL, type TEXT NOT NULL,
 document TEXT NOT NULL, seq INTEGER NOT NULL UNIQUE, generation INTEGER NOT NULL CHECK(generation > 0));
CREATE INDEX IF NOT EXISTS azure_arm_resources_parent ON azure_arm_resources(parent, type, seq);
CREATE INDEX IF NOT EXISTS azure_arm_resources_subscription ON azure_arm_resources(subscription, type, seq);
CREATE TABLE IF NOT EXISTS azure_arm_references(
 source TEXT NOT NULL REFERENCES azure_arm_resources(key) ON DELETE CASCADE,
 target TEXT NOT NULL REFERENCES azure_arm_resources(key) DEFERRABLE INITIALLY DEFERRED,
 path TEXT NOT NULL, in_use TEXT NOT NULL, PRIMARY KEY(source, path, target));
CREATE INDEX IF NOT EXISTS azure_arm_references_target ON azure_arm_references(target);
CREATE TABLE IF NOT EXISTS azure_arm_sequence(id INTEGER PRIMARY KEY CHECK(id = 1), next INTEGER NOT NULL);
INSERT OR IGNORE INTO azure_arm_sequence VALUES(1, 1);`); err != nil {
		return nil, err
	}
	var subscription, tenant, owner string
	err = tx.QueryRow("SELECT subscription, tenant, owner FROM azure_arm_scopes WHERE environment=?", cfg.Environment).Scan(&subscription, &tenant, &owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err = tx.Exec("INSERT INTO azure_arm_scopes VALUES(?,?,?,?)", cfg.Environment, cfg.Subscription, cfg.Tenant, cfg.Owner); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case subscription != cfg.Subscription || tenant != cfg.Tenant || owner != cfg.Owner:
		return nil, errors.New("azure ARM subscription, tenant or owner drift; the persisted binding is preserved for explicit repair")
	}
	return &store{db}, tx.Commit()
}

type querier interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

const columns = "key, id, parent, type, document, seq, generation"

type scanner interface{ Scan(...any) error }

func scanRow(s scanner) (row, error) {
	var r row
	var document string
	if err := s.Scan(&r.Key, &r.ID, &r.Parent, &r.Type, &document, &r.Seq, &r.Generation); err != nil {
		return row{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&r.Document); err != nil || r.Document == nil {
		return row{}, errors.New("azure ARM resource document is corrupt; preserved for explicit repair")
	}
	r.Document = numbers(r.Document).(map[string]any)
	return r, nil
}

// numbers restores the decoder's int64 and float64 values in a stored
// document, so hooks see the same types on create and update.
func numbers(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for k, v := range typed {
			typed[k] = numbers(v)
		}
	case []any:
		for i, v := range typed {
			typed[i] = numbers(v)
		}
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return n
		}
		f, _ := typed.Float64()
		return f
	}
	return value
}

// find returns the row of a lower-case key, or sql.ErrNoRows.
func find(q querier, key string) (row, error) {
	return scanRow(q.QueryRow("SELECT "+columns+" FROM azure_arm_resources WHERE key=?", key))
}

func rows(q querier, query string, args ...any) ([]row, error) {
	result, err := q.Query("SELECT "+columns+" FROM azure_arm_resources WHERE "+query, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	out := []row{}
	for result.Next() {
		r, err := scanRow(result)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, result.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func encode(document map[string]any) (string, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	if len(data) > maxDocumentBytes {
		return "", fail(400, "InvalidRequestContent", "The resource exceeds the simulator's 64 KiB metadata limit.")
	}
	return string(data), nil
}

func nextSeq(tx *sql.Tx) (int64, error) {
	var next int64
	if err := tx.QueryRow("SELECT next FROM azure_arm_sequence WHERE id=1").Scan(&next); err != nil {
		return 0, err
	}
	return next, exactlyOne(tx.Exec("UPDATE azure_arm_sequence SET next=? WHERE id=1 AND next=?", next+1, next))
}

func exactlyOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fail(409, "Conflict", "The resource changed concurrently; read it again before retrying.")
	}
	return nil
}

func storedBytes(tx *sql.Tx) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRow("SELECT sum(length(document)) FROM azure_arm_resources").Scan(&total)
	return total.Int64, err
}

// withinBudget enforces the aggregate metadata budget after a write inside
// its transaction, so insertions and replacements alike roll back.
func withinBudget(tx *sql.Tx) error {
	total, err := storedBytes(tx)
	if err != nil {
		return err
	}
	if total > maxStoredBytes {
		return fail(429, "QuotaExceeded", "The simulator's Azure Resource Manager metadata budget is exhausted.")
	}
	return nil
}
