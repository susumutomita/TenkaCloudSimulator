package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const snsNotificationService = "codestar-notifications.amazonaws.com"

func snsPolicyFields(object map[string]any, fields ...string) bool {
	for name := range object {
		if !slices.Contains(fields, name) {
			return false
		}
	}
	return true
}

// IAM accepts a scalar or a list for these fields. In this subset a list
// must have exactly one member, so no other authority can be discarded.
func snsPolicyString(value any) string {
	if list, ok := value.([]any); ok && len(list) == 1 {
		value = list[0]
	}
	s, _ := value.(string)
	return s
}

func snsPolicyID(object map[string]any, field string) (string, bool) {
	value, present := object[field]
	if !present {
		return "", true
	}
	s, ok := value.(string)
	return s, ok && len(s) > 0 && len(s) <= 128 && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// Only evaluate the official notification service's publish statement for
// this exact topic. Every accepted statement applies to the same request.
func snsPolicyDecision(policy, arn string) (bool, error) {
	invalid := ssmFailure{"InvalidParameterException", "Policy must contain only supported notification service statements for this topic."}
	if len(policy) == 0 || len(policy) > 8192 || !kmsUnicode([]byte(policy)) {
		return false, invalid
	}
	if err := kmsJSONValue(json.NewDecoder(strings.NewReader(policy)), 0); err != nil {
		return false, invalid
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(policy), &doc); err != nil || !snsPolicyFields(doc, "Version", "Id", "Statement") {
		return false, invalid
	}
	version, _ := doc["Version"].(string)
	if version != "2008-10-17" && version != "2012-10-17" {
		return false, invalid
	}
	if _, ok := snsPolicyID(doc, "Id"); !ok {
		return false, invalid
	}
	statements, ok := doc["Statement"].([]any)
	if !ok {
		statements = []any{doc["Statement"]}
	}
	if len(statements) < 1 || len(statements) > 8 {
		return false, invalid
	}
	seen := map[string]bool{}
	denied := false
	for _, value := range statements {
		st, ok := value.(map[string]any)
		if !ok || !snsPolicyFields(st, "Sid", "Effect", "Principal", "Action", "Resource") {
			return false, invalid
		}
		sid, ok := snsPolicyID(st, "Sid")
		if !ok || sid != "" && seen[sid] {
			return false, invalid
		}
		seen[sid] = true
		principal, ok := st["Principal"].(map[string]any)
		action := snsPolicyString(st["Action"])
		if !ok || len(principal) != 1 || snsPolicyString(principal["Service"]) != snsNotificationService ||
			len(action) != len("SNS:Publish") || !strings.EqualFold(action, "SNS:Publish") || snsPolicyString(st["Resource"]) != arn {
			return false, invalid
		}
		switch st["Effect"] {
		case "Allow":
		case "Deny":
			denied = true
		default:
			return false, invalid
		}
	}
	return !denied, nil
}

func snsStandardNotificationTopic(tx *sql.Tx, arn string) error {
	fifo, err := snsRequireTopic(tx, arn)
	if err == nil && fifo {
		return ssmFailure{"InvalidParameterException", "Notification policies require a Standard topic."}
	}
	return err
}

// A sparse policy row identifies a physical topic lifetime to the notification
// worker. Replacing a policy retains the row, while deleting a topic retires it.
func snsNotificationTopicGrant(ctx context.Context, tx *sql.Tx, arn string) (int64, bool, error) {
	if err := snsStandardNotificationTopic(tx, arn); err != nil {
		return 0, false, err
	}
	var generation int64
	var policy string
	err := tx.QueryRowContext(ctx, "SELECT generation,policy FROM aws_sns_notification_policies WHERE topic_arn=?", arn).Scan(&generation, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	allowed, err := snsPolicyDecision(policy, arn)
	if err != nil || generation < 1 {
		return 0, false, errors.New("invalid persisted SNS notification policy")
	}
	return generation, allowed, nil
}

func (s *SNSStore) setNotificationPolicy(ctx context.Context, arn, policy string) error {
	if _, err := snsPolicyDecision(policy, arn); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = snsStandardNotificationTopic(tx, arn); err != nil {
		return err
	}
	// Update in place, including identical writes. An UPSERT attempted on
	// every edit would consume a new SQLite AUTOINCREMENT sequence value.
	var generation int64
	err = tx.QueryRowContext(ctx, "SELECT generation FROM aws_sns_notification_policies WHERE topic_arn=?", arn).Scan(&generation)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = tx.QueryRowContext(ctx, "INSERT INTO aws_sns_notification_policies(topic_arn,policy) VALUES(?,?) RETURNING generation", arn, policy).Scan(&generation)
	case err == nil:
		result, e := tx.ExecContext(ctx, "UPDATE aws_sns_notification_policies SET policy=? WHERE generation=? AND topic_arn=?", policy, generation, arn)
		err = snsAffected(result, e, 1)
	}
	if err != nil {
		return err
	}
	if err = snsCount(tx, 1, `SELECT count(*) FROM aws_sns_notification_policies p JOIN aws_sns_topics t ON t.arn=p.topic_arn
		WHERE t.fifo=0 AND p.topic_arn=? AND p.generation=? AND p.policy=?`, arn, generation, policy); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SNSStore) topicAttributes(ctx context.Context, region, arn string) ([]snsAttributeEntry, error) {
	var display string
	var fifo bool
	var subs int
	var policy sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT t.display_name,t.fifo,
		(SELECT count(*) FROM aws_sns_subscriptions s WHERE s.topic_arn=t.arn),p.policy
		FROM aws_sns_topics t LEFT JOIN aws_sns_notification_policies p ON t.arn=p.topic_arn
		WHERE t.arn=? AND t.region=?`, arn, region).Scan(&display, &fifo, &subs, &policy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ssmFailure{"NotFoundException", "Topic does not exist in this region."}
	}
	if err != nil {
		return nil, err
	}
	entries := []snsAttributeEntry{
		{"TopicArn", arn}, {"Owner", "000000000000"}, {"DisplayName", display},
		{"SubscriptionsConfirmed", strconv.Itoa(subs)}, {"SubscriptionsPending", "0"}, {"SubscriptionsDeleted", "0"},
	}
	if fifo {
		entries = append(entries, snsAttributeEntry{"FifoTopic", "true"})
	}
	if policy.Valid {
		if _, err = snsPolicyDecision(policy.String, arn); err != nil || fifo {
			return nil, errors.New("invalid persisted SNS notification policy")
		}
		entries = append(entries, snsAttributeEntry{"Policy", policy.String})
	}
	return entries, nil
}
