package catalog

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func revisionInput(family, image string) map[string]any {
	return map[string]any{
		"family": family, "requiresCompatibilities": []string{"FARGATE"},
		"networkMode": "awsvpc", "cpu": "256", "memory": "512",
		"containerDefinitions": []any{map[string]any{
			"name": "app", "image": image,
			"portMappings": []any{map[string]any{"containerPort": 8080}},
			"environment":  []any{map[string]any{"name": "MODE", "value": "test"}},
		}},
	}
}

func TestECSRevisionsAcrossPublishesAndDeregistration(t *testing.T) {
	h := newHarness(t, ecs)
	first := "example/app@sha256:" + strings.Repeat("1", 64)
	second := "example/app@sha256:" + strings.Repeat("2", 64)
	task := func(r reply, want int, image string) string {
		t.Helper()
		expect(t, r, 200, "")
		record := r.body["taskDefinition"].(map[string]any)
		container := record["containerDefinitions"].([]any)[0].(map[string]any)
		if record["revision"] != json.Number(strconv.Itoa(want)) || container["image"] != image || record["networkMode"] != "awsvpc" {
			t.Fatalf("want revision %d with image %s, got %v", want, image, record)
		}
		return record["taskDefinitionArn"].(string)
	}
	describe := func(name string) reply {
		return h.json1("ecs", "DescribeTaskDefinition", map[string]any{"taskDefinition": name})
	}
	arn1 := task(h.json1("ecs", "RegisterTaskDefinition", revisionInput("web", first)), 1, first)
	// The previous release used the bare family as the SQLite primary key.
	if _, err := h.db.Exec("UPDATE aws_catalog_resources SET id='web' WHERE arn=?", arn1); err != nil {
		t.Fatal(err)
	}
	arn2 := task(h.json1("ecs", "RegisterTaskDefinition", revisionInput("web", second)), 2, second)
	restarted, err := New(h.db, h.engine.cfg, []*Service{ecs})
	if err != nil {
		t.Fatal(err)
	}
	h.engine = restarted
	task(describe("web"), 2, second)
	task(describe("web:1"), 1, first)
	task(describe(arn2), 2, second)
	for _, id := range []string{"web:0", "web:-1", "web:3", strings.Replace(arn2, "000000000000", "111111111111", 1), strings.Replace(arn2, "us-east-1", "ap-northeast-1", 1)} {
		expect(t, describe(id), 400, "ClientException")
	}
	deregister := func(id string) reply {
		return h.json1("ecs", "DeregisterTaskDefinition", map[string]any{"taskDefinition": id})
	}
	expect(t, deregister("web"), 400, "ClientException")
	if r := deregister(arn2); r.status != 200 || r.body["taskDefinition"].(map[string]any)["status"] != "INACTIVE" {
		t.Fatalf("deregister %v", r)
	}
	expect(t, describe(arn2), 400, "ClientException")
	task(describe("web"), 1, first)
	expect(t, deregister("web:1"), 200, "")
	expect(t, describe("web"), 400, "ClientException")
	task(h.json1("ecs", "RegisterTaskDefinition", revisionInput("web", first)), 3, first)
	for _, op := range []string{"CreateService", "UpdateService", "DescribeServices", "RunTask"} {
		expect(t, h.json1("ecs", op, map[string]any{}), 501, "UnknownOperationException")
	}
}

func TestECSRegistrationAuthorizesRevisionRolesAndTagsBeforeStoring(t *testing.T) {
	h := newHarness(t, ecs)
	in := revisionInput("worker", "example/app:v1")
	role := "arn:aws:iam::000000000000:role/task"
	in["executionRoleArn"] = "task"
	in["tags"] = []any{map[string]any{"key": "tenant", "value": "local"}}
	firstARN := "arn:aws:ecs:us-east-1:000000000000:task-definition/worker:1"
	for _, denied := range []string{"ecs:RegisterTaskDefinition " + firstARN, "iam:PassRole " + role, "ecs:TagResource " + firstARN} {
		h.policy.deny[denied] = true
		r := h.json1("ecs", "RegisterTaskDefinition", in)
		if r.status < 400 || r.status >= 500 || r.header.Get("X-Simulator-IAM-Decision") != "explicit-deny" {
			t.Fatalf("expected denial of %s: %v", denied, r)
		}
		delete(h.policy.deny, denied)
	}
	if r := h.json1("ecs", "RegisterTaskDefinition", in); r.status != 200 || r.body["taskDefinition"].(map[string]any)["taskDefinitionArn"] != firstARN {
		t.Fatalf("denied operations consumed revision %v", r)
	}
}

func TestECSConcurrentRegistrationNeverReusesARevision(t *testing.T) {
	h := newHarness(t, ecs)
	const n = 12
	responses := make(chan reply, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			responses <- h.json1("ecs", "RegisterTaskDefinition", revisionInput("worker", fmt.Sprintf("example/app:v%d", i)))
		}(i)
	}
	seen := []int{}
	for range n {
		r := <-responses
		expect(t, r, 200, "")
		n, err := r.body["taskDefinition"].(map[string]any)["revision"].(json.Number).Int64()
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, int(n))
	}
	slices.Sort(seen)
	for i, v := range seen {
		if v != i+1 {
			t.Fatalf("unexpected revisions: %v", seen)
		}
	}
}

func TestECSListsRevisionsInFamilyAndRevisionOrder(t *testing.T) {
	h := newHarness(t, ecs)
	for _, family := range []string{"z", "a", "z", "a"} {
		expect(t, h.json1("ecs", "RegisterTaskDefinition", revisionInput(family, "example/app:v1")), 200, "")
	}
	check := func(input map[string]any, wanted ...string) string {
		t.Helper()
		r := h.json1("ecs", "ListTaskDefinitions", input)
		expect(t, r, 200, "")
		arns := r.body["taskDefinitionArns"].([]any)
		var names []string
		for _, arn := range arns {
			names = append(names, strings.TrimPrefix(arn.(string), "arn:aws:ecs:us-east-1:000000000000:task-definition/"))
		}
		if !reflect.DeepEqual(wanted, names) {
			t.Fatalf("want %v, got %v", wanted, names)
		}
		token, _ := r.body["nextToken"].(string)
		return token
	}
	token := check(map[string]any{"maxResults": 2}, "a:1", "a:2")
	if token == "" {
		t.Fatal("missing cursor")
	}
	expect(t, h.json1("ecs", "DeregisterTaskDefinition", map[string]any{"taskDefinition": "a:2"}), 200, "")
	check(map[string]any{"nextToken": token, "maxResults": 3}, "z:1", "z:2")
	check(map[string]any{"sort": "DESC"}, "z:2", "z:1", "a:1")
	check(map[string]any{"familyPrefix": "z"}, "z:1", "z:2")
	check(map[string]any{"status": "INACTIVE"})
}
