package catalog

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestResourceGroupsCreateAndListUseServiceIAMTarget(t *testing.T) {
	const prefix = "arn:aws:resource-groups:us-east-1:000000000000:group/"
	for _, test := range []struct {
		name, operation, route, target string
		withGroup, deny                bool
	}{
		{"named create absent", "CreateGroup", "/groups", prefix + "owned", false, false},
		{"named create existing", "CreateGroup", "/groups", prefix + "owned", true, false},
		{"kind wildcard create", "CreateGroup", "/groups", prefix + "*", false, false},
		{"kind wildcard list empty", "ListGroups", "/groups-list", prefix + "*", false, false},
		{"kind wildcard list populated", "ListGroups", "/groups-list", prefix + "*", true, false},
		{"named list populated", "ListGroups", "/groups-list", prefix + "owned", true, false},
		{"service deny create", "CreateGroup", "/groups", "*", false, true},
		{"service deny list", "ListGroups", "/groups-list", "*", true, true},
		{"service allow create", "CreateGroup", "/groups", "*", false, false},
		{"service allow list", "ListGroups", "/groups-list", "*", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, resourceGroups)
			input := map[string]any{"Name": "owned", "Tags": map[string]any{"Team": "original"}}
			if test.withGroup {
				expect(t, h.rest("resource-groups", "POST", "/groups", input), 200, "")
			}
			before := groupCatalogFrame(t, h.db)
			action := "resource-groups:" + test.operation
			statement := core.Statement{Effect: "Allow", Action: core.StringList{action}, Resource: core.StringList{test.target}}
			policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{statement}}
			if test.deny {
				statement.Effect = "Deny"
				policy.Statement = append(policy.Statement, statement)
			}
			var targets []string
			h.engine.cfg.Authorize = func(_ context.Context, _ string, requested, resource string) (string, error) {
				targets = append(targets, requested+" "+resource)
				return core.Evaluate(policy, requested, resource), nil
			}
			if test.operation == "ListGroups" {
				input = map[string]any{}
			}
			result := h.rest("resource-groups", "POST", test.route, input)
			decision := core.Evaluate(policy, action, "*")
			if decision == "allowed" {
				expect(t, result, 200, "")
				if test.operation == "ListGroups" && len(result.body["Groups"].([]any)) != 1 {
					t.Fatalf("list-only principal could not see its regional group: %v", result.body)
				}
			} else {
				expect(t, result, 403, "ForbiddenException")
				if result.header.Get("X-Simulator-IAM-Decision") != decision || !reflect.DeepEqual(result.body, map[string]any{"Message": "IAM evaluation: " + decision}) {
					t.Fatalf("refused request disclosed group state or lost its decision: %v", result.body)
				}
			}
			if !reflect.DeepEqual(targets, []string{action + " *"}) {
				t.Fatalf("used policy targets %v", targets)
			}
			if (decision != "allowed" || test.operation == "ListGroups") && !reflect.DeepEqual(before, groupCatalogFrame(t, h.db)) {
				t.Fatal("refused or read-only request changed saved resources or allocation state")
			}
		})
	}
}

func TestResourceGroupUserTagsDoNotCreateApplicationMembership(t *testing.T) {
	for _, kind := range []string{"plain", "query"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, resourceGroups, ecr)
			tags := map[string]any{"Team": "original", "awsApplication": "ordinary-user-value"}
			input := map[string]any{"Name": "tagged-group", "Tags": tags}
			if kind == "query" {
				input["ResourceQuery"] = repositoryTagQuery("Team", "blue")
			}
			group := func(op string, args map[string]any) reply {
				return h.rest("resource-groups", "POST", "/"+op, args)
			}
			plain := func(value any) {
				t.Helper()
				document := value.(map[string]any)
				if _, ok := document["ApplicationTag"]; ok {
					t.Fatalf("ordinary %s group reported application membership: %v", kind, document)
				}
			}
			created := group("groups", input)
			expect(t, created, 200, "")
			plain(created.body["Group"])
			if !reflect.DeepEqual(created.body["Tags"], tags) {
				t.Fatal("group tags missing from creation response")
			}
			arn := created.body["Group"].(map[string]any)["GroupArn"].(string)
			path := "/resources/" + url.PathEscape(arn) + "/tags"
			readTags := func(want map[string]any) {
				t.Helper()
				r := h.rest("resource-groups", "GET", path, nil)
				expect(t, r, 200, "")
				if !reflect.DeepEqual(r.body["Tags"], want) {
					t.Fatalf("GetTags returned %v; want %v", r.body, want)
				}
			}
			readTags(tags)
			expect(t, h.rest("resource-groups", "PATCH", path, map[string]any{"Keys": []any{"awsApplication"}}), 200, "")
			expect(t, h.rest("resource-groups", "PUT", path, map[string]any{"Tags": map[string]any{"Team": "changed"}}), 200, "")
			readTags(map[string]any{"Team": "changed"})
			for _, op := range []string{"get-group", "update-group"} {
				args := map[string]any{"Group": arn}
				if op == "update-group" {
					args["Description"] = "saved"
				}
				result := group(op, args)
				expect(t, result, 200, "")
				plain(result.body["Group"])
			}
			list := group("groups-list", map[string]any{})
			expect(t, list, 200, "")
			plain(list.body["Groups"].([]any)[0])
			deleted := group("delete-group", map[string]any{"Group": arn})
			expect(t, deleted, 200, "")
			plain(deleted.body["Group"])
		})
	}
}
