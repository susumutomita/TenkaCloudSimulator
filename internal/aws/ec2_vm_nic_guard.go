package aws

import (
	"context"
	"database/sql"
)

// A pre-attachment VM reservation blocks every ordinary mutation, including an
// aliased store. There is no absent-row inference from a process-local map.
func ec2VMNICPending(ctx context.Context, tx *sql.Tx, o EC2GuestOwner) (bool, error) {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_ec2_vm_nic_launches'`).Scan(&present); err != nil || present == 0 {
		return false, err
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_vm_nic_launches WHERE account=? AND region=? AND instance=?`, o.Account, o.Region, o.Instance).Scan(&count)
	return count != 0, err
}
func ec2VMNICProfilePending(ctx context.Context, tx *sql.Tx, name, id string) (bool, error) {
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_ec2_vm_nic_launches'`).Scan(&present); err != nil || present == 0 {
		return false, err
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_vm_nic_launches v WHERE v.profile_name=? AND v.profile_id=? AND NOT EXISTS(SELECT 1 FROM aws_ec2_guest_attachments a WHERE a.account=v.account AND a.region=v.region AND a.instance=v.instance AND a.state='locally_drained')`, name, id).Scan(&count)
	return count != 0, err
}
