package catalog

import (
	"fmt"
	"testing"
)

// AWS Batch's JobStateTimeLimitAction applies to the head of a job queue.
// Exercise the native REST operations and read-back, not actual scheduling.
func TestBatchJobQueueTimeLimitConfiguration(t *testing.T) {
	h := newHarness(t, batch)
	call := func(op string, in map[string]any) reply {
		t.Helper()
		return h.rest("batch", "POST", "/v1/"+op, in)
	}
	rule := func(action string, seconds int) map[string]any {
		return map[string]any{"action": action, "maxTimeSeconds": seconds, "reason": "CAPACITY:INSUFFICIENT_INSTANCE_CAPACITY", "state": "RUNNABLE"}
	}
	ce := []any{map[string]any{"computeEnvironment": "ce", "order": 1}}
	se := []any{map[string]any{"serviceEnvironment": "se", "order": 1}}
	create := func(name, queueType string, compute, service []any, rules ...map[string]any) reply {
		t.Helper()
		in := map[string]any{"jobQueueName": name, "priority": 1, "jobStateTimeLimitActions": rules}
		if queueType != "" {
			in["jobQueueType"] = queueType
		}
		if compute != nil {
			in["computeEnvironmentOrder"] = compute
		}
		if service != nil {
			in["serviceEnvironmentOrder"] = service
		}
		return call("createjobqueue", in)
	}
	for i, tc := range []struct {
		typ              string
		compute, service []any
		action           string
		seconds          int
	}{
		{"ECS", ce, nil, "CANCEL", 599},
		{"", ce, nil, "CANCEL", 86401},
		{"", ce, nil, "TERMINATE", 600},
		{"", nil, se, "CANCEL", 600},
		{"ECS_FARGATE", nil, nil, "TERMINATE", 600},
		{"SAGEMAKER_TRAINING", nil, nil, "CANCEL", 600},
		{"EKS", nil, se, "CANCEL", 600},
		{"", nil, nil, "CANCEL", 600},
	} {
		name := fmt.Sprintf("bad-%d", i)
		expect(t, create(name, tc.typ, tc.compute, tc.service, rule(tc.action, tc.seconds)), 400, "ClientException")
	}
	list := call("describejobqueues", map[string]any{})
	expect(t, list, 200, "")
	if len(list.body["jobQueues"].([]any)) != 0 {
		t.Fatal("an invalid queue was persisted", list.body)
	}
	expect(t, create("compute", "", ce, nil, rule("CANCEL", 600)), 200, "")
	expect(t, create("service", "SAGEMAKER_TRAINING", nil, se, rule("TERMINATE", 86400)), 200, "")
	update := func(in map[string]any) reply {
		t.Helper()
		in["jobQueue"] = "compute"
		return call("updatejobqueue", in)
	}
	assertStored := func(priority, action, seconds string) {
		t.Helper()
		got := call("describejobqueues", map[string]any{"jobQueues": []any{"compute"}})
		expect(t, got, 200, "")
		q := got.body["jobQueues"].([]any)[0].(map[string]any)
		r := q["jobStateTimeLimitActions"].([]any)[0].(map[string]any)
		if fmt.Sprint(q["priority"]) != priority || r["action"] != action || fmt.Sprint(r["maxTimeSeconds"]) != seconds {
			t.Fatalf("unexpected stored queue: %v", q)
		}
	}
	// Older releases persisted this rule. A priority-only write must not carry
	// its invalid limit forward, but the owner can repair the rule in-place.
	if _, err := h.db.Exec(`UPDATE aws_catalog_resources SET document =
		json_set(document, '$.jobStateTimeLimitActions[0].maxTimeSeconds', 599)
		WHERE service='batch' AND kind='jobQueue' AND name='compute'`); err != nil {
		t.Fatal(err)
	}
	expect(t, update(map[string]any{"priority": 9}), 400, "ClientException")
	assertStored("1", "CANCEL", "599")
	expect(t, update(map[string]any{"jobStateTimeLimitActions": []any{rule("CANCEL", 600)}}), 200, "")
	// A bad later rule must invalidate the whole update, including siblings.
	expect(t, update(map[string]any{"priority": 9, "jobStateTimeLimitActions": []any{rule("CANCEL", 800), rule("CANCEL", 599)}}), 400, "ClientException")
	expect(t, update(map[string]any{"priority": 9, "jobStateTimeLimitActions": []any{rule("TERMINATE", 800)}}), 400, "ClientException")
	// Reassociating the queue must also validate previously persisted actions.
	expect(t, update(map[string]any{"computeEnvironmentOrder": []any{}, "serviceEnvironmentOrder": se, "priority": 9}), 400, "ClientException")
	assertStored("1", "CANCEL", "600")
	expect(t, update(map[string]any{"computeEnvironmentOrder": []any{}, "serviceEnvironmentOrder": se, "priority": 2, "jobStateTimeLimitActions": []any{rule("TERMINATE", 86400)}}), 200, "")
	assertStored("2", "TERMINATE", "86400")
	expect(t, update(map[string]any{"jobStateTimeLimitActions": []any{}}), 200, "")
	got := call("describejobqueues", map[string]any{"jobQueues": []any{"compute"}})
	expect(t, got, 200, "")
	if rules := got.body["jobQueues"].([]any)[0].(map[string]any)["jobStateTimeLimitActions"].([]any); len(rules) != 0 {
		t.Fatalf("clear retained rules: %v", rules)
	}
	for _, name := range []string{"compute", "service"} {
		expect(t, call("deletejobqueue", map[string]any{"jobQueue": name}), 200, "")
	}
	expect(t, call("describejobs", map[string]any{"jobs": []any{"absent"}}), 501, "UnknownOperationException")
}
