package aws

import (
	"crypto/md5" // SQS protocol checksum, not authentication.
	"fmt"
	"testing"
)

// Adapted from the pinned CC0 AWS evaluation-suite lifecycle in test/factory/upstream.
// Runs real protocol handlers and SQLite, NOT boto3, a VM, or real AWS. Using
// zero visibility prevents a no-op DeleteMessage from passing during invisibility.
func TestSQSFactoryFidelityLifecycle(t *testing.T) {
	g, _, _ := sqsAdditionalFixture(t)
	queue := sqsWire(t, g, "CreateQueue", map[string]any{"QueueName": "factory-fidelity"}, 200)["QueueUrl"]
	t.Cleanup(func() { sqsWire(t, g, "DeleteQueue", map[string]any{"QueueUrl": queue}, 200) })
	body := "hello-sqs-lifecycle-12345"
	checksum := fmt.Sprintf("%x", md5.Sum([]byte(body)))
	sent := sqsWire(t, g, "SendMessage", map[string]any{"QueueUrl": queue, "MessageBody": body}, 200)
	if sent["MD5OfMessageBody"] != checksum {
		t.Fatal("send MD5 differs from body")
	}
	receive := func() []any {
		t.Helper()
		out := sqsWire(t, g, "ReceiveMessage", map[string]any{"QueueUrl": queue, "MaxNumberOfMessages": 1, "VisibilityTimeout": 0}, 200)
		if out["Messages"] == nil {
			return nil
		}
		messages, ok := out["Messages"].([]any)
		if !ok {
			t.Fatal("malformed messages")
		}
		return messages
	}
	messages := receive()
	if len(messages) != 1 {
		t.Fatal("message not delivered", messages)
	}
	msg := messages[0].(map[string]any)
	if msg["Body"] != body || msg["MD5OfBody"] != checksum || msg["MessageId"] != sent["MessageId"] {
		t.Fatal("message body/checksum/identity changed")
	}
	sqsWire(t, g, "DeleteMessage", map[string]any{"QueueUrl": queue, "ReceiptHandle": msg["ReceiptHandle"]}, 200)
	if len(receive()) != 0 {
		t.Fatal("deleted message was redelivered")
	}
}
