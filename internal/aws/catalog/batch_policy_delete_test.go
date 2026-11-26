package catalog

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// AWS Batch DeleteSchedulingPolicy refuses to delete a policy used by any
// job queue. The queues in this fixture are stored local metadata only.
func TestBatchDeleteSchedulingPolicyRequiresNoQueueReferences(t *testing.T) {
	h := newHarness(t, batch)
	call := func(op string, input map[string]any, status int, code string) reply {
		t.Helper()
		r := h.rest("batch", "POST", "/v1/"+op, input)
		expect(t, r, status, code)
		return r
	}
	policy := func(name string) string {
		t.Helper()
		arn := str(call("createschedulingpolicy", map[string]any{
			"name": name, "fairsharePolicy": map[string]any{"computeReservation": 10},
		}, 200, "").body["arn"])
		if arn != "arn:aws:batch:us-east-1:000000000000:scheduling-policy/"+name {
			t.Fatalf("unexpected scheduling policy ARN %q", arn)
		}
		return arn
	}
	used, alone := policy("referenced"), policy("standalone")
	for _, q := range []struct{ name, state string }{
		{"active-queue", "ENABLED"}, {"inactive-queue", "DISABLED"},
	} {
		call("createjobqueue", map[string]any{"jobQueueName": q.name, "priority": 5,
			"state": q.state, "schedulingPolicyArn": used}, 200, "")
	}
	frame := func() []row {
		t.Helper()
		policies, err := listKind(h.db, "batch", "schedulingPolicy")
		if err != nil {
			t.Fatal(err)
		}
		queues, err := listKind(h.db, "batch", "jobQueue")
		if err != nil {
			t.Fatal(err)
		}
		return append(policies, queues...)
	}
	deletePolicy := func(arn string, status int, code string) reply {
		t.Helper()
		start := len(h.policy.calls)
		r := call("deleteschedulingpolicy", map[string]any{"arn": arn}, status, code)
		want := []string{"simulator-admin batch:DeleteSchedulingPolicy " + arn}
		if !slices.Equal(h.policy.calls[start:], want) {
			t.Fatalf("delete IAM evaluation %q does not match %q", h.policy.calls[start:], want)
		}
		return r
	}
	refuseUsed := func() {
		t.Helper()
		before := frame()
		r := deletePolicy(used, 400, "ClientException")
		if len(r.body) != 1 || r.body["message"] != "The scheduling policy is still referenced by a job queue." {
			t.Fatal("dependency refusal should return only the static public message")
		}
		if after := frame(); !reflect.DeepEqual(after, before) {
			t.Fatalf("dependency refusal changed durable Batch state: got %v, want %v", after, before)
		}
	}

	before := frame()
	key := "batch:DeleteSchedulingPolicy " + used
	h.policy.deny[key] = true
	denied := deletePolicy(used, 403, "AccessDeniedException")
	delete(h.policy.deny, key)
	if strings.Contains(denied.raw, "active-queue") || strings.Contains(denied.raw, "inactive-queue") ||
		!reflect.DeepEqual(frame(), before) {
		t.Fatal("IAM refusal disclosed dependencies or mutated Batch state")
	}
	// Having permission to delete this policy does not grant permission to
	// describe its queues. The dependency failure must not identify them.
	authorize := h.engine.cfg.Authorize
	h.engine.cfg.Authorize = func(ctx context.Context, principal, action, resource string) (string, error) {
		if action != "batch:DeleteSchedulingPolicy" || resource != used {
			return "implicit-deny", nil
		}
		return authorize(ctx, principal, action, resource)
	}
	call("describejobqueues", map[string]any{"jobQueues": []any{"active-queue"}}, 403, "AccessDeniedException")
	refuseUsed()
	h.engine.cfg.Authorize = authorize
	deletePolicy(alone, 200, "")
	call("updatejobqueue", map[string]any{"jobQueue": "active-queue", "state": "DISABLED"}, 200, "")
	call("deletejobqueue", map[string]any{"jobQueue": "active-queue"}, 200, "")
	// A disabled, unrun queue remains an association after the other queue is
	// removed, even though no scheduling could take place.
	refuseUsed()
	call("deletejobqueue", map[string]any{"jobQueue": "inactive-queue"}, 200, "")
	deletePolicy(used, 200, "")
	if rows := frame(); len(rows) != 0 {
		t.Fatalf("cleanup left Batch resources: %v", rows)
	}
}
