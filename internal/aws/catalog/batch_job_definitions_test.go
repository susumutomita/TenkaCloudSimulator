package catalog

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
)

func batchDefinitionInput(name, image string) map[string]any {
	return map[string]any{
		"jobDefinitionName": name, "type": "container",
		"containerProperties": map[string]any{"image": image, "resourceRequirements": []any{
			map[string]any{"type": "MEMORY", "value": "512"}, map[string]any{"type": "VCPU", "value": "1"},
		}},
	}
}

func TestBatchJobDefinitionMetadataLifecycle(t *testing.T) {
	h := newHarness(t, batch)
	const family = "metadata"
	const familyARN = "arn:aws:batch:us-east-1:000000000000:job-definition/" + family
	request := func(operation string, input map[string]any, status int, code string) reply {
		t.Helper()
		r := h.rest("batch", "POST", "/v1/"+operation, input)
		expect(t, r, status, code)
		return r
	}
	described := func(input map[string]any) []any {
		t.Helper()
		return request("describejobdefinitions", input, 200, "").body["jobDefinitions"].([]any)
	}
	register := func(revision string) string {
		t.Helper()
		r := request("registerjobdefinition", batchDefinitionInput(family, "example.invalid/fixture:"+revision), 200, "")
		if r.body["jobDefinitionName"] != family || r.body["jobDefinitionArn"] != familyARN+":"+revision ||
			fmt.Sprint(r.body["revision"]) != revision || len(r.body) != 3 {
			t.Fatalf("registration is not the native revision response: %+v", r.body)
		}
		return str(r.body["jobDefinitionArn"])
	}
	h.policy.deny["batch:RegisterJobDefinition "+familyARN] = true
	request("registerjobdefinition", batchDefinitionInput(family, "fixture"), 403, "AccessDeniedException")
	delete(h.policy.deny, "batch:RegisterJobDefinition "+familyARN)
	if rows := described(map[string]any{}); len(rows) != 0 {
		t.Fatalf("denied registration wrote state: %v", rows)
	}
	one, two := register("1"), register("2")
	h.policy.deny["batch:DescribeJobDefinitions *"] = true
	request("describejobdefinitions", map[string]any{"jobDefinitions": []any{two}}, 403, "AccessDeniedException")
	delete(h.policy.deny, "batch:DescribeJobDefinitions *")

	page := request("describejobdefinitions", map[string]any{
		"jobDefinitionName": family, "status": "ACTIVE", "maxResults": 1,
	}, 200, "").body
	first := page["jobDefinitions"].([]any)
	cursor := str(page["nextToken"])
	if len(first) != 1 || first[0].(map[string]any)["jobDefinitionArn"] != one || cursor == "" {
		t.Fatalf("unexpected first page: %+v", page)
	}
	continued := request("describejobdefinitions", map[string]any{
		"jobDefinitionName": family, "status": "ACTIVE", "maxResults": 2, "nextToken": cursor,
	}, 200, "").body
	next, ok := continued["jobDefinitions"].([]any)
	if !ok || len(next) != 1 || next[0].(map[string]any)["jobDefinitionArn"] != two || continued["nextToken"] != nil {
		t.Fatalf("unexpected second page: %+v", continued)
	}
	request("describejobdefinitions", map[string]any{"jobDefinitionName": family, "status": "INACTIVE", "nextToken": cursor}, 400, "ClientException")
	if got := described(map[string]any{"jobDefinitions": []any{family + ":1", two, "missing:1"}}); len(got) != 2 {
		t.Fatalf("explicit revision selection changed: %v", got)
	}

	// Existing tag operations still require a stored, exact revision ARN and
	// their own authorization; the family ARN used to create is not a row.
	tags := map[string]any{"tags": map[string]any{"fixture": "kept"}}
	path := func(arn string) string { return "/v1/tags/" + compute3EscapeARN(arn) }
	expect(t, h.rest("batch", "POST", path(familyARN), tags), 400, "ClientException")
	expect(t, h.rest("batch", "GET", path(familyARN+":9"), nil), 400, "ClientException")
	h.policy.deny["batch:TagResource "+two] = true
	expect(t, h.rest("batch", "POST", path(two), tags), 403, "AccessDeniedException")
	expect(t, h.rest("batch", "POST", path(one), tags), 200, "")
	delete(h.policy.deny, "batch:TagResource "+two)

	h.policy.deny["batch:DeregisterJobDefinition "+one] = true
	request("deregisterjobdefinition", map[string]any{"jobDefinition": family + ":1"}, 403, "AccessDeniedException")
	delete(h.policy.deny, "batch:DeregisterJobDefinition "+one)
	if rows := described(map[string]any{"status": "ACTIVE"}); len(rows) != 2 {
		t.Fatalf("unauthorized deregistration changed state: %v", rows)
	}
	request("deregisterjobdefinition", map[string]any{"jobDefinition": family}, 400, "ClientException")
	request("deregisterjobdefinition", map[string]any{"jobDefinition": family + ":9"}, 400, "ClientException")
	if r := request("deregisterjobdefinition", map[string]any{"jobDefinition": family + ":1"}, 200, ""); len(r.body) != 0 {
		t.Fatalf("unexpected deregister response: %s", r.raw)
	}
	before := described(map[string]any{"status": "INACTIVE"})
	if len(before) != 1 {
		t.Fatalf("inactive revision did not remain readable: %v", before)
	}
	inactive := before[0].(map[string]any)
	if inactive["jobDefinitionArn"] != one || inactive["status"] != "INACTIVE" ||
		inactive["type"] != "container" || inactive["containerOrchestrationType"] != "ECS" ||
		!reflect.DeepEqual(inactive["platformCapabilities"], []any{"EC2"}) ||
		!reflect.DeepEqual(inactive["containerProperties"], batchDefinitionInput(family, "example.invalid/fixture:1")["containerProperties"]) ||
		!reflect.DeepEqual(inactive["tags"], map[string]any{"fixture": "kept"}) {
		t.Fatalf("configuration/metadata not preserved after deregistration: %+v", inactive)
	}
	again := request("deregisterjobdefinition", map[string]any{"jobDefinition": one}, 400, "ClientException")
	if !reflect.DeepEqual(described(map[string]any{"status": "INACTIVE"}), before) || again.body["message"] == nil {
		t.Fatal("repeated deregistration did not preserve metadata")
	}
	// Inactive rows retain their inherited metadata tag surface, not a job.
	expect(t, h.rest("batch", "GET", path(one), nil), 200, "")
	expect(t, h.rest("batch", "DELETE", path(one)+"?tagKeys=fixture", nil), 200, "")
	if got := h.rest("batch", "GET", path(one), nil); !reflect.DeepEqual(got.body["tags"], map[string]any{}) {
		t.Fatalf("inactive revision tag cleanup returned: %s", got.raw)
	}
	three := register("3")
	request("deregisterjobdefinition", map[string]any{"jobDefinition": two}, 200, "")
	request("deregisterjobdefinition", map[string]any{"jobDefinition": three}, 200, "")
	if active, inactive := described(map[string]any{"status": "ACTIVE"}), described(map[string]any{"status": "INACTIVE"}); len(active) != 0 || len(inactive) != 3 {
		t.Fatalf("cleanup must be deregistration with history preserved: %v %v", active, inactive)
	}
	for _, check := range []string{
		"simulator-admin batch:RegisterJobDefinition " + familyARN,
		"simulator-admin batch:DescribeJobDefinitions *",
		"simulator-admin batch:DeregisterJobDefinition " + one,
		"simulator-admin batch:TagResource " + one,
		"simulator-admin batch:ListTagsForResource " + one,
		"simulator-admin batch:UntagResource " + one,
	} {
		if !slices.Contains(h.policy.calls, check) {
			t.Fatalf("missing required authorization: %s", check)
		}
	}
}

