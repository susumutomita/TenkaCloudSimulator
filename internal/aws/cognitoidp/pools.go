// Package cognitoidp owns the bounded Cognito User Pools control plane. It
// does not authenticate a user, issue credentials or contact another service.
package cognitoidp

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	// PoolLimit is per trusted account and region, not per requesting user.
	PoolLimit = 100
	maxPage   = 60
)

var (
	errInvalid = errors.New("invalid or unsupported pool input")
	errMissing = errors.New("user pool not found")
	errQuota   = errors.New("local user pool limit reached")
	accountRE  = regexp.MustCompile(`^[0-9]{12}$`)
	regionRE   = regexp.MustCompile(`^[a-z]{2}-[a-z0-9-]{1,22}-[1-9][0-9]?$`)
	idRE       = regexp.MustCompile(`^[0-9A-Za-z]+$`)
	nameRE     = regexp.MustCompile(`^[A-Za-z0-9_ +=,.@-]{1,128}$`)
	actorRE    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// Scope is chosen by the authenticated provider configuration, never a body
// field or unverified request hint.
type Scope struct{ Account, Region string }

func (s Scope) valid() bool {
	return accountRE.MatchString(s.Account) && regionRE.MatchString(s.Region)
}
func (s Scope) validPoolID(id string) bool {
	suffix, ok := strings.CutPrefix(id, s.Region+"_")
	return ok && len(id) <= 55 && idRE.MatchString(suffix)
}
func (s Scope) arn(id string) string {
	return "arn:aws:cognito-idp:" + s.Region + ":" + s.Account + ":userpool/" + id
}

// Actor names the authenticated credential and selected exercise identity
// installed by the caller's trusted middleware. It scopes pagination only;
// the resource itself is at account/region scope and uses IAM policy checks.
type Actor struct{ Credential, Exercise string }

func (a Actor) valid() bool {
	return actorRE.MatchString(a.Credential) && actorRE.MatchString(a.Exercise)
}

type Store struct {
	db        *sql.DB
	cursorKey [32]byte
}

type pool struct {
	ID, Name  string
	CreatedMS int64
}

func NewStore(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("Cognito user pools require caller-owned SQLite")
	}
	s := &Store{db: db}
	if _, err := rand.Read(s.cursorKey[:]); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_cognito_user_pools (
		account TEXT NOT NULL, region TEXT NOT NULL, pool_id TEXT NOT NULL,
		name TEXT NOT NULL, created_ms INTEGER NOT NULL,
		PRIMARY KEY (account, region, pool_id))`); err != nil {
		return nil, err
	}
	return s, nil
}

func validPoolName(s string) bool {
	return nameRE.MatchString(s) && strings.TrimSpace(s) != ""
}

func (s *Store) create(ctx context.Context, scope Scope, name string) (pool, error) {
	if !scope.valid() || !validPoolName(name) {
		return pool{}, errInvalid
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return pool{}, err
	}
	p := pool{ID: scope.Region + "_" + hex.EncodeToString(nonce[:]), Name: name, CreatedMS: time.Now().UnixMilli()}
	// The quota check and INSERT are one SQLite write statement, including
	// independent provider processes that intentionally share this database.
	res, err := s.db.ExecContext(ctx, `INSERT INTO aws_cognito_user_pools(account,region,pool_id,name,created_ms)
		SELECT ?,?,?,?,? WHERE (SELECT count(*) FROM aws_cognito_user_pools WHERE account=? AND region=?) < ?`,
		scope.Account, scope.Region, p.ID, p.Name, p.CreatedMS, scope.Account, scope.Region, PoolLimit)
	if err != nil {
		return pool{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return pool{}, err
	}
	if n != 1 {
		return pool{}, errQuota
	}
	return p, nil
}

func (s *Store) describe(ctx context.Context, scope Scope, id string) (pool, error) {
	if !scope.valid() || !scope.validPoolID(id) {
		return pool{}, errInvalid
	}
	p := pool{ID: id}
	err := s.db.QueryRowContext(ctx, `SELECT name,created_ms FROM aws_cognito_user_pools
		WHERE account=? AND region=? AND pool_id=?`, scope.Account, scope.Region, id).Scan(&p.Name, &p.CreatedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return pool{}, errMissing
	}
	return p, err
}

func (s *Store) delete(ctx context.Context, scope Scope, id string) error {
	if !scope.valid() || !scope.validPoolID(id) {
		return errInvalid
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM aws_cognito_user_pools WHERE account=? AND region=? AND pool_id=?`,
		scope.Account, scope.Region, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errMissing
	}
	return nil
}
