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

// Kinesis signals more pages with the required HasMoreStreams flag that the
// official paginator follows.
func TestKinesisListStreamsReportsHasMoreStreams(t *testing.T) {
	h := newHarness(t, kinesis)
	for _, name := range []string{"a", "b"} {
		expect(t, h.json1("kinesis", "CreateStream", map[string]any{"StreamName": name, "ShardCount": 1}), 200, "")
	}
	page := h.json1("kinesis", "ListStreams", map[string]any{"Limit": 1})
	if page.body["HasMoreStreams"] != true || len(page.body["StreamNames"].([]any)) != 1 {
		t.Fatalf("first page %v", page.body)
	}
	if last := h.json1("kinesis", "ListStreams", map[string]any{"Limit": 5}); last.body["HasMoreStreams"] != false {
		t.Fatalf("complete page %v", last.body)
	}
}

// Descriptors prefer structured lists and describe identifier-only lists,
// so the console can show ECS clusters and task definitions.
func TestDescriptorsServeStructuredAndIdentifierLists(t *testing.T) {
	h := newHarness(t, ecs)
	lists, describes := map[string]OpDescriptor{}, map[string]OpDescriptor{}
	for _, r := range h.engine.Describe()[0].Resources {
		lists[r.Kind], describes[r.Kind] = r.Ops["list"], r.Ops["describe"]
	}
	if d := describes["cluster"]; d.Name != "DescribeClusters" || d.Names != "clusters" || d.Items != "clusters" {
		t.Fatalf("cluster describe %+v", d)
	}
	// DescribeClusters needs cluster names, so ListClusters lists them.
	if c := lists["cluster"]; c.Name != "ListClusters" || c.ValueItems != "clusterArns" || c.ValueMember != "clusterArn" {
		t.Fatalf("cluster list %+v", c)
	}
	if d := lists["taskDefinition"]; d.Name != "ListTaskDefinitions" || d.ValueItems != "taskDefinitionArns" || d.ValueMember != "taskDefinitionArn" {
		t.Fatalf("task definition list %+v", d)
	}
}

// A REST payload member is documented as the whole body, as restInput and
// writeOutput treat it (Pinpoint CreateApp sends CreateApplicationRequest).
func TestReferenceDocumentsPayloadMembersAsWholeBodies(t *testing.T) {
	h := newHarness(t, pinpoint)
	doc := h.engine.OpenAPI()["pinpoint"]
	create := doc["paths"].(map[string]any)["/v1/apps"].(map[string]any)["post"].(map[string]any)
	schema := create["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if schema["$ref"] != "#/components/schemas/CreateApplicationRequest" {
		t.Fatalf("request body %v", schema)
	}
	response := create["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if response["$ref"] != "#/components/schemas/ApplicationResponse" {
		t.Fatalf("response body %v", response)
	}
}

// List descriptors name their own item identity keys: Athena summaries use
// CatalogName and IoT thing-group summaries use groupName/groupArn.
func TestListDescriptorsNameSummaryIdentityKeys(t *testing.T) {
	h := newHarness(t, athena, iotCore)
	lists := map[string]OpDescriptor{}
	for _, d := range h.engine.Describe() {
		for _, r := range d.Resources {
			lists[d.Model+"/"+r.Kind] = r.Ops["list"]
		}
	}
	if l := lists["athena/dataCatalog"]; l.ItemName != "CatalogName" {
		t.Fatalf("athena list %+v", l)
	}
	if l := lists["iot/thingGroup"]; l.ItemName != "groupName" || l.ItemARN != "groupArn" {
		t.Fatalf("thing group list %+v", l)
	}
	if l := lists["iot/securityProfile"]; l.ItemName != "name" || l.ItemARN != "arn" {
		t.Fatalf("security profile list %+v", l)
	}
}