func TestBatchJobDefinitionRejectsOutOfBoundsFields(t *testing.T) {
	h := newHarness(t, batch)
	for _, bad := range []struct {
		field  string
		value  any
		nested bool
	}{
		{"type", "multinode", false},
		{"jobDefinitionName", "foo:4", false},
		{"platformCapabilities", []any{"FARGATE"}, false},
		{"tags", map[string]any{"tag": "unsupported"}, false},
		{"parameters", map[string]any{"name": "metadata"}, false},
		{"command", []any{"disabled-fixture-command"}, true},
		{"environment", []any{}, true},
		{"privileged", false, true},
		{"jobRoleArn", "arn:aws:iam::000000000000:role/fixture", true},
		{"volumes", []any{}, true},
		{"secrets", []any{}, true},
		{"image", "https://example.invalid/example", true},
		{"image", "example.invalid:5000/repo", true},
		{"resourceRequirements", []any{
			map[string]any{"type": "VCPU", "value": "1"}, map[string]any{"type": "VCPU", "value": "2"},
		}, true},
		{"resourceRequirements", []any{
			map[string]any{"type": "MEMORY", "value": "3"}, map[string]any{"type": "VCPU", "value": "1"},
		}, true},
	} {
		request := batchDefinitionInput("bounded", "image")
		target := request
		if bad.nested {
			target = request["containerProperties"].(map[string]any)
		}
		target[bad.field] = bad.value
		expect(t, h.rest("batch", "POST", "/v1/registerjobdefinition", request), 400, "ClientException")
	}
	for _, input := range []map[string]any{
		{"jobDefinitions": []any{"foo"}},
		{"jobDefinitions": []any{"foo:1"}, "maxResults": 1},
		{"jobDefinitions": []any{}},
		{"jobDefinitions": make([]string, 101)}, // exceeds the exclusive set bound
		{"status": "unknown"},
		{"jobDefinitionName": "foo:2"},
		{"maxResults": 0},
		{"maxResults": 101},
		{"nextToken": "invalid"},
	} {
		expect(t, h.rest("batch", "POST", "/v1/describejobdefinitions", input), 400, "ClientException")
	}
	if got := h.rest("batch", "POST", "/v1/describejobdefinitions", map[string]any{}); !reflect.DeepEqual(got.body["jobDefinitions"], []any{}) {
		t.Fatalf("refused registration added metadata: %s", got.raw)
	}
}

