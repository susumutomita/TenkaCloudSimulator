package aws

import (
	"strings"
	"sync"
	"testing"
)

// Trusted fixture writes real persisted rows efficiently. It does not modify
// production limits, clock functions, allocator behavior or SQL drivers.
func seedSQSBudgetRows(t *testing.T, s *SQSStore, q SQSQueue, count int, body string) {
	t.Helper()
	_, err := s.db.Exec(`WITH RECURSIVE rows(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM rows WHERE n<?)
INSERT INTO aws_sqs_messages SELECT printf('budget-%08d',n),?,?,?,'null',?,0,0,'' FROM rows`, count, q.Region, q.Name, body, sqsNow())
	if err != nil {
		t.Fatal(err)
	}
}

func TestSQSPhysicalRowBudgetSerializesLastSlotWriters(t *testing.T) {
	s, q := sqsQueueFixture(t)
	seedSQSBudgetRows(t, s, q, maxSQSMessages-1, "seed")
	start := make(chan struct{})
	results := make(chan error, 2)
	var writers sync.WaitGroup
	for range 2 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			_, err := s.Send(q.Region, q, "last-slot", nil, 0)
			results <- err
		}()
	}
	close(start)
	writers.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else {
			sqsFailureCode(t, err, "SimulatorQuotaExceeded")
		}
	}
	if succeeded != 1 || sqsSQLCount(t, s, "aws_sqs_messages") != maxSQSMessages {
		t.Fatal("last slot admitted multiple writers or lost prior rows", succeeded)
	}
}

func TestSQSStoredPayloadBudgetIncludesAttributeEncodingAndUTF8Bytes(t *testing.T) {
	s, q := sqsQueueFixture(t)
	// Populate the actual budget using individually legal UTF-8 payloads;
	// production constants and per-message API limits remain unchanged.
	value := "é"
	attrs := map[string]SQSMessageAttribute{"a": {DataType: "String.local", StringValue: &value}}
	// A first supported send obtains its exact persisted JSON byte encoding.
	m, err := s.Send(q.Region, q, "✓", attrs, 0)
	if err != nil {
		t.Fatal(err)
	}
	var bytes int64
	if err = s.db.QueryRow("SELECT length(CAST(body AS BLOB))+length(CAST(attributes AS BLOB)) FROM aws_sqs_messages WHERE id=?", m.ID).Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("DELETE FROM aws_sqs_messages WHERE id=?", m.ID); err != nil {
		t.Fatal(err)
	}
	seedSQSBudgetRows(t, s, q, 256, strings.Repeat("x", 262144-4))
	if _, err = s.db.Exec("UPDATE aws_sqs_messages SET body=substr(body,1,length(body)-?) WHERE id='budget-00000001'", bytes); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Send(q.Region, q, "✓", attrs, 0); err != nil {
		t.Fatal("exact byte boundary refused", err)
	}
	_, err = s.Send(q.Region, q, "x", nil, 0)
	sqsFailureCode(t, err, "SimulatorQuotaExceeded")
	var total int64
	if err = s.db.QueryRow("SELECT SUM(length(CAST(body AS BLOB))+length(CAST(attributes AS BLOB))) FROM aws_sqs_messages").Scan(&total); err != nil || total != maxSQSStoredPayloadBytes {
		t.Fatal("payload budget exceeded or wrong byte accounting", total, err)
	}
	// Existing over-cap data is not destroyed or made unreadable by startup.
	if _, err = s.db.Exec("INSERT INTO aws_sqs_messages VALUES('legacy-overcap',?,?,'legacy','null',?,0,0,'')", q.Region, q.Name, sqsNow()); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewSQSStore(s.db)
	if err != nil {
		t.Fatal("legacy over-cap startup refused", err)
	}
	read, err := reopened.Receive(q.Region, q, 1, 30)
	if err != nil || len(read) != 1 {
		t.Fatal("legacy over-cap data cannot be read", err)
	}
	if err = reopened.DeleteMessage(q.Region, q.Name, read[0].ReceiptHandle); err != nil {
		t.Fatal("legacy over-cap data cannot be deleted", err)
	}
}

