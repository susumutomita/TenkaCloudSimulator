// Copyright (c) 2026 TenkaCloudSimulator contributors
// SPDX-License-Identifier: MIT
package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Customer managed policies are local IAM state, never an AWS-managed ARN grant.
type IAMManagedPolicy struct {
	PolicyName       string    `xml:"PolicyName"`
	PolicyID         string    `xml:"PolicyId"`
	Arn              string    `xml:"Arn"`
	Path             string    `xml:"Path"`
	Description      string    `xml:"Description,omitempty"`
	DefaultVersionID string    `xml:"DefaultVersionId"`
	CreateDate       time.Time `xml:"CreateDate"`
	UpdateDate       time.Time `xml:"UpdateDate"`
	AttachmentCount  int       `xml:"AttachmentCount"`
	IsAttachable     bool      `xml:"IsAttachable"`
}
type IAMManagedPolicyVersion struct {
	VersionID        string    `xml:"VersionId"`
	IsDefaultVersion bool      `xml:"IsDefaultVersion"`
	CreateDate       time.Time `xml:"CreateDate"`
	Document         string    `xml:"Document,omitempty"`
}
type IAMAttachedPolicy struct {
	PolicyName string `xml:"PolicyName"`
	PolicyArn  string `xml:"PolicyArn"`
}

const iamManagedPrefix = "arn:aws:iam::000000000000:policy/"

var iamManagedName = regexp.MustCompile(`^[A-Za-z0-9_+=,.@-]{1,128}$`)
var iamManagedID = regexp.MustCompile(`^ANPA[A-F0-9]{16}$`)
var iamManagedRoleID = regexp.MustCompile(`^AROA[A-F0-9]{16}$`)

func iamManagedSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS aws_iam_role_generation ON aws_iam_roles(name,role_id);
 CREATE TABLE IF NOT EXISTS aws_iam_managed_policies (
 name TEXT PRIMARY KEY COLLATE NOCASE, policy_id TEXT NOT NULL, path TEXT NOT NULL,
 description TEXT NOT NULL, created TEXT NOT NULL, updated TEXT NOT NULL,
 default_version INTEGER NOT NULL, next_version INTEGER NOT NULL, deleted INTEGER NOT NULL CHECK(deleted IN(0,1)),
 UNIQUE(name,policy_id));
 CREATE TABLE IF NOT EXISTS aws_iam_managed_policy_versions (
 policy_name TEXT NOT NULL COLLATE NOCASE, policy_id TEXT NOT NULL, version_number INTEGER NOT NULL CHECK(version_number>0),
 document TEXT NOT NULL, created TEXT NOT NULL, PRIMARY KEY(policy_name,policy_id,version_number),
 FOREIGN KEY(policy_name,policy_id) REFERENCES aws_iam_managed_policies(name,policy_id));
 CREATE TABLE IF NOT EXISTS aws_iam_role_managed_policies (
 role_name TEXT NOT NULL COLLATE NOCASE, role_id TEXT NOT NULL, policy_name TEXT NOT NULL COLLATE NOCASE, policy_id TEXT NOT NULL,
 PRIMARY KEY(role_name,policy_name),
 FOREIGN KEY(role_name,role_id) REFERENCES aws_iam_roles(name,role_id),
 FOREIGN KEY(policy_name,policy_id) REFERENCES aws_iam_managed_policies(name,policy_id));`)
	return err
}
func iamManagedBad() error { return fmt.Errorf("invalid persisted local IAM managed policy state") }
func iamExactlyOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, e := result.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return fmt.Errorf("local IAM required write was not acknowledged")
	}
	return nil
}
func iamManagedText(text string, max int) bool {
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > max {
		return false
	}
	for _, r := range text {
		if r != '\t' && r != '\n' && r != '\r' && (r < 0x20 || r > 0xff) {
			return false
		}
	}
	return true
}
func iamManagedDocument(document string) error {
	var compact bytes.Buffer
	if len(document) == 0 || len(document) > 32<<10 || !iamManagedText(document, 32<<10) || json.Compact(&compact, []byte(document)) != nil || utf8.RuneCount(compact.Bytes()) > 6144 {
		return ssmFailure{"MalformedPolicyDocument", "Use a Latin-1 IAM JSON document of at most32KiB and6144 compact characters."}
	}
	if _, err := core.ParsePolicy([]byte(document)); err != nil {
		return ssmFailure{"MalformedPolicyDocument", "Use the supported identity-policy grammar."}
	}
	return nil
}

var iamManagedVersionSyntax = regexp.MustCompile(`^v[1-9][0-9]*(\.[A-Za-z0-9-]*)?$`)

func iamManagedVersionNumber(id string) (int64, error) {
	if len(id) > 128 || !iamManagedVersionSyntax.MatchString(id) {
		return 0, ssmFailure{"InvalidInput", "Use a valid IAM policy version identifier."}
	}
	n, e := strconv.ParseInt(strings.TrimPrefix(id, "v"), 10, 64)
	if e != nil || id != "v"+strconv.FormatInt(n, 10) {
		return 0, nil
	} // Valid AWS syntax, but not a saved local version.
	return n, nil
}
func iamManagedARNName(arn string) (string, error) {
	name := strings.TrimPrefix(arn, iamManagedPrefix)
	if !strings.HasPrefix(arn, iamManagedPrefix) || !iamManagedName.MatchString(name) {
		return "", ssmFailure{"InvalidInput", "Use an explicit root-path customer policy ARN in the local account."}
	}
	return name, nil
}

type iamManagedRecord struct {
	IAMManagedPolicy
	next, defaultNumber int64
	versions            []IAMManagedPolicyVersion
}

func iamManagedRead(ctx context.Context, tx *sql.Tx, arn string) (iamManagedRecord, error) {
	var p iamManagedRecord
	name, e := iamManagedARNName(arn)
	if e != nil {
		return p, e
	}
	var created, updated string
	var deleted int
	e = tx.QueryRowContext(ctx, `SELECT name,policy_id,path,description,created,updated,default_version,next_version,deleted FROM aws_iam_managed_policies WHERE name=?`, name).Scan(&p.PolicyName, &p.PolicyID, &p.Path, &p.Description, &created, &updated, &p.defaultNumber, &p.next, &deleted)
	if e == sql.ErrNoRows || e == nil && deleted == 1 {
		return p, ssmFailure{"NoSuchEntity", "The customer managed policy does not exist."}
	}
	if e != nil {
		return p, e
	}
	p.Arn = iamManagedPrefix + p.PolicyName
	if p.Arn != arn {
		return p, ssmFailure{"NoSuchEntity", "The exact customer policy ARN does not exist."}
	}
	p.IsAttachable = true
	p.DefaultVersionID = "v" + strconv.FormatInt(p.defaultNumber, 10)
	p.CreateDate, e = time.Parse(time.RFC3339Nano, created)
	if e != nil {
		return p, iamManagedBad()
	}
	p.UpdateDate, e = time.Parse(time.RFC3339Nano, updated)
	if e != nil || deleted != 0 || !iamManagedName.MatchString(p.PolicyName) || !iamManagedID.MatchString(p.PolicyID) || p.Path != "/" || !iamManagedText(p.Description, 1000) || p.UpdateDate.Before(p.CreateDate) || p.defaultNumber < 1 || p.next <= p.defaultNumber {
		return p, iamManagedBad()
	}
	rows, e := tx.QueryContext(ctx, `SELECT version_number,document,created FROM aws_iam_managed_policy_versions WHERE policy_name=? AND policy_id=? ORDER BY version_number`, p.PolicyName, p.PolicyID)
	if e != nil {
		return p, e
	}
	p.versions = []IAMManagedPolicyVersion{}
	found := false
	for rows.Next() {
		var n int64
		var v IAMManagedPolicyVersion
		var date string
		if e = rows.Scan(&n, &v.Document, &date); e != nil {
			rows.Close()
			return p, e
		}
		v.CreateDate, e = time.Parse(time.RFC3339Nano, date)
		if e != nil || n < 1 || n >= p.next || v.CreateDate.Before(p.CreateDate) || iamManagedDocument(v.Document) != nil {
			rows.Close()
			return p, iamManagedBad()
		}
		v.VersionID = "v" + strconv.FormatInt(n, 10)
		v.IsDefaultVersion = n == p.defaultNumber
		found = found || v.IsDefaultVersion
		p.versions = append(p.versions, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	if !found || len(p.versions) > 5 {
		return p, iamManagedBad()
	}
	// Inspect every referring generation; stale/corrupt rows are not attachment metadata.
	rows, e = tx.QueryContext(ctx, `SELECT a.role_name,a.role_id,r.role_id,r.deleted,a.policy_id FROM aws_iam_role_managed_policies a LEFT JOIN aws_iam_roles r ON r.name=a.role_name WHERE a.policy_name=?`, p.PolicyName)
	if e != nil {
		return p, e
	}
	for rows.Next() {
		var roleName, attachedID, currentID, attachedPolicyID string
		var retired int
		if e = rows.Scan(&roleName, &attachedID, &currentID, &retired, &attachedPolicyID); e != nil {
			rows.Close()
			return p, e
		}
		if !iamRoleName.MatchString(roleName) || !iamManagedRoleID.MatchString(currentID) || attachedID != currentID || retired != 0 || attachedPolicyID != p.PolicyID {
			rows.Close()
			return p, iamManagedBad()
		}
		p.AttachmentCount++
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return p, e
	}
	if p.AttachmentCount > 1000 {
		return p, iamManagedBad()
	}
	return p, nil
}
func iamManagedRole(ctx context.Context, tx *sql.Tx, name string) (IAMRole, error) {
	r, e := iamReadRole(ctx, tx, name, false)
	if e != nil {
		return r, e
	}
	if !iamManagedRoleID.MatchString(r.RoleID) || !iamRoleName.MatchString(r.RoleName) {
		return r, iamManagedBad()
	}
	return r, nil
}
func (s *IAMRoleStore) CreateManagedPolicy(ctx context.Context, name, path, description, document string) (IAMManagedPolicy, error) {
	if !iamManagedName.MatchString(name) || !iamManagedText(description, 1000) {
		return IAMManagedPolicy{}, ssmFailure{"InvalidInput", "Use a valid customer policy name and description."}
	}
	if path != "/" {
		return IAMManagedPolicy{}, ssmFailure{"NotImplemented", "Only root-path customer managed policies are supported."}
	}
	if e := iamManagedDocument(document); e != nil {
		return IAMManagedPolicy{}, e
	}
	id, e := s3NewID()
	if e != nil {
		return IAMManagedPolicy{}, e
	}
	id = "ANPA" + strings.ToUpper(id[:16])
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return IAMManagedPolicy{}, e
	}
	defer tx.Rollback()
	var oldID string
	var deleted int
	e = tx.QueryRowContext(ctx, `SELECT policy_id,deleted FROM aws_iam_managed_policies WHERE name=?`, name).Scan(&oldID, &deleted)
	if e == nil && deleted == 0 {
		return IAMManagedPolicy{}, ssmFailure{"EntityAlreadyExists", "The customer policy name already exists."}
	}
	if e != nil && e != sql.ErrNoRows {
		return IAMManagedPolicy{}, e
	}
	existing := e == nil
	var total int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_iam_managed_policies`).Scan(&total); e != nil {
		return IAMManagedPolicy{}, e
	}
	if total >= 1000 && !existing {
		return IAMManagedPolicy{}, ssmFailure{"LimitExceeded", "Local active and retired customer policy claims are limited to1000."}
	}
	if existing {
		var remaining int
		if deleted != 1 || !iamManagedID.MatchString(oldID) {
			return IAMManagedPolicy{}, iamManagedBad()
		}
		if e = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM aws_iam_managed_policy_versions WHERE policy_name=?)+(SELECT count(*) FROM aws_iam_role_managed_policies WHERE policy_name=?)`, name, name).Scan(&remaining); e != nil {
			return IAMManagedPolicy{}, e
		}
		if remaining != 0 {
			return IAMManagedPolicy{}, iamManagedBad()
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if e = iamExactlyOne(tx.ExecContext(ctx, `INSERT INTO aws_iam_managed_policies(name,policy_id,path,description,created,updated,default_version,next_version,deleted) VALUES(?,?,?,?,?,?,1,2,0) ON CONFLICT(name) DO UPDATE SET name=excluded.name,policy_id=excluded.policy_id,path=excluded.path,description=excluded.description,created=excluded.created,updated=excluded.updated,default_version=1,next_version=2,deleted=0`, name, id, path, description, now, now)); e != nil {
		return IAMManagedPolicy{}, e
	}
	if e = iamExactlyOne(tx.ExecContext(ctx, `INSERT INTO aws_iam_managed_policy_versions(policy_name,policy_id,version_number,document,created) VALUES(?,?,1,?,?)`, name, id, document, now)); e != nil {
		return IAMManagedPolicy{}, e
	}
	p, e := iamManagedRead(ctx, tx, iamManagedPrefix+name)
	if e != nil {
		return p.IAMManagedPolicy, e
	}
	return p.IAMManagedPolicy, tx.Commit()
}
func (s *IAMRoleStore) ManagedPolicy(ctx context.Context, arn string) (IAMManagedPolicy, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return IAMManagedPolicy{}, e
	}
	defer tx.Rollback()
	p, e := iamManagedRead(ctx, tx, arn)
	if e != nil {
		return p.IAMManagedPolicy, e
	}
	return p.IAMManagedPolicy, tx.Commit()
}
func (s *IAMRoleStore) ManagedPolicies(ctx context.Context, afterName string, limit int) ([]IAMManagedPolicy, error) {
	if limit < 1 || limit > 1001 || afterName != "" && !iamManagedName.MatchString(afterName) {
		return nil, ssmFailure{"InvalidInput", "Use a bounded policy list page."}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, `SELECT name FROM aws_iam_managed_policies WHERE deleted=0 AND name>? COLLATE NOCASE ORDER BY name COLLATE NOCASE LIMIT ?`, afterName, limit)
	if e != nil {
		return nil, e
	}
	names := []string{}
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			rows.Close()
			return nil, e
		}
		names = append(names, name)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	list := []IAMManagedPolicy{}
	for _, name := range names {
		p, e := iamManagedRead(ctx, tx, iamManagedPrefix+name)
		if e != nil {
			return nil, e
		}
		list = append(list, p.IAMManagedPolicy)
	}
	return list, tx.Commit()
}
func (s *IAMRoleStore) ManagedPolicyVersions(ctx context.Context, arn string) (IAMManagedPolicy, []IAMManagedPolicyVersion, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return IAMManagedPolicy{}, nil, e
	}
	defer tx.Rollback()
	p, e := iamManagedRead(ctx, tx, arn)
	if e != nil {
		return IAMManagedPolicy{}, nil, e
	}
	return p.IAMManagedPolicy, p.versions, tx.Commit()
}
func (s *IAMRoleStore) ManagedPolicyVersion(ctx context.Context, arn, id string) (IAMManagedPolicyVersion, error) {
	if _, e := iamManagedVersionNumber(id); e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	_, versions, e := s.ManagedPolicyVersions(ctx, arn)
	if e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	for _, v := range versions {
		if v.VersionID == id {
			return v, nil
		}
	}
	return IAMManagedPolicyVersion{}, ssmFailure{"NoSuchEntity", "The policy version does not exist."}
}

// Caller owns tx. Close all cursors before recomputing role aggregates.
func iamManagedSyncAttached(ctx context.Context, tx *sql.Tx, p iamManagedRecord) error {
	rows, e := tx.QueryContext(ctx, `SELECT role_name,role_id FROM aws_iam_role_managed_policies WHERE policy_name=? AND policy_id=? ORDER BY role_name COLLATE NOCASE`, p.PolicyName, p.PolicyID)
	if e != nil {
		return e
	}
	type pair struct{ name, id string }
	roles := []pair{}
	for rows.Next() {
		var r pair
		if e = rows.Scan(&r.name, &r.id); e != nil {
			rows.Close()
			return e
		}
		roles = append(roles, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, a := range roles {
		r, e := iamManagedRole(ctx, tx, a.name)
		if e != nil {
			return e
		}
		if r.RoleID != a.id {
			return iamManagedBad()
		}
		if e = iamSyncPolicy(ctx, tx, r.RoleName); e != nil {
			return e
		}
	}
	return nil
}
func (s *IAMRoleStore) CreateManagedPolicyVersion(ctx context.Context, arn, document string, setDefault bool) (IAMManagedPolicyVersion, error) {
	if e := iamManagedDocument(document); e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	defer tx.Rollback()
	p, e := iamManagedRead(ctx, tx, arn)
	if e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	if len(p.versions) >= 5 || p.next == math.MaxInt64 {
		return IAMManagedPolicyVersion{}, ssmFailure{"LimitExceeded", "Local managed policies support at most5 live versions and bounded version identifiers."}
	}
	now := time.Now().UTC()
	number := p.next
	if e = iamExactlyOne(tx.ExecContext(ctx, `INSERT INTO aws_iam_managed_policy_versions(policy_name,policy_id,version_number,document,created) VALUES(?,?,?,?,?)`, p.PolicyName, p.PolicyID, number, document, now.Format(time.RFC3339Nano))); e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	def := p.defaultNumber
	if setDefault {
		def = number
	}
	if e = iamExactlyOne(tx.ExecContext(ctx, `UPDATE aws_iam_managed_policies SET next_version=?,default_version=?,updated=? WHERE name=? AND policy_id=? AND deleted=0`, number+1, def, now.Format(time.RFC3339Nano), p.PolicyName, p.PolicyID)); e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	if setDefault {
		if e = iamManagedSyncAttached(ctx, tx, p); e != nil {
			return IAMManagedPolicyVersion{}, e
		}
	}
	if _, e = iamManagedRead(ctx, tx, arn); e != nil {
		return IAMManagedPolicyVersion{}, e
	}
	return IAMManagedPolicyVersion{VersionID: "v" + strconv.FormatInt(number, 10), IsDefaultVersion: setDefault, CreateDate: now, Document: document}, tx.Commit()
}
func (s *IAMRoleStore) SetDefaultManagedPolicyVersion(ctx context.Context, arn, id string) error {
	n, e := iamManagedVersionNumber(id)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := iamManagedRead(ctx, tx, arn)
	if e != nil {
		return e
	}
	found := false
	for _, v := range p.versions {
		found = found || v.VersionID == id
	}
	if !found {
		return ssmFailure{"NoSuchEntity", "The policy version does not exist."}
	}
	if e = iamExactlyOne(tx.ExecContext(ctx, `UPDATE aws_iam_managed_policies SET default_version=?,updated=? WHERE name=? AND policy_id=? AND deleted=0`, n, time.Now().UTC().Format(time.RFC3339Nano), p.PolicyName, p.PolicyID)); e != nil {
		return e
	}
	if e = iamManagedSyncAttached(ctx, tx, p); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *IAMRoleStore) DeleteManagedPolicyVersion(ctx context.Context, arn, id string) error {
	n, e := iamManagedVersionNumber(id)
	if e != nil {
		return e
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := iamManagedRead(ctx, tx, arn)
	if e != nil {
		return e
	}
	if n == p.defaultNumber {
		return ssmFailure{"DeleteConflict", "The default policy version cannot be deleted."}
	}
	found := false
	for _, v := range p.versions {
		found = found || v.VersionID == id
	}
	if !found {
		return ssmFailure{"NoSuchEntity", "The policy version does not exist."}
	}
	if e = iamExactlyOne(tx.ExecContext(ctx, `DELETE FROM aws_iam_managed_policy_versions WHERE policy_name=? AND policy_id=? AND version_number=?`, p.PolicyName, p.PolicyID, n)); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *IAMRoleStore) DeleteManagedPolicy(ctx context.Context, arn string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	p, e := iamManagedRead(ctx, tx, arn)
	if e != nil {
		return e
	}
	if p.AttachmentCount != 0 || len(p.versions) != 1 {
		return ssmFailure{"DeleteConflict", "Detach all roles and remove nondefault policy versions before deleting the policy."}
	}
	if e = iamExactlyOne(tx.ExecContext(ctx, `DELETE FROM aws_iam_managed_policy_versions WHERE policy_name=? AND policy_id=? AND version_number=?`, p.PolicyName, p.PolicyID, p.defaultNumber)); e != nil {
		return e
	}
	if e = iamExactlyOne(tx.ExecContext(ctx, `UPDATE aws_iam_managed_policies SET deleted=1 WHERE name=? AND policy_id=? AND deleted=0`, p.PolicyName, p.PolicyID)); e != nil {
		return e
	}
	return tx.Commit()
}

// Reads and validates every attachment of the current role, not just one page.
func iamManagedRolePolicies(ctx context.Context, tx *sql.Tx, role IAMRole) ([]iamManagedRecord, error) {
	rows, e := tx.QueryContext(ctx, `SELECT role_id,policy_name,policy_id FROM aws_iam_role_managed_policies WHERE role_name=? ORDER BY policy_name COLLATE NOCASE`, role.RoleName)
	if e != nil {
		return nil, e
	}
	type ref struct{ roleID, name, id string }
	refs := []ref{}
	for rows.Next() {
		var r ref
		if e = rows.Scan(&r.roleID, &r.name, &r.id); e != nil {
			rows.Close()
			return nil, e
		}
		refs = append(refs, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if len(refs) > 20 {
		return nil, iamManagedBad()
	}
	result := []iamManagedRecord{}
	for _, ref := range refs {
		if ref.roleID != role.RoleID {
			return nil, iamManagedBad()
		}
		p, e := iamManagedRead(ctx, tx, iamManagedPrefix+ref.name)
		if e != nil {
			var failure ssmFailure
			if errors.As(e, &failure) {
				return nil, iamManagedBad()
			}
			return nil, e
		}
		if p.PolicyID != ref.id || p.PolicyName != ref.name {
			return nil, iamManagedBad()
		}
		result = append(result, p)
	}
	return result, nil
}
func (s *IAMRoleStore) AttachRolePolicy(ctx context.Context, roleName, arn string, expectedRoleID ...string) error {
	return s.iamManagedAttachment(ctx, roleName, arn, true, expectedRoleID)
}
func (s *IAMRoleStore) DetachRolePolicy(ctx context.Context, roleName, arn string, expectedRoleID ...string) error {
	return s.iamManagedAttachment(ctx, roleName, arn, false, expectedRoleID)
}

// Optional expectedRoleID is a trusted caller's preauthorized incarnation, not
// wire authority. Query callers must supply it; direct host callers may omit it.
func iamManagedExpectedRole(role IAMRole, expected []string) error {
	if len(expected) == 0 {
		return nil
	}
	if len(expected) != 1 || expected[0] == "" {
		return ssmFailure{"InvalidInput", "Use one nonempty expected role incarnation."}
	}
	if expected[0] != role.RoleID {
		return ssmFailure{"NoSuchEntity", "The preauthorized role incarnation no longer exists."}
	}
	return nil
}
func (s *IAMRoleStore) iamManagedAttachment(ctx context.Context, roleName, arn string, attach bool, expectedRoleID []string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	role, e := iamManagedRole(ctx, tx, roleName)
	if e != nil {
		return e
	}
	if e = iamManagedExpectedRole(role, expectedRoleID); e != nil {
		return e
	}
	p, e := iamManagedRead(ctx, tx, arn)
	if e != nil {
		return e
	}
	all, e := iamManagedRolePolicies(ctx, tx, role)
	if e != nil {
		return e
	}
	exists := false
	for _, v := range all {
		exists = exists || v.PolicyID == p.PolicyID
	}
	// Valid-parent duplicate attach / absent detach are deliberate local no-ops.
	if exists == attach {
		return tx.Commit()
	}
	if attach {
		var total int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_iam_role_managed_policies`).Scan(&total); e != nil {
			return e
		}
		if len(all) >= 20 || total >= 20000 {
			return ssmFailure{"LimitExceeded", "Local roles support20 managed policies and20000 total attachments."}
		}
		e = iamExactlyOne(tx.ExecContext(ctx, `INSERT INTO aws_iam_role_managed_policies(role_name,role_id,policy_name,policy_id) VALUES(?,?,?,?)`, role.RoleName, role.RoleID, p.PolicyName, p.PolicyID))
	} else {
		e = iamExactlyOne(tx.ExecContext(ctx, `DELETE FROM aws_iam_role_managed_policies WHERE role_name=? AND role_id=? AND policy_name=? AND policy_id=?`, role.RoleName, role.RoleID, p.PolicyName, p.PolicyID))
	}
	if e != nil {
		return e
	}
	if e = iamSyncPolicy(ctx, tx, role.RoleName); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *IAMRoleStore) AttachedRolePolicies(ctx context.Context, roleName, afterName string, limit int, expectedRoleID ...string) (IAMRole, []IAMAttachedPolicy, error) {
	if limit < 1 || limit > 1001 || afterName != "" && !iamManagedName.MatchString(afterName) {
		return IAMRole{}, nil, ssmFailure{"InvalidInput", "Use a bounded attached-policy page."}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return IAMRole{}, nil, e
	}
	defer tx.Rollback()
	role, e := iamManagedRole(ctx, tx, roleName)
	if e != nil {
		return IAMRole{}, nil, e
	}
	if e = iamManagedExpectedRole(role, expectedRoleID); e != nil {
		return IAMRole{}, nil, e
	}
	all, e := iamManagedRolePolicies(ctx, tx, role)
	if e != nil {
		return IAMRole{}, nil, e
	}
	list := []IAMAttachedPolicy{}
	for _, p := range all {
		if strings.ToLower(p.PolicyName) > strings.ToLower(afterName) && len(list) < limit {
			list = append(list, IAMAttachedPolicy{PolicyName: p.PolicyName, PolicyArn: p.Arn})
		}
	}
	return role, list, tx.Commit()
}
