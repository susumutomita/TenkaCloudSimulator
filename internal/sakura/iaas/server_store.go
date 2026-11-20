package iaas

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"time"
)

// Server is local configuration metadata only; it never represents a running VM.
type Server struct {
	ID                            int64
	Generation, Name, Description string
	CPU, MemoryMB                 int
	Created, Modified             time.Time
}
type CreateServerInput struct {
	Name, Description string
	CPU, MemoryMB     int
}
type ServerPatch struct{ Name, Description *string }
type ServerPage struct {
	Servers     []Server
	Total, From int64
}
type ServerStore struct {
	db     *sql.DB
	config Config
}

func NewServerStore(db *sql.DB, c Config) (*ServerStore, error) {
	if db == nil || !validConfig(c) {
		return nil, ErrInvalid
	}
	s := &ServerStore{db: db, config: c}
	ctx := context.Background()
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var fk int
	if e = tx.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); e != nil {
		return nil, e
	}
	if fk != 1 {
		return nil, ErrScope
	}
	var existing int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('sakura_server_scope','sakura_servers')").Scan(&existing); e != nil {
		return nil, e
	}
	if existing != 0 && existing != 2 {
		return nil, ErrScope
	}
	_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sakura_server_scope (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL,
 environment TEXT NOT NULL, project TEXT NOT NULL, zone TEXT NOT NULL, owner TEXT NOT NULL, next_id INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS sakura_servers (
 id INTEGER PRIMARY KEY, generation TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT NOT NULL,
 cpu INTEGER NOT NULL, memory_mb INTEGER NOT NULL, created TEXT NOT NULL, modified TEXT NOT NULL);`)
	if e != nil {
		return nil, e
	}
	var n, servers int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_server_scope").Scan(&n); e != nil {
		return nil, e
	}
	if n == 0 {
		// Existing namespaces without their binding are never adopted, even empty.
		if existing != 0 {
			return nil, ErrScope
		}
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_servers").Scan(&servers); e != nil {
			return nil, e
		}
		if servers != 0 {
			return nil, ErrScope
		}
		if e = ack(tx.ExecContext(ctx, "INSERT INTO sakura_server_scope VALUES(1,1,?,?,?,?,1)", c.Environment, c.Project, c.Zone, c.Owner)); e != nil {
			return nil, e
		}
	}
	if _, e = s.check(ctx, tx); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return s, nil
}
func (s *ServerStore) check(ctx context.Context, tx *sql.Tx) (int64, error) {
	var c Config
	var v int
	var next, max int64
	e := tx.QueryRowContext(ctx, "SELECT version,environment,project,zone,owner,next_id FROM sakura_server_scope WHERE singleton=1").Scan(&v, &c.Environment, &c.Project, &c.Zone, &c.Owner, &next)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, ErrScope
	}
	if e != nil {
		return 0, e
	}
	if c != s.config || v != 1 {
		return 0, ErrScope
	}
	if e = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM sakura_servers").Scan(&max); e != nil {
		return 0, e
	}
	if next <= 0 || max >= next {
		return 0, ErrCorrupt
	}
	return next, nil
}
func (s *ServerStore) begin(ctx context.Context) (*sql.Tx, int64, error) {
	if ctx == nil {
		return nil, 0, ErrInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, 0, e
	}
	next, e := s.check(ctx, tx)
	if e != nil {
		tx.Rollback()
		return nil, 0, e
	}
	return tx, next, nil
}

const serverColumns = "id,generation,name,description,cpu,memory_mb,created,modified"

func scanServer(row scanner, next int64) (Server, error) {
	var d Server
	var created, modified string
	e := row.Scan(&d.ID, &d.Generation, &d.Name, &d.Description, &d.CPU, &d.MemoryMB, &created, &modified)
	if errors.Is(e, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if e != nil {
		return d, e
	}
	d.Created, e = time.Parse(time.RFC3339Nano, created)
	if e != nil {
		return Server{}, ErrCorrupt
	}
	d.Modified, e = time.Parse(time.RFC3339Nano, modified)
	if e != nil {
		return Server{}, ErrCorrupt
	}
	b, e := hex.DecodeString(d.Generation)
	if e != nil || len(b) != 16 || hex.EncodeToString(b) != d.Generation || d.ID <= 0 || d.ID >= next || !textOK(d.Name, 1, 64) || !textOK(d.Description, 0, 512) || d.CPU != 2 || d.MemoryMB != 4096 || d.Modified.Before(d.Created) {
		return Server{}, ErrCorrupt
	}
	return d, nil
}
func readServer(ctx context.Context, tx *sql.Tx, id, next int64) (Server, error) {
	if id <= 0 {
		return Server{}, ErrInvalid
	}
	return scanServer(tx.QueryRowContext(ctx, "SELECT "+serverColumns+" FROM sakura_servers WHERE id=?", id), next)
}
func serverGenerationOK(d Server, expected []string) error {
	if len(expected) > 1 || (len(expected) == 1 && expected[0] == "") {
		return ErrInvalid
	}
	if len(expected) == 1 && expected[0] != d.Generation {
		return ErrConflict
	}
	return nil
}
func (s *ServerStore) Create(ctx context.Context, in CreateServerInput) (Server, error) {
	if !textOK(in.Name, 1, 64) || !textOK(in.Description, 0, 512) || in.CPU != 2 || in.MemoryMB != 4096 {
		return Server{}, ErrInvalid
	}
	tx, next, e := s.begin(ctx)
	if e != nil {
		return Server{}, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_servers").Scan(&count); e != nil {
		return Server{}, e
	}
	if count >= 100 {
		return Server{}, ErrQuota
	}
	if next == math.MaxInt64 {
		return Server{}, ErrQuota
	}
	var b [16]byte
	if _, e = rand.Read(b[:]); e != nil {
		return Server{}, e
	}
	now := time.Now().UTC()
	d := Server{next, hex.EncodeToString(b[:]), in.Name, in.Description, in.CPU, in.MemoryMB, now, now}
	e = ack(tx.ExecContext(ctx, "INSERT INTO sakura_servers VALUES(?,?,?,?,?,?,?,?)", d.ID, d.Generation, d.Name, d.Description, d.CPU, d.MemoryMB, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	if e != nil {
		return Server{}, e
	}
	if e = ack(tx.ExecContext(ctx, "UPDATE sakura_server_scope SET next_id=? WHERE singleton=1 AND next_id=?", next+1, next)); e != nil {
		return Server{}, e
	}
	saved, e := readServer(ctx, tx, next, next+1)
	if e != nil {
		return Server{}, e
	}
	if saved != d {
		return Server{}, ErrCorrupt
	}
	if allocator, err := s.check(ctx, tx); err != nil {
		return Server{}, err
	} else if allocator != next+1 {
		return Server{}, ErrCorrupt
	}
	if e = tx.Commit(); e != nil {
		return Server{}, e
	}
	return d, nil
}
func (s *ServerStore) Get(ctx context.Context, id int64) (Server, error) {
	tx, next, e := s.begin(ctx)
	if e != nil {
		return Server{}, e
	}
	defer tx.Rollback()
	d, e := readServer(ctx, tx, id, next)
	if e != nil {
		return Server{}, e
	}
	if e = tx.Commit(); e != nil {
		return Server{}, e
	}
	return d, nil
}
func (s *ServerStore) ListFromCount(ctx context.Context, from int64, count int) (ServerPage, error) {
	if from < 0 || count < 1 || count > 100 {
		return ServerPage{}, ErrInvalid
	}
	tx, next, e := s.begin(ctx)
	if e != nil {
		return ServerPage{}, e
	}
	defer tx.Rollback()
	p := ServerPage{Servers: []Server{}, From: from}
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_servers").Scan(&p.Total); e != nil {
		return ServerPage{}, e
	}
	rows, e := tx.QueryContext(ctx, "SELECT "+serverColumns+" FROM sakura_servers ORDER BY id LIMIT ? OFFSET ?", count, from)
	if e != nil {
		return ServerPage{}, e
	}
	for rows.Next() {
		d, err := scanServer(rows, next)
		if err != nil {
			rows.Close()
			return ServerPage{}, err
		}
		p.Servers = append(p.Servers, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return ServerPage{}, e
	}
	if e = tx.Commit(); e != nil {
		return ServerPage{}, e
	}
	return p, nil
}
func (s *ServerStore) UpdateNameDesc(ctx context.Context, id int64, patch ServerPatch, expected ...string) (Server, error) {
	if (patch.Name != nil && !textOK(*patch.Name, 1, 64)) || (patch.Description != nil && !textOK(*patch.Description, 0, 512)) {
		return Server{}, ErrInvalid
	}
	tx, next, e := s.begin(ctx)
	if e != nil {
		return Server{}, e
	}
	defer tx.Rollback()
	d, e := readServer(ctx, tx, id, next)
	if e != nil {
		return Server{}, e
	}
	if e = serverGenerationOK(d, expected); e != nil {
		return Server{}, e
	}
	old := d
	if patch.Name != nil {
		d.Name = *patch.Name
	}
	if patch.Description != nil {
		d.Description = *patch.Description
	}
	if d != old {
		d.Modified = time.Now().UTC()
		if d.Modified.Before(old.Modified) {
			d.Modified = old.Modified
		}
		e = ack(tx.ExecContext(ctx, "UPDATE sakura_servers SET name=?,description=?,modified=? WHERE id=? AND generation=?", d.Name, d.Description, d.Modified.Format(time.RFC3339Nano), id, d.Generation))
		if e != nil {
			return Server{}, e
		}
		saved, err := readServer(ctx, tx, id, next)
		if err != nil {
			return Server{}, err
		}
		if saved != d {
			return Server{}, ErrCorrupt
		}
	}
	if e = tx.Commit(); e != nil {
		return Server{}, e
	}
	return d, nil
}
func (s *ServerStore) Delete(ctx context.Context, id int64, expected ...string) error {
	tx, next, e := s.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	d, e := readServer(ctx, tx, id, next)
	if e != nil {
		return e
	}
	if e = serverGenerationOK(d, expected); e != nil {
		return e
	}
	if e = ack(tx.ExecContext(ctx, "DELETE FROM sakura_servers WHERE id=? AND generation=?", id, d.Generation)); e != nil {
		return e
	}
	if _, e = readServer(ctx, tx, id, next); !errors.Is(e, ErrNotFound) {
		if e != nil {
			return e
		}
		return ErrCorrupt
	}
	return tx.Commit()
}
