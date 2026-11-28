package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// A launch template is saved configuration, not an allocated EC2 instance.
// This bounded provider stores one immutable version. PCS must independently
// validate its subnet and instance profile when consuming this configuration.
type ec2LaunchTemplate struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Region         string   `json:"region"`
	Creator        string   `json:"creator"`
	Created        string   `json:"created"`
	Description    string   `json:"description"`
	SecurityGroups []string `json:"securityGroups"`
	VPCID          string   `json:"vpcId"`
}

func initEC2LaunchTemplates(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS aws_ec2_launch_templates(region TEXT NOT NULL,id TEXT NOT NULL,name TEXT NOT NULL,document TEXT NOT NULL,retired INTEGER NOT NULL CHECK(retired IN (0,1)),PRIMARY KEY(region,id)) WITHOUT ROWID`,
		`CREATE UNIQUE INDEX IF NOT EXISTS aws_ec2_launch_template_names ON aws_ec2_launch_templates(region,name) WHERE retired=0`,
		`CREATE TABLE IF NOT EXISTS aws_ec2_launch_template_tokens(region TEXT NOT NULL,principal TEXT NOT NULL,token TEXT NOT NULL,fingerprint TEXT NOT NULL,template_id TEXT NOT NULL,PRIMARY KEY(region,principal,token),FOREIGN KEY(region,template_id) REFERENCES aws_ec2_launch_templates(region,id) ON DELETE RESTRICT) WITHOUT ROWID`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func readEC2LaunchTemplate(ctx context.Context, tx *sql.Tx, region, id string, historical bool) (ec2LaunchTemplate, error) {
	var out ec2LaunchTemplate
	var raw, name string
	var retired int
	err := tx.QueryRowContext(ctx, `SELECT document,name,retired FROM aws_ec2_launch_templates WHERE region=? AND id=?`, region, id).Scan(&raw, &name, &retired)
	if err != nil {
		return out, err
	}
	if json.Unmarshal([]byte(raw), &out) != nil || out.ID != id || out.Region != region || out.Name != name || out.Creator == "" || !ec2LaunchTemplateID.MatchString(id) || !ec2LaunchTemplateName.MatchString(name) || !ec2VPCIDPattern.MatchString(out.VPCID) || len(out.SecurityGroups) < 1 || len(out.SecurityGroups) > 5 || (retired != 0 && retired != 1) {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template identity invariant failed")
	}
	created, e := time.Parse(time.RFC3339Nano, out.Created)
	if e != nil || created.IsZero() {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template time invariant failed")
	}
	seen := map[string]bool{}
	for _, group := range out.SecurityGroups {
		if !ec2NativeSecurityGroupID.MatchString(group) || seen[group] {
			return ec2LaunchTemplate{}, fmt.Errorf("launch template group invariant failed")
		}
		seen[group] = true
	}
	if len(out.Description) > 255 {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template description invariant failed")
	}
	for _, r := range out.Description {
		if r < 32 || r > 126 {
			return ec2LaunchTemplate{}, fmt.Errorf("launch template description invariant failed")
		}
	}
	if retired == 1 && !historical {
		return ec2LaunchTemplate{}, sql.ErrNoRows
	}
	return out, nil
}

// Current admission is deliberately separate from historical Describe. It is
// called inside the consuming transaction, with no second policy-store read.
func validateEC2LaunchTemplateGroups(ctx context.Context, tx *sql.Tx, template ec2LaunchTemplate) error {
	seen := map[string]bool{}
	for _, id := range template.SecurityGroups {
		if seen[id] {
			return fmt.Errorf("duplicate template security group")
		}
		seen[id] = true
		group, err := readEC2NativeSecurityGroup(ctx, tx, template.Region, id)
		if err != nil {
			return err
		}
		if group.StackID != ec2NativeOwner || group.VPCID != template.VPCID {
			return ec2Invalid("InvalidParameterValue", "Template security groups must belong to one native VPC.")
		}
		if _, err = checkEC2NativeSGParent(ctx, tx, template.Region, group.VPCID); err != nil {
			return err
		}
	}
	return nil
}

// Admission resolves the active physical template and its dependencies in the
// caller's one transaction. A historical receipt value cannot restore authority.
func admitEC2LaunchTemplate(ctx context.Context, tx *sql.Tx, region, id string) (ec2LaunchTemplate, error) {
	saved, err := readEC2LaunchTemplate(ctx, tx, region, id, false)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	if err = validateEC2LaunchTemplateGroups(ctx, tx, saved); err != nil {
		return ec2LaunchTemplate{}, err
	}
	return saved, nil
}

func launchTemplateChanges(ctx context.Context, tx *sql.Tx) (int64, error) {
	var n int64
	err := tx.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&n)
	return n, err
}

// The retained row is also the immutable create receipt after retirement.
// Replaying a token can therefore never resurrect an old physical resource.
func (s *StackStore) createEC2LaunchTemplate(ctx context.Context, candidate ec2LaunchTemplate, token, fingerprint string, authorize func(string) error) (ec2LaunchTemplate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	defer tx.Rollback()
	if len(candidate.SecurityGroups) < 1 || len(candidate.SecurityGroups) > 5 || !ec2LaunchTemplateName.MatchString(candidate.Name) || candidate.Creator == "" {
		return ec2LaunchTemplate{}, ec2Invalid("InvalidParameterValue", "Invalid bounded launch-template configuration.")
	}
	if token != "" {
		var prior, id string
		err = tx.QueryRowContext(ctx, `SELECT fingerprint,template_id FROM aws_ec2_launch_template_tokens WHERE region=? AND principal=? AND token=?`, candidate.Region, candidate.Creator, token).Scan(&prior, &id)
		if err == nil {
			if !ec2LaunchTemplateID.MatchString(id) {
				return ec2LaunchTemplate{}, fmt.Errorf("launch template replay identity invariant failed")
			}
			if e := authorize(id); e != nil {
				return ec2LaunchTemplate{}, e
			}
			if prior != fingerprint {
				return ec2LaunchTemplate{}, ec2Invalid("IdempotentParameterMismatch", "The token belongs to another request.")
			}
			saved, e := readEC2LaunchTemplate(ctx, tx, candidate.Region, id, true)
			if e != nil {
				return ec2LaunchTemplate{}, e
			}
			if saved.Name != candidate.Name || saved.Creator != candidate.Creator || saved.Description != candidate.Description || !reflect.DeepEqual(saved.SecurityGroups, candidate.SecurityGroups) {
				return ec2LaunchTemplate{}, fmt.Errorf("launch template replay receipt differs")
			}
			if e = tx.Commit(); e != nil {
				return ec2LaunchTemplate{}, e
			}
			return saved, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return ec2LaunchTemplate{}, err
		}
	}
	rawID := make([]byte, 9)
	if _, err = rand.Read(rawID); err != nil {
		return ec2LaunchTemplate{}, err
	}
	candidate.ID = "lt-" + hex.EncodeToString(rawID)[:17]
	if err = authorize(candidate.ID); err != nil {
		return ec2LaunchTemplate{}, err
	}
	var active, retained int
	if err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN retired=0 THEN 1 ELSE 0 END),0) FROM aws_ec2_launch_templates WHERE region=?`, candidate.Region).Scan(&retained, &active); err != nil {
		return ec2LaunchTemplate{}, err
	}
	if active >= 20 || retained >= 1000 {
		return ec2LaunchTemplate{}, ec2Invalid("ResourceLimitExceeded", "The local launch-template bound was reached.")
	}
	var duplicate int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM aws_ec2_launch_templates WHERE region=? AND name=? AND retired=0`, candidate.Region, candidate.Name).Scan(&duplicate); err != nil {
		return ec2LaunchTemplate{}, err
	}
	if duplicate != 0 {
		return ec2LaunchTemplate{}, ec2Invalid("InvalidLaunchTemplateName.AlreadyExistsException", "The launch-template name exists.")
	}
	group, err := readEC2NativeSecurityGroup(ctx, tx, candidate.Region, candidate.SecurityGroups[0])
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	candidate.VPCID = group.VPCID
	if err = validateEC2LaunchTemplateGroups(ctx, tx, candidate); err != nil {
		return ec2LaunchTemplate{}, err
	}
	candidate.Created = time.Now().UTC().Format(time.RFC3339Nano)
	raw, err := json.Marshal(candidate)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	before, err := launchTemplateChanges(ctx, tx)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO aws_ec2_launch_templates VALUES(?,?,?,?,0)`, candidate.Region, candidate.ID, candidate.Name, string(raw))
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template was not inserted exactly once")
	}
	expected := int64(1)
	if token != "" {
		result, err = tx.ExecContext(ctx, `INSERT INTO aws_ec2_launch_template_tokens VALUES(?,?,?,?,?)`, candidate.Region, candidate.Creator, token, fingerprint, candidate.ID)
		if err != nil {
			return ec2LaunchTemplate{}, err
		}
		n, err = result.RowsAffected()
		if err != nil || n != 1 {
			return ec2LaunchTemplate{}, fmt.Errorf("launch template token was not inserted exactly once")
		}
		var gotFingerprint, gotID string
		if err = tx.QueryRowContext(ctx, `SELECT fingerprint,template_id FROM aws_ec2_launch_template_tokens WHERE region=? AND principal=? AND token=?`, candidate.Region, candidate.Creator, token).Scan(&gotFingerprint, &gotID); err != nil {
			return ec2LaunchTemplate{}, err
		}
		if gotFingerprint != fingerprint || gotID != candidate.ID {
			return ec2LaunchTemplate{}, fmt.Errorf("launch template token readback differs")
		}
		expected++
	}
	saved, err := readEC2LaunchTemplate(ctx, tx, candidate.Region, candidate.ID, false)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	after, err := launchTemplateChanges(ctx, tx)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	if after-before != expected || !reflect.DeepEqual(saved, candidate) {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template write frame differs")
	}
	if err = tx.Commit(); err != nil {
		return ec2LaunchTemplate{}, err
	}
	return saved, nil
}

