package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// Native fanout is possible only when the two stores share one database handle.
// The old callback seam cannot provide rollback and is not used here.
func (g *Gateway) nativeSNSQueues() bool {
	return g.SNS != nil && g.Queues != nil && g.SNS.db == g.Queues.db
}

// Queue resource policies/service trust are not implemented. This deliberately
// stricter local subset requires the caller's SendMessage permission. Capture
// the policy before opening the write transaction; the closure performs no I/O.
func (g *Gateway) snsAuthorizationSnapshot(ctx context.Context, principal string) (func(string, string) string, error) {
	if principal == core.Administrator {
		return func(string, string) string { return "allowed" }, nil
	}
	policy, _, err := g.Identities.PolicyContext(ctx, principal)
	if err != nil {
		return nil, err
	}
	return func(action, arn string) string { return core.Evaluate(policy, action, arn) }, nil
}

func snsQueueAuthorization(decide func(string, string) string) func(string) error {
	return func(arn string) error {
		decision := decide("sqs:SendMessage", arn)
		if decision != "allowed" {
			return snsQueueDenied{decision}
		}
		return nil
	}
}

type snsQueueDenied struct{ decision string }

func (e snsQueueDenied) Error() string {
	return "Local SQS fanout requires sqs:SendMessage on every destination: " + e.decision
}
func (e snsQueueDenied) Unwrap() error { return ssmFailure{"AuthorizationErrorException", e.Error()} }
func snsFanoutError(w http.ResponseWriter, err error) {
	var denied snsQueueDenied
	if errors.As(err, &denied) {
		w.Header().Set("X-Simulator-IAM-Decision", denied.decision)
	}
	snsError(w, err)
}

func snsNativeQueue(tx *sql.Tx, topic SNSTopic, endpoint string) (SQSQueue, error) {
	region, name, ok := snsQueueEndpoint(endpoint)
	if !ok || region != topic.Region || !validQueueName(name) {
		return SQSQueue{}, ssmFailure{"InvalidParameterException", "SQS endpoints must name a Standard queue in this region and simulator account."}
	}
	q, err := sqsQueueTx(tx, region, name)
	var failure sqsFailure
	if errors.As(err, &failure) && failure.code == "AWS.SimpleQueueService.NonExistentQueue" {
		return q, ssmFailure{"NotFoundException", "Subscribed queue does not exist."}
	}
	return q, err
}

func (s *SNSStore) subscribeQueue(ctx context.Context, queues *SQSStore, topic SNSTopic, endpoint string) (SNSSubscription, error) {
	if queues == nil || queues.db != s.db {
		return SNSSubscription{}, errors.New("SNS fanout requires the same native database")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SNSSubscription{}, err
	}
	defer tx.Rollback()
	fifo, err := snsRequireTopic(tx, topic.ARN)
	if err != nil {
		return SNSSubscription{}, err
	}
	if fifo {
		return SNSSubscription{}, ssmFailure{"InvalidParameterException", "Native SQS fanout supports Standard topics only."}
	}
	q, err := snsNativeQueue(tx, topic, endpoint)
	if err != nil {
		return SNSSubscription{}, err
	}
	sub, err := snsSubscribeTx(tx, topic, "sqs", endpoint)
	if err != nil {
		return SNSSubscription{}, err
	}
	current, err := snsNativeQueue(tx, topic, endpoint)
	if err != nil {
		return SNSSubscription{}, err
	}
	if current != q {
		return SNSSubscription{}, errors.New("SNS destination changed during subscription")
	}
	return sub, tx.Commit()
}

type snsQueueReceipt struct {
	sub     SNSSubscription
	queue   SQSQueue
	message SQSMessage
}

