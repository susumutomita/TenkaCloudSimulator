package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func repositoryTagQuery(key, value string) map[string]any {
	return map[string]any{"Type": "TAG_FILTERS_1_0", "Query": fmt.Sprintf(`{"ResourceTypeFilters":["AWS::ECR::Repository"],"TagFilters":[{"Key":%q,"Values":[%q]}]}`, key, value)}
}

func TestNamedGroupQueryTracksOnlyRealLocalECRResources(t *testing.T) {
	h := newHarness(t, resourceGroups, ecr)
	h.engine.cfg.AuthorizeSnapshot = func(context.Context, string) (func(string, string) string, error) {
		refused := maps.Clone(h.policy.deny)
		return func(action, arn string) string {
			if refused[action+" "+arn] {
				return "explicitDeny"
			}
			return "allowed"
		}, nil
	}
	group := func(op string, input map[string]any) reply {
		t.Helper()
		return h.rest("resource-groups", "POST", "/"+op, input)
	}
	query := repositoryTagQuery("Team", "blue")
	green := repositoryTagQuery("Team", "green")
	r := group("groups", map[string]any{"Name": "team-query", "ResourceQuery": query})
	expect(t, r, 200, "")
	arn := str(r.body["Group"].(map[string]any)["GroupArn"])
	if !reflect.DeepEqual(r.body["ResourceQuery"], query) {
		t.Fatal(r.body)
	}
	q := func(name string) map[string]any { return map[string]any{"Group": name} }
	expect(t, group("get-group-query", q("team-query")), 200, "")
	if got := group("get-group", q(arn)); got.body["Group"].(map[string]any)["ResourceQuery"] != nil {
		t.Fatal("query leaked in Group")
	}
	create := func(name, value string) string {
		t.Helper()
		r := h.json1("ecr", "CreateRepository", map[string]any{"repositoryName": name, "tags": []any{map[string]any{"Key": "Team", "Value": value}}})
		expect(t, r, 200, "")
		return str(r.body["repository"].(map[string]any)["repositoryArn"])
	}
	one, two := create("repo-one", "blue"), create("repo-two", "blue")
	create("repo-three", "green")
	listMembers := func(input map[string]any) reply { t.Helper(); return group("list-group-resources", input) }
	members := func(r reply, want ...string) {
		t.Helper()
		expect(t, r, 200, "")
		list := r.body["Resources"].([]any)
		ids := r.body["ResourceIdentifiers"].([]any)
		if len(list) != len(want) || len(ids) != len(want) {
			t.Fatalf("members = %v; want %v", r.body, want)
		}
		for i, arn := range want {
			item := list[i].(map[string]any)
			id := map[string]any{"ResourceArn": arn, "ResourceType": ecrRepositoryType}
			if len(item) != 1 || !reflect.DeepEqual(item["Identifier"], id) || !reflect.DeepEqual(ids[i], id) {
				t.Fatalf("invalid member %v", item)
			}
		}
	}
	members(listMembers(q(arn)), one, two)
	members(listMembers(map[string]any{"Group": arn, "MaxResults": 50}), one, two)
	for _, size := range []int{0, 51} {
		expect(t, listMembers(map[string]any{"Group": arn, "MaxResults": size}), 400, "BadRequestException")
	}
	page := listMembers(map[string]any{"Group": arn, "MaxResults": 1})
	members(page, one)
	token := str(page.body["NextToken"])
	if token == "" {
		t.Fatal("missing continuation")
	}
	members(listMembers(map[string]any{"Group": arn, "NextToken": token}), two)
	for _, action := range []string{"resource-groups:ListGroupResources " + arn, "tag:GetResources *", "cloudformation:DescribeStacks *", "cloudformation:ListStackResources *"} {
		h.policy.deny[action] = true
		refused := listMembers(map[string]any{"Group": arn, "NextToken": token})
		if action == "tag:GetResources *" {
			expect(t, listMembers(q("absent")), 403, "ForbiddenException")
		}
		expect(t, refused, 403, "ForbiddenException")
		if refused.body["Resources"] != nil {
			t.Fatal("denied membership leaked")
		}
		delete(h.policy.deny, action)
	}
	expect(t, listMembers(map[string]any{"Group": arn, "Filters": []any{map[string]any{"Name": "resource-type", "Values": []any{ecrRepositoryType}}}}), 400, "BadRequestException")
	expect(t, listMembers(map[string]any{"Group": arn, "NextToken": "arbitrary"}), 400, "BadRequestException")
	expect(t, listMembers(q("absent")), 404, "NotFoundException")
	expect(t, h.json1("ecr", "UntagResource", map[string]any{"resourceArn": one, "tagKeys": []string{"Team"}}), 200, "")
	members(listMembers(q(arn)), two)
	expect(t, h.json1("ecr", "TagResource", map[string]any{"resourceArn": one, "tags": []any{map[string]any{"Key": "Team", "Value": "blue"}}}), 200, "")
	members(listMembers(q(arn)), one, two)
	expect(t, h.json1("ecr", "DeleteRepository", map[string]any{"repositoryName": "repo-two"}), 200, "")
	members(listMembers(q(arn)), one)
	updated := group("update-group-query", map[string]any{"Group": arn, "ResourceQuery": green})
	expect(t, updated, 200, "")
	expected := map[string]any{"GroupName": "team-query", "ResourceQuery": green}
	if !reflect.DeepEqual(updated.body["GroupQuery"], expected) || !reflect.DeepEqual(group("get-group-query", q(arn)).body["GroupQuery"], expected) {
		t.Fatal("query round trip")
	}
	members(listMembers(q(arn)), strings.Replace(one, "repo-one", "repo-three", 1))
	expect(t, listMembers(map[string]any{"Group": arn, "NextToken": token}), 400, "BadRequestException")
	for _, op := range []string{"get-group-query", "list-group-resources"} {
		r := group(op, map[string]any{"GroupName": "team-query"})
		expect(t, r, 400, "BadRequestException")
	}
	expect(t, group("groups", map[string]any{"Name": "plain"}), 200, "")
	expect(t, group("update-group-query", map[string]any{"Group": "plain", "ResourceQuery": green}), 400, "BadRequestException")
	expect(t, group("get-group-query", q("plain")), 400, "BadRequestException")
	expect(t, listMembers(q("plain")), 400, "BadRequestException")
	d := h.binding("resource-groups").describe()
	ops := d.Resources[0].Ops
	if ops["read"].Name != "GetGroup" || ops["update"].Name != "UpdateGroup" || ops["list"].Name != "ListGroups" {
		t.Fatal("primary group descriptors changed")
	}
	for _, name := range []string{"team-query", "plain"} {
		expect(t, group("delete-group", q(name)), 200, "")
	}
	for _, name := range []string{"repo-one", "repo-three"} {
		expect(t, h.json1("ecr", "DeleteRepository", map[string]any{"repositoryName": name}), 200, "")
	}
}

