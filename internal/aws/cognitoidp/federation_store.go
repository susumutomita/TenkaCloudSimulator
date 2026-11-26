// Internal storage boundary for a future federation adapter. This has no route,
// grants no authority, and is not enabled by NewStore or any SDK operation.
package cognitoidp

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
)

const (
	federationRevisionBytes = 16
	maxFederationMetadata   = 64 << 10 // Upper bound for this local metadata slot, not an AWS limit.
)

type federationRevision [federationRevisionBytes]byte

type federationSnapshot struct {
	revision federationRevision
	metadata []byte // Opaque to storage; never interpreted as authenticated configuration.
}

type federationStorage struct{ pools *Store }

var (
	errFederationStale = errors.New("federation metadata revision no longer current")
	errFederationState = errors.New("federation metadata storage mismatch")
)

// openFederationStorage opts in to a nullable extension of the canonical #559
// row. An old-format database migrates without rewriting pool identities. Old
// SDK clients (even an older simulator process) create NULL slots and delete
// them with their parent. There is no sidecar table or cleanup callback.
func (s *Store) openFederationStorage(ctx context.Context) (*federationStorage, error) {
	// go-sqlite3 uses BEGIN IMMEDIATE for LevelSerializable, so independent
	// initializers cannot both decide a column is missing before either adds it.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var present bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('aws_cognito_user_pools') WHERE name='federation_state')").Scan(&present); err != nil {
		return nil, err
	}
	if !present {
		// NULL = never opened; otherwise [fresh revision | bounded metadata].
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE aws_cognito_user_pools ADD COLUMN federation_state BLOB
			CHECK(federation_state IS NULL OR
			(typeof(federation_state) = 'blob' AND length(federation_state) BETWEEN %d AND %d))`,
			federationRevisionBytes, federationRevisionBytes+maxFederationMetadata))
		if err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &federationStorage{pools: s}, nil
}

// snapshot lazily binds a generation on first use. A missing parent is never
// created, and a newly recreated ID cannot use a prior revision. A revision is
// an optimistic concurrency marker, not a credential.
func (s *federationStorage) snapshot(ctx context.Context, scope Scope, id string) (federationSnapshot, error) {
	return s.transition(ctx, scope, id, func(_ federationSnapshot, exists bool) ([]byte, bool, error) {
		return nil, !exists, nil
	})
}

func (s *federationStorage) replace(ctx context.Context, scope Scope, id string, expected federationRevision, metadata []byte) (federationSnapshot, error) {
	if len(metadata) > maxFederationMetadata {
		return federationSnapshot{}, errInvalid
	}
	return s.transition(ctx, scope, id, func(current federationSnapshot, exists bool) ([]byte, bool, error) {
		if !exists || current.revision != expected {
			return nil, false, errFederationStale
		}
		return metadata, true, nil
	})
}

// transition without an edit only reads the current slot. With an edit, the
// bounded local callback checks the already scoped version and decides whether
// to publish. It must not do I/O, take a second DB connection or make policy
// decisions. Failed validation never initializes an empty slot.
type federationEdit func(current federationSnapshot, initialized bool) (metadata []byte, changed bool, err error)

func (s *federationStorage) transition(ctx context.Context, scope Scope, id string, edit federationEdit) (federationSnapshot, error) {
	if !scope.valid() || !scope.validPoolID(id) {
		return federationSnapshot{}, errInvalid
	}
	tx, err := s.pools.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return federationSnapshot{}, err
	}
	defer tx.Rollback()
	// Check shape inside SQLite before copying bytes from a damaged or
	// incompatible file; an invalid one-byte marker always fails validation.
	const selectSlot = `SELECT CASE WHEN federation_state IS NULL THEN NULL
		WHEN typeof(federation_state)='blob' AND length(federation_state) BETWEEN ? AND ?
		THEN federation_state ELSE X'00' END
		FROM aws_cognito_user_pools WHERE account=? AND region=? AND pool_id=?`
	var slot []byte
	err = tx.QueryRowContext(ctx, selectSlot, federationRevisionBytes, federationRevisionBytes+maxFederationMetadata, scope.Account, scope.Region, id).Scan(&slot)
	if errors.Is(err, sql.ErrNoRows) {
		return federationSnapshot{}, errMissing
	}
	if err != nil {
		return federationSnapshot{}, err
	}
	if slot != nil && (len(slot) < federationRevisionBytes || len(slot) > federationRevisionBytes+maxFederationMetadata) {
		return federationSnapshot{}, errFederationState
	}
	var current federationSnapshot
	if slot != nil {
		current = federationSnapshot{federationRevision(slot[:federationRevisionBytes]), bytes.Clone(slot[federationRevisionBytes:])}
	}
	var metadata []byte
	var changed bool
	if edit != nil {
		metadata, changed, err = edit(current, slot != nil)
		if err != nil {
			return federationSnapshot{}, err
		}
		if len(metadata) > maxFederationMetadata {
			return federationSnapshot{}, errInvalid
		}
	}
	if changed {
		// Mint a fresh marker even when replacing the payload with identical bytes.
		next := make([]byte, federationRevisionBytes+len(metadata))
		if _, err = rand.Read(next[:federationRevisionBytes]); err != nil {
			return federationSnapshot{}, err
		}
		copy(next[federationRevisionBytes:], metadata)
		// An untyped nil binds SQLite NULL; a typed nil []byte can bind an empty BLOB.
		var previous any
		if slot != nil {
			previous = slot
		}
		res, err := tx.ExecContext(ctx, `UPDATE aws_cognito_user_pools SET federation_state=?
			WHERE account=? AND region=? AND pool_id=? AND federation_state IS ?`,
			next, scope.Account, scope.Region, id, previous)
		if err != nil {
			return federationSnapshot{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return federationSnapshot{}, err
		}
		if n != 1 {
			return federationSnapshot{}, errFederationState
		}
		// SQLite RETURNING observes the row before AFTER triggers. Acknowledgment
		// requires exact readback and commit; ignored or altered writes roll back.
		if err = tx.QueryRowContext(ctx, selectSlot, federationRevisionBytes, federationRevisionBytes+maxFederationMetadata, scope.Account, scope.Region, id).Scan(&slot); err != nil {
			return federationSnapshot{}, err
		}
		if !bytes.Equal(next, slot) {
			return federationSnapshot{}, errFederationState
		}
		current = federationSnapshot{federationRevision(slot[:federationRevisionBytes]), slot[federationRevisionBytes:]}
	}
	if err = tx.Commit(); err != nil {
		return federationSnapshot{}, err
	}
	return current, nil
}
