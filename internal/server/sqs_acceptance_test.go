package server

import (
	"encoding/json"
	"testing"
)

func TestSQSExplicitZeroRejectedAndOmittedDefaultsPreserved(t *testing.T) {
	s := instance(t)
	url := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "zero-vs-omitted"}, 200, nil)["QueueUrl"].(string)
	for _, test := range []struct {
		op    string
		input map[string]any
	}{
		{"ListQueues", map[string]any{"MaxResults": 0}},
		{"ReceiveMessage", map[string]any{"QueueUrl": url, "MaxNumberOfMessages": 0}},
	} {
		out := sqsCall(t, s, test.op, test.input, 400, nil)
		if out["__type"] != "InvalidParameterValue" {
			t.Fatal(test.op, out)
		}
	}
	list := sqsCall(t, s, "ListQueues", map[string]any{}, 200, nil)
	if urls := list["QueueUrls"].([]any); len(urls) != 1 || urls[0] != url {
		t.Fatal("omitted list bound changed", list)
	}
	for _, body := range []string{"first", "second"} {
		sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": body}, 200, nil)
	}
	for range 2 {
		out := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": url}, 200, nil)
		if messages := out["Messages"].([]any); len(messages) != 1 {
			t.Fatal("omitted receive bound must remain one", out)
		}
	}
}

func TestSQSQueueDefaultsDelayOverrideAndExactAttributeSelection(t *testing.T) {
	s := instance(t)
	create := map[string]any{"QueueName": "delayed", "Attributes": map[string]string{"DelaySeconds": "900", "MessageRetentionPeriod": "60", "VisibilityTimeout": "30"}}
	url := sqsCall(t, s, "CreateQueue", create, 200, nil)["QueueUrl"].(string)
	if again := sqsCall(t, s, "CreateQueue", create, 200, nil); again["QueueUrl"] != url {
		t.Fatal("identical nondefault attributes not idempotent", again)
	}
	if got := sqsCall(t, s, "GetQueueUrl", map[string]any{"QueueName": "delayed"}, 200, nil); got["QueueUrl"] != url {
		t.Fatal("GetQueueUrl differs from CreateQueue", got)
	}
	attrs := sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url, "AttributeNames": []string{"DelaySeconds", "MessageRetentionPeriod", "VisibilityTimeout"}}, 200, nil)["Attributes"].(map[string]any)
	if len(attrs) != 3 || attrs["DelaySeconds"] != "900" || attrs["MessageRetentionPeriod"] != "60" || attrs["VisibilityTimeout"] != "30" {
		t.Fatal("queue settings not persisted", attrs)
	}
	sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "default-delayed"}, 200, nil)
	if empty := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": url}, 200, nil); empty["Messages"] != nil {
		t.Fatal("queue default delay ignored", empty)
	}
	sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "override-immediate", "DelaySeconds": 0, "MessageAttributes": map[string]any{
		"Keep": map[string]any{"DataType": "String.local", "StringValue": "selected"},
		"Hide": map[string]any{"DataType": "String", "StringValue": "unrequested"},
	}}, 200, nil)
	out := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": url, "MessageAttributeNames": []string{"Keep", "Absent"}}, 200, nil)
	messages := out["Messages"].([]any)
	if len(messages) != 1 {
		t.Fatal("per-message zero delay did not override queue default", out)
	}
	msg := messages[0].(map[string]any)
	selected := msg["MessageAttributes"].(map[string]any)
	if msg["Body"] != "override-immediate" || len(selected) != 1 {
		t.Fatal("wrong message or unrequested attributes", msg)
	}
	keep := selected["Keep"].(map[string]any)
	if keep["DataType"] != "String.local" || keep["StringValue"] != "selected" {
		t.Fatal("custom type/value not preserved", keep)
	}
}

func TestSQSUnknownNestedAttributeFieldsFailWithoutSending(t *testing.T) {
	s := instance(t)
	url := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "strict-nested"}, 200, nil)["QueueUrl"].(string)
	for _, field := range []string{"StringListValues", "BinaryListValues", "Unexpected"} {
		attribute := map[string]any{"DataType": "String", "StringValue": "must-not-send", field: []string{"ignored-before-repair"}}
		out := sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "must-not-send", "MessageAttributes": map[string]any{"a": attribute}}, 400, nil)
		if out["__type"] != "InvalidParameterValue" {
			t.Fatal("nested unsupported field accepted", field, out)
		}
	}
	attrs := sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url, "AttributeNames": []string{"ApproximateNumberOfMessages", "ApproximateNumberOfMessagesNotVisible"}}, 200, nil)["Attributes"].(map[string]any)
	if attrs["ApproximateNumberOfMessages"] != "0" || attrs["ApproximateNumberOfMessagesNotVisible"] != "0" {
		t.Fatal("rejected nested input enqueued a message", attrs)
	}
	// A supported custom data-type suffix must remain usable after refusals.
	sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": url, "MessageBody": "valid", "MessageAttributes": map[string]any{"a": map[string]any{"DataType": "String.local", "StringValue": "kept"}}}, 200, nil)
}

func TestSQSAllAttributesCannotHideUnsupportedNames(t *testing.T) {
	s := instance(t)
	url := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "all-validation"}, 200, nil)["QueueUrl"].(string)
	for _, names := range [][]string{{"All", "KmsMasterKeyId"}, {"KmsMasterKeyId", "All"}} {
		out := sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url, "AttributeNames": names}, 400, nil)
		if out["__type"] != "InvalidAttributeName" {
			t.Fatal("attribute order bypass", names, out)
		}
	}
	sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url, "AttributeNames": []string{"All", "QueueArn"}}, 200, nil)
}

func TestSQSBodyEncodingRejectedWithoutNormalization(t *testing.T) {
	s := instance(t)
	url := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "raw-encoding"}, 200, nil)["QueueUrl"].(string)
	encodedURL, err := json.Marshal(url)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"X-Amz-Target": "AmazonSQS.SendMessage", "Content-Type": "application/x-amz-json-1.0"}
	for _, raw := range []string{`"\ud800"`, `"\udc00"`, `"\ud800x"`, `"\ud800\u0041"`, "\"" + string([]byte{0xff}) + "\""} {
		status(t, s, "POST", "/", `{"QueueUrl":`+string(encodedURL)+`,"MessageBody":`+raw+`}`, 400, headers)
	}
	attrs := sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": url, "AttributeNames": []string{"ApproximateNumberOfMessages"}}, 200, nil)["Attributes"].(map[string]any)
	if attrs["ApproximateNumberOfMessages"] != "0" {
		t.Fatal("invalid encoding was normalized and enqueued", attrs)
	}
	for _, test := range []struct{ raw, body string }{
		{`"\ud83d\ude00"`, "😀"},
		{`"literal \\ud800"`, `literal \ud800`},
		{`"replacement \ufffd"`, "replacement �"},
		{`"mixed ✓ \u0061 \ud83d\ude00"`, "mixed ✓ a 😀"},
	} {
		status(t, s, "POST", "/", `{"QueueUrl":`+string(encodedURL)+`,"MessageBody":`+test.raw+`}`, 200, headers)
		got := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": url}, 200, nil)["Messages"].([]any)
		if len(got) != 1 || got[0].(map[string]any)["Body"] != test.body {
			t.Fatal("valid body changed during decode/store", test.raw, got)
		}
	}
}