func TestResourceGroupQueryRejectsAmbiguityBeforeMutation(t *testing.T) {
	h := newHarness(t, resourceGroups, ecr)
	valid := repositoryTagQuery("match", "value")
	raw := valid["Query"].(string)
	mutants := []string{strings.Replace(raw, "AWS::ECR::Repository", "AWS::AllSupported", 1), strings.Replace(raw, "AWS::ECR::Repository", "AWS::S3::Bucket", 1), strings.Replace(raw, `"match"`, `"match","Key":"other"`, 1), strings.Replace(raw, `["value"]`, `["value","value"]`, 1), strings.Replace(raw, `["value"]`, `[]`, 1), strings.Replace(raw, `"Key":`, `"key":`, 1), strings.Replace(raw, `"match"`, `""`, 1), strings.Replace(raw, `"value"`, `""`, 1), strings.Replace(raw, `"Values":`, `"Other":0,"Values":`, 1), strings.Replace(raw, `"TagFilters":`, `"TagFilters":[],"TagFilters":`, 1), strings.Replace(raw, `"TagFilters":`, `"Unknown":[],"TagFilters":`, 1), raw + ` {}`, `{`, strings.Repeat("[", 26) + strings.Repeat("]", 26)}
	expect(t, h.rest("resource-groups", "POST", "/groups", map[string]any{"Name": "stable", "ResourceQuery": valid}), 200, "")
	for _, m := range mutants {
		q := map[string]any{"Type": "TAG_FILTERS_1_0", "Query": m}
		expect(t, h.rest("resource-groups", "POST", "/groups", map[string]any{"Name": "refused", "ResourceQuery": q}), 400, "BadRequestException")
		expect(t, h.rest("resource-groups", "POST", "/update-group-query", map[string]any{"Group": "stable", "ResourceQuery": q}), 400, "BadRequestException")
	}
	wire, _ := json.Marshal(raw)
	req := httptest.NewRequest("POST", "/groups", strings.NewReader(`{"Name":"refused","ResourceQuery":{"Type":"TAG_FILTERS_1_0","Type":"TAG_FILTERS_1_0","Query":`+string(wire)+`}}`))
	sign(req, "resource-groups", "us-east-1")
	expect(t, h.do(req), 400, "BadRequestException")
	unchanged := h.rest("resource-groups", "POST", "/get-group-query", map[string]any{"Group": "stable"})
	expect(t, unchanged, 200, "")
	if !reflect.DeepEqual(unchanged.body["GroupQuery"].(map[string]any)["ResourceQuery"], valid) {
		t.Fatal("refusal changed query")
	}
	expect(t, h.rest("resource-groups", "POST", "/get-group", map[string]any{"Group": "refused"}), 404, "NotFoundException")
}

