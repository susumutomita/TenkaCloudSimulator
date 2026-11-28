package codestarnotifications

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/catalog"
)

const maxPendingTargets = 2048
const maxQueuesPerTarget = 20

type destination struct {
	Subscription string
	Queue        string
	Generation   int64
}

type pendingTarget struct {
	Rule            string
	RuleGeneration  int64
	Topic           string
	TopicGeneration int64
}

func messageID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// destinations freezes subscription IDs and physical queue row lifetimes.
// An existing SNS subscription can outlive a queue at the same ARN. The
// foreign key invalidates its old generation even for a same-clock recreation.
func destinations(ctx context.Context, tx *sql.Tx, region, topic string, assign bool) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT arn,protocol,endpoint FROM aws_sns_subscriptions
		WHERE topic_arn=? ORDER BY arn LIMIT ?`, topic, maxQueuesPerTarget+1)
	if err != nil {
		return "", err
	}
	type subscription struct{ arn, protocol, endpoint string }
	var subs []subscription
	for rows.Next() {
		var sub subscription
		if err := rows.Scan(&sub.arn, &sub.protocol, &sub.endpoint); err != nil {
			rows.Close()
			return "", err
		}
		subs = append(subs, sub)
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(subs) > maxQueuesPerTarget {
		return "", nil
	}
	captured := make([]destination, 0, len(subs))
	for _, sub := range subs {
		name, ok := strings.CutPrefix(sub.endpoint, prefix("sqs", region))
		if !ok || sub.protocol != "sqs" || name == "" || strings.HasSuffix(name, ".fifo") {
			return "", nil
		}
		if assign {
			if _, err := tx.ExecContext(ctx, `INSERT INTO aws_notification_queue_instances(region,queue)
			SELECT region,name FROM aws_sqs_queues q WHERE region=? AND name=?
			AND NOT EXISTS(SELECT 1 FROM aws_notification_queue_instances i WHERE i.region=q.region AND i.queue=q.name)`, region, name); err != nil {
				return "", err
			}
		}
		var gen int64
		err := tx.QueryRowContext(ctx, "SELECT generation FROM aws_notification_queue_instances WHERE region=? AND queue=?", region, name).Scan(&gen)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		// A queue deleted after this subscription was first observed invalidates
		// the binding even if its endpoint string and native subscription survive.
		if assign {
			if _, err = tx.ExecContext(ctx, `INSERT INTO aws_notification_subscriptions(subscription_arn,queue_generation)
				SELECT ?,? WHERE NOT EXISTS(SELECT 1 FROM aws_notification_subscriptions WHERE subscription_arn=?)`, sub.arn, gen, sub.arn); err != nil {
				return "", err
			}
		}
		var original int64
		err = tx.QueryRowContext(ctx, "SELECT queue_generation FROM aws_notification_subscriptions WHERE subscription_arn=?", sub.arn).Scan(&original)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && original != gen) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		captured = append(captured, destination{Subscription: sub.arn, Queue: sub.endpoint, Generation: gen})
	}
	b, err := json.Marshal(captured)
	return string(b), err
}

func (s *Store) RecordRef(ctx context.Context, tx *sql.Tx, c catalog.CodeCommitRefChange) error {
	event, eventName := BranchCreated, "referenceCreated"
	if c.OldCommit != "" {
		event, eventName = BranchUpdated, "referenceUpdated"
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.arn,r.generation,t.topic_arn,t.generation
		FROM aws_notification_rules r JOIN aws_notification_targets t ON r.arn=t.rule_arn
		WHERE r.enabled=1 AND r.region=? AND r.source=? AND r.repository_id=?
		AND instr(','||r.events||',', ','||?||',')>0 ORDER BY r.arn,t.topic_arn`, c.Region, c.RepositoryARN, c.RepositoryID, event)
	if err != nil {
		return err
	}
	var targets []pendingTarget
	for rows.Next() {
		var t pendingTarget
		if err := rows.Scan(&t.Rule, &t.RuleGeneration, &t.Topic, &t.TopicGeneration); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM aws_notification_outbox").Scan(&count); err != nil {
		return err
	}
	if count+len(targets) > maxPendingTargets {
		return catalog.ErrCodeCommitRefCapacity
	}
	detail := map[string]string{"event": eventName, "repositoryName": strings.TrimPrefix(c.RepositoryARN, prefix("codecommit", c.Region)),
		"repositoryId": c.RepositoryID, "referenceType": "branch", "referenceName": c.Branch, "referenceFullName": "refs/heads/" + c.Branch, "commitId": c.Commit}
	if c.OldCommit != "" {
		detail["oldCommitId"] = c.OldCommit
	}
	id := messageID()
	at := s.now().UTC().Format(time.RFC3339Nano)
	for _, t := range targets {
		topology, err := destinations(ctx, tx, c.Region, t.Topic, true)
		if err != nil {
			return err
		}
		body, err := json.Marshal(map[string]any{"account": "000000000000", "detailType": "CodeCommit Repository State Change", "region": c.Region,
			"source": "aws.codecommit", "time": at, "notificationRuleArn": t.Rule, "resources": []string{c.RepositoryARN}, "detail": detail})
		if err != nil {
			return err
		}
		if err := checkedExec(ctx, tx, 1, `INSERT INTO aws_notification_outbox
			(rule_arn,rule_generation,topic_arn,topic_generation,event_id,envelope,topology) VALUES(?,?,?,?,?,?,?)`,
			t.Rule, t.RuleGeneration, t.Topic, t.TopicGeneration, id, string(body), topology); err != nil {
			return err
		}
		if err := requireCount(ctx, tx, 1, `SELECT count(*) FROM aws_notification_outbox WHERE
			rule_arn=? AND rule_generation=? AND topic_arn=? AND topic_generation=? AND event_id=? AND envelope=? AND topology=? AND retry_ms=0`,
			t.Rule, t.RuleGeneration, t.Topic, t.TopicGeneration, id, string(body), topology); err != nil {
			return err
		}
	}
	return requireCount(ctx, tx, len(targets), "SELECT count(*) FROM aws_notification_outbox WHERE event_id=?", id)
}
