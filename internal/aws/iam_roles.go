// SPDX-License-Identifier: MIT
// Native IAM role state and independently authored AWS Query/XML projection.
// Historical notice retained for earlier, replaced Kumo adaptations:
// Copyright (c) 2025 sivchari
// Original permission and historical provenance: third_party/KUMO-LICENSE.txt
// and the corresponding third_party/kumo-*.json record.
package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Native IAM state is separate from legacy local identities. An owned name is
// retained after deletion so a local-identity write cannot adopt a deleted role.
type IAMRoleStore struct{ db *sql.DB }

// IAMRole is provider-owned role data. Its Query/XML representation is projected
// explicitly from the supported AWS Role fields rather than struct-tag coupling.
// Source contract: https://docs.aws.amazon.com/IAM/latest/APIReference/API_Role.html
type IAMRole struct {
	RoleName                 string
	RoleID                   string
	Arn                      string
	Path                     string
	CreateDate               time.Time
	AssumeRolePolicyDocument string
	Description              string
	MaxSessionDuration       int
}

// MarshalXML leaves the caller's Role/member element and namespace intact. The
// Query adapter already URL-encodes trust documents once; this layer only applies
// XML escaping. Unsupported AWS Role fields are not fabricated here.
func (role IAMRole) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	if err := encoder.EncodeToken(start); err != nil {
		return err
	}
	fields := []struct {
		name  string
		value any
	}{
		{"RoleName", role.RoleName}, {"RoleId", role.RoleID},
		{"Arn", role.Arn}, {"Path", role.Path}, {"CreateDate", role.CreateDate},
		{"AssumeRolePolicyDocument", role.AssumeRolePolicyDocument},
	}
	if role.Description != "" {
		fields = append(fields, struct {
			name  string
			value any
		}{"Description", role.Description})
	}
	fields = append(fields, struct {
		name  string
		value any
	}{"MaxSessionDuration", role.MaxSessionDuration})
	for _, field := range fields {
		if err := encoder.EncodeElement(field.value, xml.StartElement{Name: xml.Name{Local: field.name}}); err != nil {
			return err
		}
	}
	return encoder.EncodeToken(start.End())
}

// UnmarshalXML preserves the existing RoleId spelling, absent-field behavior and
// unknown-element skipping for Go consumers of the bounded native XML subset.
func (role *IAMRole) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch element := token.(type) {
		case xml.StartElement:
			var target any
			switch element.Name.Local {
			case "RoleName":
				target = &role.RoleName
			case "RoleId":
				target = &role.RoleID
			case "Arn":
				target = &role.Arn
			case "Path":
				target = &role.Path
			case "CreateDate":
				target = &role.CreateDate
			case "AssumeRolePolicyDocument":
				target = &role.AssumeRolePolicyDocument
			case "Description":
				target = &role.Description
			case "MaxSessionDuration":
				target = &role.MaxSessionDuration
			}
			if target == nil {
				err = decoder.Skip()
			} else {
				err = decoder.DecodeElement(target, &element)
			}
			if err != nil {
				return err
			}
		case xml.EndElement:
			if element.Name == start.Name {
				return nil
			}
		}
	}
}

var iamRoleName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var iamInlineName = regexp.MustCompile(`^[a-zA-Z0-9_+=,.@-]{1,128}$`)
var iamServicePrincipal = regexp.MustCompile(`^[a-z0-9.-]+\.amazonaws\.com$`)

