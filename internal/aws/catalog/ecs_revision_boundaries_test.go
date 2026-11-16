package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestECSRegistrationFailuresLeaveNextFamilyRevisionAvailable(t *testing.T) {
	h := newHarness(t, ecs)
	input := revisionInput("api", "example/app")
	for _, role := range []string{"arn:aws:iam::111111111111:role/foreign", "arn:aws:iam::000000000000:role/team/", "arn:aws:iam::000000000000:role/team/*"} {
		input["taskRoleArn"] = role
		r := h.json1("ecs", "RegisterTaskDefinition", input)
		expect(t, r, 400, "ClientException")
		if !strings.Contains(r.raw, "simulator account") {
			t.Fatal(r.raw)
		}
	}
	delete(input, "taskRoleArn")
	ref := h.json1("ecs", "RegisterTaskDefinition", input)
	expect(t, ref, 200, "")
	arn := ref.body["taskDefinition"].(map[string]any)["taskDefinitionArn"].(string)
	if !strings.HasSuffix(arn, "/api:1") {
		t.Fatalf("failed registration consumed revision %s", arn)
	}
	h.policy.deny["ecs:DescribeTaskDefinition "+arn] = true
	for _, id := range []string{"api", "api:1", arn} {
		r := h.json1("ecs", "DescribeTaskDefinition", map[string]any{"taskDefinition": id})
		if r.header.Get("X-Simulator-IAM-Decision") != "explicit-deny" || r.status < 400 || r.status >= 500 {
			t.Fatalf("%s: got %v", id, r)
		}
	}
}

func TestECSRevisionListRejectsCursorFromDifferentScope(t *testing.T) {
	h := newHarness(t, ecs)
	for _, f := range []string{"a", "b", "b"} {
		expect(t, h.json1("ecs", "RegisterTaskDefinition", revisionInput(f, "example/app")), 200, "")
	}
	list := func(input map[string]any) reply { return h.json1("ecs", "ListTaskDefinitions", input) }
	first := list(map[string]any{"maxResults": 1, "sort": "DESC"})
	expect(t, first, 200, "")
	token, ok := first.body["nextToken"].(string)
	if !ok || token == "" {
		t.Fatal("first page has no cursor")
	}
	for _, q := range []map[string]any{{"nextToken": token, "sort": "ASC"}, {"nextToken": token, "sort": "DESC", "familyPrefix": "b"}, {"nextToken": "unknown", "sort": "DESC"}} {
		r := list(q)
		if r.status != 400 || !strings.Contains(r.raw, "Invalid pagination token") {
			t.Fatalf("expected cursor error, got %s", r.raw)
		}
	}
	last := list(map[string]any{"nextToken": token, "sort": "DESC", "maxResults": 2})
	expect(t, last, 200, "")
	arns := last.body["taskDefinitionArns"].([]any)
	if len(arns) != 2 || !strings.HasSuffix(arns[0].(string), "b:1") || !strings.HasSuffix(arns[1].(string), "a:1") {
		t.Fatal(arns)
	}
}

func TestECSTaskStackRegistrationAdmission(t *testing.T) {
	input := func() map[string]any {
		return map[string]any{"family": "api", "containerDefinitions": []any{map[string]any{"name": "app", "image": "example/app"}}}
	}
	for _, tc := range []struct {
		name    string
		modify  func(map[string]any)
		message string
	}{
		{"unknown input", func(in map[string]any) { in["invalidField"] = true }, "invalidField"},
		{"invalid family", func(in map[string]any) { in["family"] = "bad/family" }, "families contain"},
		{"too many tags", func(in map[string]any) {
			tags := []any{}
			for i := 0; i <= maxTagsPerResource; i++ {
				tags = append(tags, map[string]any{"key": fmt.Sprint(i), "value": "demo"})
			}
			in["tags"] = tags
		}, "tags"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := input()
			tc.modify(in)
			_, err := PrepareStackTaskDefinition("us-east-1", "000000000000", 1, in)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("want %q: %v", tc.message, err)
			}
		})
	}
	task, err := PrepareStackTaskDefinition("us-east-1", "000000000000", 1, input())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	denied := errors.New("registration denied")
	err = task.Authorize(func(action, resource string) error {
		calls++
		if action != "ecs:RegisterTaskDefinition" || resource != task.ARN() {
			t.Fatalf("wrong permission %s %s", action, resource)
		}
		return denied
	})
	if !errors.Is(err, denied) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestECSStackRetirementChecksScopeAndCanBeRetried(t *testing.T) {
	h := newHarness(t, ecs)
	r := h.json1("ecs", "RegisterTaskDefinition", revisionInput("api", "example/app:v1"))
	expect(t, r, 200, "")
	arn := r.body["taskDefinition"].(map[string]any)["taskDefinitionArn"].(string)
	other := h.json1("ecs", "CreateCluster", map[string]any{"clusterName": "sample"})
	expect(t, other, 200, "")
	clusterARN := other.body["cluster"].(map[string]any)["clusterArn"].(string)
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, tc := range []struct{ region, arn string }{{"ap-northeast-1", arn}, {"us-east-1", clusterARN}} {
		err := DeregisterStackTaskDefinition(tx, tc.region, tc.arn)
		if err == nil || !strings.Contains(err.Error(), "outside stack scope") {
			t.Fatal(tc, err)
		}
	}
	if err := DeregisterStackTaskDefinition(tx, "us-east-1", arn+"-missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if err := DeregisterStackTaskDefinition(tx, "us-east-1", arn); err != nil {
		t.Fatal(err)
	}
	first, err := findARN(tx, "ecs", arn)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeregisterStackTaskDefinition(tx, "us-east-1", arn); err != nil {
		t.Fatal(err)
	}
	second, err := findARN(tx, "ecs", arn)
	if err != nil || first.Document["deregisteredAt"] != second.Document["deregisteredAt"] || second.Document["status"] != "INACTIVE" {
		t.Fatalf("%v %v %v", first.Document, second.Document, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, h.json1("ecs", "DescribeTaskDefinition", map[string]any{"taskDefinition": "api:1"}), 400, "ClientException")
}

func TestECSRegistrationStopsOnCancelledRequestOrStoreFailure(t *testing.T) {
	h := newHarness(t, ecs)
	b := h.binding("ecs")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := request{e: h.engine, b: b, op: b.ops["RegisterTaskDefinition"], ctx: ctx, region: "us-east-1", input: map[string]any{"family": "api"}, w: httptest.NewRecorder()}
	if _, err := q.registerECSRevision(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request: %v", err)
	}
	next, err := NextECSTaskRevision(h.db, "us-east-1", "000000000000", "api")
	if err != nil || next != 1 {
		t.Fatal(next, err)
	}
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	task, err := PrepareStackTaskDefinition("us-east-1", "000000000000", 1, map[string]any{"family": "api", "containerDefinitions": []any{map[string]any{"name": "app", "image": "example/app"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Register(tx); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("closed transaction accepted: %v", err)
	}
	if err := h.db.Close(); err != nil {
		t.Fatal(err)
	}
	q.ctx = context.Background()
	if _, err := q.registerECSRevision(); err == nil {
		t.Fatal("closed revision store accepted registration")
	}
	q.op = b.ops["DescribeTaskDefinition"]
	if _, err := q.lookupECSRevision("api"); err == nil || !strings.Contains(err.Error(), "database is closed") {
		t.Fatalf("closed store misreported as a missing task: %v", err)
	}
}