func TestBatchJobDefinitionConcurrentRevisions(t *testing.T) {
	h := newHarness(t, batch)
	const count = 6
	replies := make(chan reply, count)
	for i := 0; i < count; i++ {
		go func() {
			replies <- h.rest("batch", "POST", "/v1/registerjobdefinition", batchDefinitionInput("concurrent", "image"))
		}()
	}
	seen := map[string]bool{}
	for i := 0; i < count; i++ {
		result := <-replies
		expect(t, result, 200, "")
		seen[fmt.Sprint(result.body["revision"])] = true
	}
	for i := 1; i <= count; i++ {
		if !seen[strconv.Itoa(i)] {
			t.Fatalf("missing revision %d: %v", i, seen)
		}
	}
	if response := h.rest("batch", "POST", "/v1/describejobdefinitions", map[string]any{"jobDefinitionName": "concurrent"}); response.status != 200 || len(response.body["jobDefinitions"].([]any)) != count {
		t.Fatalf("committed revisions did not all persist: %s", response.raw)
	}
}

func TestBatchJobDefinitionRetainedLimit(t *testing.T) {
	h := newHarness(t, batch)
	for i := 1; i <= batchDefinitionLimit; i++ {
		result := h.rest("batch", "POST", "/v1/registerjobdefinition", batchDefinitionInput("retained", "image"))
		expect(t, result, 200, "")
		if fmt.Sprint(result.body["revision"]) != strconv.Itoa(i) {
			t.Fatalf("unexpected revision %d: %s", i, result.raw)
		}
	}
	expect(t, h.rest("batch", "POST", "/v1/deregisterjobdefinition", map[string]any{"jobDefinition": "retained:1"}), 200, "")
	refused := h.rest("batch", "POST", "/v1/registerjobdefinition", batchDefinitionInput("another-family", "image"))
	expect(t, refused, 400, "ClientException")
	if refused.body["message"] != "The simulator job definition revision limit for this region is exhausted." {
		t.Fatalf("unexpected retained quota refusal: %s", refused.raw)
	}
	rows := h.rest("batch", "POST", "/v1/describejobdefinitions", map[string]any{})
	expect(t, rows, 200, "")
	if got := len(rows.body["jobDefinitions"].([]any)); got != batchDefinitionLimit {
		t.Fatalf("quota refusal changed the persisted revision count: %d", got)
	}
}

func TestBatchJobDefinitionLeavesWorkloadRoutesUnbound(t *testing.T) {
	h := newHarness(t, batch)
	model := h.binding("batch").model
	// Feed empty test data into the in-process HTTP handler. Route resolution
	// refuses each unimplemented official operation before any actual job work.
	for _, operation := range []string{
		"CancelJob", "DescribeJobs", "DescribeServiceJob", "GetJobQueueSnapshot", "ListJobs",
		"ListJobsByConsumableResource", "ListServiceJobs", "SubmitJob", "SubmitServiceJob",
		"TerminateJob", "TerminateServiceJob", "UpdateConsumableResource",
	} {
		http, ok := model.Routes[operation]
		if !ok || http.HTTP.Method == "" {
			t.Fatalf("missing pinned official route for %s", operation)
		}
		expect(t, h.rest("batch", http.HTTP.Method, http.HTTP.RequestURI, map[string]any{}), 501, "UnknownOperationException")
	}
}
