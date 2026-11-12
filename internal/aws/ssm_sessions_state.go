package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func (s *SSMSessionStore) reserve(ctx context.Context, e *ssmLiveSession) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_ssm_sessions").Scan(&count); err != nil {
		return err
	}
	if count >= 100 {
		return fmt.Errorf("persisted session quota reached")
	}
	var occupied int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ssm_sessions WHERE region=? AND stack_id=? AND instance_id=? AND state<>'terminated'`, e.region, e.target.StackID, e.target.InstanceID).Scan(&occupied); err != nil {
		return err
	}
	if occupied != 0 {
		return fmt.Errorf("target has persisted session cleanup debt")
	}
	ref := sha256.Sum256([]byte(e.owner.key))
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ssm_sessions VALUES(?,?,?,?,?,?,?,?)`, e.id, e.region, e.target.StackID, e.target.InstanceID, e.owner.identity.UserID, hex.EncodeToString(ref[:]), "starting", e.deadline.Unix())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("session reservation not acknowledged")
	}
	var region, stack, instance, owner, ownerRef, state string
	var expires int64
	if err = tx.QueryRowContext(ctx, `SELECT region,stack_id,instance_id,owner_tag,owner_ref,state,expires FROM aws_ssm_sessions WHERE id=?`, e.id).Scan(&region, &stack, &instance, &owner, &ownerRef, &state, &expires); err != nil {
		return err
	}
	if region != e.region || stack != e.target.StackID || instance != e.target.InstanceID || owner != e.owner.identity.UserID || ownerRef != hex.EncodeToString(ref[:]) || state != "starting" || expires != e.deadline.Unix() {
		return fmt.Errorf("session reservation readback differs")
	}
	return tx.Commit()
}
func (s *SSMSessionStore) transition(ctx context.Context, e *ssmLiveSession, from, to string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ref := sha256.Sum256([]byte(e.owner.key))
	result, err := tx.ExecContext(ctx, `UPDATE aws_ssm_sessions SET state=? WHERE id=? AND region=? AND stack_id=? AND instance_id=? AND owner_tag=? AND owner_ref=? AND expires=? AND state=?`, to, e.id, e.region, e.target.StackID, e.target.InstanceID, e.owner.identity.UserID, hex.EncodeToString(ref[:]), e.deadline.Unix(), from)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return fmt.Errorf("session transition not acknowledged")
	}
	var region, stack, instance, owner, ownerRef, state string
	var expires int64
	if err = tx.QueryRowContext(ctx, `SELECT region,stack_id,instance_id,owner_tag,owner_ref,state,expires FROM aws_ssm_sessions WHERE id=?`, e.id).Scan(&region, &stack, &instance, &owner, &ownerRef, &state, &expires); err != nil {
		return err
	}
	if region != e.region || stack != e.target.StackID || instance != e.target.InstanceID || owner != e.owner.identity.UserID || ownerRef != hex.EncodeToString(ref[:]) || expires != e.deadline.Unix() || state != to {
		return fmt.Errorf("session transition readback differs")
	}
	return tx.Commit()
}
