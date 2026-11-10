package aws

import (
	"context"
	"errors"
)

// DeleteStream removes exactly one regional stream and its events atomically.
// The parent group and sibling streams remain available. A recreated stream
// receives a new generation in CreateStream, invalidating prior event cursors.
func (s *CloudWatchLogsStore) DeleteStream(ctx context.Context, region, group, name string) error {
	if err := validLogsRegion(region); err != nil {
		return err
	}
	if !logsGroupName.MatchString(group) || !validLogsStream(name) {
		return logsInvalid("Invalid log group or stream name.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var present bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM aws_logs_streams WHERE region=? AND group_name=? AND name=?)", region, group, name).Scan(&present); err != nil {
		return err
	}
	if !present {
		return logsMissing()
	}
	if err = logsExactlyOne(tx.ExecContext(ctx, "DELETE FROM aws_logs_streams WHERE region=? AND group_name=? AND name=?", region, group, name)); err != nil {
		return err
	}
	// A successful DELETE must not acknowledge retained events if the host's
	// foreign-key configuration or a trigger prevented the cascade.
	var retained bool
	if err = tx.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM aws_logs_streams WHERE region=? AND group_name=? AND name=?) OR
 EXISTS(SELECT 1 FROM aws_logs_events WHERE region=? AND group_name=? AND stream_name=?)`, region, group, name, region, group, name).Scan(&retained); err != nil {
		return err
	}
	if retained {
		return errors.New("log stream cleanup was not acknowledged")
	}
	return tx.Commit()
}
