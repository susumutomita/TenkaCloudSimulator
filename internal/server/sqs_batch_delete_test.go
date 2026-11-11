package server

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestSQSDeleteMessageBatchCapabilitiesAndProviderRoute(t *testing.T) {
	s := instance(t)
	var capabilities struct {
		Services struct {
			SQS struct {
				Operations                  []string `json:"operations"`
				Unsupported                 []string `json:"unsupported"`
				DeleteMessageBatchEntries   int      `json:"deleteMessageBatchEntries"`
				DeleteMessageBatchIAMAction string   `json:"deleteMessageBatchIAMAction"`
			} `json:"sqs"`
		} `json:"services"`
	}
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil), &capabilities); err != nil {
		t.Fatal(err)
	}
	capability := capabilities.Services.SQS
	want := []string{"CreateQueue", "ListQueues", "GetQueueUrl", "DeleteQueue", "SendMessage", "ReceiveMessage", "DeleteMessage", "DeleteMessageBatch", "ChangeMessageVisibility", "GetQueueAttributes"}
	sort.Strings(want)
	sort.Strings(capability.Operations)
	if !reflect.DeepEqual(capability.Operations, want) || capability.DeleteMessageBatchEntries != 10 || capability.DeleteMessageBatchIAMAction != "sqs:DeleteMessage" {
		t.Fatalf("incorrect batch capability: %+v", capability)
	}
	unsupported := []string{"FIFO queues", "Query protocol", "dead-letter redrive policies", "SSE/KMS settings", "SendMessageBatch", "ChangeMessageVisibilityBatch"}
	if !reflect.DeepEqual(capability.Unsupported, unsupported) {
		t.Fatalf("unsupported operations changed: %v", capability.Unsupported)
	}
	queue := sqsCall(t, s, "CreateQueue", map[string]any{"QueueName": "batch-route"}, 200, nil)["QueueUrl"].(string)
	sqsCall(t, s, "SendMessage", map[string]any{"QueueUrl": queue, "MessageBody": "batch body"}, 200, nil)
	message := sqsCall(t, s, "ReceiveMessage", map[string]any{"QueueUrl": queue}, 200, nil)["Messages"].([]any)[0].(map[string]any)
	body, err := json.Marshal(map[string]any{"QueueUrl": queue, "Entries": []map[string]any{
		{"Id": "current", "ReceiptHandle": message["ReceiptHandle"]},
		{"Id": "duplicate", "ReceiptHandle": message["ReceiptHandle"]},
	}})
	if err != nil {
		t.Fatal(err)
	}
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:DeleteMessage","Resource":"arn:aws:sqs:us-east-1:000000000000:batch-route"}]}`
	status(t, s, "PUT", "/_simulator/v1/iam/principals/batch-consumer/policy", policy, 200, nil)
	headers := map[string]string{"Content-Type": "application/x-amz-json-1.0", "X-Amz-Target": "AmazonSQS.DeleteMessageBatch", "X-Simulator-Principal": "batch-consumer"}
	var result struct {
		Successful []struct{ Id string }
		Failed     []struct {
			Id, Code, Message string
			SenderFault       bool
		}
	}
	if err := json.Unmarshal(status(t, s, "POST", "/_simulator/providers/aws/", string(body), 200, headers), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Successful) != 1 || result.Successful[0].Id != "current" || len(result.Failed) != 1 || result.Failed[0].Id != "duplicate" || result.Failed[0].Code != "ReceiptHandleIsInvalid" || !result.Failed[0].SenderFault || result.Failed[0].Message != "The input receipt handle is invalid." {
		t.Fatalf("incorrect provider batch partition: %+v", result)
	}
	if got := sqsCall(t, s, "GetQueueAttributes", map[string]any{"QueueUrl": queue, "AttributeNames": []string{"All"}}, 200, nil)["Attributes"].(map[string]any); got["ApproximateNumberOfMessages"] != "0" || got["ApproximateNumberOfMessagesNotVisible"] != "0" {
		t.Fatalf("batch did not delete shared native state: %v", got)
	}
	for _, op := range []string{"SendMessageBatch", "ChangeMessageVisibilityBatch"} {
		if got := sqsCall(t, s, op, map[string]any{}, 501, nil); got["__type"] != "UnknownOperationException" {
			t.Fatalf("%s must remain unsupported: %v", op, got)
		}
	}
}
