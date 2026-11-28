package codestarnotifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Publish executes the native SNS publication and its SQS fanout in tx.
// The caller keeps ownership of commit and records the durable delivery ID.
type Publish func(ctx context.Context, tx *sql.Tx, topic, body string, allowQueue func(string) error) (string, error)

type delivery struct {
	seq int64
	pendingTarget
	event, body, topology, region, owner string
	enabled                              bool
	currentRule                          int64
}

// The sparse owner binding is lost when the original principal is deleted.
// Recreating a principal with an identical name cannot revive an old grant.
func ownerPolicy(ctx context.Context, tx *sql.Tx, rule, owner string) (Decision, bool, error) {
	if owner == core.Administrator {
		return func(string, string) string { return "allowed" }, true, nil
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT p.document FROM aws_notification_owners o JOIN identity_policies p ON p.name=o.principal
		WHERE o.rule_arn=? AND o.principal=?`, rule, owner).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	p, err := core.ParsePolicy(raw)
	if err != nil {
		return nil, false, err
	}
	return func(action, resource string) string { return core.Evaluate(p, action, resource) }, true, nil
}

// permittedQueues evaluates every physical destination against one captured
// creator policy. An empty result means the target cannot currently publish.
func permittedQueues(decide Decision, topology string) (map[string]bool, error) {
	var recipients []destination
	if err := json.Unmarshal([]byte(topology), &recipients); err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(recipients))
	for _, recipient := range recipients {
		if decide("sqs:SendMessage", recipient.Queue) != "allowed" {
			return nil, nil
		}
		allowed[recipient.Queue] = true
	}
	return allowed, nil
}

// DeliverOne makes one eligible target attempt. A failed publish rolls back
// SNS and every SQS insertion before the retry delay is persisted. The worker
// never executes a Git operation. Returns false when there is no eligible row.
func (s *Store) DeliverOne(ctx context.Context, publish Publish) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	var d delivery
	err = tx.QueryRowContext(ctx, `SELECT o.seq,o.rule_arn,o.rule_generation,o.topic_arn,o.topic_generation,o.event_id,o.envelope,o.topology,
		r.region,r.owner,r.enabled,r.generation FROM aws_notification_outbox o JOIN aws_notification_rules r ON r.arn=o.rule_arn
		WHERE o.retry_ms<=? ORDER BY o.retry_ms,o.seq LIMIT 1`, now.UnixMilli()).
		Scan(&d.seq, &d.Rule, &d.RuleGeneration, &d.Topic, &d.TopicGeneration, &d.event, &d.body, &d.topology, &d.region, &d.owner, &d.enabled, &d.currentRule)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	finish := func() (bool, error) {
		if err := checkedExec(ctx, tx, 1, "DELETE FROM aws_notification_outbox WHERE seq=?", d.seq); err != nil {
			return false, err
		}
		if err := requireCount(ctx, tx, 0, "SELECT count(*) FROM aws_notification_outbox WHERE seq=? OR (rule_arn=? AND topic_arn=? AND event_id=?)", d.seq, d.Rule, d.Topic, d.event); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	retry := func() (bool, error) {
		if err := checkedExec(ctx, tx, 1, "UPDATE aws_notification_outbox SET retry_ms=? WHERE seq=?", now.Add(2*time.Second).UnixMilli(), d.seq); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if !d.enabled || d.currentRule != d.RuleGeneration || d.topology == "" || d.topology == "[]" {
		return finish()
	}
	generation, granted, err := s.grant(ctx, tx, d.Topic)
	if err != nil {
		return false, err
	}
	if generation != 0 && generation != d.TopicGeneration {
		return finish()
	}
	if !granted {
		return retry()
	}
	current, err := destinations(ctx, tx, d.region, d.Topic, false)
	if err != nil {
		return false, err
	}
	if current != d.topology {
		return finish()
	}
	decide, exists, err := ownerPolicy(ctx, tx, d.Rule, d.owner)
	if err != nil {
		return false, err
	}
	if !exists {
		return finish()
	}
	allowed, err := permittedQueues(decide, current)
	if err != nil {
		return false, err
	}
	if len(allowed) == 0 {
		return retry()
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT notification_publish"); err != nil {
		return false, err
	}
	id, err := publish(ctx, tx, d.Topic, d.body, func(arn string) error {
		if !allowed[arn] {
			return errors.New("delivery does not include this queue")
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if _, err = tx.ExecContext(ctx, "ROLLBACK TO notification_publish"); err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, "RELEASE notification_publish"); err != nil {
			return false, err
		}
		return retry()
	}
	if id == "" {
		return false, errors.New("missing native SNS publication receipt")
	}
	if err = checkedExec(ctx, tx, 1, `INSERT INTO aws_notification_deliveries(rule_arn,topic_arn,event_id,message_id)
		SELECT ?,?,?,id FROM aws_sns_messages WHERE id=? AND topic_arn=? AND message=?`, d.Rule, d.Topic, d.event, id, d.Topic, d.body); err != nil {
		return false, err
	}
	if err = requireCount(ctx, tx, 1, "SELECT count(*) FROM aws_notification_deliveries WHERE rule_arn=? AND topic_arn=? AND event_id=? AND message_id=?", d.Rule, d.Topic, d.event, id); err != nil {
		return false, err
	}
	return finish()
}

// RunWorker periodically drains the team's persistent backlog, including
// rows written before restart. Shutdown cancels the current transaction.
func (s *Store) RunWorker(ctx context.Context, publish Publish) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		for range 32 {
			if ok, err := s.DeliverOne(ctx, publish); err != nil || !ok {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
