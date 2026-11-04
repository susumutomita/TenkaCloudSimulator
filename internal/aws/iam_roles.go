// Copyright (c) 2025 sivchari
// SPDX-License-Identifier: MIT
// IAMRole wire fields adapted from Kumo ce37a8fb406ecb96b5619204f645c26302b651a5.
// Original permission notice: third_party/KUMO-LICENSE.txt.
// SQLite storage, trust checks, ownership and aggregate-policy behavior are local.
package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Native IAM state is separate from legacy local identities. An owned name is
// retained after deletion so a local-identity write cannot adopt a deleted role.
type IAMRoleStore struct{ db *sql.DB }

// IAMRole adapts Kumo's MIT IAM Role wire model; see third_party/kumo-iam.json.
// SQLite ownership, trust evaluation and policy aggregation are local adapters.
type IAMRole struct {
	RoleName                 string    `xml:"RoleName"`
	RoleID                   string    `xml:"RoleId"`
	Arn                      string    `xml:"Arn"`
	Path                     string    `xml:"Path"`
	CreateDate               time.Time `xml:"CreateDate"`
	AssumeRolePolicyDocument string    `xml:"AssumeRolePolicyDocument"`
	Description              string    `xml:"Description,omitempty"`
	MaxSessionDuration       int       `xml:"MaxSessionDuration"`
}

var iamRoleName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var iamInlineName = regexp.MustCompile(`^[a-zA-Z0-9_+=,.@-]{1,128}$`)
var iamServicePrincipal = regexp.MustCompile(`^[a-z0-9.-]+\.amazonaws\.com$`)

