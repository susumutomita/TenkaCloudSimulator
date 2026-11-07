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

// Lookups name the identity they carry, and REST payload outputs are not
// wrapped: AppConfig reads by ApplicationId, App Mesh returns the mesh body.
func TestDescriptorsNameLookupIdentityAndPayloadOutputs(t *testing.T) {
	h := newHarness(t, appConfig, appMesh)
	ops := map[string]OpDescriptor{}
	for _, d := range h.engine.Describe() {
		for _, r := range d.Resources {
			for kind, op := range r.Ops {
				ops[d.Model+"/"+r.Kind+"/"+kind] = op
			}
		}
	}
	read := ops["appconfig/application/read"]
	if read.LookupKey != "id" {
		t.Fatalf("appconfig read %+v", read)
	}
	for _, m := range read.Members {
		if m.Name == "ApplicationId" && m.Pattern == "" {
			t.Fatal("the lookup member must carry its official pattern")
		}
	}
	if mesh := ops["appmesh/mesh/read"]; mesh.Wrapper != "" {
		t.Fatalf("payload output must not be wrapped: %+v", mesh)
	}
	for lookup, want := range map[string]string{"ResourceArn": "arn", "Name": "name", "": ""} {
		if got := lookupKey(lookup, &Resource{}); got != want {
			t.Fatalf("lookupKey(%q) = %q, want %q", lookup, got, want)
		}
	}
}

// EFS pages continue from NextMarker, and a request Marker continues from the
// output's NextMarker even when no official paginator names it.
func TestMarkerPagesContinueFromNextMarker(t *testing.T) {
	h := newHarness(t, efs)
	for _, token := range []string{"t1", "t2"} {
		expect(t, h.rest("efs", "POST", "/2015-02-01/file-systems", map[string]any{"CreationToken": token}), 201, "")
	}
	first := h.rest("efs", "GET", "/2015-02-01/file-systems?MaxItems=1", nil)
	expect(t, first, 200, "")
	next := str(first.body["NextMarker"])
	if len(first.body["FileSystems"].([]any)) != 1 || next == "" || first.body["Marker"] != nil {
		t.Fatalf("first page %v", first.body)
	}
	second := h.rest("efs", "GET", "/2015-02-01/file-systems?MaxItems=1&Marker="+url.QueryEscape(next), nil)
	expect(t, second, 200, "")
	if len(second.body["FileSystems"].([]any)) != 1 || second.body["NextMarker"] != nil {
		t.Fatalf("second page %v", second.body)
	}

	model := &Model{Shapes: map[string]*Shape{
		"In":  {Type: "structure", Members: map[string]Member{"Marker": {Shape: "S"}, "MaxItems": {Shape: "S"}}},
		"Out": {Type: "structure", Members: map[string]Member{"Marker": {Shape: "S"}, "NextMarker": {Shape: "S"}}},
		"S":   {Type: "string"},
	}}
	q := &request{b: &binding{model: model}, official: &Operation{Input: "In", Output: "Out"}}
	if in, out, limit := q.pagination(); in != "Marker" || out != "NextMarker" || limit != "MaxItems" {
		t.Fatalf("pagination() = %q, %q, %q", in, out, limit)
	}
	model.Shapes["Out"].Members = map[string]Member{"Marker": {Shape: "S"}}
	if in, out, _ := q.pagination(); in != "Marker" || out != "Marker" {
		t.Fatalf("pagination() without NextMarker = %q, %q", in, out)
	}
}

// Leaving the provisioned throughput mode drops the provisioned throughput,
// and throughput alone cannot be added to an elastic or bursting file system.
func TestEFSThroughputUpdatesValidateTheMergedFileSystem(t *testing.T) {
	h := newHarness(t, efs)
	created := h.rest("efs", "POST", "/2015-02-01/file-systems", map[string]any{"CreationToken": "t1", "ThroughputMode": "elastic"})
	expect(t, created, 201, "")
	path := "/2015-02-01/file-systems/" + str(created.body["FileSystemId"])
	expect(t, h.rest("efs", "PUT", path, map[string]any{"ProvisionedThroughputInMibps": 10.0}), 400, "BadRequest")
	expect(t, h.rest("efs", "PUT", path, map[string]any{"ThroughputMode": "provisioned", "ProvisionedThroughputInMibps": 10.0}), 202, "")
	expect(t, h.rest("efs", "PUT", path, map[string]any{"ProvisionedThroughputInMibps": 20.0}), 202, "")
	updated := h.rest("efs", "PUT", path, map[string]any{"ThroughputMode": "elastic"})
	expect(t, updated, 202, "")
	if _, ok := updated.body["ProvisionedThroughputInMibps"]; ok || updated.body["ThroughputMode"] != "elastic" {
		t.Fatalf("elastic update kept provisioned throughput: %v", updated.body)
	}
	listed := h.rest("efs", "GET", "/2015-02-01/file-systems", nil)
	if fs := listed.body["FileSystems"].([]any)[0].(map[string]any); fs["ProvisionedThroughputInMibps"] != nil {
		t.Fatalf("stored file system %v", fs)
	}
}

// Every bound list accepts the tokens it issues: they fit each official
// token member's length limits and pattern.
func TestPageTokensFitEveryOfficialTokenMember(t *testing.T) {
	h := newHarness(t, Services()...)
	token := pageToken("Op", "us-east-1", "scope", 1<<40)
	checked := 0
	for _, b := range h.engine.bindings {
		for name, op := range b.ops {
			if op.Kind != List {
				continue
			}
			q := &request{b: b, op: op, official: b.model.Operations[name]}
			inputToken, _, _ := q.pagination()
			if inputToken == "" {
				continue
			}
			shape := b.model.shape(b.model.shape(q.official.Input).Members[inputToken].Shape)
			if shape.Max != nil && float64(len(token)) > *shape.Max || shape.Min != nil && float64(len(token)) < *shape.Min {
				t.Fatalf("%s %s: token length %d outside [%v, %v]", b.spec.Model, name, len(token), shape.Min, shape.Max)
			}
			if re := compiledPattern(shape.Pattern); shape.Pattern != "" && re != nil && !re.MatchString(token) {
				t.Fatalf("%s %s: token %q does not match %s", b.spec.Model, name, token, shape.Pattern)
			}
			checked++
		}
	}
	if checked < 50 {
		t.Fatalf("only %d list operations checked", checked)
	}
	if after, ok := pageAfter(token, "Op", "us-east-1", "scope"); !ok || after != 1<<40 {
		t.Fatalf("pageAfter(%q) = %d, %v", token, after, ok)
	}
	for _, bad := range []string{token[:31], pageToken("Other", "us-east-1", "scope", 1), pageToken("Op", "us-east-1", "scope", 1)[:16] + "zzzzzzzzzzzzzzzz", pageToken("Op", "us-east-1", "scope", 1)[:16] + "000000000000000A"} {
		if _, ok := pageAfter(bad, "Op", "us-east-1", "scope"); ok {
			t.Fatalf("pageAfter accepted %q", bad)
		}
	}
}