func TestSQSExpiredForeignRowsConsumeBudgetAndRejectedSendRollsBackOwnPurge(t *testing.T) {
	s, q := sqsQueueFixture(t)
	foreign, err := s.CreateQueue("ap-northeast-1", SQSQueue{Name: "foreign-expired", VisibilityTimeout: 30, RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	seedSQSBudgetRows(t, s, foreign, maxSQSMessages, "foreign")
	if _, err = s.db.Exec("UPDATE aws_sqs_messages SET sent=0"); err != nil {
		t.Fatal(err)
	}
	// Legacy over-cap state must be retained, including the scoped expired row.
	if _, err = s.db.Exec("INSERT INTO aws_sqs_messages VALUES('own-expired',?,?,'own','null',0,0,0,'')", q.Region, q.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = NewSQSStore(s.db); err != nil {
		t.Fatal("over-cap legacy data must remain readable/deletable", err)
	}
	_, err = s.Send(q.Region, q, "must not fit", nil, 0)
	sqsFailureCode(t, err, "SimulatorQuotaExceeded")
	if sqsSQLCount(t, s, "aws_sqs_messages") != maxSQSMessages+1 {
		t.Fatal("rejected send committed scoped purge or discarded foreign data")
	}
	if _, _, err = s.Counts(q.Region, q); err != nil || sqsSQLCount(t, s, "aws_sqs_messages") != maxSQSMessages {
		t.Fatal("read must permit own scoped expiry cleanup", err)
	}
	if err = s.DeleteQueue(foreign.Region, foreign.Name); err != nil {
		t.Fatal("legacy over-cap data must be explicitly deletable", err)
	}
	if _, err = s.Send(q.Region, q, "fits after explicit foreign cleanup", nil, 0); err != nil {
		t.Fatal(err)
	}
}

func TestSQSFailedInsertDoesNotConsumeLastPhysicalBudgetSlot(t *testing.T) {
	s, q := sqsQueueFixture(t)
	seedSQSBudgetRows(t, s, q, maxSQSMessages-1, "seed")
	if _, err := s.db.Exec("CREATE TRIGGER budget_insert_failure BEFORE INSERT ON aws_sqs_messages BEGIN SELECT RAISE(ABORT,'injected budget insert failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(q.Region, q, "failed", nil, 0); err == nil || sqsSQLCount(t, s, "aws_sqs_messages") != maxSQSMessages-1 {
		t.Fatal("failed send consumed or corrupted budget", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER budget_insert_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(q.Region, q, strings.Repeat("x", 256), nil, 0); err != nil {
		t.Fatal("failure did not release final slot", err)
	}
}

func TestSQSQuotaFailureIsExplicitOnWireAndPreservesPriorRows(t *testing.T) {
	db, _, identities := lambdaState(t)
	s, err := NewSQSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateQueue("us-east-1", SQSQueue{Name: "wire-budget", VisibilityTimeout: 30, RetentionSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	seedSQSBudgetRows(t, s, q, maxSQSMessages, "retained")
	g := &Gateway{Store: db, Identities: identities, Queues: s}
	out := sqsWire(t, g, "SendMessage", map[string]any{"QueueUrl": q.Name, "MessageBody": "refused"}, 400)
	if out["__type"] != "SimulatorQuotaExceeded" || sqsSQLCount(t, s, "aws_sqs_messages") != maxSQSMessages {
		t.Fatal("quota not explicit or prior data changed", out)
	}
}

func TestSQSBudgetReadFailureRollsBackScopedExpiry(t *testing.T) {
	s, q := sqsQueueFixture(t)
	if _, err := s.Send(q.Region, q, "old", nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE aws_sqs_messages SET sent=0"); err != nil {
		t.Fatal(err)
	}
	// A real persisted schema failure happens after scoped purge but before
	// admission. Restoring the schema permits retry without lost prior state.
	if _, err := s.db.Exec("ALTER TABLE aws_sqs_messages RENAME COLUMN attributes TO unavailable_attributes"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(q.Region, q, "new", nil, 0); err == nil || sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
		t.Fatal("budget lookup failure committed expiry cleanup", err)
	}
	if _, err := s.db.Exec("ALTER TABLE aws_sqs_messages RENAME COLUMN unavailable_attributes TO attributes"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(q.Region, q, "new", nil, 0); err != nil || sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
		t.Fatal("restored budget lookup cannot retry atomically", err)
	}
}
