// Package iaas stores bounded, local Sakura configuration metadata. It does not
// allocate block devices, execute virtual machines, or authenticate callers.
package iaas

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalid  = errors.New("invalid disk configuration")
	ErrScope    = errors.New("disk database configuration mismatch or unbound state")
	ErrCorrupt  = errors.New("invalid persisted disk state")
	ErrNotFound = errors.New("disk not found")
	ErrConflict = errors.New("disk generation mismatch")
	ErrQuota    = errors.New("local disk limit reached")
)

// Config is trusted immutable host configuration, not request identity.
type Config struct{ Environment, Project, Zone, Owner string }
type Disk struct {
	ID                            int64
	Generation, Name, Description string
	SizeMB, PlanID                int
	Connection                    string
	Created, Modified             time.Time
}
type CreateDiskInput struct {
	Name, Description string
	SizeMB, PlanID    int
	Connection        string
}
type DiskPatch struct{ Name, Description *string }
type DiskPage struct {
	Disks []Disk
	Total int64
	From  int64
}
type DiskStore struct {
	db     *sql.DB
	config Config
}

func textOK(s string, min, max int) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) < min || utf8.RuneCountInString(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validConfig(c Config) bool {
	for _, v := range []string{c.Environment, c.Project, c.Zone, c.Owner} {
		if !textOK(v, 1, 128) || strings.TrimSpace(v) != v {
			return false
		}
	}
	return true
}
func ack(r sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrCorrupt
	}
	return nil
}