func (s *StackStore) selectEC2LaunchTemplates(ctx context.Context, region string, ids, names []string) ([]ec2LaunchTemplate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	query := `SELECT id FROM aws_ec2_launch_templates WHERE region=? AND retired=0`
	args := []any{region}
	selectors, column := ids, "id"
	if len(names) > 0 {
		selectors, column = names, "name"
	}
	if len(selectors) > 0 {
		query += ` AND ` + column + ` IN (` + strings.TrimSuffix(strings.Repeat("?,", len(selectors)), ",") + `)`
		for _, value := range selectors {
			args = append(args, value)
		}
	}
	query += ` ORDER BY name,id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	var allIDs []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		allIDs = append(allIDs, id)
		if len(allIDs) > 20 {
			rows.Close()
			return nil, fmt.Errorf("active launch template bound exceeded")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []ec2LaunchTemplate{}
	matched := map[string]bool{}
	for _, id := range allIDs {
		saved, e := readEC2LaunchTemplate(ctx, tx, region, id, false)
		if e != nil {
			return nil, e
		}
		wanted := len(ids) == 0 && len(names) == 0
		for _, v := range ids {
			if saved.ID == v {
				wanted = true
				matched[v] = true
			}
		}
		for _, v := range names {
			if saved.Name == v {
				wanted = true
				matched[v] = true
			}
		}
		if wanted {
			out = append(out, saved)
		}
	}
	for _, id := range ids {
		if !matched[id] {
			return nil, ec2Invalid("InvalidLaunchTemplateId.NotFound", "The launch template does not exist in this region.")
		}
	}
	for _, name := range names {
		if !matched[name] {
			return nil, ec2Invalid("InvalidLaunchTemplateName.NotFoundException", "The launch template does not exist in this region.")
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *StackStore) deleteEC2LaunchTemplate(ctx context.Context, region, id, name string, authorize func(string) error) (ec2LaunchTemplate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	defer tx.Rollback()
	if name != "" {
		err = tx.QueryRowContext(ctx, `SELECT id FROM aws_ec2_launch_templates WHERE region=? AND name=? AND retired=0`, region, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			if e := authorize("*"); e != nil {
				return ec2LaunchTemplate{}, e
			}
			return ec2LaunchTemplate{}, ec2Invalid("InvalidLaunchTemplateName.NotFoundException", "The launch template does not exist in this region.")
		}
		if err != nil {
			return ec2LaunchTemplate{}, err
		}
	}
	if err = authorize(id); err != nil {
		return ec2LaunchTemplate{}, err
	}
	saved, err := readEC2LaunchTemplate(ctx, tx, region, id, false)
	if errors.Is(err, sql.ErrNoRows) {
		return ec2LaunchTemplate{}, ec2Invalid("InvalidLaunchTemplateId.NotFound", "The launch template does not exist in this region.")
	}
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	before, err := launchTemplateChanges(ctx, tx)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE aws_ec2_launch_templates SET retired=1 WHERE region=? AND id=? AND retired=0`, region, id)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template was not retired exactly once")
	}
	actual, err := readEC2LaunchTemplate(ctx, tx, region, id, true)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	var retired int
	if err = tx.QueryRowContext(ctx, `SELECT retired FROM aws_ec2_launch_templates WHERE region=? AND id=?`, region, id).Scan(&retired); err != nil {
		return ec2LaunchTemplate{}, err
	}
	after, err := launchTemplateChanges(ctx, tx)
	if err != nil {
		return ec2LaunchTemplate{}, err
	}
	if retired != 1 || after-before != 1 || !reflect.DeepEqual(actual, saved) {
		return ec2LaunchTemplate{}, fmt.Errorf("launch template retirement frame differs")
	}
	if err = tx.Commit(); err != nil {
		return ec2LaunchTemplate{}, err
	}
	return saved, nil
}
