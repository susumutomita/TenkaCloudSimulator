//go:build linux && amd64

package catalog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/rdsruntime"
)

// The single durable fence is written with the authorized RDS creation. The
// SQLite triggers also cover tag writes and a later metadata-only engine.
const ownedPostgresFence = `
CREATE TABLE IF NOT EXISTS aws_catalog_owned_postgres(
 slot INTEGER PRIMARY KEY CHECK(slot=1), arn TEXT NOT NULL,
 seq INTEGER NOT NULL CHECK(seq>0), generation INTEGER NOT NULL CHECK(generation>0),
 owner TEXT NOT NULL);
CREATE TRIGGER IF NOT EXISTS aws_catalog_owned_postgres_update
BEFORE UPDATE ON aws_catalog_resources
WHEN OLD.service='rds' AND OLD.kind='dbInstance' AND EXISTS(
 SELECT 1 FROM aws_catalog_owned_postgres
 WHERE arn=OLD.arn AND seq=OLD.seq AND generation=OLD.generation)
BEGIN SELECT RAISE(ABORT,'owned PostgreSQL cleanup required'); END;
CREATE TRIGGER IF NOT EXISTS aws_catalog_owned_postgres_delete
BEFORE DELETE ON aws_catalog_resources
WHEN OLD.service='rds' AND OLD.kind='dbInstance' AND EXISTS(
 SELECT 1 FROM aws_catalog_owned_postgres
 WHERE arn=OLD.arn AND seq=OLD.seq AND generation=OLD.generation)
BEGIN SELECT RAISE(ABORT,'owned PostgreSQL cleanup required'); END;
`

var errOwnedPostgresDebt = errors.New("private RDS PostgreSQL ownership or cleanup is unconfirmed")

// OwnedPrivatePostgresConfig is trusted provider composition, not a Query
// field. Only one intentionally nominated instance can own a process in this
// engine, and then only its newly authorized generation.
type OwnedPrivatePostgresConfig struct {
	Region, Identifier string
	Bundle             *rdsruntime.Bundle
	WorkRoot           string
}

type privateDBGeneration struct {
	arn      string
	seq, gen int64
}

func generationOf(r row) privateDBGeneration {
	return privateDBGeneration{r.ARN, r.Seq, r.Generation}
}

// OwnedPrivatePostgres is a trusted lifetime owner, not a client capability.
// Close it before the shared provider database. It never publishes a secret,
// address, endpoint, or instance-availability status.
type OwnedPrivatePostgres struct {
	mu      sync.Mutex
	engine  *Engine
	options OwnedPrivatePostgresConfig
	owner   string
	key     privateDBGeneration
	child   *rdsruntime.Runtime
	last    error
	used    bool
	closed  bool
	debt    bool
}

// NewWithOwnedPrivatePostgres explicitly enables one native workload in an
// otherwise canonical catalog. A previous fence or nonempty work root is not
// adoptable by a new engine. Ordinary New creates no workload or fence.
func NewWithOwnedPrivatePostgres(db *sql.DB, cfg Config, options OwnedPrivatePostgresConfig) (*Engine, *OwnedPrivatePostgres, error) {
	name := map[string]any{"DBInstanceIdentifier": options.Identifier}
	if options.Bundle == nil || cfg.ValidateRDSPrivatePostgres == nil ||
		!slices.Contains(cfg.Regions, options.Region) ||
		rdsIdentifier(name, "DBInstanceIdentifier") != nil || name["DBInstanceIdentifier"] != options.Identifier {
		return nil, nil, errors.New("owned PostgreSQL requires one canonical private RDS target and native topology")
	}
	if err := rdsruntime.CheckIdleWorkRoot(options.WorkRoot); err != nil {
		return nil, nil, errOwnedPostgresDebt
	}
	engine, err := New(db, cfg, Services())
	if err != nil {
		return nil, nil, err
	}
	if err := installOwnedPostgresFence(db); err != nil {
		return nil, nil, err
	}
	owner := &OwnedPrivatePostgres{engine: engine, options: options, owner: rand.Text()}
	engine.ownedPrivateRDS = owner
	return engine, owner, nil
}

func installOwnedPostgresFence(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return errOwnedPostgresDebt
	}
	defer tx.Rollback()
	if _, err := tx.Exec(ownedPostgresFence); err != nil {
		return errOwnedPostgresDebt
	}
	var count int
	if err := tx.QueryRow("SELECT count(*) FROM aws_catalog_owned_postgres").Scan(&count); err != nil ||
		count != 0 || tx.Commit() != nil {
		return errOwnedPostgresDebt
	}
	return nil
}

func (c *OwnedPrivatePostgres) lock(region string, kind Kind) func() {
	if region == c.options.Region {
		switch kind {
		case Create, Update, Delete, Tag, Untag, Put:
			c.mu.Lock()
			return c.mu.Unlock
		}
	}
	return func() {}
}

func (c *OwnedPrivatePostgres) reserve(tx *sql.Tx, r row) (bool, error) {
	if r.Region != c.options.Region || r.ID != c.options.Identifier {
		return false, nil
	}
	// Mutators for this region hold mu across commit and physical teardown.
	version, _ := r.Document["EngineVersion"].(string)
	if c.used || c.closed || !privatePostgresShape(r) ||
		r.Name != r.ID || r.Seq <= 0 || r.Generation != 1 || version != "" && version != "15.20" {
		return false, nil
	}
	if err := exactlyOne(tx.Exec("INSERT INTO aws_catalog_owned_postgres(slot,arn,seq,generation,owner) VALUES(1,?,?,?,?)",
		r.ARN, r.Seq, r.Generation, c.owner)); err != nil {
		return false, errOwnedPostgresDebt
	}
	return true, nil
}

