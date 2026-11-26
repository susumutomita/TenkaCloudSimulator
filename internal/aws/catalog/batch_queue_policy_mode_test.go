package catalog

import (
	"reflect"
	"testing"
)

// CreateJobQueue fixes the queue as FIFO or fair-share. UpdateJobQueue can
// replace a fair-share policy, not introduce one on a queue created FIFO.
func TestBatchUpdateJobQueuePreservesSchedulingPolicyMode(t *testing.T) {
	h := newHarness(t, batch)
	call := func(op string, input map[string]any, status int, code string) reply {
		t.Helper()
		r := h.rest("batch", "POST", "/v1/"+op, input)
		expect(t, r, status, code)
		return r
	}
	createPolicy := func(name string) string {
		t.Helper()
		return str(call("createschedulingpolicy", map[string]any{"name": name}, 200, "").body["arn"])
	}
	original, replacement := createPolicy("policy-original"), createPolicy("policy-replacement")
	qARN := func(name, policy string) string {
		t.Helper()
		in := map[string]any{"jobQueueName": name, "priority": 2, "state": "DISABLED", "tags": map[string]any{"fixture": "keep"}}
		if policy != "" {
			in["schedulingPolicyArn"] = policy
		}
		return str(call("createjobqueue", in, 200, "").body["jobQueueArn"])
	}
	fifo, fair := qARN("fifo-queue", ""), qARN("fair-share-queue", original)
	read := func(arn string) row {
		t.Helper()
		stored, err := findARN(h.db, "batch", arn)
		if err != nil || stored.Kind != "jobQueue" {
			t.Fatalf("read fixture job queue: %v", err)
		}
		return stored
	}
	update := func(arn string, input map[string]any, status int, code string) reply {
		t.Helper()
		input["jobQueue"] = arn
		return call("updatejobqueue", input, status, code)
	}
	refuseModeChange := func(arn, policy string) {
		t.Helper()
		before := read(arn)
		update(arn, map[string]any{"schedulingPolicyArn": policy, "priority": 37, "state": "ENABLED"}, 400, "ClientException")
		if after := read(arn); !reflect.DeepEqual(after, before) {
			t.Fatalf("policy mode refusal changed a saved row: got %v, want %v", after, before)
		}
	}
	refuseModeChange(fifo, original)
	update(fifo, map[string]any{"priority": 3}, 200, "")
	after := read(fifo)
	p, _ := Number(after.Document["priority"])
	if str(after.Document["schedulingPolicyArn"]) != "" || p != 3 {
		t.Fatalf("ordinary FIFO update changed the mode or lost its priority: %v", after.Document)
	}
	update(fair, map[string]any{"schedulingPolicyArn": replacement}, 200, "")
	update(fair, map[string]any{"priority": 3}, 200, "")
	after = read(fair)
	p, _ = Number(after.Document["priority"])
	if str(after.Document["schedulingPolicyArn"]) != replacement || p != 3 {
		t.Fatalf("fair-share replacement or omitted policy update was not retained: %v", after.Document)
	}
	refuseModeChange(fair, "")
	for _, arn := range []string{fifo, fair} {
		call("deletejobqueue", map[string]any{"jobQueue": arn}, 200, "")
	}
	for _, arn := range []string{original, replacement} {
		call("deleteschedulingpolicy", map[string]any{"arn": arn}, 200, "")
	}
}