func NewIAMRoleStore(db *sql.DB) (*IAMRoleStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_iam_roles (
 name TEXT PRIMARY KEY COLLATE NOCASE, role_id TEXT NOT NULL, trust TEXT NOT NULL,
 created TEXT NOT NULL, description TEXT NOT NULL, duration INTEGER NOT NULL,
 deleted INTEGER NOT NULL CHECK(deleted IN(0,1)));
 CREATE TABLE IF NOT EXISTS aws_iam_role_policies (
 role_name TEXT NOT NULL REFERENCES aws_iam_roles(name), name TEXT NOT NULL,
 document TEXT NOT NULL, PRIMARY KEY(role_name,name));`)
	return &IAMRoleStore{db}, err
}

type iamTrustStatement struct {
	Sid       string `json:"Sid,omitempty"`
	Effect    string
	Action    core.StringList
	Principal struct{ Service core.StringList }
}
type iamTrustStatements []iamTrustStatement

func iamStrictJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("use one JSON document")
	}
	return nil
}
func (s *iamTrustStatements) UnmarshalJSON(data []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		var one iamTrustStatement
		if err := iamStrictJSON(data, &one); err != nil {
			return err
		}
		*s = []iamTrustStatement{one}
		return nil
	}
	var many []iamTrustStatement
	if err := iamStrictJSON(data, &many); err != nil {
		return err
	}
	*s = many
	return nil
}

func iamTrust(document string) (bool, error) {
	var trust struct {
		Version   string
		Statement iamTrustStatements
	}
	if len(document) > 32<<10 || iamStrictJSON([]byte(document), &trust) != nil || trust.Version != "2012-10-17" || len(trust.Statement) == 0 || len(trust.Statement) > 10 {
		return false, ssmFailure{"MalformedPolicyDocument", "Use the bounded service-principal trust policy subset."}
	}
	allowed, denied := false, false
	for _, statement := range trust.Statement {
		if (statement.Effect != "Allow" && statement.Effect != "Deny") || len(statement.Action) != 1 || statement.Action[0] != "sts:AssumeRole" || len(statement.Principal.Service) == 0 || len(statement.Principal.Service) > 10 {
			return false, ssmFailure{"MalformedPolicyDocument", "Only Allow/Deny, sts:AssumeRole and Service principals are supported."}
		}
		for _, principal := range statement.Principal.Service {
			if !iamServicePrincipal.MatchString(principal) {
				return false, ssmFailure{"MalformedPolicyDocument", "Use an explicit supported service principal."}
			}
			if principal == "lambda.amazonaws.com" {
				if statement.Effect == "Deny" {
					denied = true
				} else {
					allowed = true
				}
			}
		}
	}
	return allowed && !denied, nil
}

func iamReadRole(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, name string, retired bool) (IAMRole, error) {
	var role IAMRole
	var created string
	var deleted int
	err := query.QueryRowContext(ctx, "SELECT name,role_id,trust,created,description,duration,deleted FROM aws_iam_roles WHERE name=?", name).Scan(&role.RoleName, &role.RoleID, &role.AssumeRolePolicyDocument, &created, &role.Description, &role.MaxSessionDuration, &deleted)
	if err == sql.ErrNoRows || err == nil && deleted != 0 && !retired {
		return role, ssmFailure{"NoSuchEntity", "The role does not exist."}
	}
	if err != nil {
		return role, err
	}
	role.CreateDate, err = time.Parse(time.RFC3339Nano, created)
	role.Path = "/"
	role.Arn = "arn:aws:iam::000000000000:role/" + role.RoleName
	return role, err
}
func (s *IAMRoleStore) Get(ctx context.Context, name string) (IAMRole, error) {
	return iamReadRole(ctx, s.db, name, false)
}

func (s *IAMRoleStore) List(ctx context.Context, after string, limit int) ([]IAMRole, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name,role_id,trust,created,description,duration FROM aws_iam_roles WHERE deleted=0 AND name>? COLLATE NOCASE ORDER BY name COLLATE NOCASE LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roles := []IAMRole{}
	for rows.Next() {
		var role IAMRole
		var created string
		if err = rows.Scan(&role.RoleName, &role.RoleID, &role.AssumeRolePolicyDocument, &created, &role.Description, &role.MaxSessionDuration); err != nil {
			return nil, err
		}
		role.CreateDate, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		role.Path = "/"
		role.Arn = "arn:aws:iam::000000000000:role/" + role.RoleName
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
func (s *IAMRoleStore) PolicyNames(ctx context.Context, name string) ([]string, error) {
	role, err := s.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM aws_iam_role_policies WHERE role_name=? ORDER BY name", role.RoleName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := []string{}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
func (s *IAMRoleStore) Owns(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_roles WHERE name=?", name).Scan(&count)
	return count != 0, err
}

func iamExecutionRole(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, arn string) error {
	name := strings.TrimPrefix(arn, "arn:aws:iam::000000000000:role/")
	var owned int
	if err := query.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_roles WHERE name=?", name).Scan(&owned); err != nil {
		return err
	}
	if owned == 0 {
		return nil
	} // Legacy local identities retain their documented behavior.
	role, err := iamReadRole(ctx, query, name, false)
	if err != nil {
		var missing ssmFailure
		if errors.As(err, &missing) && missing.code == "NoSuchEntity" {
			return ssmFailure{"InvalidParameterValueException", "Native execution role was deleted."}
		}
		return err
	}
	allowed, err := iamTrust(role.AssumeRolePolicyDocument)
	if err != nil {
		return err
	}
	if !allowed || role.Arn != arn {
		return ssmFailure{"InvalidParameterValueException", "Native role trust does not allow the Lambda service."}
	}
	return nil
}
func (s *IAMRoleStore) Create(ctx context.Context, name, trust, description string, duration int) (IAMRole, error) {
	if !iamRoleName.MatchString(name) || strings.EqualFold(name, core.Administrator) || len(description) > 1000 || duration < 3600 || duration > 43200 {
		return IAMRole{}, ssmFailure{"ValidationError", "Use the root-path local role-name subset and duration3600–43200."}
	}
	if _, err := iamTrust(trust); err != nil {
		return IAMRole{}, err
	}
	id, err := s3NewID()
	if err != nil {
		return IAMRole{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return IAMRole{}, err
	}
	defer tx.Rollback()
	var active, total int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_roles WHERE name=? AND deleted=0", name).Scan(&active); err != nil {
		return IAMRole{}, err
	}
	var existing string
	lookup := tx.QueryRowContext(ctx, "SELECT name FROM identity_policies WHERE lower(name)=lower(?)", name).Scan(&existing)
	if active != 0 || lookup == nil {
		return IAMRole{}, ssmFailure{"EntityAlreadyExists", "A role or legacy identity with this name already exists."}
	}
	if lookup != sql.ErrNoRows {
		return IAMRole{}, lookup
	}
	var claimsTable, claims int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='aws_cfn_ownership'").Scan(&claimsTable); err != nil {
		return IAMRole{}, err
	}
	if claimsTable != 0 {
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_cfn_ownership WHERE kind='AWS::IAM::Role' AND lower(physical_id)=lower(?)", name).Scan(&claims); err != nil {
			return IAMRole{}, err
		}
		if claims != 0 {
			return IAMRole{}, ssmFailure{"EntityAlreadyExists", "The role name is reserved by a stack, including drifted resources."}
		}
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_roles").Scan(&total); err != nil {
		return IAMRole{}, err
	}
	var retired int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_roles WHERE name=? AND deleted=1", name).Scan(&retired); err != nil {
		return IAMRole{}, err
	}
	if total >= 1000 && retired == 0 {
		return IAMRole{}, ssmFailure{"LimitExceeded", "Local active and retired native role claims are limited to1000."}
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO aws_iam_roles(name,role_id,trust,created,description,duration,deleted) VALUES(?,?,?,?,?,?,0)
 ON CONFLICT(name) DO UPDATE SET name=excluded.name,role_id=excluded.role_id,trust=excluded.trust,created=excluded.created,description=excluded.description,duration=excluded.duration,deleted=0`, name, "AROA"+strings.ToUpper(id[:16]), trust, created, description, duration)
	if err != nil {
		return IAMRole{}, err
	}
	if err = iamSyncPolicy(ctx, tx, name); err != nil {
		return IAMRole{}, err
	}
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return role, err
	}
	return role, tx.Commit()
}
func iamSyncPolicy(ctx context.Context, tx *sql.Tx, name string) error {
	rows, err := tx.QueryContext(ctx, "SELECT document FROM aws_iam_role_policies WHERE role_name=? ORDER BY name", name)
	if err != nil {
		return err
	}
	p := core.Policy{Version: "2012-10-17"}
	count := 0
	for rows.Next() {
		var data []byte
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return err
		}
		part, e := core.ParsePolicy(data)
		if e != nil {
			rows.Close()
			return e
		}
		p.Statement = append(p.Statement, part.Statement...)
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count > 10 || len(p.Statement) > 100 {
		return ssmFailure{"LimitExceeded", "Local roles support10inline policies and100aggregate statements."}
	}
	if len(p.Statement) == 0 {
		_, err = tx.ExecContext(ctx, "DELETE FROM identity_policies WHERE name=?", name)
		return err
	}
	var identities, existing int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM identity_policies").Scan(&identities); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM identity_policies WHERE name=?", name).Scan(&existing); err != nil {
		return err
	}
	if identities >= 1000 && existing == 0 {
		return ssmFailure{"LimitExceeded", "Local identity-policy state is limited to1000records."}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(data) > 32<<10 {
		return ssmFailure{"LimitExceeded", "Local aggregate role policy exceeds32KiB."}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO identity_policies(name,document) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET document=excluded.document", name, string(data))
	return err
}
func (s *IAMRoleStore) PutPolicy(ctx context.Context, name, policyName, document string) error {
	if !iamInlineName.MatchString(policyName) || len(document) > 32<<10 {
		return ssmFailure{"MalformedPolicyDocument", "Use a named inline policy of at most32KiB."}
	}
	_, err := core.ParsePolicy([]byte(document))
	if err != nil {
		return ssmFailure{"MalformedPolicyDocument", err.Error()}
	}
	// Preserve the validated wire document; aggregate authorization is normalized separately.
	data := []byte(document)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO aws_iam_role_policies(role_name,name,document) VALUES(?,?,?) ON CONFLICT(role_name,name) DO UPDATE SET document=excluded.document", role.RoleName, policyName, string(data))
	if err != nil {
		return err
	}
	if err = iamSyncPolicy(ctx, tx, role.RoleName); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *IAMRoleStore) Policy(ctx context.Context, name, policyName string) (string, error) {
	role, err := s.Get(ctx, name)
	if err != nil {
		return "", err
	}
	var document string
	err = s.db.QueryRowContext(ctx, "SELECT document FROM aws_iam_role_policies WHERE role_name=? AND name=?", role.RoleName, policyName).Scan(&document)
	if err == sql.ErrNoRows {
		err = ssmFailure{"NoSuchEntity", "Inline policy does not exist."}
	}
	return document, err
}
func (s *IAMRoleStore) DeletePolicy(ctx context.Context, name, policyName string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM aws_iam_role_policies WHERE role_name=? AND name=?", role.RoleName, policyName)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ssmFailure{"NoSuchEntity", "Inline policy does not exist."}
	}
	if err = iamSyncPolicy(ctx, tx, role.RoleName); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *IAMRoleStore) UpdateTrust(ctx context.Context, name, trust string) error {
	if _, err := iamTrust(trust); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE aws_iam_roles SET trust=? WHERE name=? AND deleted=0", trust, name)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ssmFailure{"NoSuchEntity", "The role does not exist."}
	}
	return nil
}
func (s *IAMRoleStore) Delete(ctx context.Context, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_role_policies WHERE role_name=?", role.RoleName).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ssmFailure{"DeleteConflict", "Remove the role's inline policies before deleting it."}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM identity_policies WHERE name=?", role.RoleName); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE aws_iam_roles SET deleted=1 WHERE name=?", role.RoleName); err != nil {
		return err
	}
	return tx.Commit()
}
