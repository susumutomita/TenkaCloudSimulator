package aws

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// MutateIdentityPolicy keeps legacy control-API writes and native role ownership
// in the same SQLite transaction. A prior ownership read alone would race with
// CreateRole, particularly because a role without policies has no identity row.
// A nil policy means DELETE; createOnly retains the control API's precondition.
func (s *IAMRoleStore) MutateIdentityPolicy(ctx context.Context, name string, policy *core.Policy, createOnly bool) error {
	if strings.EqualFold(name, core.Administrator) {
		return core.Fail("Conflict", "The simulator administrator is reserved.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_roles WHERE name=?", name).Scan(&owned); err != nil {
		return err
	}
	if owned != 0 {
		return core.Fail("Conflict", "This identity name belongs to native IAM. Use the IAM role API to manage it.")
	}
	if policy == nil {
		if _, err = tx.ExecContext(ctx, "DELETE FROM identity_policies WHERE name=?", name); err != nil {
			return err
		}
		return tx.Commit()
	}
	var count, exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*),count(CASE WHEN name=? THEN 1 END) FROM identity_policies", name).Scan(&count, &exists); err != nil {
		return err
	}
	if count >= 1000 && exists == 0 {
		return core.Fail("ServiceUnavailable", "Simulator principal limit reached.")
	}
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	query := "INSERT INTO identity_policies(name,document) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET document=excluded.document"
	if createOnly {
		query = "INSERT INTO identity_policies(name,document) VALUES(?,?) ON CONFLICT(name) DO NOTHING"
	}
	result, err := tx.ExecContext(ctx, query, name, string(data))
	if err != nil {
		return err
	}
	if createOnly {
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n == 0 {
			return core.Fail("PreconditionFailed", "An identity with this name already exists.")
		}
	}
	return tx.Commit()
}
