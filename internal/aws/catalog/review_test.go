package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Credentials nested in auto-branch settings are refused like top-level ones.
func TestAmplifyRefusesNestedBasicAuth(t *testing.T) {
	h := newHarness(t, amplify)
	for _, nested := range []map[string]any{{"basicAuthCredentials": "dXNlcjpwYXNz"}, {"enableBasicAuth": true}} {
		expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": "s", "autoBranchCreationConfig": nested}), 400, "BadRequestException")
	}
	expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": "s", "autoBranchCreationConfig": map[string]any{"stage": "BETA"}}), 200, "")
}

// A $name default fills an omitted member but never replaces a supplied one.
func TestNameDefaultsNeverOverrideSuppliedMembers(t *testing.T) {
	h := newHarness(t, amplify)
	appID := str(h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)["appId"])
	named := h.rest("amplify", "POST", "/apps/"+appID+"/branches", map[string]any{"branchName": "main", "displayName": "Production"})
	expect(t, named, 200, "")
	if got := named.body["branch"].(map[string]any)["displayName"]; got != "Production" {
		t.Fatalf("supplied display name replaced: %v", got)
	}
	if got := h.rest("amplify", "GET", "/apps/"+appID+"/branches/main", nil).body["branch"].(map[string]any)["displayName"]; got != "Production" {
		t.Fatalf("stored display name %v", got)
	}
}

// Tagging requests stay in their regional scope like every other lookup.
func TestTaggingIsRegionScoped(t *testing.T) {
	h := newHarness(t, amplify)
	arn := str(h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)["appArn"])
	path := "/tags/" + url.PathEscape(arn)
	for _, call := range []struct {
		method string
		body   any
	}{{"GET", nil}, {"POST", map[string]any{"tags": map[string]any{"a": "b"}}}} {
		var body []byte
		if call.body != nil {
			body, _ = json.Marshal(call.body)
		}
		req := httptest.NewRequest(call.method, path, bytes.NewReader(body))
		sign(req, "amplify", "ap-northeast-1")
		expect(t, h.do(req), 404, "ResourceNotFoundException")
	}
	expect(t, h.rest("amplify", "GET", path, nil), 200, "")
}

// The insert transaction rechecks the parent read before authorization.
func TestParentIsRecheckedInsideTheInsertTransaction(t *testing.T) {
	h := newHarness(t, amplify)
	app := h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if parentAlive(tx, nil) != nil || parentAlive(tx, &row{Service: "amplify", ARN: str(app["appArn"])}) != nil {
		t.Fatal("a missing or live parent must pass")
	}
	var f *fault
	if err := parentAlive(tx, &row{Service: "amplify", ARN: str(app["appArn"]) + "-gone"}); !errors.As(err, &f) || f.kind != "notfound" {
		t.Fatalf("a deleted parent must be refused: %v", err)
	}
}

// Replacements are charged against the aggregate budget and roll back.
func TestUpdatesRespectTheAggregateMetadataBudget(t *testing.T) {
	h := newHarness(t, amplify)
	appID := str(h.rest("amplify", "POST", "/apps", map[string]any{"name": "site"}).body["app"].(map[string]any)["appId"])
	tx, err := h.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	total, err := storedBytes(tx)
	if err != nil {
		t.Fatal(err)
	}
	filler := `{"x":"` + strings.Repeat("f", int(maxStoredBytes-total)-64) + `"}`
	if _, err := tx.Exec("INSERT INTO aws_catalog_resources("+columns+") VALUES('filler','us-east-1','filler','','f','f','arn:filler',?,'{}',999999,1)", filler); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	expect(t, h.rest("amplify", "POST", "/apps/"+appID, map[string]any{"description": strings.Repeat("d", 200)}), 400, "BadRequestException")
	if got := h.rest("amplify", "GET", "/apps/"+appID, nil).body["app"].(map[string]any)["description"]; got != nil && got != "" {
		t.Fatalf("a refused update must roll back, got %v", got)
	}
	expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": strings.Repeat("n", 200)}), 429, "LimitExceededException")
}

// Budgets authorizes the documented budgets:ModifyBudget/ViewBudget
// actions, not the operation names.
func TestBudgetsUseDocumentedIAMActions(t *testing.T) {
	h := newHarness(t, budgets)
	budget := map[string]any{"BudgetName": "monthly", "BudgetType": "COST", "TimeUnit": "MONTHLY", "BudgetLimit": map[string]any{"Amount": "100", "Unit": "USD"}}
	h.policy.deny["budgets:ModifyBudget"] = true
	expect(t, h.json1("budgets", "CreateBudget", map[string]any{"AccountId": "000000000000", "Budget": budget}), 403, "AccessDeniedException")
	delete(h.policy.deny, "budgets:ModifyBudget")
	expect(t, h.json1("budgets", "CreateBudget", map[string]any{"AccountId": "000000000000", "Budget": budget}), 200, "")
	h.policy.deny["budgets:ViewBudget"] = true
	expect(t, h.json1("budgets", "DescribeBudgets", map[string]any{"AccountId": "000000000000"}), 403, "AccessDeniedException")
	expect(t, h.json1("budgets", "DescribeBudget", map[string]any{"AccountId": "000000000000", "BudgetName": "monthly"}), 403, "AccessDeniedException")
	for _, call := range h.policy.calls {
		if strings.Contains(call, "budgets:CreateBudget") || strings.Contains(call, "budgets:DescribeBudget") {
			t.Fatalf("operation names must not be authorized: %s", call)
		}
	}
}

// First-party descriptors and the reference use serialized wire names, so
// the console sends and reads what SDKs do (Amazon MQ uses lowerCamel keys).
func TestDescriptorsAndReferenceUseWireNames(t *testing.T) {
	h := newHarness(t, amazonMQ)
	d := h.engine.Describe()[0]
	var create, list OpDescriptor
	var res ResourceDescriptor
	for _, r := range d.Resources {
		if op, ok := r.Ops["create"]; ok && r.Ops["list"].Name != "" {
			res, create, list = r, op, r.Ops["list"]
		}
	}
	wire := map[string]string{}
	for _, m := range create.Members {
		wire[m.Name] = m.LocationName
	}
	if wire["Name"] != "name" || wire["EngineType"] != "engineType" {
		t.Fatalf("create members %v", create.Members)
	}
	if list.Items != "configurations" || res.NameWire != "name" || res.ARNWire != "arn" || res.NameMember != "Name" {
		t.Fatalf("list %+v resource %+v", list, res)
	}
	doc, _ := json.Marshal(h.engine.OpenAPI()["mq"])
	if !strings.Contains(string(doc), `"engineType"`) || strings.Contains(string(doc), `"EngineType":{`) {
		t.Fatal("the reference must document wire names")
	}
}
