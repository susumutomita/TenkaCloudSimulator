package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const (
	maxDocumentBytes  = 64 << 10
	maxStoredBytes    = 32 << 20
	defaultLimit      = 100
	maxOperations     = 10000
	requestIDLifetime = 24 * 3600
	maxPageSize       = 1000
	defaultPageSize   = 50
)

type row struct {
	API        string
	Name       string
	Parent     string
	Collection string
	ID         string
	Document   map[string]any
	Seq        int64
	Generation int64
}

type store struct{ db *sql.DB }

func newStore(db *sql.DB) (*store, error) {
	if db == nil {
		return nil, errors.New("the GCP catalog requires a provider database")
	}
	// Resource names are unique per API, as in Google Cloud: Memorystore
	// and Filestore may each hold projects/p/locations/l/instances/x.
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS gcp_catalog_resources(
 api TEXT NOT NULL, name TEXT NOT NULL, parent TEXT NOT NULL, collection TEXT NOT NULL,
 id TEXT NOT NULL, document TEXT NOT NULL, seq INTEGER NOT NULL, generation INTEGER NOT NULL, PRIMARY KEY(api, name));
CREATE INDEX IF NOT EXISTS gcp_catalog_resources_parent ON gcp_catalog_resources(api, parent, collection, seq);
CREATE TABLE IF NOT EXISTS gcp_catalog_operations(
 api TEXT NOT NULL, name TEXT NOT NULL PRIMARY KEY, scope TEXT NOT NULL, document TEXT NOT NULL, seq INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS gcp_catalog_requests(
 api TEXT NOT NULL, request_id TEXT NOT NULL, method TEXT NOT NULL, fingerprint TEXT NOT NULL,
 response TEXT NOT NULL, created INTEGER NOT NULL, PRIMARY KEY(api, method, request_id));
CREATE TABLE IF NOT EXISTS gcp_catalog_sequence(id INTEGER PRIMARY KEY CHECK(id = 1), next INTEGER NOT NULL);
INSERT OR IGNORE INTO gcp_catalog_sequence VALUES(1, 1);`)
	if err != nil {
		return nil, err
	}
	return &store{db}, nil
}

type querier interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

const columns = "api, name, parent, collection, id, document, seq, generation"

type scanner interface{ Scan(...any) error }

func scanRow(s scanner) (row, error) {
	var r row
	var document string
	if err := s.Scan(&r.API, &r.Name, &r.Parent, &r.Collection, &r.ID, &document, &r.Seq, &r.Generation); err != nil {
		return row{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&r.Document); err != nil || r.Document == nil {
		return row{}, errors.New("GCP catalog resource document is corrupt; preserved for explicit repair")
	}
	return r, nil
}

// findName reads one resource of an API. Resource names are scoped per API:
// two services may use the same relative name, as Google full resource
// names (//<service>.googleapis.com/...) differ.
func findName(q querier, api, name string) (row, error) {
	return scanRow(q.QueryRow("SELECT "+columns+" FROM gcp_catalog_resources WHERE api=? AND name=?", api, name))
}

func listChildren(q querier, api, parent, collection string) ([]row, error) {
	rows, err := q.Query("SELECT "+columns+" FROM gcp_catalog_resources WHERE api=? AND parent=? AND collection=? ORDER BY seq", api, parent, collection)
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

func encode(document map[string]any) (string, error) {
	data, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	if len(data) > maxDocumentBytes {
		return "", &status{"INVALID_ARGUMENT", "The resource exceeds the simulator's 64 KiB metadata limit."}
	}
	return string(data), nil
}

func nextSeq(tx *sql.Tx) (int64, error) {
	var next int64
	if err := tx.QueryRow("SELECT next FROM gcp_catalog_sequence WHERE id=1").Scan(&next); err != nil {
		return 0, err
	}
	return next, exactlyOne(tx.Exec("UPDATE gcp_catalog_sequence SET next=? WHERE id=1 AND next=?", next+1, next))
}

func exactlyOne(result sql.Result, err error) error {
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return &status{"ALREADY_EXISTS", "The resource already exists."}
		}
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return &status{"ABORTED", "The resource changed concurrently; read it again before retrying."}
	}
	return nil
}

func storedBytes(tx *sql.Tx) (int64, error) {
	var total sql.NullInt64
	err := tx.QueryRow("SELECT sum(length(document)) FROM gcp_catalog_resources").Scan(&total)
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
		return &status{"RESOURCE_EXHAUSTED", "The simulator catalog metadata budget is exhausted."}
	}
	return nil
}