func NewIAMRoleStore(db *sql.DB) (*IAMRoleStore, error) {
	if db == nil {
		return nil, fmt.Errorf("local IAM database is required")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_iam_roles (
 name TEXT PRIMARY KEY COLLATE NOCASE, role_id TEXT NOT NULL, trust TEXT NOT NULL,
 created TEXT NOT NULL, description TEXT NOT NULL, duration INTEGER NOT NULL,
 deleted INTEGER NOT NULL CHECK(deleted IN(0,1)));
 CREATE TABLE IF NOT EXISTS aws_iam_role_policies (
 role_name TEXT NOT NULL REFERENCES aws_iam_roles(name), name TEXT NOT NULL,
 document TEXT NOT NULL, PRIMARY KEY(role_name,name));`)
	if err == nil {
		err = iamManagedSchema(db)
	}
	return &IAMRoleStore{db}, err
}

type iamTrustStatement struct {
	Sid       string `json:"Sid,omitempty"`
	Effect    string
	Action    core.StringList
	Principal struct{ Service core.StringList }
}
type iamTrustStatements []iamTrustStatement

// encoding/json's case-insensitive struct matching is broader than this trust
// wire contract. Preserve the accepted original document, but require exact
// field names at every object level before it can enter shared API/UI state.
func iamTrustFields(data []byte, allowed ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return false
	}
	for field := range fields {
		found := false
		for _, name := range allowed {
			found = found || field == name
		}
		if !found {
			return false
		}
	}
	return true
}

func (s *iamTrustStatement) UnmarshalJSON(data []byte) error {
	if !iamTrustFields(data, "Sid", "Effect", "Action", "Principal") {
		return fmt.Errorf("use canonical trust statement field names")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if !iamTrustFields(fields["Principal"], "Service") {
		return fmt.Errorf("use canonical Service principal fields")
	}
	type plain iamTrustStatement
	return iamStrictJSON(data, (*plain)(s))
}

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
	if len(document) > 32<<10 || !iamTrustFields([]byte(document), "Version", "Statement") || iamStrictJSON([]byte(document), &trust) != nil || trust.Version != "2012-10-17" || len(trust.Statement) == 0 || len(trust.Statement) > 10 {
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
func (s *IAMRoleStore) PolicyNames(ctx context.Context, name string, expectedRoleID ...string) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return nil, err
	}
	if err = iamManagedExpectedRole(role, expectedRoleID); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT name FROM aws_iam_role_policies WHERE role_name=? ORDER BY name", role.RoleName)
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
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return names, nil
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
	// Match CreateRole's documented description character range before XML output.
	if !utf8.ValidString(description) || utf8.RuneCountInString(description) > 1000 {
		return IAMRole{}, ssmFailure{"ValidationError", "Description must use at most1000 permitted IAM characters."}
	}
	for _, r := range description {
		if r != '\t' && r != '\n' && r != '\r' && !(r >= 0x20 && r <= 0x7e) && !(r >= 0xa1 && r <= 0xff) {
			return IAMRole{}, ssmFailure{"ValidationError", "Description contains characters outside the documented IAM range."}
		}
	}

	if !iamRoleName.MatchString(name) || strings.EqualFold(name, core.Administrator) || duration < 3600 || duration > 43200 {
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
	if count > 10 {
		return ssmFailure{"LimitExceeded", "Local roles support10inline policies."}
	}
	role, err := iamManagedRole(ctx, tx, name)
	if err != nil {
		return err
	}
	managed, err := iamManagedRolePolicies(ctx, tx, role)
	if err != nil {
		return err
	}
	for _, policy := range managed {
		for _, version := range policy.versions {
			if version.IsDefaultVersion {
				part, e := core.ParsePolicy([]byte(version.Document))
				if e != nil {
					return e
				}
				p.Statement = append(p.Statement, part.Statement...)
			}
		}
	}
	if len(p.Statement) > 100 {
		return ssmFailure{"LimitExceeded", "Local role aggregates support100 statements."}
	}
	var previous string
	lookup := tx.QueryRowContext(ctx, "SELECT document FROM identity_policies WHERE name=?", name).Scan(&previous)
	if lookup != nil && lookup != sql.ErrNoRows {
		return lookup
	}
	if lookup == nil {
		if _, e := core.ParsePolicy([]byte(previous)); e != nil {
			return e
		}
	}
	if len(p.Statement) == 0 {
		result, e := tx.ExecContext(ctx, "DELETE FROM identity_policies WHERE name=?", name)
		if lookup == sql.ErrNoRows {
			if e != nil {
				return e
			}
			n, e := result.RowsAffected()
			if e != nil {
				return e
			}
			if n != 0 {
				return iamManagedBad()
			}
			return nil
		}
		return iamExactlyOne(result, e)
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
	if err = iamExactlyOne(tx.ExecContext(ctx, "INSERT INTO identity_policies(name,document) VALUES(?,?) ON CONFLICT(name) DO UPDATE SET document=excluded.document", name, string(data))); err != nil {
		return err
	}
	var saved string
	if err = tx.QueryRowContext(ctx, "SELECT document FROM identity_policies WHERE name=?", name).Scan(&saved); err != nil {
		return err
	}
	if saved != string(data) {
		return iamManagedBad()
	}
	return nil
}
func (s *IAMRoleStore) PutPolicy(ctx context.Context, name, policyName, document string, expectedRoleID ...string) error {
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
	if err = iamManagedExpectedRole(role, expectedRoleID); err != nil {
		return err
	}
	err = iamExactlyOne(tx.ExecContext(ctx, "INSERT INTO aws_iam_role_policies(role_name,name,document) VALUES(?,?,?) ON CONFLICT(role_name,name) DO UPDATE SET document=excluded.document", role.RoleName, policyName, string(data)))
	if err != nil {
		return err
	}
	if err = iamSyncPolicy(ctx, tx, role.RoleName); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *IAMRoleStore) Policy(ctx context.Context, name, policyName string, expectedRoleID ...string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return "", err
	}
	if err = iamManagedExpectedRole(role, expectedRoleID); err != nil {
		return "", err
	}
	var document string
	err = tx.QueryRowContext(ctx, "SELECT document FROM aws_iam_role_policies WHERE role_name=? AND name=?", role.RoleName, policyName).Scan(&document)
	if err == sql.ErrNoRows {
		return "", ssmFailure{"NoSuchEntity", "Inline policy does not exist."}
	}
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return document, nil
}
func (s *IAMRoleStore) DeletePolicy(ctx context.Context, name, policyName string, expectedRoleID ...string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return err
	}
	if err = iamManagedExpectedRole(role, expectedRoleID); err != nil {
		return err
	}
	var present int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_iam_role_policies WHERE role_name=? AND name=?", role.RoleName, policyName).Scan(&present); err != nil {
		return err
	}
	if present == 0 {
		return ssmFailure{"NoSuchEntity", "Inline policy does not exist."}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM aws_iam_role_policies WHERE role_name=? AND name=?", role.RoleName, policyName)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("local IAM inline policy delete was not acknowledged")
	}
	if err = iamSyncPolicy(ctx, tx, role.RoleName); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *IAMRoleStore) UpdateTrust(ctx context.Context, name, trust string, expectedRoleID ...string) error {
	if _, err := iamTrust(trust); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return err
	}
	if err = iamManagedExpectedRole(role, expectedRoleID); err != nil {
		return err
	}
	if err = iamExactlyOne(tx.ExecContext(ctx, "UPDATE aws_iam_roles SET trust=? WHERE name=? AND role_id=? AND deleted=0", trust, role.RoleName, role.RoleID)); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *IAMRoleStore) Delete(ctx context.Context, name string, expectedRoleID ...string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	role, err := iamReadRole(ctx, tx, name, false)
	if err != nil {
		return err
	}
	if err = iamManagedExpectedRole(role, expectedRoleID); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM aws_iam_role_policies WHERE role_name=?)+(SELECT count(*) FROM aws_iam_role_managed_policies WHERE role_name=?)", role.RoleName, role.RoleName).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ssmFailure{"DeleteConflict", "Remove the role's inline and attached policies before deleting it."}
	}
	var aggregate int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM identity_policies WHERE name=?", role.RoleName).Scan(&aggregate); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM identity_policies WHERE name=?", role.RoleName)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != int64(aggregate) {
		return iamManagedBad()
	}
	if err = iamExactlyOne(tx.ExecContext(ctx, "UPDATE aws_iam_roles SET deleted=1 WHERE name=? AND role_id=? AND deleted=0", role.RoleName, role.RoleID)); err != nil {
		return err
	}
	return tx.Commit()
}
