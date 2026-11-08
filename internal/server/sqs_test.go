package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func sqsCall(t *testing.T, s *httptest.Server, op string, input any, code int, extra map[string]string) map[string]any {
	t.Helper()
	h := map[string]string{"X-Amz-Target": "AmazonSQS." + op, "Content-Type": "application/x-amz-json-1.0"}
	for k, v := range extra {
		h[k] = v
	}
	body, _ := json.Marshal(input)
	data := status(t, s, "POST", "/", string(body), code, h)
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		t.Fatal(string(data))
	}
	return out
}

func TestSQSLifecycleAndIAM(t *testing.T) {
	s := instance(t)
	created := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "orders", "Attributes": map[string]string{"VisibilityTimeout": "30"}}, 200, nil)
	queueURL, _ := created["QueueUrl"].(string)
	if queueURL == "" {
		t.Fatal(created)
	}
	// Same attributes are idempotent; different attributes are an error.
	sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "orders", "Attributes": map[string]string{"VisibilityTimeout": "30"}}, 200, nil)
	dup := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "orders", "Attributes": map[string]string{"VisibilityTimeout": "60"}}, 400, nil)
	if dup["__type"] != "QueueNameExists" {
		t.Fatal(dup)
	}
	if bad := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "fifo-queue.fifo"}, 400, nil); bad["__type"] != "InvalidParameterValue" {
		t.Fatal("FIFO must be rejected explicitly", bad)
	}
	if unknown := sqsCall(t, s, "SendMessageBatch", map[string]any{}, 501, nil); unknown["__type"] != "UnknownOperationException" {
		t.Fatal("unimplemented operations must not fake success", unknown)
	}
	list := sqsCall(t, s, "ListQueues", map[string]any{"QueueNamePrefix": "ord"}, 200, nil)
	if urls, _ := list["QueueUrls"].([]any); len(urls) != 1 || urls[0] != queueURL {
		t.Fatal(list)
	}
	sent := sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "hello sqs", "MessageAttributes": map[string]any{"Kind": map[string]any{"DataType": "String", "StringValue": "greeting"}}}, 200, nil)
	if sent["MessageId"] == "" || sent["MD5OfMessageBody"] == "" || sent["MD5OfMessageAttributes"] == "" {
		t.Fatal(sent)
	}
	attrs := sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": queueURL, "AttributeNames": []string{"All"}}, 200, nil)["Attributes"].(map[string]any)
	if attrs["ApproximateNumberOfMessages"] != "1" || attrs["VisibilityTimeout"] != "30" {
		t.Fatal(attrs)
	}
	received := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": queueURL, "MaxNumberOfMessages": 10}, 200, nil)
	messages, _ := received["Messages"].([]any)
	if len(messages) != 1 {
		t.Fatal(received)
	}
	message := messages[0].(map[string]any)
	if message["Body"] != "hello sqs" {
		t.Fatal(message)
	}
	if kind := message["MessageAttributes"].(map[string]any)["Kind"].(map[string]any); kind["StringValue"] != "greeting" {
		t.Fatal(message)
	}
	receipt := message["ReceiptHandle"].(string)
	// Still inside the visibility timeout: a second receive sees nothing.
	empty := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": queueURL}, 200, nil)
	if got, _ := empty["Messages"].([]any); len(got) != 0 {
		t.Fatal("message must be invisible", empty)
	}
	badDelete := sqsCall(t, s, "DeleteMessage", map[string]any{"QueueUrl": queueURL, "ReceiptHandle": "bogus"}, 400, nil)
	if badDelete["__type"] != "ReceiptHandleIsInvalid" {
		t.Fatal(badDelete)
	}
	// IAM: a principal allowed only to receive/delete on this queue ARN.
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage"],"Resource":"arn:aws:sqs:us-east-1:000000000000:orders"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/consumer/policy", policy, 200, nil)
	consumer := map[string]string{"X-Simulator-Principal": "consumer"}
	sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "denied"}, 403, consumer)
	sqsCall(t, s, "DeleteMessage", map[string]any{"QueueUrl": queueURL, "ReceiptHandle": receipt}, 200, consumer)
	// Region isolation: the queue does not exist in ap-northeast-1.
	tokyo := map[string]string{"X-Simulator-Region": "ap-northeast-1"}
	missing := sqsCall(t, s, "GetQueueUrl", map[string]any{"QueueName": "orders"}, 400, tokyo)
	if missing["__type"] != "AWS.SimpleQueueService.NonExistentQueue" {
		t.Fatal(missing)
	}
	sqsCall(t, s, "DeleteQueue", map[string]any{"QueueUrl": queueURL}, 200, nil)
	gone := sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "x"}, 400, nil)
	if gone["__type"] != "AWS.SimpleQueueService.NonExistentQueue" {
		t.Fatal(gone)
	}
}

func TestSQSVisibilityOverrideAndChange(t *testing.T) {
	s := instance(t)
	created := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "timed"}, 200, nil)
	queueURL := created["QueueUrl"].(string)
	sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": queueURL, "MessageBody": "timed body"}, 200, nil)
	// Per-call visibility override hides the message for an hour.
	received := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": queueURL, "VisibilityTimeout": 3600}, 200, nil)
	messages, _ := received["Messages"].([]any)
	if len(messages) != 1 {
		t.Fatal(received)
	}
	receipt := messages[0].(map[string]any)["ReceiptHandle"].(string)
	empty := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": queueURL}, 200, nil)
	if got, _ := empty["Messages"].([]any); len(got) != 0 {
		t.Fatal("override must hide the message", empty)
	}
	// ChangeMessageVisibility to 0 makes it deliverable again.
	sqsCall(t, s, "ChangeMessageVisibility", map[string]any{"QueueUrl": queueURL, "ReceiptHandle": receipt, "VisibilityTimeout": 0}, 200, nil)
	again := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": queueURL}, 200, nil)
	got, _ := again["Messages"].([]any)
	if len(got) != 1 {
		t.Fatal(again)
	}
	sysAttrs := got[0].(map[string]any)["Attributes"].(map[string]any)
	if sysAttrs["ApproximateReceiveCount"] != "2" {
		t.Fatal(sysAttrs)
	}
}
