package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

// SNSStore owns AWS SNS state. It never shares the S3/SSM resource
// namespaces, and it records publish fanout locally instead of attempting
// any outbound delivery (see docs/sns.md for the bounded delivery model).
type SNSStore struct{ db *sql.DB }

type SNSTopic struct {
	Region      string
	Name        string
	ARN         string
	DisplayName string
	FIFO        bool
	CreatedAt   float64
}

type SNSSubscription struct {
	ARN      string
	TopicARN string
	Protocol string
	Endpoint string
}

type SNSMessage struct {
	ID          string
	TopicARN    string
	Message     string
	Subject     string
	PublishedAt float64
}

const (
	maxSNSTopics        = 1000
	maxSNSSubscriptions = 10000
)

func NewSNSStore(db *sql.DB) (*SNSStore, error) {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS aws_sns_topics(region TEXT NOT NULL,name TEXT NOT NULL,arn TEXT NOT NULL UNIQUE,display_name TEXT NOT NULL DEFAULT '',fifo INTEGER NOT NULL DEFAULT 0 CHECK(fifo IN (0,1)),created REAL NOT NULL,PRIMARY KEY(region,name));`,
		`CREATE TABLE IF NOT EXISTS aws_sns_subscriptions(arn TEXT PRIMARY KEY,topic_arn TEXT NOT NULL,protocol TEXT NOT NULL,endpoint TEXT NOT NULL,created REAL NOT NULL);`,
		`CREATE TABLE IF NOT EXISTS aws_sns_messages(id TEXT PRIMARY KEY,topic_arn TEXT NOT NULL,message TEXT NOT NULL,subject TEXT NOT NULL DEFAULT '',published REAL NOT NULL);`,
		`CREATE TABLE IF NOT EXISTS aws_sns_deliveries(message_id TEXT NOT NULL,subscription_arn TEXT NOT NULL,PRIMARY KEY(message_id,subscription_arn));`,
	}
	for _, q := range statements {
		if _, err := db.Exec(q); err != nil {
			return nil, err
		}
	}
	return &SNSStore{db}, nil
}

func snsARN(region, name string) string {
	return fmt.Sprintf("arn:aws:sns:%s:000000000000:%s", region, name)
}

func snsRandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// CreateTopic is idempotent like real SNS: an existing (region, name)
// returns its topic with created=false and stored attributes unchanged.
func (s *SNSStore) CreateTopic(region, name, displayName string, fifo bool) (SNSTopic, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SNSTopic{}, false, err
	}
	defer tx.Rollback()
	var existing SNSTopic
	err = tx.QueryRow("SELECT region,name,arn,display_name,fifo,created FROM aws_sns_topics WHERE region=? AND name=?", region, name).
		Scan(&existing.Region, &existing.Name, &existing.ARN, &existing.DisplayName, &existing.FIFO, &existing.CreatedAt)
	if err == nil {
		return existing, false, nil
	}
	if err != sql.ErrNoRows {
		return SNSTopic{}, false, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_sns_topics").Scan(&count); err != nil {
		return SNSTopic{}, false, err
	}
	if count >= maxSNSTopics {
		return SNSTopic{}, false, ssmFailure{"LimitExceededException", "Simulator topic limit reached."}
	}
	topic := SNSTopic{Region: region, Name: name, ARN: snsARN(region, name), DisplayName: displayName, FIFO: fifo, CreatedAt: float64(time.Now().UnixMilli()) / 1000}
	if _, err = tx.Exec("INSERT INTO aws_sns_topics VALUES(?,?,?,?,?,?)", topic.Region, topic.Name, topic.ARN, topic.DisplayName, topic.FIFO, topic.CreatedAt); err != nil {
		return SNSTopic{}, false, err
	}
	return topic, true, tx.Commit()
}

func (s *SNSStore) GetTopic(arn string) (SNSTopic, error) {
	var t SNSTopic
	err := s.db.QueryRow("SELECT region,name,arn,display_name,fifo,created FROM aws_sns_topics WHERE arn=?", arn).
		Scan(&t.Region, &t.Name, &t.ARN, &t.DisplayName, &t.FIFO, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return SNSTopic{}, ssmFailure{"NotFoundException", "Topic does not exist."}
	}
	return t, err
}

func (s *SNSStore) ListTopics(ctx context.Context, region string) ([]SNSTopic, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT region,name,arn,display_name,fifo,created FROM aws_sns_topics WHERE region=? ORDER BY arn", region)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SNSTopic{}
	for rows.Next() {
		var t SNSTopic
		if err = rows.Scan(&t.Region, &t.Name, &t.ARN, &t.DisplayName, &t.FIFO, &t.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

// DeleteTopic removes the topic, its subscriptions and their recorded
// deliveries in one transaction. Published message records are retained
// as local audit state; they carry no delivery effect after deletion.
func (s *SNSStore) DeleteTopic(arn string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM aws_sns_deliveries WHERE subscription_arn IN (SELECT arn FROM aws_sns_subscriptions WHERE topic_arn=?)", arn); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM aws_sns_subscriptions WHERE topic_arn=?", arn); err != nil {
		return err
	}
	r, err := tx.Exec("DELETE FROM aws_sns_topics WHERE arn=?", arn)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ssmFailure{"NotFoundException", "Topic does not exist."}
	}
	return tx.Commit()
}

func (s *SNSStore) Subscribe(topic SNSTopic, protocol, endpoint string) (SNSSubscription, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SNSSubscription{}, err
	}
	defer tx.Rollback()
	var existing SNSSubscription
	err = tx.QueryRow("SELECT arn,topic_arn,protocol,endpoint FROM aws_sns_subscriptions WHERE topic_arn=? AND protocol=? AND endpoint=?", topic.ARN, protocol, endpoint).
		Scan(&existing.ARN, &existing.TopicARN, &existing.Protocol, &existing.Endpoint)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return SNSSubscription{}, err
	}
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM aws_sns_subscriptions").Scan(&count); err != nil {
		return SNSSubscription{}, err
	}
	if count >= maxSNSSubscriptions {
		return SNSSubscription{}, ssmFailure{"LimitExceededException", "Simulator subscription limit reached."}
	}
	sub := SNSSubscription{ARN: topic.ARN + ":" + snsRandomID(), TopicARN: topic.ARN, Protocol: protocol, Endpoint: endpoint}
	if _, err = tx.Exec("INSERT INTO aws_sns_subscriptions VALUES(?,?,?,?,?)", sub.ARN, sub.TopicARN, sub.Protocol, sub.Endpoint, float64(time.Now().UnixMilli())/1000); err != nil {
		return SNSSubscription{}, err
	}
	return sub, tx.Commit()
}

func (s *SNSStore) GetSubscription(arn string) (SNSSubscription, error) {
	var sub SNSSubscription
	err := s.db.QueryRow("SELECT arn,topic_arn,protocol,endpoint FROM aws_sns_subscriptions WHERE arn=?", arn).
		Scan(&sub.ARN, &sub.TopicARN, &sub.Protocol, &sub.Endpoint)
	if err == sql.ErrNoRows {
		return SNSSubscription{}, ssmFailure{"NotFoundException", "Subscription does not exist."}
	}
	return sub, err
}

func (s *SNSStore) Unsubscribe(arn string) error {
	r, err := s.db.Exec("DELETE FROM aws_sns_subscriptions WHERE arn=?", arn)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ssmFailure{"NotFoundException", "Subscription does not exist."}
	}
	return err
}

func (s *SNSStore) listSubscriptions(ctx context.Context, where string, arg string) ([]SNSSubscription, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT arn,topic_arn,protocol,endpoint FROM aws_sns_subscriptions "+where+" ORDER BY arn", arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SNSSubscription{}
	for rows.Next() {
		var sub SNSSubscription
		if err = rows.Scan(&sub.ARN, &sub.TopicARN, &sub.Protocol, &sub.Endpoint); err != nil {
			return nil, err
		}
		result = append(result, sub)
	}
	return result, rows.Err()
}

func (s *SNSStore) ListSubscriptionsByTopic(ctx context.Context, topicARN string) ([]SNSSubscription, error) {
	if _, err := s.GetTopic(topicARN); err != nil {
		return nil, err
	}
	return s.listSubscriptions(ctx, "WHERE topic_arn=?", topicARN)
}

func (s *SNSStore) ListSubscriptions(ctx context.Context, region string) ([]SNSSubscription, error) {
	return s.listSubscriptions(ctx, "WHERE topic_arn IN (SELECT arn FROM aws_sns_topics WHERE region=?)", region)
}

// Publish records the message and one local delivery row per current
// subscription of the topic. No network delivery is attempted.
func (s *SNSStore) Publish(topic SNSTopic, message, subject string) (string, int, error) {
	return s.PublishWithID(topic, snsRandomID(), message, subject)
}

// PublishWithID is Publish with a caller-chosen message ID, so protocol
// fanout (e.g. the SNS notification envelope sent to an SQS queue) can
// reference the same MessageId the Publish call returns.
func (s *SNSStore) PublishWithID(topic SNSTopic, id, message, subject string) (string, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO aws_sns_messages VALUES(?,?,?,?,?)", id, topic.ARN, message, subject, float64(time.Now().UnixMilli())/1000); err != nil {
		return "", 0, err
	}
	r, err := tx.Exec("INSERT INTO aws_sns_deliveries(message_id,subscription_arn) SELECT ?,arn FROM aws_sns_subscriptions WHERE topic_arn=?", id, topic.ARN)
	if err != nil {
		return "", 0, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return "", 0, err
	}
	return id, int(n), tx.Commit()
}

func (s *SNSStore) ListMessages(ctx context.Context, topicARN string) ([]SNSMessage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,topic_arn,message,subject,published FROM aws_sns_messages WHERE topic_arn=? ORDER BY published,id", topicARN)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SNSMessage{}
	for rows.Next() {
		var m SNSMessage
		if err = rows.Scan(&m.ID, &m.TopicARN, &m.Message, &m.Subject, &m.PublishedAt); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (s *SNSStore) DeliveryCount(subscriptionARN string) (int, error) {
	var n int
	err := s.db.QueryRow("SELECT count(*) FROM aws_sns_deliveries WHERE subscription_arn=?", subscriptionARN).Scan(&n)
	return n, err
}
