package server

import (
	"strings"
	"testing"
)

func TestSQSMessageBodyUnicodeAndLocalBound(t *testing.T) {
	s := instance(t)
	url := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "body-bounds"}, 200, nil)["QueueUrl"].(string)
	for _, body := range []string{"\x00", "\x01", "\uFFFE", "\uFFFF"} {
		out := sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": body}, 400, nil)
		if out["__type"] != "InvalidMessageContents" {
			t.Fatal("forbidden Unicode accepted", out)
		}
	}
	for _, body := range []string{"", strings.Repeat("x", 262145)} {
		out := sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": body}, 400, nil)
		if out["__type"] != "InvalidParameterValue" {
			t.Fatal(out)
		}
	}
	sent := sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "\t\n\rUnicode ✓ 😀"}, 200, nil)
	if sent["MessageId"] == "" {
		t.Fatal(sent)
	}
}
func TestSQSStrictRequestAndAttributeBoundaries(t *testing.T) {
	s := instance(t)
	for _, attrs := range []map[string]string{{"VisibilityTimeout": "-1"}, {"VisibilityTimeout": "43201"}, {"DelaySeconds": "-1"}, {"DelaySeconds": "901"}, {"MessageRetentionPeriod": "59"}, {"MessageRetentionPeriod": "1209601"}, {"VisibilityTimeout": "not-an-int"}, {"KmsMasterKeyId": "123"}} {
		sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "invalid-attributes", "Attributes": attrs}, 400, nil)
	}
	for _, name := range []string{"", "bad name", strings.Repeat("q", 81)} {
		sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": name}, 400, nil)
	}
	url := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "request-bounds"}, 200, nil)["QueueUrl"].(string)
	for _, attrs := range []map[string]any{{"": map[string]any{"DataType": "String", "StringValue": "x"}}, {"x": map[string]any{"DataType": "Unknown", "StringValue": "x"}}, {"x": map[string]any{"DataType": "String"}}, {"x": map[string]any{"DataType": "String", "StringValue": "x", "BinaryValue": "eA=="}}, {"x": map[string]any{"DataType": "Binary", "StringValue": "x"}}, {"x": map[string]any{"DataType": "Binary", "BinaryValue": "not-base64"}}} {
		sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "x", "MessageAttributes": attrs}, 400, nil)
	}
	tooMany := map[string]any{}
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		tooMany[name] = map[string]any{"DataType": "String", "StringValue": "x"}
	}
	sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "x", "MessageAttributes": tooMany}, 400, nil)
	for _, delay := range []int{-1, 901} {
		sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "x", "DelaySeconds": delay}, 400, nil)
	}
	for _, input := range []map[string]any{{"MaxNumberOfMessages": -1}, {"MaxNumberOfMessages": 11}, {"VisibilityTimeout": -1}, {"VisibilityTimeout": 43201}, {"WaitTimeSeconds": -1}, {"WaitTimeSeconds": 21}} {
		input["QueueUrl"] = url
		sqsCall(t, s, "ReceiveMessage", input, 400, nil)
	}
	sqsCall(t, s, "DeleteMessage", map[string]any{"QueueUrl": url}, 400, nil)
	sqsCall(t, s, "ChangeMessageVisibility", map[string]any{"QueueUrl": url, "ReceiptHandle": "x"}, 400, nil)
	sqsCall(t, s, "ChangeMessageVisibility", map[string]any{"QueueUrl": url, "ReceiptHandle": "x", "VisibilityTimeout": -1}, 400, nil)
	sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url}, 400, nil)
	sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url, "AttributeNames": []string{"KmsMasterKeyId"}}, 400, nil)
	for _, body := range []string{"{", "null", `{"QueueName":null}`, `{"QueueName":42}`, `{"QueueName":"x","UnknownField":true}`} {
		status(t, s, "POST", "/", body, 400, map[string]string{"X-Amz-Target": "AmazonSQS.CreateQueue", "Content-Type": "application/x-amz-json-1.0"})
	}
}
func TestSQSListPaginationBindingAndSelectiveAttributes(t *testing.T) {
	s := instance(t)
	for _, name := range []string{"page-a", "page-b", "page-c"} {
		sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": name}, 200, nil)
	}
	first := sqsCall(t, s, "ListQueues", map[string]any{"QueueNamePrefix": "page-", "MaxResults": 1}, 200, nil)
	token := first["NextToken"].(string)
	next := sqsCall(t, s, "ListQueues", map[string]any{"QueueNamePrefix": "page-", "MaxResults": 1, "NextToken": token}, 200, nil)
	if first["QueueUrls"].([]any)[0] == next["QueueUrls"].([]any)[0] {
		t.Fatal("pagination repeated queue")
	}
	sqsCall(t, s, "ListQueues", map[string]any{"NextToken": "not-a-token"}, 400, nil)
	sqsCall(t, s, "ListQueues", map[string]any{"QueueNamePrefix": "different", "NextToken": token}, 400, nil)
	sqsCall(t, s, "ListQueues", map[string]any{"QueueNamePrefix": "page-", "NextToken": token}, 400, map[string]string{"X-Simulator-Region": "eu-west-1"})
	for _, max := range []int{-1, 1001} {
		sqsCall(t, s, "ListQueues", map[string]any{"MaxResults": max}, 400, nil)
	}
	url := first["QueueUrls"].([]any)[0].(string)
	attrs := sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url, "AttributeNames": []string{"QueueArn", "VisibilityTimeout"}}, 200, nil)["Attributes"].(map[string]any)
	if len(attrs) != 2 || attrs["QueueArn"] == "" {
		t.Fatal(attrs)
	}
	binary := sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "binary-attribute", "MessageAttributes": map[string]any{"Data": map[string]any{"DataType": "Binary", "BinaryValue": "AQID"}}}, 200, nil)
	if binary["MD5OfMessageAttributes"] == "" {
		t.Fatal(binary)
	}
	msg := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": url, "MessageAttributeNames": []string{"All"}}, 200, nil)["Messages"].([]any)[0].(map[string]any)
	if msg["MessageAttributes"].(map[string]any)["Data"].(map[string]any)["BinaryValue"] != "AQID" {
		t.Fatal(msg)
	}
}