func TestResourceGroupMembershipRequiresWorkingPolicySnapshot(t *testing.T) {
	h := newHarness(t, resourceGroups, ecr)
	group := map[string]any{"Group": "snapshot-group"}
	expect(t, h.rest("resource-groups", "POST", "/groups", map[string]any{"Name": "snapshot-group", "ResourceQuery": repositoryTagQuery("Tag", "present")}), 200, "")
	for _, snapshot := range []func(context.Context, string) (func(string, string) string, error){
		nil,
		func(context.Context, string) (func(string, string) string, error) { return nil, context.Canceled },
		func(context.Context, string) (func(string, string) string, error) { return nil, nil },
	} {
		h.engine.cfg.AuthorizeSnapshot = snapshot
		reply := h.rest("resource-groups", "POST", "/list-group-resources", group)
		if reply.status != 500 || reply.body["Resources"] != nil || reply.body["ResourceIdentifiers"] != nil {
			t.Fatal("membership requires a valid policy snapshot")
		}
	}
}

func TestResourceGroupMembershipRefusesIncompleteReads(t *testing.T) {
	h := newHarness(t, resourceGroups, ecr)
	h.engine.cfg.AuthorizeSnapshot = func(context.Context, string) (func(string, string) string, error) {
		return func(string, string) string { return "allowed" }, nil
	}
	query := repositoryTagQuery("Team", "blue")
	input := map[string]any{"Group": "release"}
	expect(t, h.rest("resource-groups", "POST", "/groups", map[string]any{"Name": "release", "ResourceQuery": query}), 200, "")
	for _, name := range []string{"first", "second"} {
		expect(t, h.json1("ecr", "CreateRepository", map[string]any{"repositoryName": name, "tags": []any{map[string]any{"Key": "Team", "Value": "blue"}}}), 200, "")
	}
	refused := func(want int) {
		t.Helper()
		got := h.rest("resource-groups", "POST", "/list-group-resources", input)
		if got.status != want || got.body["Resources"] != nil || got.body["ResourceIdentifiers"] != nil || got.body["NextToken"] != nil {
			t.Fatalf("incomplete projection returned status %d with body %v", got.status, got.body)
		}
	}
	healthy := func() {
		t.Helper()
		got := h.rest("resource-groups", "POST", "/list-group-resources", input)
		expect(t, got, 200, "")
		if len(got.body["Resources"].([]any)) != 2 || len(got.body["ResourceIdentifiers"].([]any)) != 2 {
			t.Fatal("both repositories must match before and after a storage failure")
		}
	}
	healthy()
	// Losing the ECR binding must not turn membership into an empty success.
	const target = "AmazonEC2ContainerRegistry_V20150921"
	binding := h.engine.byTarget[target]
	delete(h.engine.byTarget, target)
	refused(400)
	h.engine.byTarget[target] = binding
	healthy()

	// Each case starts with both repositories readable. A refused read neither
	// repairs the stored value nor returns the earlier matching repository.
	for _, tc := range []struct{ name, read, write, bad string }{
		{"unreadable second repository", "SELECT tags FROM aws_catalog_resources WHERE service='ecr' AND name='second'", "UPDATE aws_catalog_resources SET tags=? WHERE service='ecr' AND name='second'", "not-json"},
		{"invalid stored group query", "SELECT document FROM aws_catalog_resources WHERE service='resource-groups' AND name='release'", "UPDATE aws_catalog_resources SET document=? WHERE service='resource-groups' AND name='release'", `{"Name":"release","ResourceQuery":{"Type":"TAG_FILTERS_1_0","Query":"[]"}}`},
		{"mismatched stored group ARN", "SELECT arn FROM aws_catalog_resources WHERE service='resource-groups' AND name='release'", "UPDATE aws_catalog_resources SET arn=? WHERE service='resource-groups' AND name='release'", "different"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var original, after string
			if err := h.db.QueryRow(tc.read).Scan(&original); err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.Exec(tc.write, tc.bad); err != nil {
				t.Fatal(err)
			}
			refused(500)
			if err := h.db.QueryRow(tc.read).Scan(&after); err != nil || after != tc.bad {
				t.Fatal("refused read changed stored data", err)
			}
			if _, err := h.db.Exec(tc.write, original); err != nil {
				t.Fatal(err)
			}
			healthy()
		})
	}
	expect(t, h.rest("resource-groups", "POST", "/get-group-query", map[string]any{"Group": "absent"}), 404, "NotFoundException")
	expect(t, h.rest("resource-groups", "POST", "/update-group-query", map[string]any{"GroupName": "release", "ResourceQuery": query}), 400, "BadRequestException")
}
