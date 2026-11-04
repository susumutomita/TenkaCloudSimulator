package aws

import "testing"

// These faults require a nonstandard/corrupt SQLite trigger, not an ordinary
// service API input. They test storage-ack resilience, not a cloud exploit.
func TestSQSInsertIgnoreCannotAcknowledgeUnpersistedMessage(t *testing.T) {
	s, q := sqsQueueFixture(t)
	if _, err := s.Send(q.Region, q, "expired but preserved on failure", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE aws_sqs_messages SET sent=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER ignore_sqs_insert BEFORE INSERT ON aws_sqs_messages BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Send(q.Region, q, "not saved", nil, 0)
	if err == nil || m.ID != "" || sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
		t.Fatal("ignored insert acknowledged or committed expiry purge", m, err)
	}
	if _, err = s.db.Exec("DROP TRIGGER ignore_sqs_insert"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Send(q.Region, q, "saved after repair", nil, 0); err != nil || sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
		t.Fatal("repaired insert did not commit atomically", err)
	}
}

func TestSQSReceiveIgnoredClaimCannotLoseMessageOrAcknowledgeReceipt(t *testing.T) {
	s, q := sqsQueueFixture(t)
	for _, body := range []string{"first", "second", "expired"} {
		if _, err := s.Send(q.Region, q, body, nil, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE aws_sqs_messages SET sent=CASE body WHEN 'expired' THEN 0 WHEN 'first' THEN ? ELSE ? END", sqsNow()-2, sqsNow()-1); err != nil {
		t.Fatal(err)
	}
	// The first claim is written, then the second row is deleted and its update
	// silently skipped. The entire batch and preceding expiry must roll back.
	if _, err := s.db.Exec("CREATE TRIGGER ignore_sqs_claim BEFORE UPDATE ON aws_sqs_messages WHEN old.body='second' BEGIN DELETE FROM aws_sqs_messages WHERE id=old.id; SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	messages, err := s.Receive(q.Region, q, 2, 30)
	if err == nil || len(messages) != 0 || sqsSQLCount(t, s, "aws_sqs_messages") != 3 {
		t.Fatal("ignored batch claim acknowledged or lost rows", messages, err)
	}
	var claimed int
	if err = s.db.QueryRow("SELECT count(*) FROM aws_sqs_messages WHERE receive_count<>0 OR receipt<>''").Scan(&claimed); err != nil || claimed != 0 {
		t.Fatal("failed batch left receipt authority", claimed, err)
	}
	if _, err = s.db.Exec("DROP TRIGGER ignore_sqs_claim"); err != nil {
		t.Fatal(err)
	}
	messages, err = s.Receive(q.Region, q, 2, 30)
	if err != nil || len(messages) != 2 || sqsSQLCount(t, s, "aws_sqs_messages") != 2 {
		t.Fatal("repaired batch failed", messages, err)
	}
	for _, message := range messages {
		if message.ReceiveCount != 1 || message.ReceiptHandle == "" {
			t.Fatal("retry inherited partial claim", message)
		}
	}
}

func TestSQSDeleteQueueIgnoredWriteRetainsQueueAndMessages(t *testing.T) {
	s, q := sqsQueueFixture(t)
	if _, err := s.Send(q.Region, q, "retained", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER ignore_sqs_queue_delete BEFORE DELETE ON aws_sqs_queues BEGIN DELETE FROM aws_sqs_messages WHERE region=old.region AND queue=old.name; SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteQueue(q.Region, q.Name); err == nil || sqsSQLCount(t, s, "aws_sqs_messages") != 1 || sqsSQLCount(t, s, "aws_sqs_queues") != 1 {
		t.Fatal("ignored delete acknowledged or removed state", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER ignore_sqs_queue_delete"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteQueue(q.Region, q.Name); err != nil {
		t.Fatal(err)
	}
	if sqsSQLCount(t, s, "aws_sqs_messages") != 0 || sqsSQLCount(t, s, "aws_sqs_queues") != 0 {
		t.Fatal("repaired queue delete did not clean up")
	}
}

func TestSQSCreateQueueIgnoredInsertCannotAcknowledgePhantomQueue(t *testing.T) {
	db, _, identities := lambdaState(t)
	s, err := NewSQSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	g := &Gateway{Store: db, Identities: identities, Queues: s}
	if _, err = s.db.Exec("CREATE TRIGGER ignore_sqs_queue_insert BEFORE INSERT ON aws_sqs_queues BEGIN SELECT RAISE(IGNORE); END"); err != nil {
		t.Fatal(err)
	}
	out := sqsWire(t, g, "CreateQueue", map[string]any{"QueueName": "phantom"}, 500)
	if out["__type"] != "InternalError" || out["QueueUrl"] != nil || sqsSQLCount(t, s, "aws_sqs_queues") != 0 {
		t.Fatal("ignored create acknowledged a phantom queue", out)
	}
	if _, err = s.db.Exec("DROP TRIGGER ignore_sqs_queue_insert"); err != nil {
		t.Fatal(err)
	}
	out = sqsWire(t, g, "CreateQueue", map[string]any{"QueueName": "phantom"}, 200)
	if out["QueueUrl"] == "" || sqsSQLCount(t, s, "aws_sqs_queues") != 1 {
		t.Fatal("repaired create failed", out)
	}
}
