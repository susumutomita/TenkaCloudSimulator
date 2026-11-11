package appengine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Local bounds. They are simulator budgets, not App Engine quotas.
const (
	maxServices        = 10
	maxVersions        = 20 // per service
	maxAllVersions     = 50
	maxFirewallRules   = 100 // besides the default rule
	maxOperations      = 1000
	defaultRule        = 2147483647
	allocationUnits    = 1000 // traffic shares are stored in thousandths
	defaultPageSize    = 50
	maxPageSize        = 1000
	maxEnvironmentVars = 32
)

// createTables keeps the provider graph in SQLite. Foreign keys make a
// version that still receives traffic, a service with versions and an
// application with services or rules impossible to delete out of order.
func createTables(db *sql.DB) error {
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		return errors.New("App Engine requires SQLite foreign keys enabled")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS gcp_appengine_applications(
 project TEXT PRIMARY KEY, location TEXT NOT NULL, serving_status TEXT NOT NULL, created TEXT NOT NULL, updated TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS gcp_appengine_services(
 project TEXT NOT NULL, id TEXT NOT NULL, shard_by TEXT NOT NULL, created TEXT NOT NULL,
 PRIMARY KEY(project, id), FOREIGN KEY(project) REFERENCES gcp_appengine_applications(project) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS gcp_appengine_versions(
 project TEXT NOT NULL, service TEXT NOT NULL, id TEXT NOT NULL, runtime TEXT NOT NULL, serving_status TEXT NOT NULL,
 env_variables TEXT NOT NULL, deployment TEXT NOT NULL, code BLOB NOT NULL, code_sha256 TEXT NOT NULL, files INTEGER NOT NULL,
 created TEXT NOT NULL, created_by TEXT NOT NULL, seq INTEGER NOT NULL,
 PRIMARY KEY(project, service, id), FOREIGN KEY(project, service) REFERENCES gcp_appengine_services(project, id) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS gcp_appengine_allocations(
 project TEXT NOT NULL, service TEXT NOT NULL, version TEXT NOT NULL, share INTEGER NOT NULL CHECK(share > 0 AND share <= 1000),
 PRIMARY KEY(project, service, version), FOREIGN KEY(project, service, version) REFERENCES gcp_appengine_versions(project, service, id) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS gcp_appengine_firewall(
 project TEXT NOT NULL, priority INTEGER NOT NULL CHECK(priority >= 1 AND priority <= 2147483647), action TEXT NOT NULL,
 source_range TEXT NOT NULL, description TEXT NOT NULL,
 PRIMARY KEY(project, priority), FOREIGN KEY(project) REFERENCES gcp_appengine_applications(project) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS gcp_appengine_operations(
 project TEXT NOT NULL, id TEXT NOT NULL, seq INTEGER NOT NULL, document TEXT NOT NULL, PRIMARY KEY(project, id));
CREATE TABLE IF NOT EXISTS gcp_appengine_sequence(id INTEGER PRIMARY KEY CHECK(id = 1), next INTEGER NOT NULL);
INSERT OR IGNORE INTO gcp_appengine_sequence VALUES(1, 1);`)
	return err
}

// write runs fn in one serializable write transaction.
func (e *Engine) write(ctx context.Context, fn func(*sql.Tx) (any, error)) (any, error) {
	tx, err := e.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out, err := fn(tx)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// read runs fn in one transaction, so a response is one snapshot. It does not
// use a read-only transaction: the SQLite driver keeps query_only on the
// pooled connection, which would refuse later writes.
func (e *Engine) read(ctx context.Context, fn func(*sql.Tx) (any, error)) (any, error) {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return fn(tx)
}

func next(tx *sql.Tx) (int64, error) {
	var seq int64
	if err := tx.QueryRow("UPDATE gcp_appengine_sequence SET next = next + 1 WHERE id = 1 RETURNING next - 1").Scan(&seq); err != nil {
		return 0, err
	}
	return seq, nil
}

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

type application struct {
	Location, ServingStatus, Created, Updated string
}

func loadApplication(tx *sql.Tx, project string) (application, error) {
	var a application
	err := tx.QueryRow("SELECT location, serving_status, created, updated FROM gcp_appengine_applications WHERE project = ?", project).Scan(&a.Location, &a.ServingStatus, &a.Created, &a.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, notFound("Apps instance not found: apps/" + project + ". Create the App Engine application first.")
	}
	return a, err
}

// operation stores a completed long-running operation and returns it.
// Mutations complete synchronously, as the catalog's operations do.
func (e *Engine) operation(tx *sql.Tx, subject, rpc, target string, response map[string]any) (map[string]any, error) {
	seq, err := next(tx)
	if err != nil {
		return nil, err
	}
	now := timestamp(e.cfg.Now())
	name := "apps/" + e.cfg.Project + "/operations/" + uuid()
	op := map[string]any{"name": name, "done": true,
		"metadata": map[string]any{"@type": "type.googleapis.com/google.appengine.v1.OperationMetadataV1", "method": "google.appengine.v1." + rpc,
			"insertTime": now, "endTime": now, "user": subject, "target": target},
		"response": response}
	document, err := json.Marshal(op)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec("INSERT INTO gcp_appengine_operations(project, id, seq, document) VALUES(?, ?, ?, ?)", e.cfg.Project, name, seq, string(document)); err != nil {
		return nil, err
	}
	// Keep the newest operations; older ones are pruned and then NOT_FOUND.
	_, err = tx.Exec(`DELETE FROM gcp_appengine_operations WHERE project = ? AND seq <= (
 SELECT seq FROM gcp_appengine_operations WHERE project = ? ORDER BY seq DESC LIMIT 1 OFFSET ?)`, e.cfg.Project, e.cfg.Project, maxOperations)
	return op, err
}

func typed(kind string, value map[string]any) map[string]any {
	out := map[string]any{"@type": "type.googleapis.com/google." + kind}
	for k, v := range value {
		out[k] = v
	}
	return out
}