// A pricing-rule type change keeps the stored rule consistent: the other
// type's configuration is dropped and the new type's must be present.
func TestPricingRuleUpdatesValidateTheMergedRule(t *testing.T) {
	h := newHarness(t, billingConductor)
	created := h.rest("billingconductor", "POST", "/create-pricing-rule", map[string]any{"Name": "markup", "Scope": "GLOBAL", "Type": "MARKUP", "ModifierPercentage": 10})
	expect(t, created, 200, "")
	arn := str(created.body["Arn"])
	expect(t, h.rest("billingconductor", "PUT", "/update-pricing-rule", map[string]any{"Arn": arn, "Type": "TIERING"}), 400, "ValidationException")
	tiered := h.rest("billingconductor", "PUT", "/update-pricing-rule", map[string]any{"Arn": arn, "Type": "TIERING", "Tiering": map[string]any{"FreeTier": map[string]any{"Activated": true}}})
	expect(t, tiered, 200, "")
	if _, stale := tiered.body["ModifierPercentage"]; stale || tiered.body["Tiering"] == nil {
		t.Fatalf("type transition left %v", tiered.body)
	}
	listed := h.rest("billingconductor", "POST", "/list-pricing-rules", map[string]any{}).body["PricingRules"].([]any)[0].(map[string]any)
	if _, stale := listed["ModifierPercentage"]; stale || listed["Type"] != "TIERING" {
		t.Fatalf("stored rule %v", listed)
	}
	expect(t, h.rest("billingconductor", "PUT", "/update-pricing-rule", map[string]any{"Arn": arn, "Type": "DISCOUNT"}), 400, "ValidationException")
	expect(t, h.rest("billingconductor", "PUT", "/update-pricing-rule", map[string]any{"Arn": arn, "Type": "DISCOUNT", "ModifierPercentage": 5}), 200, "")
}

// Budget updates keep one limit representation in the stored budget.
func TestBudgetUpdatesKeepOneLimitRepresentation(t *testing.T) {
	h := newHarness(t, budgets)
	budget := map[string]any{"BudgetName": "monthly", "BudgetType": "COST", "TimeUnit": "MONTHLY", "BudgetLimit": map[string]any{"Amount": "100", "Unit": "USD"}}
	expect(t, h.json1("budgets", "CreateBudget", map[string]any{"AccountId": "000000000000", "Budget": budget}), 200, "")
	planned := map[string]any{"BudgetName": "monthly", "BudgetType": "COST", "TimeUnit": "MONTHLY",
		"PlannedBudgetLimits": map[string]any{"1798761600": map[string]any{"Amount": "120", "Unit": "USD"}}}
	expect(t, h.json1("budgets", "UpdateBudget", map[string]any{"AccountId": "000000000000", "NewBudget": planned}), 200, "")
	stored := h.json1("budgets", "DescribeBudget", map[string]any{"AccountId": "000000000000", "BudgetName": "monthly"}).body["Budget"].(map[string]any)
	if _, stale := stored["BudgetLimit"]; stale || stored["PlannedBudgetLimits"] == nil {
		t.Fatalf("budget kept both limits: %v", stored)
	}
}

// A partial MWAA update cannot raise MinWorkers above the stored MaxWorkers.
func TestAirflowUpdatesValidateMergedCapacity(t *testing.T) {
	h := newHarness(t, mwaa)
	expect(t, h.rest("mwaa", "PUT", "/environments/etl", map[string]any{"ExecutionRoleArn": "arn:aws:iam::000000000000:role/mwaa-execution",
		"SourceBucketArn": "arn:aws:s3:::fixture-dags", "DagS3Path": "dags", "NetworkConfiguration": map[string]any{"SubnetIds": []any{"subnet-0a", "subnet-0b"}, "SecurityGroupIds": []any{"sg-0a"}},
		"MaxWorkers": 5, "MinWorkers": 1}), 200, "")
	expect(t, h.rest("mwaa", "PATCH", "/environments/etl", map[string]any{"MinWorkers": 6}), 400, "ValidationException")
	expect(t, h.rest("mwaa", "PATCH", "/environments/etl", map[string]any{"MinWorkers": 4}), 200, "")
}

func TestNumberReadsRequestAndStoredValues(t *testing.T) {
	for _, v := range []any{int64(3), 3.0, json.Number("3")} {
		if n, ok := Number(v); !ok || n != 3 {
			t.Fatalf("%T %v", v, v)
		}
	}
	if _, ok := Number("3"); ok {
		t.Fatal("text is not a number")
	}
	if _, ok := Number(json.Number("x")); ok {
		t.Fatal("a malformed number is not a number")
	}
}
