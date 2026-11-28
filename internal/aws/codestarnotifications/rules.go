package codestarnotifications

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func (s *Store) Run(ctx context.Context, operation, region, actor string, in Input, decide Decision) (map[string]any, error) {
	if decide == nil || actor == "" {
		return nil, denied()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var result map[string]any
	switch operation {
	case "CreateNotificationRule":
		result, err = s.create(ctx, tx, region, actor, in, decide)
	case "ListNotificationRules":
		if decide("codestar-notifications:"+operation, "*") != "allowed" {
			return nil, denied()
		}
		result, err = s.list(ctx, tx, region, actor, in)
	case "DescribeNotificationRule", "UpdateNotificationRule", "DeleteNotificationRule":
		if !validRuleARN(in.ARN, region) {
			return nil, invalid("Specify a notification rule ARN in this account and region.")
		}
		if decide("codestar-notifications:"+operation, in.ARN) != "allowed" {
			return nil, denied()
		}
		r, e := selectRule(ctx, tx, region, in.ARN)
		if e != nil {
			var f Fault
			if operation == "DeleteNotificationRule" && errors.As(e, &f) && f.Code == "ResourceNotFoundException" {
				return nil, invalid("The selected local notification rule does not exist.")
			}
			return nil, e
		}
		switch operation {
		case "DescribeNotificationRule":
			result, err = s.describe(ctx, tx, r)
		case "UpdateNotificationRule":
			result, err = s.update(ctx, tx, r, in, decide)
		case "DeleteNotificationRule":
			err = checkedExec(ctx, tx, 1, "DELETE FROM aws_notification_rules WHERE arn=? AND generation=?", r.ARN, r.Generation)
			if err == nil {
				var n int
				err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM aws_notification_rules WHERE arn=?", r.ARN).Scan(&n)
				if err == nil && n != 0 {
					err = errors.New("notification rule survived deletion")
				}
			}
			result = map[string]any{"Arn": r.ARN}
		}
	default:
		return nil, Fault{"UnknownOperationException", "This notification operation is unsupported."}
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func normalizeFingerprint(in Input) string {
	enabled := in.Status == nil || *in.Status == "ENABLED"
	targets := make([]string, 0, len(*in.Targets))
	for _, t := range *in.Targets {
		targets = append(targets, t.Address)
	}
	slices.Sort(targets)
	b, _ := json.Marshal([]any{*in.Name, in.Source, normalizedEvents(*in.Events), targets, *in.DetailType, enabled})
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

func (s *Store) create(ctx context.Context, tx *sql.Tx, region, actor string, in Input, decide Decision) (map[string]any, error) {
	if in.Name == nil || in.Events == nil || in.DetailType == nil || in.Targets == nil || in.Source == "" {
		return nil, invalid("Name, EventTypeIds, Resource, Targets and DetailType are required.")
	}
	if err := validateChanges(in); err != nil {
		return nil, err
	}
	if in.Token != "" && !validToken.MatchString(in.Token) {
		return nil, invalid("Invalid client request token.")
	}
	arn := newARN(region)
	fingerprint := normalizeFingerprint(in)
	var savedARN, savedFingerprint string
	var token any
	if in.Token != "" {
		token = in.Token
		err := tx.QueryRowContext(ctx, "SELECT arn,fingerprint FROM aws_notification_rules WHERE region=? AND owner=? AND token=?", region, actor, in.Token).Scan(&savedARN, &savedFingerprint)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if savedARN != "" {
			arn = savedARN
		}
	}
	if decide("codestar-notifications:CreateNotificationRule", arn) != "allowed" || decide("codecommit:GetRepository", in.Source) != "allowed" {
		return nil, denied()
	}
	if savedARN != "" {
		if fingerprint != savedFingerprint {
			return nil, invalid("The client request token was already used with different parameters.")
		}
		return map[string]any{"Arn": savedARN}, nil
	}
	if !strings.HasPrefix(in.Source, prefix("codecommit", region)) {
		return nil, invalid("Select a local CodeCommit repository in the same region.")
	}
	var repoID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM aws_catalog_resources
		WHERE service='codecommit' AND kind='repository' AND region=? AND parent='' AND arn=?`, region, in.Source).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Fault{"ConfigurationException", "The specified local CodeCommit repository does not exist."}
	}
	if err != nil {
		return nil, err
	}
	if err = availableName(ctx, tx, region, *in.Name, ""); err != nil {
		return nil, err
	}
	var total, sourceTotal int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*), COUNT(*) FILTER(WHERE source=?) FROM aws_notification_rules", in.Source).Scan(&total, &sourceTotal)
	if err != nil {
		return nil, err
	}
	if total >= maxTeamRules || sourceTotal >= MaxRulesPerSource {
		return nil, Fault{"LimitExceededException", "The local notification rule limit has been reached."}
	}
	enabled := in.Status == nil || *in.Status == "ENABLED"
	now := s.now().UTC().UnixMilli()
	if err = checkedExec(ctx, tx, 1, `INSERT INTO aws_notification_rules
		(arn,region,name,owner,source,repository_id,created_ms,updated_ms,generation,enabled,events,token,fingerprint)
		VALUES(?,?,?,?,?,?,?,?,1,?,?,?,?)`, arn, region, *in.Name, actor, in.Source, repoID, now, now, enabled, normalizedEvents(*in.Events), token, fingerprint); err != nil {
		return nil, err
	}
	if actor != core.Administrator {
		if err := checkedExec(ctx, tx, 1, "INSERT INTO aws_notification_owners(rule_arn,principal) VALUES(?,?)", arn, actor); err != nil {
			return nil, err
		}
	}
	if err = s.replaceTargets(ctx, tx, region, arn, *in.Targets); err != nil {
		return nil, err
	}
	return map[string]any{"Arn": arn}, nil
}

func availableName(ctx context.Context, tx *sql.Tx, region, name, oldARN string) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_notification_rules WHERE region=? AND name=? AND arn<>?", region, name, oldARN).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		if oldARN != "" {
			return invalid("A notification rule already uses this name.")
		}
		return Fault{"ResourceAlreadyExistsException", "A notification rule already uses this name."}
	}
	return nil
}

func (s *Store) replaceTargets(ctx context.Context, tx *sql.Tx, region, rule string, targets []Target) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM aws_notification_targets WHERE rule_arn=?", rule); err != nil {
		return err
	}
	for _, target := range targets {
		if !strings.HasPrefix(target.Address, prefix("sns", region)) {
			return invalid("Select a local SNS topic in the source region.")
		}
		generation, allowed, err := s.grant(ctx, tx, target.Address)
		if err != nil {
			return err
		}
		if !allowed || generation <= 0 {
			return Fault{"ConfigurationException", "The target must allow the CodeStar Notifications service to publish."}
		}
		topology, err := destinations(ctx, tx, region, target.Address, true)
		if err != nil {
			return err
		}
		if topology == "" || topology == "[]" {
			return Fault{"ConfigurationException", "Select a Standard SNS topic with one to twenty local Standard SQS subscriptions."}
		}
		if err := checkedExec(ctx, tx, 1, "INSERT INTO aws_notification_targets(rule_arn,topic_arn,generation) VALUES(?,?,?)", rule, target.Address, generation); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) update(ctx context.Context, tx *sql.Tx, r Rule, in Input, decide Decision) (map[string]any, error) {
	if err := validateChanges(in); err != nil {
		return nil, err
	}
	if decide("codecommit:GetRepository", r.Source) != "allowed" {
		return nil, denied()
	}
	if in.Name != nil {
		if err := availableName(ctx, tx, r.Region, *in.Name, r.ARN); err != nil {
			return nil, err
		}
		r.Name = *in.Name
	}
	if in.Status != nil {
		r.Enabled = *in.Status == "ENABLED"
	}
	if in.Events != nil {
		r.Events = *in.Events
	}
	if in.Targets != nil {
		if err := s.replaceTargets(ctx, tx, r.Region, r.ARN, *in.Targets); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM aws_notification_outbox WHERE rule_arn=?", r.ARN); err != nil {
		return nil, err
	}
	err := checkedExec(ctx, tx, 1, `UPDATE aws_notification_rules SET name=?,updated_ms=?,enabled=?,events=?,generation=generation+1 WHERE arn=? AND generation=?`,
		r.Name, s.now().UTC().UnixMilli(), r.Enabled, normalizedEvents(r.Events), r.ARN, r.Generation)
	return map[string]any{}, err
}

func (s *Store) describe(ctx context.Context, tx *sql.Tx, r Rule) (map[string]any, error) {
	status := "DISABLED"
	if r.Enabled {
		status = "ENABLED"
	}
	events := make([]map[string]string, 0, len(r.Events))
	for _, id := range r.Events {
		name := "Branches and tags: Created"
		if id == BranchUpdated {
			name = "Branches and tags: Updated"
		}
		events = append(events, map[string]string{"EventTypeId": id, "ServiceName": "CodeCommit", "ResourceType": "Repository", "EventTypeName": name})
	}
	type targetGeneration struct {
		arn        string
		generation int64
	}
	rows, err := tx.QueryContext(ctx, "SELECT topic_arn,generation FROM aws_notification_targets WHERE rule_arn=? ORDER BY topic_arn", r.ARN)
	if err != nil {
		return nil, err
	}
	var saved []targetGeneration
	for rows.Next() {
		var t targetGeneration
		if err := rows.Scan(&t.arn, &t.generation); err != nil {
			rows.Close()
			return nil, err
		}
		saved = append(saved, t)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	targets := make([]map[string]string, 0, len(saved))
	for _, t := range saved {
		generation, allowed, err := s.grant(ctx, tx, t.arn)
		if err != nil {
			return nil, err
		}
		ts := "UNREACHABLE"
		if allowed && generation == t.generation {
			topology, err := destinations(ctx, tx, r.Region, t.arn, false)
			if err != nil {
				return nil, err
			}
			if topology != "" && topology != "[]" {
				ts = "ACTIVE"
			}
		}
		targets = append(targets, map[string]string{"TargetType": "SNS", "TargetAddress": t.arn, "TargetStatus": ts})
	}
	return map[string]any{"Arn": r.ARN, "Name": r.Name, "Resource": r.Source, "Status": status, "CreatedBy": r.Owner,
		"CreatedTimestamp": float64(r.Created) / 1000, "LastModifiedTimestamp": float64(r.Updated) / 1000,
		"EventTypes": events, "DetailType": "BASIC", "Targets": targets}, nil
}
