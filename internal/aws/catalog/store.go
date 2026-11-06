package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Local physical budgets for catalog metadata. They are simulator limits,
// not AWS service quotas, and are reported in capabilities.
const (
	maxDocumentBytes    = 64 << 10
	maxStoredBytes      = 32 << 20
	maxTagsPerResource  = 50
	defaultKindLimit    = 100
	maxIdempotencyRows  = 10000
	idempotencyLifetime = 24 * 3600
)

// row is one persisted resource. Document holds stored-JSON members of the
// official resource shape; Tags holds the resource tag map.
type row struct {
	Service  string
	Region   string
	Kind     string
	Parent   string
	ID       string
	Name     string
	ARN      string
	Document map[string]any
	Tags     map[string]string
	// Seq orders creation for stable pagination; Generation guards writes.
	Seq        int64
	Generation int64
}

// store owns the catalog tables; it shares no rows with other AWS services.
type store struct{ db *sql.DB }

func newStore(db *sql.DB) (*store, error) {
	if db == nil {
		return nil, errors.New("the AWS catalog requires a provider database")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_catalog_resources(
 service TEXT NOT NULL, region TEXT NOT NULL, kind TEXT NOT NULL, parent TEXT NOT NULL,
 id TEXT NOT NULL, name TEXT NOT NULL, arn TEXT NOT NULL, document TEXT NOT NULL,
 tags TEXT NOT NULL, seq INTEGER NOT NULL, generation INTEGER NOT NULL,
 PRIMARY KEY(service, region, kind, parent, id));
CREATE UNIQUE INDEX IF NOT EXISTS aws_catalog_resources_arn ON aws_catalog_resources(arn);
CREATE INDEX IF NOT EXISTS aws_catalog_resources_name ON aws_catalog_resources(service, region, kind, parent, name);
CREATE TABLE IF NOT EXISTS aws_catalog_idempotency(
 service TEXT NOT NULL, region TEXT NOT NULL, operation TEXT NOT NULL, token TEXT NOT NULL,
 fingerprint TEXT NOT NULL, arn TEXT NOT NULL, created INTEGER NOT NULL,
 PRIMARY KEY(service, region, operation, token));
CREATE TABLE IF NOT EXISTS aws_catalog_sequence(id INTEGER PRIMARY KEY CHECK(id = 1), next INTEGER NOT NULL);
INSERT OR IGNORE INTO aws_catalog_sequence VALUES(1, 1);`)
	if err != nil {
		return nil, err
	}
	return &store{db}, nil
}

type scanner interface{ Scan(...any) error }

func scanRow(s scanner) (row, error) {
	var r row
	var document, tags string
	if err := s.Scan(&r.Service, &r.Region, &r.Kind, &r.Parent, &r.ID, &r.Name, &r.ARN, &document, &tags, &r.Seq, &r.Generation); err != nil {
		return row{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&r.Document); err != nil || r.Document == nil {
		return row{}, errors.New("catalog resource document is corrupt; preserved for explicit repair")
	}
	if err := json.Unmarshal([]byte(tags), &r.Tags); err != nil || r.Tags == nil {
		return row{}, errors.New("catalog resource tags are corrupt; preserved for explicit repair")
	}
	return r, nil
}

const columns = "service, region, kind, parent, id, name, arn, document, tags, seq, generation"

type querier interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

// find resolves an identifier that may be the ARN, name or ID of one resource.
func find(q querier, service, region, kind, parent, identifier string) (row, error) {
	rows, err := q.Query("SELECT "+columns+" FROM aws_catalog_resources WHERE service=? AND region=? AND kind=? AND parent=? AND (arn=? OR name=? OR id=?) ORDER BY seq", service, region, kind, parent, identifier, identifier, identifier)
	if err != nil {
		return row{}, err
	}
	defer rows.Close()
	var found []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return row{}, err
		}
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return row{}, err
	}
	switch {
	case len(found) == 0:
		return row{}, sql.ErrNoRows
	case len(found) > 1:
		// A non-unique name cannot identify one resource; ARN or ID can.
		for _, r := range found {
			if r.ARN == identifier || r.ID == identifier {
				return r, nil
			}
		}
		return row{}, &fault{kind: "validation", message: "The name matches several resources; use the ARN or identifier."}
	}
	return found[0], nil
}

func findARN(q querier, service, arn string) (row, error) {
	return scanRow(q.QueryRow("SELECT "+columns+" FROM aws_catalog_resources WHERE service=? AND arn=?", service, arn))
}

func list(q querier, service, region, kind, parent string) ([]row, error) {
	rows, err := q.Query("SELECT "+columns+" FROM aws_catalog_resources WHERE service=? AND region=? AND kind=? AND parent=? ORDER BY seq", service, region, kind, parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []row{}
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func encodeRow(r row) (string, string, error) {
	document, err := json.Marshal(r.Document)
	if err != nil {
		return "", "", err
	}
	if len(document) > maxDocumentBytes {
		return "", "", &fault{kind: "limit", message: "The resource description exceeds the simulator's 64 KiB metadata limit."}
	}
	tags, err := json.Marshal(r.Tags)
	return string(document), string(tags), err
}

func storedBytes(tx *sql.Tx) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRow("SELECT sum(length(document) + length(tags)) FROM aws_catalog_resources").Scan(&total)
	return total.Int64, err
}

func nextSeq(tx *sql.Tx) (int64, error) {
	var next int64
	if err := tx.QueryRow("SELECT next FROM aws_catalog_sequence WHERE id=1").Scan(&next); err != nil {
		return 0, err
	}
	written, err := tx.Exec("UPDATE aws_catalog_sequence SET next=? WHERE id=1 AND next=?", next+1, next)
	if err != nil {
		return 0, err
	}
	if n, err := written.RowsAffected(); err != nil || n != 1 {
		return 0, errors.New("catalog sequence did not advance exactly once")
	}
	return next, nil
}

func exactlyOne(result sql.Result, err error) error {
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return &fault{kind: "conflict", message: "A resource with this identity already exists."}
		}
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("catalog write did not affect exactly one resource")
	}
	return nil
}

func insertRow(tx *sql.Tx, r row) error {
	document, tags, err := encodeRow(r)
	if err != nil {
		return err
	}
	if err := exactlyOne(tx.Exec("INSERT INTO aws_catalog_resources("+columns+") VALUES(?,?,?,?,?,?,?,?,?,?,1)", r.Service, r.Region, r.Kind, r.Parent, r.ID, r.Name, r.ARN, document, tags, r.Seq)); err != nil {
		return err
	}
	return withinBudget(tx)
}

// withinBudget enforces the aggregate metadata budget after a write inside
// its transaction, so insertions and replacements alike roll back.
func withinBudget(tx *sql.Tx) error {
	total, err := storedBytes(tx)
	if err != nil {
		return err
	}
	if total > maxStoredBytes {
		return &fault{kind: "limit", message: "The simulator catalog metadata budget is exhausted."}
	}
	return nil
}

// updateRow replaces document/tags only when the row still has the
// generation observed before authorization, so a concurrently changed or
// replaced resource is never overwritten.
func updateRow(tx *sql.Tx, r row) error {
	document, tags, err := encodeRow(r)
	if err != nil {
		return err
	}
	if err := exactlyOne(tx.Exec("UPDATE aws_catalog_resources SET document=?, tags=?, generation=generation+1 WHERE service=? AND arn=? AND seq=? AND generation=?", document, tags, r.Service, r.ARN, r.Seq, r.Generation)); err != nil {
		return err
	}
	return withinBudget(tx)
}

func deleteRow(tx *sql.Tx, r row) error {
	return exactlyOne(tx.Exec("DELETE FROM aws_catalog_resources WHERE service=? AND arn=? AND seq=? AND generation=?", r.Service, r.ARN, r.Seq, r.Generation))
}

func countChildren(tx *sql.Tx, service, region, parentID string, kinds []string) (int, error) {
	total := 0
	for _, kind := range kinds {
		var n int
		if err := tx.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service=? AND region=? AND kind=? AND parent=?", service, region, kind, parentID).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func deleteChildren(tx *sql.Tx, service, region, parentID string, kinds []string) error {
	for _, kind := range kinds {
		if _, err := tx.Exec("DELETE FROM aws_catalog_resources WHERE service=? AND region=? AND kind=? AND parent=?", service, region, kind, parentID); err != nil {
			return err
		}
	}
	return nil
}

func countKind(tx *sql.Tx, service, region, kind, parent string) (int, error) {
	var n int
	err := tx.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service=? AND region=? AND kind=? AND parent=?", service, region, kind, parent).Scan(&n)
	return n, err
}

func nameTaken(tx *sql.Tx, service, region, kind, parent, name string) (bool, error) {
	var n int
	err := tx.QueryRow("SELECT count(*) FROM aws_catalog_resources WHERE service=? AND region=? AND kind=? AND parent=? AND name=?", service, region, kind, parent, name).Scan(&n)
	return n > 0, err
}
