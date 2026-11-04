// Package storage implements a bounded, local Cloud Storage JSON API leaf.
// It does not implement Google authentication or register itself with a server.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Actor is supplied only by trusted Go composition after local authentication.
// Subject is the selected exercise identity, not the credential's operator role.
// Environment is attached by the configured factory, not copied from a header.
type Actor struct{ Subject, Environment string }
type actorKey struct{}

func WithActor(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// Resource carries persisted ownership. A missing bucket has Exists=false and
// an empty Project, never a caller-suggested owner. Authorizers must handle that
// case explicitly; refusal precedes any not-found result sent to the caller.
type Resource struct {
	Project, Bucket, Object string
	Exists                  bool
}

var ErrDenied = errors.New("GCP Storage permission denied")

// AuthorizeTx MUST NOT commit/rollback tx, do external I/O, or start a second
// transaction. Read the provider's trusted policy snapshot using this same tx.
// Return ErrDenied for refusal; other errors become generic infrastructure 500.
type AuthorizeTx func(context.Context, *sql.Tx, Actor, string, Resource) error

type Project struct{ ID, Number string }
type Limits struct{ Buckets, Objects, ObjectBytes, StoredBytes int64 }

func DefaultLimits() Limits { return Limits{100, 10000, 1 << 20, 64 << 20} }

type Config struct {
	Environment string
	Projects    []Project
	AuthorizeTx AuthorizeTx
	Limits      Limits
}
type Store struct {
	db          *sql.DB
	environment string
	projects    map[string]string
	authorize   AuthorizeTx
	limits      Limits
}

type apiError struct {
	status          int
	reason, message string
}

func (e *apiError) Error() string  { return e.message }
func invalid(message string) error { return &apiError{400, "invalid", message} }
func absent() error                { return &apiError{404, "notFound", "The requested resource was not found."} }
func conditionFailed() error {
	return &apiError{412, "conditionNotMet", "The generation precondition was not met."}
}
func quotaExceeded() error {
	return &apiError{429, "rateLimitExceeded", "The local Storage state budget is exhausted."}
}

func New(db *sql.DB, cfg Config) (*Store, error) {
	return newStore(db, cfg, nil)
}

// initialize, when provided by this package's trusted factory, joins schema,
// project and immutable owner initialization to the same constructor transaction.
func newStore(db *sql.DB, cfg Config, initialize func(*sql.Tx) error) (*Store, error) {
	if db == nil || cfg.AuthorizeTx == nil || !validScope(cfg.Environment) || len(cfg.Projects) == 0 || len(cfg.Projects) > 100 {
		return nil, errors.New("GCP Storage requires SQLite, configured environment/projects and a transaction-aware authorizer")
	}
	limits := cfg.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits()
	}
	hard := DefaultLimits()
	if limits.Buckets < 1 || limits.Buckets > hard.Buckets || limits.Objects < 1 || limits.Objects > hard.Objects || limits.ObjectBytes < 1 || limits.ObjectBytes > hard.ObjectBytes || limits.StoredBytes < limits.ObjectBytes || limits.StoredBytes > hard.StoredBytes {
		return nil, errors.New("invalid bounded GCP Storage budgets")
	}
	s := &Store{db: db, environment: cfg.Environment, projects: map[string]string{}, authorize: cfg.AuthorizeTx, limits: limits}
	for _, project := range cfg.Projects {
		number, err := strconv.ParseUint(project.Number, 10, 64)
		if !validProject(project.ID) || err != nil || number == 0 || strconv.FormatUint(number, 10) != project.Number || s.projects[project.ID] != "" {
			return nil, errors.New("invalid or duplicate synthetic project")
		}
		for _, existing := range s.projects {
			if existing == project.Number {
				return nil, errors.New("duplicate synthetic project number")
			}
		}
		s.projects[project.ID] = project.Number
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		return nil, errors.New("GCP Storage requires SQLite foreign keys enabled")
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS gcp_storage_projects(environment TEXT NOT NULL,id TEXT NOT NULL,number TEXT NOT NULL,PRIMARY KEY(environment,id),UNIQUE(environment,number));
CREATE TABLE IF NOT EXISTS gcp_storage_buckets(environment TEXT NOT NULL,name TEXT NOT NULL,project TEXT NOT NULL,location TEXT NOT NULL,created TEXT NOT NULL,PRIMARY KEY(environment,name),FOREIGN KEY(environment,project) REFERENCES gcp_storage_projects(environment,id) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS gcp_storage_objects(environment TEXT NOT NULL,bucket TEXT NOT NULL,name TEXT NOT NULL,data BLOB NOT NULL,content_type TEXT NOT NULL,metadata TEXT NOT NULL,generation INTEGER NOT NULL CHECK(generation>0),crc32c TEXT NOT NULL,md5 TEXT NOT NULL,created TEXT NOT NULL,updated TEXT NOT NULL,PRIMARY KEY(environment,bucket,name),FOREIGN KEY(environment,bucket) REFERENCES gcp_storage_buckets(environment,name) ON DELETE RESTRICT);
CREATE TABLE IF NOT EXISTS gcp_storage_sequence(environment TEXT PRIMARY KEY,next_generation INTEGER NOT NULL CHECK(next_generation>=0));`)
	if err != nil {
		return nil, err
	}
	for id, number := range s.projects {
		if _, err = tx.Exec("INSERT INTO gcp_storage_projects(environment,id,number) VALUES(?,?,?) ON CONFLICT(environment,id) DO NOTHING", s.environment, id, number); err != nil {
			return nil, err
		}
		var saved string
		if err = tx.QueryRow("SELECT number FROM gcp_storage_projects WHERE environment=? AND id=?", s.environment, id).Scan(&saved); err != nil || saved != number {
			return nil, errors.New("persisted synthetic project binding differs from configuration")
		}
	}
	rows, err := tx.Query("SELECT DISTINCT project FROM gcp_storage_buckets WHERE environment=?", s.environment)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var project string
		if err = rows.Scan(&project); err != nil {
			rows.Close()
			return nil, err
		}
		if s.projects[project] == "" {
			rows.Close()
			return nil, errors.New("persisted bucket owner is not a configured synthetic project")
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var orphans int
	if err = tx.QueryRow("SELECT count(*) FROM gcp_storage_objects o WHERE o.environment=? AND NOT EXISTS(SELECT 1 FROM gcp_storage_buckets b WHERE b.environment=o.environment AND b.name=o.bucket)", s.environment).Scan(&orphans); err != nil || orphans != 0 {
		return nil, errors.New("invalid persisted GCP Storage parent graph")
	}
	if _, err = tx.Exec("INSERT INTO gcp_storage_sequence(environment,next_generation) VALUES(?,0) ON CONFLICT(environment) DO NOTHING", s.environment); err != nil {
		return nil, err
	}
	var generation int64
	if err = tx.QueryRow("SELECT next_generation FROM gcp_storage_sequence WHERE environment=?", s.environment).Scan(&generation); err != nil || generation < 0 {
		return nil, errors.New("invalid persisted generation sequence")
	}
	var buckets, objects, bytes, largest, maxGeneration int64
	if err = tx.QueryRow("SELECT count(*) FROM gcp_storage_buckets WHERE environment=?", s.environment).Scan(&buckets); err != nil {
		return nil, err
	}
	if err = tx.QueryRow("SELECT count(*),COALESCE(SUM(length(data)+length(CAST(metadata AS BLOB))),0),COALESCE(MAX(length(data)),0),COALESCE(MAX(generation),0) FROM gcp_storage_objects WHERE environment=?", s.environment).Scan(&objects, &bytes, &largest, &maxGeneration); err != nil {
		return nil, err
	}
	if buckets > limits.Buckets || objects > limits.Objects || bytes > limits.StoredBytes || largest > limits.ObjectBytes || maxGeneration > generation {
		return nil, errors.New("persisted GCP Storage state exceeds configured budgets or sequence")
	}
	if initialize != nil {
		if err = initialize(tx); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx, Actor) (any, error)) (any, error) {
	actor, ok := ctx.Value(actorKey{}).(Actor)
	if !ok || actor.Subject == "" || actor.Environment != s.environment {
		return nil, &apiError{401, "required", "An authenticated local provider actor is required."}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out, err := fn(tx, actor)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) permit(ctx context.Context, tx *sql.Tx, actor Actor, permission string, target Resource) error {
	if err := s.authorize(ctx, tx, actor, permission, target); err != nil {
		if errors.Is(err, ErrDenied) {
			return &apiError{403, "forbidden", "The local provider policy denied this operation."}
		}
		return err
	}
	return nil
}

type bucket struct{ Name, Project, Location, Created string }

func (s *Store) loadBucket(ctx context.Context, tx *sql.Tx, actor Actor, permission, name, object string) (bucket, error) {
	b := bucket{Name: name}
	err := tx.QueryRowContext(ctx, "SELECT project,location,created FROM gcp_storage_buckets WHERE environment=? AND name=?", s.environment, name).Scan(&b.Project, &b.Location, &b.Created)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	if e := s.permit(ctx, tx, actor, permission, Resource{Project: b.Project, Bucket: name, Object: object, Exists: err == nil}); e != nil {
		return b, e
	}
	if errors.Is(err, sql.ErrNoRows) {
		return b, absent()
	}
	if s.projects[b.Project] == "" {
		return b, fmt.Errorf("unconfigured persisted owner")
	}
	return b, nil
}