// publishQueues uses one snapshot of subscriptions and one SQLite commit for
// SNS audit rows and every SQS enqueue. No external side effect can escape rollback.
func (s *SNSStore) publishQueues(ctx context.Context, queues *SQSStore, topic SNSTopic, message, subject string, authorize func(string) error) (string, error) {
	if queues == nil || queues.db != s.db || authorize == nil {
		return "", errors.New("SNS fanout requires native queues and authorization")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	rows, err := tx.Query("SELECT arn,topic_arn,protocol,endpoint FROM aws_sns_subscriptions WHERE topic_arn=? ORDER BY arn", topic.ARN)
	if err != nil {
		return "", err
	}
	var subscriptions []SNSSubscription
	for rows.Next() {
		var sub SNSSubscription
		if err = rows.Scan(&sub.ARN, &sub.TopicARN, &sub.Protocol, &sub.Endpoint); err != nil {
			rows.Close()
			return "", err
		}
		subscriptions = append(subscriptions, sub)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	var receipts []snsQueueReceipt
	for _, sub := range subscriptions {
		if sub.Protocol != "sqs" {
			continue
		}
		if err = authorize(sub.Endpoint); err != nil {
			return "", err
		}
		q, err := snsNativeQueue(tx, topic, sub.Endpoint)
		if err != nil {
			return "", err
		}
		receipts = append(receipts, snsQueueReceipt{sub: sub, queue: q})
	}
	id := snsRandomID()
	count, err := snsPublishTx(tx, topic, id, message, subject)
	if err != nil {
		return "", err
	}
	if count != len(subscriptions) {
		return "", errors.New("SNS subscription set changed during publication")
	}
	envelope := snsNotificationEnvelope(id, topic, message, subject)
	for i := range receipts {
		receipts[i].message, err = sqsSendTx(tx, topic.Region, receipts[i].queue.Name, envelope, nil, -1)
		if err != nil {
			var failure sqsFailure
			if errors.As(err, &failure) {
				if failure.code == "SimulatorQuotaExceeded" {
					return "", ssmFailure{"LimitExceededException", "Local SQS fanout storage budget exceeded."}
				}
				return "", ssmFailure{"InvalidParameterException", "Notification envelope exceeds the local SQS message constraints."}
			}
			return "", err
		}
	}
	// A later SQLite trigger/fault must not remove or alter an earlier write.
	for _, receipt := range receipts {
		q, err := snsNativeQueue(tx, topic, receipt.sub.Endpoint)
		if err != nil {
			return "", err
		}
		if q != receipt.queue {
			return "", errors.New("SNS destination changed during publication")
		}
		m := receipt.message
		if err = snsCount(tx, 1, "SELECT count(*) FROM aws_sqs_messages WHERE region=? AND queue=? AND id=? AND body=? AND attributes='null' AND sent=? AND visible=? AND receive_count=0 AND receipt=''", q.Region, q.Name, m.ID, envelope, m.Sent, m.Sent+float64(q.DelaySeconds)); err != nil {
			return "", err
		}
	}
	if err = snsCount(tx, 1, "SELECT count(*) FROM aws_sns_messages WHERE id=? AND topic_arn=? AND message=? AND subject=?", id, topic.ARN, message, subject); err != nil {
		return "", err
	}
	if err = snsCount(tx, count, "SELECT count(*) FROM aws_sns_subscriptions WHERE topic_arn=?", topic.ARN); err != nil {
		return "", err
	}
	if err = snsCount(tx, count, "SELECT count(*) FROM aws_sns_deliveries WHERE message_id=?", id); err != nil {
		return "", err
	}
	for _, sub := range subscriptions {
		if err = snsCount(tx, 1, "SELECT count(*) FROM aws_sns_subscriptions WHERE arn=? AND topic_arn=? AND protocol=? AND endpoint=?", sub.ARN, topic.ARN, sub.Protocol, sub.Endpoint); err != nil {
			return "", err
		}
		if err = snsCount(tx, 1, "SELECT count(*) FROM aws_sns_deliveries WHERE message_id=? AND subscription_arn=?", id, sub.ARN); err != nil {
			return "", err
		}
	}
	if fifo, err := snsRequireTopic(tx, topic.ARN); err != nil || fifo {
		return "", fmt.Errorf("SNS topic changed during publication: %v", err)
	}
	return id, tx.Commit()
}
