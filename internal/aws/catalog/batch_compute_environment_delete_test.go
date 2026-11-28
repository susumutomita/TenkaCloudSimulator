package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestBatchDeleteComputeEnvironmentRequiresCurrentDisassociation(t *testing.T) {
	h := newHarness(t, batch)
	call := func(op string, input map[string]any, status int, code string) reply {
		t.Helper()
		res := h.rest("batch", "POST", "/v1/"+op, input)
		expect(t, res, status, code)
		return res
	}
	read := func(arn string) row {
		t.Helper()
		row, err := findARN(h.db, "batch", arn)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	order := func(identifiers ...string) []any {
		result := make([]any, len(identifiers))
		for i, identifier := range identifiers {
			result[i] = map[string]any{"order": i + 1, "computeEnvironment": identifier}
		}
		return result
	}
	const name = "test-env"
	role := "arn:aws:iam::000000000000:role/test"
	arn := str(call("createcomputeenvironment", map[string]any{"computeEnvironmentName": name, "type": "UNMANAGED", "serviceRole": role, "tags": map[string]any{"class": "retain"}}, 200, "").body["computeEnvironmentArn"])
	second := str(call("createcomputeenvironment", map[string]any{"computeEnvironmentName": name + "-other", "type": "UNMANAGED", "serviceRole": role}, 200, "").body["computeEnvironmentArn"])
	remove := func(identifier string, status int, code string) reply {
		t.Helper()
		return call("deletecomputeenvironment", map[string]any{"computeEnvironment": identifier}, status, code)
	}

	enabled := read(arn)
	remove(name, 400, "ClientException")
	if !reflect.DeepEqual(read(arn), enabled) {
		t.Fatal("enabled environment changed on refusal")
	}
	call("updatecomputeenvironment", map[string]any{"computeEnvironment": arn, "state": "DISABLED"}, 200, "")
	disabled := read(arn)
	// The referenced environment is deliberately not first. The second
	// queue stores the accepted bare name rather than the official ARN.
	q1 := str(call("createjobqueue", map[string]any{"jobQueueName": "q1", "priority": 1, "computeEnvironmentOrder": order(second, arn)}, 200, "").body["jobQueueArn"])
	q2 := str(call("createjobqueue", map[string]any{"jobQueueName": "q2", "priority": 2, "state": "DISABLED", "computeEnvironmentOrder": order(name)}, 200, "").body["jobQueueArn"])
	q1Before, q2Before, secondBefore := read(q1), read(q2), read(second)

	key := "batch:DeleteComputeEnvironment " + arn
	h.policy.deny[key] = true
	remove(arn, 403, "AccessDeniedException")
	delete(h.policy.deny, key)
	const referenced = "The compute environment is still referenced by a job queue."
	if refusal := remove(name, 400, "ClientException"); refusal.body["message"] != referenced {
		t.Fatalf("unexpected dependency error: %s", refusal.raw)
	}
	if !reflect.DeepEqual(read(arn), disabled) || !reflect.DeepEqual(read(q1), q1Before) ||
		!reflect.DeepEqual(read(q2), q2Before) || !reflect.DeepEqual(read(second), secondBefore) {
		t.Fatal("refusal mutated an environment or queue")
	}

	call("updatejobqueue", map[string]any{"jobQueue": q1, "computeEnvironmentOrder": order(second)}, 200, "")
	remove(arn, 400, "ClientException")
	call("deletejobqueue", map[string]any{"jobQueue": q2}, 200, "")

	// Attach a queue after the authorized target was read, before the
	// delete transaction. A detached snapshot must not authorize deletion.
	authorize := h.engine.cfg.Authorize
	pending := true
	h.engine.cfg.Authorize = func(ctx context.Context, principal, action, resource string) (string, error) {
		if pending && action == "batch:DeleteComputeEnvironment" && resource == arn {
			pending = false
			call("updatejobqueue", map[string]any{"jobQueue": q1, "computeEnvironmentOrder": order(name)}, 200, "")
		}
		return authorize(ctx, principal, action, resource)
	}
	if refusal := remove(arn, 400, "ClientException"); pending || refusal.body["message"] != referenced || !reflect.DeepEqual(read(arn), disabled) {
		t.Fatalf("delete missed the new queue association: %s", refusal.raw)
	}
	h.engine.cfg.Authorize = authorize

	foreign := "arn:aws:batch:ap-northeast-1:000000000000:compute-environment/" + name
	call("updatejobqueue", map[string]any{"jobQueue": q1, "computeEnvironmentOrder": order(second, foreign, name+"-other")}, 200, "")
	// A same-name reference in another region and a service environment
	// order are separate scopes even if their saved strings match exactly.
	input, err := json.Marshal(map[string]any{"jobQueueName": "foreign", "priority": 1, "computeEnvironmentOrder": order(name)})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/createjobqueue", bytes.NewReader(input))
	sign(req, "batch", "ap-northeast-1")
	expect(t, h.do(req), 200, "")
	call("createjobqueue", map[string]any{"jobQueueName": "service", "priority": 1, "serviceEnvironmentOrder": []any{map[string]any{"order": 1, "serviceEnvironment": name}}}, 200, "")
	q1AfterDetach := read(q1)
	remove(arn, 200, "")
	if _, err := findARN(h.db, "batch", arn); !errors.Is(err, sql.ErrNoRows) ||
		!reflect.DeepEqual(read(second), secondBefore) || !reflect.DeepEqual(read(q1), q1AfterDetach) {
		t.Fatal("deletion did not remove exactly the detached environment")
	}
	remove(name, 400, "ClientException")
}

func TestBatchDeleteComputeEnvironmentPreservesConcurrentUpdate(t *testing.T) {
	h := newHarness(t, batch)
	res := h.rest("batch", "POST", "/v1/createcomputeenvironment", map[string]any{
		"computeEnvironmentName": "stale", "type": "UNMANAGED", "state": "DISABLED",
		"serviceRole": "arn:aws:iam::000000000000:role/test",
	})
	expect(t, res, 200, "")
	arn := str(res.body["computeEnvironmentArn"])
	authorize := h.engine.cfg.Authorize
	h.engine.cfg.Authorize = func(ctx context.Context, principal, action, resource string) (string, error) {
		if action == "batch:DeleteComputeEnvironment" && resource == arn {
			update := h.rest("batch", "POST", "/v1/updatecomputeenvironment", map[string]any{"computeEnvironment": arn, "state": "ENABLED", "context": "after authorization"})
			expect(t, update, 200, "")
		}
		return authorize(ctx, principal, action, resource)
	}
	refusal := h.rest("batch", "POST", "/v1/deletecomputeenvironment", map[string]any{"computeEnvironment": arn})
	expect(t, refusal, 400, "ClientException")
	saved, err := findARN(h.db, "batch", arn)
	if err != nil || saved.Document["state"] != "ENABLED" || saved.Document["context"] != "after authorization" ||
		refusal.body["message"] != "The resource changed concurrently; read it again before retrying." {
		t.Fatalf("stale delete did not preserve the concurrent update: %s, %+v, %v", refusal.raw, saved, err)
	}
}
