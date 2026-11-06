package aws

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func sqsState(t *testing.T, path string) (*core.Store, *SQSStore) {
	t.Helper()
	db, err := core.New(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQSStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func TestSQSStoreLifecycleAndVisibility(t *testing.T) {
	db, store := sqsState(t, ":memory:")
	defer db.Close()
	q := SQSQueue{Name: "orders", VisibilityTimeout: 30, RetentionSeconds: 345600}
	created, err := store.CreateQueue("us-east-1", q)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.CreateQueue("us-east-1", q)
	if err != nil || again.Created != created.Created {
		t.Fatal("identical CreateQueue must be idempotent", err, again)
	}
	q.VisibilityTimeout = 60
	var failure sqsFailure
	if _, err = store.CreateQueue("us-east-1", q); !errors.As(err, &failure) || failure.code != "QueueNameExists" {
		t.Fatal("different attributes must fail with QueueNameExists", err)
	}
	q.VisibilityTimeout = 30
	m, err := store.Send("us-east-1", q, "hello", map[string]SQSMessageAttribute{}, -1)
	if err != nil {
		t.Fatal(err)
	}
	if m.MD5OfBody != "5d41402abc4b2a76b9719d911017c592" { // md5("hello")
		t.Fatal("wrong MD5OfBody", m.MD5OfBody)
	}
	got, err := store.Receive("us-east-1", q, 10, 3600)
	if err != nil || len(got) != 1 || got[0].Body != "hello" || got[0].ReceiveCount != 1 {
		t.Fatal("first receive", got, err)
	}
	receipt := got[0].ReceiptHandle
	// The received message is invisible until its visibility timeout expires.
	again2, err := store.Receive("us-east-1", q, 10, 3600)
	if err != nil || len(again2) != 0 {
		t.Fatal("message must stay invisible", again2, err)
	}
	if visible, notVisible, err := store.Counts("us-east-1", q); err != nil || visible != 0 || notVisible != 1 {
		t.Fatal("counts while inflight", visible, notVisible, err)
	}
	if err = store.DeleteMessage("us-east-1", "orders", "bogus-handle"); !errors.As(err, &failure) || failure.code != "ReceiptHandleIsInvalid" {
		t.Fatal("bogus receipt must fail", err)
	}
	// Making it visible again yields a second delivery with a higher count.
	if err = store.ChangeVisibility("us-east-1", "orders", receipt, 0); err != nil {
		t.Fatal(err)
	}
	got, err = store.Receive("us-east-1", q, 10, 3600)
	if err != nil || len(got) != 1 || got[0].ReceiveCount != 2 {
		t.Fatal("second receive", got, err)
	}
	if err = store.DeleteMessage("us-east-1", "orders", got[0].ReceiptHandle); err != nil {
		t.Fatal(err)
	}
	if visible, notVisible, err := store.Counts("us-east-1", q); err != nil || visible != 0 || notVisible != 0 {
		t.Fatal("counts after delete", visible, notVisible, err)
	}
	if err = store.DeleteQueue("us-east-1", "orders"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetQueue("us-east-1", "orders"); !errors.As(err, &failure) || failure.code != "AWS.SimpleQueueService.NonExistentQueue" {
		t.Fatal("deleted queue must be gone", err)
	}
}

func TestSQSStorePersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, store := sqsState(t, path)
	q := SQSQueue{Name: "durable", VisibilityTimeout: 30, RetentionSeconds: 345600}
	if _, err := store.CreateQueue("ap-northeast-1", q); err != nil {
		t.Fatal(err)
	}
	body := "survives restart"
	if _, err := store.Send("ap-northeast-1", q, body, nil, -1); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db2, store2 := sqsState(t, path)
	defer db2.Close()
	got, err := store2.GetQueue("ap-northeast-1", "durable")
	if err != nil || got.VisibilityTimeout != 30 {
		t.Fatal("queue must survive reopen", got, err)
	}
	msgs, err := store2.Receive("ap-northeast-1", got, 1, 30)
	if err != nil || len(msgs) != 1 || msgs[0].Body != body {
		t.Fatal("message must survive reopen", msgs, err)
	}
}

func TestSQSMessageAttributeChecksum(t *testing.T) {
	s := "v"
	attrs := map[string]SQSMessageAttribute{"Kind": {DataType: "String", StringValue: &s}}
	first := MD5OfMessageAttributes(attrs)
	if first == "" || first != MD5OfMessageAttributes(attrs) {
		t.Fatal("attribute checksum must be deterministic")
	}
	other := "w"
	attrs["Kind"] = SQSMessageAttribute{DataType: "String", StringValue: &other}
	if MD5OfMessageAttributes(attrs) == first {
		t.Fatal("attribute checksum must change with the value")
	}
}
