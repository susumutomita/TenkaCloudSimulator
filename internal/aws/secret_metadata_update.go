package aws

import (
	"context"
	"errors"
	"unicode/utf8"
)

// UpdateDescription replaces metadata only. Resolution, authorization, the
// checked write and final-state validation share one transaction. No version
// value is selected, no token is reserved and no stage is moved.
func (s *SecretStore) UpdateDescription(ctx context.Context, region, id, description string, authorize SecretAuthorizer) (SecretIdentity, error) {
	var zero SecretIdentity
	if s == nil || s.db == nil || s.now == nil {
		return zero, errors.New("secret metadata store unavailable")
	}
	if !secretRegion(region) || !utf8.ValidString(description) || utf8.RuneCountInString(description) > 2048 {
		return zero, secretInvalid()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	meta, err := secretResolve(ctx, tx, region, id)
	if err != nil {
		return zero, err
	}
	if err = secretAuthorize(authorize, "UpdateSecret", meta.ARN); err != nil {
		return zero, err
	}
	var created int64
	if err = tx.QueryRowContext(ctx, "SELECT created FROM aws_secrets WHERE region=? AND arn=?", region, meta.ARN).Scan(&created); err != nil {
		return zero, err
	}
	now := s.now().UnixMilli()
	if err = secretExecRows(ctx, tx, 1, "UPDATE aws_secrets SET description=?,changed=? WHERE region=? AND arn=?", description, now, region, meta.ARN); err != nil {
		return zero, err
	}
	var name, savedDescription string
	var savedCreated, savedChanged int64
	if err = tx.QueryRowContext(ctx, "SELECT name,description,created,changed FROM aws_secrets WHERE region=? AND arn=?", region, meta.ARN).Scan(&name, &savedDescription, &savedCreated, &savedChanged); err != nil {
		return zero, err
	}
	if name != meta.Name || savedDescription != description || savedCreated != created || savedChanged != now {
		return zero, errors.New("secret metadata durable state mismatch")
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return SecretIdentity{ARN: meta.ARN, Name: meta.Name}, nil
}
