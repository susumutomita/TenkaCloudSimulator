//go:build cognito_hosted

package cognitoidp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	hostedSlotBytes    = 512 << 10
	hostedPendingLimit = 128
	hostedCodeLimit    = 128
	hostedReplayLimit  = 256
	hostedPendingTTL   = 5 * time.Minute
	hostedCodeTTL      = time.Minute
)

const hostedPoolSlots = `SELECT
	CASE WHEN federation_state IS NULL THEN NULL
	     WHEN typeof(federation_state)='blob' AND length(federation_state) BETWEEN 16 AND 65552
	     THEN federation_state ELSE X'00' END,
	CASE WHEN federation_hosted_state IS NULL THEN NULL
	     WHEN typeof(federation_hosted_state)='blob' AND length(federation_hosted_state) <= 524288
	     THEN federation_hosted_state ELSE X'00' END
	FROM aws_cognito_user_pools WHERE account=? AND region=? AND pool_id=?`

// Hosted state is an optional extension of the same pool row. In particular a
// browser request cannot create a pool, or outlive its deletion or its revision.
type hostedState struct {
	Epoch    string
	Revision federationRevision
	Pending  map[string]hostedPending
	Codes    map[string]hostedGrant
	Replay   map[string]time.Time
}

type hostedPending struct {
	Browser, RequestID, Provider, Client, Redirect, Challenge, Scope, State, Nonce string
	Until                                                                          time.Time
}

type hostedGrant struct {
	Client, Provider, Redirect, Challenge, Scope, Nonce, Subject string
	Attributes                                                   map[string]string
	AuthTime, Until                                              time.Time
}

func (s *Store) openHostedState(ctx context.Context) error {
	if _, err := s.openFederationStorage(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var present bool
	err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pragma_table_info('aws_cognito_user_pools') WHERE name='federation_hosted_state')").Scan(&present)
	if err != nil {
		return err
	}
	if !present {
		_, err = tx.ExecContext(ctx, `ALTER TABLE aws_cognito_user_pools ADD COLUMN federation_hosted_state BLOB
			CHECK(federation_hosted_state IS NULL OR
			(typeof(federation_hosted_state)='blob' AND length(federation_hosted_state) BETWEEN 1 AND 524288))`)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func readHostedState(raw []byte, epoch string, rev federationRevision) (hostedState, error) {
	state := hostedState{
		Epoch: epoch, Revision: rev,
		Pending: map[string]hostedPending{}, Codes: map[string]hostedGrant{}, Replay: map[string]time.Time{},
	}
	if raw == nil {
		return state, nil
	}
	var old hostedState
	if len(raw) == 0 || len(raw) > hostedSlotBytes || json.Unmarshal(raw, &old) != nil ||
		old.Pending == nil || old.Codes == nil || old.Replay == nil ||
		len(old.Pending) > hostedPendingLimit || len(old.Codes) > hostedCodeLimit || len(old.Replay) > hostedReplayLimit {
		return hostedState{}, errFederationState
	}
	canonical, err := json.Marshal(old)
	if err != nil || !bytes.Equal(canonical, raw) {
		return hostedState{}, errFederationState
	}
	if old.Epoch == epoch && old.Revision == rev {
		return old, nil
	}
	return state, nil
}

func (s *hostedState) clean(now time.Time) {
	for k, p := range s.Pending {
		if !now.Before(p.Until) {
			delete(s.Pending, k)
		}
	}
	for k, g := range s.Codes {
		if !now.Before(g.Until) {
			delete(s.Codes, k)
		}
	}
	for k, expiry := range s.Replay {
		if !now.Before(expiry) {
			delete(s.Replay, k)
		}
	}
}

type hostedEdit func(federationConfig, federationRevision, *hostedState) (bool, error)

// A caller can read and perform expensive signature verification outside the
// write lock, but must pass and recheck the original revision to grant access.
// Nothing in this function consults the network or accepts a browser actor.
func (b *hostedBrowser) transition(ctx context.Context, scope Scope, id string, expected *federationRevision, edit hostedEdit) error {
	if !scope.valid() || !scope.validPoolID(id) {
		return errInvalid
	}
	tx, err := b.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var config, previous []byte
	err = tx.QueryRowContext(ctx, hostedPoolSlots, scope.Account, scope.Region, id).Scan(&config, &previous)
	if errors.Is(err, sql.ErrNoRows) {
		return errMissing
	}
	if err != nil {
		return err
	}
	if len(config) < federationRevisionBytes || len(config) > federationRevisionBytes+maxFederationMetadata {
		return errInvalid
	}
	revision := federationRevision(config[:federationRevisionBytes])
	if expected != nil && *expected != revision {
		return errFederationStale
	}
	cfg, err := readFederationConfig(config[federationRevisionBytes:], federationOwner{scope, id})
	if err != nil {
		return err
	}
	state, err := readHostedState(previous, b.epoch, revision)
	if err != nil {
		return err
	}
	state.clean(time.Now().UTC())
	changed, err := edit(cfg, revision, &state)
	if err != nil {
		return err
	}
	if changed {
		next, err := json.Marshal(state)
		if err != nil || len(next) > hostedSlotBytes || len(state.Pending) > hostedPendingLimit ||
			len(state.Codes) > hostedCodeLimit || len(state.Replay) > hostedReplayLimit {
			return errFederationLimit
		}
		var comparePrevious any
		if previous != nil {
			comparePrevious = previous
		}
		res, err := tx.ExecContext(ctx, `UPDATE aws_cognito_user_pools SET federation_hosted_state=?
			WHERE account=? AND region=? AND pool_id=? AND federation_state IS ? AND federation_hosted_state IS ?`,
			next, scope.Account, scope.Region, id, config, comparePrevious)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			return errFederationState
		}
		var storedConfig, storedState []byte
		if err := tx.QueryRowContext(ctx, hostedPoolSlots, scope.Account, scope.Region, id).Scan(&storedConfig, &storedState); err != nil {
			return err
		}
		if !bytes.Equal(storedConfig, config) || !bytes.Equal(storedState, next) {
			return errFederationState
		}
	}
	return tx.Commit()
}