func NewDiskStore(db *sql.DB, c Config) (*DiskStore, error) {
	if db == nil || !validConfig(c) {
		return nil, ErrInvalid
	}
	s := &DiskStore{db: db, config: c}
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
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('sakura_disk_scope','sakura_disks')").Scan(&existing); e != nil {
		return nil, e
	}
	if existing != 0 && existing != 2 {
		return nil, ErrScope
	}
	_, e = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sakura_disk_scope (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), version INTEGER NOT NULL,
 environment TEXT NOT NULL, project TEXT NOT NULL, zone TEXT NOT NULL, owner TEXT NOT NULL, next_id INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS sakura_disks (
 id INTEGER PRIMARY KEY, generation TEXT NOT NULL UNIQUE, name TEXT NOT NULL, description TEXT NOT NULL,
 size_mb INTEGER NOT NULL, plan_id INTEGER NOT NULL, connection TEXT NOT NULL, created TEXT NOT NULL, modified TEXT NOT NULL);`)
	if e != nil {
		return nil, e
	}
	var n, disks int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_disk_scope").Scan(&n); e != nil {
		return nil, e
	}
	if n == 0 {
		// Existing namespaces without their binding are never adopted, even empty.
		if existing != 0 {
			return nil, ErrScope
		}
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_disks").Scan(&disks); e != nil {
			return nil, e
		}
		if disks != 0 {
			return nil, ErrScope
		}
		if e = ack(tx.ExecContext(ctx, "INSERT INTO sakura_disk_scope VALUES(1,1,?,?,?,?,1)", c.Environment, c.Project, c.Zone, c.Owner)); e != nil {
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
func (s *DiskStore) check(ctx context.Context, tx *sql.Tx) (int64, error) {
	var c Config
	var v int
	var next, max int64
	e := tx.QueryRowContext(ctx, "SELECT version,environment,project,zone,owner,next_id FROM sakura_disk_scope WHERE singleton=1").Scan(&v, &c.Environment, &c.Project, &c.Zone, &c.Owner, &next)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, ErrScope
	}
	if e != nil {
		return 0, e
	}
	if c != s.config || v != 1 {
		return 0, ErrScope
	}
	if e = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM sakura_disks").Scan(&max); e != nil {
		return 0, e
	}
	if next <= 0 || max >= next {
		return 0, ErrCorrupt
	}
	return next, nil
}
func (s *DiskStore) begin(ctx context.Context) (*sql.Tx, int64, error) {
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

const diskColumns = "id,generation,name,description,size_mb,plan_id,connection,created,modified"

type scanner interface{ Scan(...any) error }

func scanDisk(row scanner, next int64) (Disk, error) {
	var d Disk
	var created, modified string
	e := row.Scan(&d.ID, &d.Generation, &d.Name, &d.Description, &d.SizeMB, &d.PlanID, &d.Connection, &created, &modified)
	if errors.Is(e, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	if e != nil {
		return d, e
	}
	d.Created, e = time.Parse(time.RFC3339Nano, created)
	if e != nil {
		return Disk{}, ErrCorrupt
	}
	d.Modified, e = time.Parse(time.RFC3339Nano, modified)
	if e != nil {
		return Disk{}, ErrCorrupt
	}
	b, e := hex.DecodeString(d.Generation)
	if e != nil || len(b) != 16 || hex.EncodeToString(b) != d.Generation || d.ID <= 0 || d.ID >= next || !textOK(d.Name, 1, 64) || !textOK(d.Description, 0, 512) || d.SizeMB != 20480 || d.PlanID != 4 || d.Connection != "virtio" || d.Modified.Before(d.Created) {
		return Disk{}, ErrCorrupt
	}
	return d, nil
}
func readDisk(ctx context.Context, tx *sql.Tx, id, next int64) (Disk, error) {
	if id <= 0 {
		return Disk{}, ErrInvalid
	}
	return scanDisk(tx.QueryRowContext(ctx, "SELECT "+diskColumns+" FROM sakura_disks WHERE id=?", id), next)
}
func generationOK(d Disk, expected []string) error {
	if len(expected) > 1 || (len(expected) == 1 && expected[0] == "") {
		return ErrInvalid
	}
	if len(expected) == 1 && expected[0] != d.Generation {
		return ErrConflict
	}
	return nil
}
func (s *DiskStore) Create(ctx context.Context, in CreateDiskInput) (Disk, error) {
	if !textOK(in.Name, 1, 64) || !textOK(in.Description, 0, 512) || in.SizeMB != 20480 || in.PlanID != 4 || in.Connection != "virtio" {
		return Disk{}, ErrInvalid
	}
	tx, next, e := s.begin(ctx)
	if e != nil {
		return Disk{}, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_disks").Scan(&count); e != nil {
		return Disk{}, e
	}
	if count >= 100 {
		return Disk{}, ErrQuota
	}
	if next == math.MaxInt64 {
		return Disk{}, ErrQuota
	}
	var b [16]byte
	if _, e = rand.Read(b[:]); e != nil {
		return Disk{}, e
	}
	now := time.Now().UTC()
	d := Disk{next, hex.EncodeToString(b[:]), in.Name, in.Description, in.SizeMB, in.PlanID, in.Connection, now, now}
	e = ack(tx.ExecContext(ctx, "INSERT INTO sakura_disks VALUES(?,?,?,?,?,?,?,?,?)", d.ID, d.Generation, d.Name, d.Description, d.SizeMB, d.PlanID, d.Connection, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)))
	if e != nil {
		return Disk{}, e
	}
	if e = ack(tx.ExecContext(ctx, "UPDATE sakura_disk_scope SET next_id=? WHERE singleton=1 AND next_id=?", next+1, next)); e != nil {
		return Disk{}, e
	}
	saved, e := readDisk(ctx, tx, next, next+1)
	if e != nil {
		return Disk{}, e
	}
	if saved != d {
		return Disk{}, ErrCorrupt
	}
	if allocator, err := s.check(ctx, tx); err != nil {
		return Disk{}, err
	} else if allocator != next+1 {
		return Disk{}, ErrCorrupt
	}
	if e = tx.Commit(); e != nil {
		return Disk{}, e
	}
	return d, nil
}
func (s *DiskStore) Get(ctx context.Context, id int64) (Disk, error) {
	tx, next, e := s.begin(ctx)
	if e != nil {
		return Disk{}, e
	}
	defer tx.Rollback()
	d, e := readDisk(ctx, tx, id, next)
	if e != nil {
		return Disk{}, e
	}
	if e = tx.Commit(); e != nil {
		return Disk{}, e
	}
	return d, nil
}
func (s *DiskStore) ListFromCount(ctx context.Context, from int64, count int) (DiskPage, error) {
	if from < 0 || count < 1 || count > 100 {
		return DiskPage{}, ErrInvalid
	}
	tx, next, e := s.begin(ctx)
	if e != nil {
		return DiskPage{}, e
	}
	defer tx.Rollback()
	p := DiskPage{Disks: []Disk{}, From: from}
	if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM sakura_disks").Scan(&p.Total); e != nil {
		return DiskPage{}, e
	}
	rows, e := tx.QueryContext(ctx, "SELECT "+diskColumns+" FROM sakura_disks ORDER BY id LIMIT ? OFFSET ?", count, from)
	if e != nil {
		return DiskPage{}, e
	}
	for rows.Next() {
		d, err := scanDisk(rows, next)
		if err != nil {
			rows.Close()
			return DiskPage{}, err
		}
		p.Disks = append(p.Disks, d)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return DiskPage{}, e
	}
	if e = tx.Commit(); e != nil {
		return DiskPage{}, e
	}
	return p, nil
}
func (s *DiskStore) UpdateNameDesc(ctx context.Context, id int64, patch DiskPatch, expected ...string) (Disk, error) {
	if (patch.Name != nil && !textOK(*patch.Name, 1, 64)) || (patch.Description != nil && !textOK(*patch.Description, 0, 512)) {
		return Disk{}, ErrInvalid
	}
	tx, next, e := s.begin(ctx)
	if e != nil {
		return Disk{}, e
	}
	defer tx.Rollback()
	d, e := readDisk(ctx, tx, id, next)
	if e != nil {
		return Disk{}, e
	}
	if e = generationOK(d, expected); e != nil {
		return Disk{}, e
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
		e = ack(tx.ExecContext(ctx, "UPDATE sakura_disks SET name=?,description=?,modified=? WHERE id=? AND generation=?", d.Name, d.Description, d.Modified.Format(time.RFC3339Nano), id, d.Generation))
		if e != nil {
			return Disk{}, e
		}
		saved, err := readDisk(ctx, tx, id, next)
		if err != nil {
			return Disk{}, err
		}
		if saved != d {
			return Disk{}, ErrCorrupt
		}
	}
	if e = tx.Commit(); e != nil {
		return Disk{}, e
	}
	return d, nil
}
func (s *DiskStore) Delete(ctx context.Context, id int64, expected ...string) error {
	tx, next, e := s.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	d, e := readDisk(ctx, tx, id, next)
	if e != nil {
		return e
	}
	if e = generationOK(d, expected); e != nil {
		return e
	}
	if e = ack(tx.ExecContext(ctx, "DELETE FROM sakura_disks WHERE id=? AND generation=?", id, d.Generation)); e != nil {
		return e
	}
	if _, e = readDisk(ctx, tx, id, next); !errors.Is(e, ErrNotFound) {
		if e != nil {
			return e
		}
		return ErrCorrupt
	}
	return tx.Commit()
}