func (c *OwnedPrivatePostgres) startCommitted(caller context.Context, r row) {
	c.used, c.key = true, generationOf(r)
	// The authorization and fence already committed. A disconnected caller
	// must not interrupt ownership acquisition or teardown. The result is
	// still configuration-only even when this internal start fails.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(caller), 60*time.Second)
	defer cancel()
	if !c.current(ctx) {
		c.last = rdsruntime.ErrNotReady
		_ = c.retireCurrent()
		return
	}
	child, err := rdsruntime.Start(ctx, c.options.Bundle, c.options.WorkRoot)
	if err != nil {
		c.last = err
		if errors.Is(err, rdsruntime.ErrCleanupDebt) || rdsruntime.CheckIdleWorkRoot(c.options.WorkRoot) != nil {
			c.debt = true // retain the matching durable fence indefinitely
			return
		}
		_ = c.retireCurrent()
		return
	}
	c.child = child
	if !c.current(ctx) {
		c.last = rdsruntime.ErrNotReady
		_ = c.retireCurrent() // no SQLite transaction lives across physical Close
	}
}

func (c *OwnedPrivatePostgres) current(ctx context.Context) bool {
	// This is a lookup-only transaction. Do not change query_only: the
	// provider's SQLite connection must remain usable for fence release.
	tx, err := c.engine.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	if !c.currentLease(ctx, tx) {
		return false
	}
	r, err := findARN(tx, "rds", c.key.arn)
	return err == nil && generationOf(r) == c.key && r.Region == c.options.Region && r.ID == c.options.Identifier &&
		privatePostgresShape(r) && validatePrivatePostgresRefs(ctx, tx, r, c.engine.cfg.ValidateRDSPrivatePostgres) == nil &&
		tx.Commit() == nil
}

func (c *OwnedPrivatePostgres) currentLease(ctx context.Context, tx *sql.Tx) bool {
	var found int
	return tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_catalog_owned_postgres WHERE slot=1 AND arn=? AND seq=? AND generation=? AND owner=?",
		c.key.arn, c.key.seq, c.key.gen, c.owner).Scan(&found) == nil && found == 1
}

func (c *OwnedPrivatePostgres) preflightModify(q *request, proposed row) error {
	// This is after the existing authorization and proposal decoding. A bad
	// proposed tuple must not stop its currently healthy, owned generation.
	if q.op.Name != "ModifyDBInstance" || c.child == nil || generationOf(proposed) != c.key {
		return nil
	}
	ctx, cancel := context.WithTimeout(q.ctx, 3*time.Second)
	defer cancel()
	tx, err := c.engine.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !c.currentLease(ctx, tx) {
		return errOwnedPostgresDebt
	}
	current, err := findARN(tx, "rds", c.key.arn)
	if err != nil || generationOf(current) != c.key || current.Region != c.options.Region ||
		current.ID != c.options.Identifier {
		return &fault{kind: "conflict", message: "The resource changed concurrently; read it again before retrying."}
	}
	if err := q.checkRefsIn(tx, q.resource(), proposed.Document); err != nil {
		return err
	}
	if privatePostgresShape(proposed) {
		if err := validatePrivatePostgresRefs(ctx, tx, proposed, c.engine.cfg.ValidateRDSPrivatePostgres); err != nil {
			return err
		}
	}
	// End the lookup before sending a shutdown. The existing mutation
	// transaction repeats reference/topology checks and the generation CAS.
	return tx.Commit()
}

func (c *OwnedPrivatePostgres) retire(r row) error {
	if c.key.arn == "" || r.Region != c.options.Region || generationOf(r) != c.key {
		return nil // a retained fence still rejects an unowned write at SQLite
	}
	return c.retireCurrent()
}

func (c *OwnedPrivatePostgres) retireCurrent() error {
	if c.debt {
		return errOwnedPostgresDebt
	}
	if c.key.arn == "" {
		return nil
	}
	// Never release the durable exact-row fence until no owned external
	// process can remain. A failed close irrevocably holds the fence.
	if c.child != nil {
		if err := c.child.Close(); err != nil {
			c.debt = true
			return errOwnedPostgresDebt
		}
		c.child = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := exactlyOne(c.engine.store.db.ExecContext(ctx,
		"DELETE FROM aws_catalog_owned_postgres WHERE slot=1 AND arn=? AND seq=? AND generation=? AND owner=?",
		c.key.arn, c.key.seq, c.key.gen, c.owner)); err != nil {
		c.debt = true
		return errOwnedPostgresDebt
	}
	c.key = privateDBGeneration{}
	return nil
}

// Probe rechecks the exact canonical instance and native private topology, then
// runs only the workload's fixed authenticated proof. No request inputs or
// connection credentials cross this boundary.
func (c *OwnedPrivatePostgres) Probe(ctx context.Context) error {
	if c == nil || ctx == nil {
		return rdsruntime.ErrNotReady
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.debt {
		return errOwnedPostgresDebt
	}
	if c.closed || c.child == nil {
		return errors.Join(rdsruntime.ErrNotReady, c.last)
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := bounded.Err(); err != nil {
		return err
	}
	if !c.current(bounded) {
		return errors.Join(rdsruntime.ErrNotReady, c.retireCurrent())
	}
	return c.child.Probe(bounded)
}

// Close confirms bounded physical teardown before the provider may discard
// the owning database. A lost supervisor or unknown cleanup is non-adoptable.
func (c *OwnedPrivatePostgres) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return c.retireCurrent()
}
