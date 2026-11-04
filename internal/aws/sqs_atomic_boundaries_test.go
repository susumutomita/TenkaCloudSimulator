package aws

import (
	"errors"
	"testing"
)

func sqsFailureCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure sqsFailure
	if !errors.As(err, &failure) || failure.code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func sqsSQLCount(t *testing.T, s *SQSStore, table string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func sqsQueueFixture(t *testing.T) (*SQSStore, SQSQueue) {
	t.Helper()
	db, s := sqsState(t, ":memory:")
	t.Cleanup(func() { _ = db.Close() })
	q, err := s.CreateQueue("us-east-1", SQSQueue{Name: "atomic", VisibilityTimeout: 30, RetentionSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	return s, q
}
func TestSQSParentDeletionRejectsCachedQueueOperations(t *testing.T) {
	s, q := sqsQueueFixture(t)
	if err := s.DeleteQueue(q.Region, q.Name); err != nil {
		t.Fatal(err)
	}
	_, err := s.Send(q.Region, q, "must not orphan", nil, -1)
	sqsFailureCode(t, err, "AWS.SimpleQueueService.NonExistentQueue")
	_, err = s.Receive(q.Region, q, 1, 30)
	sqsFailureCode(t, err, "AWS.SimpleQueueService.NonExistentQueue")
	_, _, err = s.Counts(q.Region, q)
	sqsFailureCode(t, err, "AWS.SimpleQueueService.NonExistentQueue")
	sqsFailureCode(t, s.DeleteMessage(q.Region, q.Name, "stale"), "AWS.SimpleQueueService.NonExistentQueue")
	sqsFailureCode(t, s.ChangeVisibility(q.Region, q.Name, "stale", 30), "AWS.SimpleQueueService.NonExistentQueue")
	if sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
		t.Fatal("cached request created orphan message")
	}
}
func TestSQSPurgeFailureNeverContinuesOperation(t *testing.T) {
	for _, operation := range []string{"Send", "Receive", "Counts"} {
		t.Run(operation, func(t *testing.T) {
			s, q := sqsQueueFixture(t)
			_, err := s.Send(q.Region, q, "expired", nil, -1)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.db.Exec("UPDATE aws_sqs_messages SET sent=?,visible=0", sqsNow()-120)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.db.Exec("CREATE TRIGGER fail_sqs_purge BEFORE DELETE ON aws_sqs_messages BEGIN SELECT RAISE(ABORT,'injected purge failure'); END")
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "Send":
				_, err = s.Send(q.Region, q, "new", nil, -1)
			case "Receive":
				_, err = s.Receive(q.Region, q, 1, 30)
			case "Counts":
				_, _, err = s.Counts(q.Region, q)
			}
			if err == nil {
				t.Fatal("purge failure ignored")
			}
			var protocol sqsFailure
			if errors.As(err, &protocol) {
				t.Fatal("infrastructure failure became protocol success/missing", err)
			}
			var receives int
			if err = s.db.QueryRow("SELECT receive_count FROM aws_sqs_messages").Scan(&receives); err != nil {
				t.Fatal(err)
			}
			if receives != 0 || sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
				t.Fatal("failed operation changed expired message")
			}
			_, _ = s.db.Exec("DROP TRIGGER fail_sqs_purge")
			got, err := s.Receive(q.Region, q, 1, 30)
			if err != nil || len(got) != 0 || sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
				t.Fatal("expiry cleanup did not commit", got, err)
			}
		})
	}
}
func TestSQSSendInsertFailureRollsBackExpiryCleanup(t *testing.T) {
	s, q := sqsQueueFixture(t)
	_, err := s.Send(q.Region, q, "retained on failure", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = s.db.Exec("UPDATE aws_sqs_messages SET sent=?", sqsNow()-120)
	_, err = s.db.Exec("CREATE TRIGGER fail_sqs_insert BEFORE INSERT ON aws_sqs_messages BEGIN SELECT RAISE(ABORT,'injected insert failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Send(q.Region, q, "new", nil, -1)
	if err == nil || sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
		t.Fatal("partial send/cleanup committed", err)
	}
}
func TestSQSReceiveBatchClaimAndCorruptionAreAtomic(t *testing.T) {
	s, q := sqsQueueFixture(t)
	first, err := s.Send(q.Region, q, "first", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Send(q.Region, q, "second", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("UPDATE aws_sqs_messages SET sent=CASE id WHEN ? THEN ? ELSE ? END", first.ID, sqsNow()-2, sqsNow()-1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("CREATE TRIGGER fail_sqs_claim BEFORE UPDATE ON aws_sqs_messages WHEN old.id='" + second.ID + "' BEGIN SELECT RAISE(ABORT,'injected claim failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Receive(q.Region, q, 2, 30); err == nil {
		t.Fatal("claim failure ignored")
	}
	var count int
	if err = s.db.QueryRow("SELECT sum(receive_count) FROM aws_sqs_messages").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial batch claimed", count, err)
	}
	_, _ = s.db.Exec("DROP TRIGGER fail_sqs_claim")
	_, _ = s.db.Exec("UPDATE aws_sqs_messages SET attributes=? WHERE id=?", `{"Broken":{}}`, second.ID)
	if _, err = s.Receive(q.Region, q, 2, 30); err == nil {
		t.Fatal("corrupt attribute acknowledged")
	}
	if err = s.db.QueryRow("SELECT sum(receive_count) FROM aws_sqs_messages").Scan(&count); err != nil || count != 0 {
		t.Fatal("corrupt batch partially claimed", count, err)
	}
	_, _ = s.db.Exec("UPDATE aws_sqs_messages SET attributes='null' WHERE id=?", second.ID)
	got, err := s.Receive(q.Region, q, 2, 30)
	if err != nil || len(got) != 2 || got[0].ReceiveCount != 1 || got[1].ReceiveCount != 1 {
		t.Fatal(got, err)
	}
}
func TestSQSDeleteQueueFailurePreservesGraph(t *testing.T) {
	s, q := sqsQueueFixture(t)
	_, err := s.Send(q.Region, q, "retained", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("CREATE TRIGGER fail_sqs_delete BEFORE DELETE ON aws_sqs_messages BEGIN SELECT RAISE(ABORT,'injected cascade failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteQueue(q.Region, q.Name); err == nil {
		t.Fatal("delete failure ignored")
	}
	if sqsSQLCount(t, s, "aws_sqs_messages") != 1 || sqsSQLCount(t, s, "aws_sqs_queues") != 1 {
		t.Fatal("partial queue graph deleted")
	}
	_, _ = s.db.Exec("DROP TRIGGER fail_sqs_delete")
	if err = s.DeleteQueue(q.Region, q.Name); err != nil {
		t.Fatal(err)
	}
	if sqsSQLCount(t, s, "aws_sqs_messages") != 0 {
		t.Fatal("message orphan after delete")
	}
}
func TestSQSExpiredInflightAndRedeliveryHandleBoundaries(t *testing.T) {
	s, q := sqsQueueFixture(t)
	m, err := s.Send(q.Region, q, "clock boundary", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	old := got[0].ReceiptHandle
	_, _ = s.db.Exec("UPDATE aws_sqs_messages SET visible=? WHERE id=?", sqsNow()-1, m.ID)
	sqsFailureCode(t, s.ChangeVisibility(q.Region, q.Name, old, 10), "MessageNotInflight")
	got, err = s.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 1 || got[0].ReceiptHandle == old || got[0].ReceiveCount != 2 {
		t.Fatal("redelivery did not rotate authority", got, err)
	}
	sqsFailureCode(t, s.DeleteMessage(q.Region, q.Name, old), "ReceiptHandleIsInvalid")
	sqsFailureCode(t, s.ChangeVisibility(q.Region, q.Name, old, 10), "ReceiptHandleIsInvalid")
	_, _ = s.db.Exec("UPDATE aws_sqs_messages SET sent=? WHERE id=?", sqsNow()-120, m.ID)
	sqsFailureCode(t, s.ChangeVisibility(q.Region, q.Name, got[0].ReceiptHandle, 10), "ReceiptHandleIsInvalid")
	got, err = s.Receive(q.Region, q, 1, 30)
	if err != nil || len(got) != 0 {
		t.Fatal("expired body delivered", got, err)
	}
}
func TestSQSRegionalReceiptsCannotMutatePeerQueue(t *testing.T) {
	s, q := sqsQueueFixture(t)
	other, err := s.CreateQueue("eu-west-1", q)
	if err != nil {
		t.Fatal(err)
	}
	for _, queue := range []SQSQueue{q, other} {
		if _, err = s.Send(queue.Region, queue, "isolated", nil, -1); err != nil {
			t.Fatal(err)
		}
	}
	a, err := s.Receive(q.Region, q, 1, 30)
	if err != nil || len(a) != 1 {
		t.Fatal(a, err)
	}
	b, err := s.Receive(other.Region, other, 1, 30)
	if err != nil || len(b) != 1 {
		t.Fatal(b, err)
	}
	sqsFailureCode(t, s.DeleteMessage(other.Region, other.Name, a[0].ReceiptHandle), "ReceiptHandleIsInvalid")
	sqsFailureCode(t, s.ChangeVisibility(other.Region, other.Name, a[0].ReceiptHandle, 1), "ReceiptHandleIsInvalid")
	if sqsSQLCount(t, s, "aws_sqs_messages") != 2 {
		t.Fatal("foreign receipt deleted a peer message")
	}
}
func TestSQSConstructorRejectsOrphansWithoutDeletingThem(t *testing.T) {
	s, q := sqsQueueFixture(t)
	_, err := s.Send(q.Region, q, "preserve corrupt graph", nil, -1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec("DELETE FROM aws_sqs_queues")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewSQSStore(s.db); err == nil {
		t.Fatal("orphan state adopted")
	}
	if sqsSQLCount(t, s, "aws_sqs_messages") != 1 {
		t.Fatal("corrupt original data destroyed")
	}
	if _, err = NewSQSStore(nil); err == nil {
		t.Fatal("nil provider DB accepted")
	}
}
