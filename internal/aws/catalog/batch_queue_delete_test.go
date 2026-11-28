package catalog

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
)

func TestBatchDeleteJobQueueRequiresDisabledState(t *testing.T) {
	h := newHarness(t, batch)
	call := func(op string, input map[string]any, code int, errorCode string) reply {
		t.Helper()
		got := h.rest("batch", "POST", "/v1/"+op, input)
		expect(t, got, code, errorCode)
		return got
	}
	read := func(arn string) row {
		t.Helper()
		saved, err := findARN(h.db, "batch", arn)
		if err != nil {
			t.Fatalf("read %s: %v", arn, err)
		}
		return saved
	}
	policy := str(call("createschedulingpolicy", map[string]any{"name": "share"}, 200, "").body["arn"])
	environment := str(call("createcomputeenvironment", map[string]any{
		"computeEnvironmentName": "config", "type": "UNMANAGED", "serviceRole": "arn:aws:iam::000000000000:role/test",
	}, 200, "").body["computeEnvironmentArn"])
	const name = "delete-test"
	arn := str(call("createjobqueue", map[string]any{
		"jobQueueName": name, "priority": 21, "schedulingPolicyArn": policy,
		"tags":                    map[string]any{"fixture": "keep"},
		"computeEnvironmentOrder": []any{map[string]any{"order": 1, "computeEnvironment": environment}},
	}, 200, "").body["jobQueueArn"])
	other := str(call("createjobqueue", map[string]any{
		"jobQueueName": "other", "state": "DISABLED", "priority": 5,
	}, 200, "").body["jobQueueArn"])
	before := read(arn)
	if before.Document["state"] != "ENABLED" {
		t.Fatalf("fixture did not start ENABLED: %+v", before)
	}
	policyBefore, environmentBefore, otherBefore := read(policy), read(environment), read(other)
	assertUnchanged := func() {
		t.Helper()
		if !reflect.DeepEqual(read(arn), before) {
			t.Fatal("refused deletion changed the target, including its generation")
		}
		if !reflect.DeepEqual(read(policy), policyBefore) || !reflect.DeepEqual(read(environment), environmentBefore) ||
			!reflect.DeepEqual(read(other), otherBefore) {
			t.Fatal("refused deletion changed a reference or the other queue")
		}
	}

	key := "batch:DeleteJobQueue " + arn
	h.policy.deny[key] = true
	call("deletejobqueue", map[string]any{"jobQueue": name}, 403, "AccessDeniedException")
	delete(h.policy.deny, key)
	assertUnchanged()
	for _, identifier := range []string{name, arn} {
		refusal := call("deletejobqueue", map[string]any{"jobQueue": identifier}, 400, "ClientException")
		if refusal.body["message"] != "The job queue must be DISABLED before it can be deleted." {
			t.Fatalf("unexpected lifecycle refusal: %s", refusal.raw)
		}
		assertUnchanged()
	}

	call("updatejobqueue", map[string]any{"jobQueue": arn, "state": "DISABLED"}, 200, "")
	disabled := read(arn)
	if disabled.Document["state"] != "DISABLED" || disabled.Document["schedulingPolicyArn"] != policy ||
		!reflect.DeepEqual(disabled.Document["computeEnvironmentOrder"], before.Document["computeEnvironmentOrder"]) {
		t.Fatalf("disabling changed job queue definitions: %+v", disabled)
	}
	// Attached environments and policy need not be detached before deletion.
	call("deletejobqueue", map[string]any{"jobQueue": arn}, 200, "")
	if _, err := findARN(h.db, "batch", arn); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted queue is still present: %v", err)
	}
	call("deletejobqueue", map[string]any{"jobQueue": name}, 400, "ClientException")
	if !reflect.DeepEqual(read(policy), policyBefore) || !reflect.DeepEqual(read(environment), environmentBefore) ||
		!reflect.DeepEqual(read(other), otherBefore) {
		t.Fatal("deleting the disabled queue changed its references or the other queue")
	}
	call("deletejobqueue", map[string]any{"jobQueue": other}, 200, "")
	call("updatecomputeenvironment", map[string]any{"computeEnvironment": environment, "state": "DISABLED"}, 200, "")
	call("deletecomputeenvironment", map[string]any{"computeEnvironment": environment}, 200, "")
	call("deleteschedulingpolicy", map[string]any{"arn": policy}, 200, "")
}

func TestBatchDeleteJobQueuePreservesExistingCASFailure(t *testing.T) {
	for _, test := range []struct {
		name, first, later string
		recreate           bool
	}{
		{"newer generation disabled the queue", "ENABLED", "DISABLED", false},
		{"newer generation enabled the queue", "DISABLED", "ENABLED", false},
		{"new row with same ARN and generation", "DISABLED", "ENABLED", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, batch)
			call := func(op string, input map[string]any, code int, errorCode string) reply {
				t.Helper()
				got := h.rest("batch", "POST", "/v1/"+op, input)
				expect(t, got, code, errorCode)
				return got
			}
			create := func(state string) string {
				t.Helper()
				return str(call("createjobqueue", map[string]any{
					"jobQueueName": "stale", "state": state, "priority": 3,
				}, 200, "").body["jobQueueArn"])
			}
			arn := create(test.first)
			original, err := findARN(h.db, "batch", arn)
			if err != nil {
				t.Fatal(err)
			}
			authorizer := h.engine.cfg.Authorize
			pending := true
			var latest row
			h.engine.cfg.Authorize = func(ctx context.Context, principal, action, resource string) (string, error) {
				if pending && action == "batch:DeleteJobQueue" && resource == arn {
					pending = false
					// lookup already read the target, while the delete transaction is
					// not yet open. Change it using a separate native request rather
					// than time the race or edit the store directly.
					if test.recreate {
						call("deletejobqueue", map[string]any{"jobQueue": arn}, 200, "")
						if create(test.later) != arn {
							t.Fatal("replacement did not reuse the ARN")
						}
					} else {
						call("updatejobqueue", map[string]any{"jobQueue": arn, "state": test.later}, 200, "")
					}
					next, err := findARN(h.db, "batch", arn)
					if err != nil || next.Document["state"] != test.later {
						t.Fatalf("could not establish newer state: %+v, %v", next, err)
					}
					latest = next
				}
				return authorizer(ctx, principal, action, resource)
			}
			stale := call("deletejobqueue", map[string]any{"jobQueue": "stale"}, 400, "ClientException")
			if pending || stale.body["message"] != "The resource changed concurrently; read it again before retrying." {
				t.Fatalf("stale request must keep the existing CAS failure: %s", stale.raw)
			}
			if test.recreate {
				if latest.Seq == original.Seq || latest.Generation != original.Generation {
					t.Fatalf("wrong recreation control: before %+v, after %+v", original, latest)
				}
			} else if latest.Seq != original.Seq || latest.Generation != original.Generation+1 {
				t.Fatalf("wrong update control: before %+v, after %+v", original, latest)
			}
			if after, err := findARN(h.db, "batch", arn); err != nil || !reflect.DeepEqual(after, latest) {
				t.Fatalf("stale delete altered newest row: %+v, %v", after, err)
			}
		})
	}
}
