package aws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type aspHarness struct {
	db     *core.Store
	dynamo *DynamoDBStore
	plans  *AutoScalingPlansStore
	ids    *core.Identities
	g      *Gateway
	clock  time.Time
}

func aspSetup(t *testing.T) *aspHarness {
	t.Helper()
	db, err := core.New(filepath.Join(t.TempDir(), "autoscaling.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dynamo, err := NewDynamoDBStore(db.Database())
	if err != nil {
		t.Fatal(err)
	}
	plans, err := NewAutoScalingPlansStore(db.Database(), dynamo)
	if err != nil {
		t.Fatal(err)
	}
	h := &aspHarness{db: db, dynamo: dynamo, plans: plans, ids: core.NewIdentities(db), clock: time.UnixMilli(1762862400123)}
	plans.now = func() time.Time { return h.clock }
	h.g = &Gateway{AutoScalingPlans: plans, DynamoDB: dynamo, Identities: h.ids}
	return h
}

func (h *aspHarness) table(t *testing.T, region, name, billing string, read, write int64) {
	t.Helper()
	if billing == "PAY_PER_REQUEST" {
		read, write = 0, 0
	}
	_, err := h.dynamo.CreateTable(DynamoDBTable{Region: region, Name: name, KeySchema: []DynamoDBKeyElement{{"id", "HASH"}}, Definitions: map[string]string{"id": "S"}, BillingMode: billing, ReadUnits: read, WriteUnits: write})
	if err != nil {
		t.Fatal(err)
	}
}

func (h *aspHarness) units(t *testing.T, region, name string) (int64, int64) {
	t.Helper()
	table, err := h.dynamo.GetTable(region, name)
	if err != nil {
		t.Fatal(err)
	}
	return table.ReadUnits, table.WriteUnits
}

func aspRequest(op, body string, headers ...string) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", aspTargetPrefix+op)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func (h *aspHarness) call(t *testing.T, op, body string, headers ...string) (int, map[string]any, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	h.g.ServeHTTP(w, aspRequest(op, body, headers...))
	var decoded map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		t.Fatal("response is not JSON:", w.Body.String())
	}
	return w.Code, decoded, w
}

func (h *aspHarness) must(t *testing.T, op, body string, headers ...string) map[string]any {
	t.Helper()
	code, decoded, w := h.call(t, op, body, headers...)
	if code != 200 {
		t.Fatal(op, code, w.Body.String())
	}
	return decoded
}

func (h *aspHarness) fails(t *testing.T, status int, errorType, op, body string, headers ...string) string {
	t.Helper()
	code, decoded, w := h.call(t, op, body, headers...)
	if code != status || decoded["__type"] != errorType || w.Header().Get("X-Amzn-Errortype") != errorType {
		t.Fatalf("%s: got %d %s, want %d %s", op, code, w.Body.String(), status, errorType)
	}
	message, _ := decoded["message"].(string)
	return message
}

func aspInstructionJSON(table, dimension string, low, high int, extra string) string {
	metric := autoScalingReadUtilization
	if dimension == autoScalingWriteDimension {
		metric = autoScalingWriteUtilization
	}
	tracking := `[{"PredefinedScalingMetricSpecification":{"PredefinedScalingMetricType":"` + metric + `"},"TargetValue":70}]`
	if strings.Contains(extra, `"DisableDynamicScaling":true`) {
		tracking = `[]`
	}
	return `{"ServiceNamespace":"dynamodb","ResourceId":"table/` + table + `","ScalableDimension":"` + dimension + `","MinCapacity":` + jsonInt(low) + `,"MaxCapacity":` + jsonInt(high) + `,"TargetTrackingConfigurations":` + tracking + extra + `}`
}

func aspCode(err error) string {
	var failure aspFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	return ""
}

func jsonInt(v int) string { data, _ := json.Marshal(v); return string(data) }

func aspCreateJSON(name, source string, instructions ...string) string {
	return `{"ScalingPlanName":"` + name + `","ApplicationSource":` + source + `,"ScalingInstructions":[` + strings.Join(instructions, ",") + `]}`
}

func aspTags(value string) string {
	return `{"TagFilters":[{"Key":"app","Values":["` + value + `"]}]}`
}

func aspPlans(t *testing.T, h *aspHarness, body string, headers ...string) []any {
	t.Helper()
	plans, _ := h.must(t, "DescribeScalingPlans", body, headers...)["ScalingPlans"].([]any)
	return plans
}

func aspResources(t *testing.T, h *aspHarness, plan string, headers ...string) []any {
	t.Helper()
	resources, _ := h.must(t, "DescribeScalingPlanResources", `{"ScalingPlanName":"`+plan+`","ScalingPlanVersion":1}`, headers...)["ScalingPlanResources"].([]any)
	return resources
}

func TestAutoScalingPlansLifecycleBoundsLocalDynamoDBCapacity(t *testing.T) {
	h := aspSetup(t)
	h.table(t, "us-east-1", "orders", "PROVISIONED", 1, 100)
	h.table(t, "us-east-1", "audit", "PROVISIONED", 10, 10)
	created := h.must(t, "CreateScalingPlan", aspCreateJSON("shop", aspTags("shop"),
		aspInstructionJSON("orders", autoScalingReadDimension, 5, 50, `,"ScalingPolicyUpdateBehavior":"KeepExternalPolicies"`),
		aspInstructionJSON("orders", autoScalingWriteDimension, 1, 20, ""),
		aspInstructionJSON("audit", autoScalingReadDimension, 1, 40, `,"DisableDynamicScaling":true`)))
	if created["ScalingPlanVersion"] != 1.0 || len(created) != 1 {
		t.Fatal(created)
	}
	if read, write := h.units(t, "us-east-1", "orders"); read != 5 || write != 20 {
		t.Fatal("capacity was not bounded to the instruction limits", read, write)
	}
	if read, write := h.units(t, "us-east-1", "audit"); read != 10 || write != 10 {
		t.Fatal("in-range or untargeted capacity changed", read, write)
	}
	plans := aspPlans(t, h, `{}`)
	if len(plans) != 1 {
		t.Fatal(plans)
	}
	plan := plans[0].(map[string]any)
	if plan["ScalingPlanName"] != "shop" || plan["ScalingPlanVersion"] != 1.0 || plan["StatusCode"] != "ActiveWithProblems" || plan["StatusMessage"] != autoScalingPlanProblemsMessage || plan["CreationTime"] != 1762862400.123 || plan["StatusStartTime"] != 1762862400.123 {
		t.Fatal(plan)
	}
	instructions := plan["ScalingInstructions"].([]any)
	first := instructions[0].(map[string]any)
	if len(instructions) != 3 || first["ResourceId"] != "table/orders" || first["MinCapacity"] != 5.0 || first["ScalingPolicyUpdateBehavior"] != "KeepExternalPolicies" {
		t.Fatal(instructions)
	}
	if source := plan["ApplicationSource"].(map[string]any); source["TagFilters"].([]any)[0].(map[string]any)["Key"] != "app" {
		t.Fatal(source)
	}
	resources := aspResources(t, h, "shop")
	if len(resources) != 3 {
		t.Fatal(resources)
	}
	read := resources[0].(map[string]any)
	policies := read["ScalingPolicies"].([]any)
	policy := policies[0].(map[string]any)
	if read["ScalingStatusCode"] != "PartiallyActive" || read["ScalingStatusMessage"] != autoScalingPartialMessage || len(policies) != 1 || policy["PolicyName"] != "ScalingPlan-shop-DynamoDBReadCapacityUtilization" || policy["PolicyType"] != "TargetTrackingScaling" || policy["TargetTrackingConfiguration"].(map[string]any)["TargetValue"] != 70.0 {
		t.Fatal(read)
	}
	audit := resources[2].(map[string]any)
	if audit["ScalingStatusCode"] != "Active" || audit["ScalingStatusMessage"] != autoScalingActiveMessage || audit["ScalingPolicies"] != nil {
		t.Fatal(audit)
	}

	h.clock = h.clock.Add(time.Second)
	h.must(t, "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1,"ScalingInstructions":[`+aspInstructionJSON("orders", autoScalingReadDimension, 8, 9, `,"DisableDynamicScaling":true`)+`]}`)
	if read, write := h.units(t, "us-east-1", "orders"); read != 8 || write != 20 {
		t.Fatal("update must re-apply new limits and keep removed targets unchanged", read, write)
	}
	plan = aspPlans(t, h, `{"ScalingPlanNames":["shop"],"ScalingPlanVersion":1}`)[0].(map[string]any)
	if plan["StatusCode"] != "Active" || plan["StatusMessage"] != nil || plan["StatusStartTime"] != 1762862401.123 || plan["CreationTime"] != 1762862400.123 || len(plan["ScalingInstructions"].([]any)) != 1 {
		t.Fatal(plan)
	}
	h.clock = h.clock.Add(time.Second)
	stack := `{"CloudFormationStackARN":"arn:aws:cloudformation:us-east-1:000000000000:stack/shop/0a1b2c3d"}`
	h.must(t, "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1,"ApplicationSource":`+stack+`}`)
	plan = aspPlans(t, h, `{"ApplicationSources":[`+stack+`]}`)[0].(map[string]any)
	if plan["ApplicationSource"].(map[string]any)["CloudFormationStackARN"] == nil || plan["StatusStartTime"] != 1762862402.123 || len(plan["ScalingInstructions"].([]any)) != 1 {
		t.Fatal(plan)
	}
	if len(aspPlans(t, h, `{"ApplicationSources":[`+aspTags("shop")+`]}`)) != 0 {
		t.Fatal("replaced source still selects the plan")
	}
	h.clock = h.clock.Add(time.Second)
	h.must(t, "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	if aspPlans(t, h, `{}`)[0].(map[string]any)["StatusStartTime"] != 1762862402.123 {
		t.Fatal("an empty update changed the plan")
	}
	if deleted := h.must(t, "DeleteScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`); len(deleted) != 0 {
		t.Fatal(deleted)
	}
	if read, write := h.units(t, "us-east-1", "orders"); read != 8 || write != 20 || len(aspPlans(t, h, `{}`)) != 0 {
		t.Fatal("deletion must keep capacity and remove the plan", read, write)
	}
	h.fails(t, 400, "ObjectNotFoundException", "DeleteScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	h.fails(t, 400, "ObjectNotFoundException", "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	h.fails(t, 400, "ValidationException", "DescribeScalingPlanResources", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	var count int
	if err := h.db.Database().QueryRow(`SELECT count(*) FROM aws_autoscaling_plan_resources`).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted plan left resources", count, err)
	}
}

func TestAutoScalingPlansTargetFailuresRollBackEveryWrite(t *testing.T) {
	h := aspSetup(t)
	h.table(t, "us-east-1", "orders", "PROVISIONED", 1, 1)
	h.table(t, "us-east-1", "ondemand", "PAY_PER_REQUEST", 0, 0)
	check := func(label string, read, write int64, plans int) {
		t.Helper()
		r, w := h.units(t, "us-east-1", "orders")
		var count int
		if err := h.db.Database().QueryRow(`SELECT count(*) FROM aws_autoscaling_plans`).Scan(&count); err != nil || r != read || w != write || count != plans {
			t.Fatal(label, r, w, count, err)
		}
	}
	message := h.fails(t, 400, "ValidationException", "CreateScalingPlan", aspCreateJSON("one", aspTags("one"),
		aspInstructionJSON("orders", autoScalingReadDimension, 5, 9, ""), aspInstructionJSON("missing", autoScalingReadDimension, 1, 9, "")))
	if !strings.Contains(message, "table/missing was not found in us-east-1") {
		t.Fatal(message)
	}
	check("missing target", 1, 1, 0)
	message = h.fails(t, 400, "ValidationException", "CreateScalingPlan", aspCreateJSON("one", aspTags("one"),
		aspInstructionJSON("orders", autoScalingWriteDimension, 5, 9, ""), aspInstructionJSON("ondemand", autoScalingReadDimension, 1, 9, "")))
	if !strings.Contains(message, "on-demand") {
		t.Fatal(message)
	}
	check("on-demand target", 1, 1, 0)
	h.must(t, "CreateScalingPlan", aspCreateJSON("one", aspTags("one"), aspInstructionJSON("orders", autoScalingReadDimension, 2, 9, "")))
	check("first plan", 2, 1, 1)
	message = h.fails(t, 400, "ValidationException", "CreateScalingPlan", aspCreateJSON("two", aspTags("two"),
		aspInstructionJSON("orders", autoScalingWriteDimension, 3, 9, ""), aspInstructionJSON("orders", autoScalingReadDimension, 4, 9, "")))
	if !strings.Contains(message, "already managed by scaling plan one") {
		t.Fatal(message)
	}
	check("target owned by another plan", 2, 1, 1)
	message = h.fails(t, 400, "ValidationException", "CreateScalingPlan", aspCreateJSON("two", `{"TagFilters":[{"Key":"app","Values":["one"]}]}`, aspInstructionJSON("orders", autoScalingWriteDimension, 3, 9, "")))
	if !strings.Contains(message, "only one scaling plan") {
		t.Fatal(message)
	}
	h.fails(t, 400, "ValidationException", "CreateScalingPlan", aspCreateJSON("one", aspTags("other"), aspInstructionJSON("orders", autoScalingWriteDimension, 3, 9, "")))
	check("duplicate source/name", 2, 1, 1)
	h.must(t, "CreateScalingPlan", aspCreateJSON("two", `{"TagFilters":[{"Key":"team","Values":["b","a"]},{"Key":"app"}]}`, aspInstructionJSON("orders", autoScalingWriteDimension, 3, 9, "")))
	check("second plan", 2, 3, 2)
	// Tag filters are order-insensitive for the one-plan-per-source rule.
	h.fails(t, 400, "ValidationException", "UpdateScalingPlan", `{"ScalingPlanName":"one","ScalingPlanVersion":1,"ApplicationSource":{"TagFilters":[{"Key":"app"},{"Key":"team","Values":["a","b"]}]}}`)
	h.fails(t, 400, "ValidationException", "UpdateScalingPlan", `{"ScalingPlanName":"one","ScalingPlanVersion":1,"ApplicationSource":`+aspTags("new")+`,"ScalingInstructions":[`+aspInstructionJSON("orders", autoScalingReadDimension, 6, 9, "")+`,`+aspInstructionJSON("missing", autoScalingReadDimension, 1, 9, "")+`]}`)
	check("failed update", 2, 3, 2)
	if plan := aspPlans(t, h, `{"ScalingPlanNames":["one"]}`)[0].(map[string]any); plan["ApplicationSource"].(map[string]any)["TagFilters"].([]any)[0].(map[string]any)["Values"].([]any)[0] != "one" || plan["ScalingInstructions"].([]any)[0].(map[string]any)["MinCapacity"] != 2.0 {
		t.Fatal("failed update changed the stored plan", plan)
	}
	h.fails(t, 400, "ValidationException", "UpdateScalingPlan", `{"ScalingPlanName":"two","ScalingPlanVersion":1,"ScalingInstructions":[`+aspInstructionJSON("orders", autoScalingReadDimension, 6, 9, "")+`]}`)
	// A plan may replace its own target with new limits.
	h.must(t, "UpdateScalingPlan", `{"ScalingPlanName":"one","ScalingPlanVersion":1,"ScalingInstructions":[`+aspInstructionJSON("orders", autoScalingReadDimension, 6, 9, "")+`]}`)
	check("owner update", 6, 3, 2)
	// Plans, tables and sources are regional namespaces.
	h.fails(t, 400, "ValidationException", "CreateScalingPlan", aspCreateJSON("one", aspTags("one"), aspInstructionJSON("orders", autoScalingReadDimension, 1, 9, "")), "X-Simulator-Region", "us-west-2")
	h.table(t, "us-west-2", "orders", "PROVISIONED", 50, 50)
	h.must(t, "CreateScalingPlan", aspCreateJSON("one", aspTags("one"), aspInstructionJSON("orders", autoScalingReadDimension, 1, 9, "")), "X-Simulator-Region", "us-west-2")
	if read, _ := h.units(t, "us-west-2", "orders"); read != 9 {
		t.Fatal(read)
	}
	check("other region", 6, 3, 3)
	if len(aspPlans(t, h, `{}`)) != 2 || len(aspPlans(t, h, `{}`, "X-Simulator-Region", "us-west-2")) != 1 {
		t.Fatal("regional plan listing leaked")
	}
}

func TestAutoScalingPlansAuthorizationPrecedesStateAndCoversTargets(t *testing.T) {
	h := aspSetup(t)
	h.table(t, "us-east-1", "orders", "PROVISIONED", 1, 1)
	plansOnly := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"autoscaling-plans:*"}, Resource: core.StringList{"*"}}}}
	if err := h.ids.Set("planner", plansOnly); err != nil {
		t.Fatal(err)
	}
	as := []string{"X-Simulator-Principal", "planner"}
	body := aspCreateJSON("shop", aspTags("shop"), aspInstructionJSON("orders", autoScalingReadDimension, 5, 9, ""))
	message := h.fails(t, 403, "AccessDeniedException", "CreateScalingPlan", body, as...)
	if !strings.Contains(message, "dynamodb:DescribeTable") {
		t.Fatal(message)
	}
	// A denied caller cannot learn whether a table exists.
	h.fails(t, 403, "AccessDeniedException", "CreateScalingPlan", aspCreateJSON("shop", aspTags("shop"), aspInstructionJSON("absent", autoScalingReadDimension, 5, 9, "")), as...)
	withDescribe := plansOnly
	withDescribe.Statement = append(core.Statements{}, plansOnly.Statement...)
	withDescribe.Statement = append(withDescribe.Statement, core.Statement{Effect: "Allow", Action: core.StringList{"dynamodb:DescribeTable"}, Resource: core.StringList{"arn:aws:dynamodb:us-east-1:000000000000:table/orders"}})
	if err := h.ids.Set("planner", withDescribe); err != nil {
		t.Fatal(err)
	}
	if message = h.fails(t, 403, "AccessDeniedException", "CreateScalingPlan", body, as...); !strings.Contains(message, "dynamodb:UpdateTable") {
		t.Fatal(message)
	}
	if read, _ := h.units(t, "us-east-1", "orders"); read != 1 || len(aspPlans(t, h, `{}`)) != 0 {
		t.Fatal("denied request changed state")
	}
	full := withDescribe
	full.Statement = append(core.Statements{}, withDescribe.Statement...)
	full.Statement = append(full.Statement,
		core.Statement{Effect: "Allow", Action: core.StringList{"dynamodb:UpdateTable"}, Resource: core.StringList{"arn:aws:dynamodb:us-east-1:000000000000:table/orders"}},
		core.Statement{Effect: "Deny", Action: core.StringList{"autoscaling-plans:DeleteScalingPlan"}, Resource: core.StringList{"*"}})
	if err := h.ids.Set("planner", full); err != nil {
		t.Fatal(err)
	}
	_, _, w := h.call(t, "CreateScalingPlan", body, as...)
	if w.Code != 200 || w.Header().Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatal(w.Code, w.Body.String())
	}
	if message = h.fails(t, 403, "AccessDeniedException", "DeleteScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`, as...); !strings.Contains(message, "explicitDeny") {
		t.Fatal(message)
	}
	if len(aspPlans(t, h, `{}`, as...)) != 1 || len(aspResources(t, h, "shop", as...)) != 1 {
		t.Fatal("explicitly denied deletion changed state")
	}
	// Removing targets from a plan needs no table permission; adding one does.
	h.must(t, "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1,"ApplicationSource":`+aspTags("renamed")+`}`, as...)
	h.fails(t, 403, "AccessDeniedException", "DescribeScalingPlans", `{}`, "X-Simulator-Principal", "unknown")
	if err := h.ids.Set("reader", core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"autoscaling-plans:Describe*"}, Resource: core.StringList{"*"}}}}); err != nil {
		t.Fatal(err)
	}
	reader := []string{"X-Simulator-Principal", "reader"}
	if len(aspPlans(t, h, `{}`, reader...)) != 1 {
		t.Fatal("reader cannot describe")
	}
	for op, body := range map[string]string{
		"UpdateScalingPlan": `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`,
		"DeleteScalingPlan": `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`,
		"CreateScalingPlan": aspCreateJSON("again", aspTags("again"), aspInstructionJSON("orders", autoScalingWriteDimension, 5, 9, "")),
	} {
		h.fails(t, 403, "AccessDeniedException", op, body, reader...)
	}
	// One immutable policy authorizes the whole request, even if the identity
	// changes while the body is being read.
	deny := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Deny", Action: core.StringList{"*"}, Resource: core.StringList{"*"}}}}
	request := aspRequest("DescribeScalingPlans", "", "X-Simulator-Principal", "reader")
	request.Body = io.NopCloser(&r53ChangingBody{reader: strings.NewReader(`{}`), change: func() {
		if err := h.ids.Set("reader", deny); err != nil {
			t.Error(err)
		}
	}})
	w = httptest.NewRecorder()
	h.g.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal("policy was re-read during the request", w.Code, w.Body.String())
	}
	h.fails(t, 403, "AccessDeniedException", "DescribeScalingPlans", `{}`, reader...)
}

func TestAutoScalingPlansRejectsMalformedAndUnsupportedInputs(t *testing.T) {
	h := aspSetup(t)
	h.table(t, "us-east-1", "orders", "PROVISIONED", 1, 1)
	read := func(fields string) string {
		return `{"ServiceNamespace":"dynamodb","ResourceId":"table/orders","ScalableDimension":"dynamodb:table:ReadCapacityUnits","MinCapacity":1,"MaxCapacity":5,"TargetTrackingConfigurations":[{"PredefinedScalingMetricSpecification":{"PredefinedScalingMetricType":"DynamoDBReadCapacityUtilization"},"TargetValue":50` + fields + `}]}`
	}
	instruction := func(replace, with string) string {
		return strings.Replace(read(""), replace, with, 1)
	}
	create := func(instructions ...string) string {
		return aspCreateJSON("shop", aspTags("shop"), instructions...)
	}
	createSource := func(source string) string {
		return aspCreateJSON("shop", source, read(""))
	}
	cases := []struct{ label, op, body, contains string }{
		{"unknown request field", "CreateScalingPlan", `{"ScalingPlanName":"shop","ApplicationSource":` + aspTags("shop") + `,"ScalingInstructions":[` + read("") + `],"Extra":1}`, "Unsupported field request.Extra"},
		{"missing name", "CreateScalingPlan", `{"ApplicationSource":` + aspTags("shop") + `,"ScalingInstructions":[` + read("") + `]}`, "ScalingPlanName"},
		{"name slash", "CreateScalingPlan", strings.Replace(create(read("")), `"shop"`, `"a/b"`, 1), "forward slashes"},
		{"name colon", "CreateScalingPlan", strings.Replace(create(read("")), `"shop"`, `"a:b"`, 1), "colons"},
		{"name too long", "CreateScalingPlan", strings.Replace(create(read("")), `"shop"`, `"`+strings.Repeat("n", 129)+`"`, 1), "1 to 128"},
		{"name non-ascii", "CreateScalingPlan", strings.Replace(create(read("")), `"shop"`, `"café"`, 1), "printable ASCII"},
		{"missing source", "CreateScalingPlan", `{"ScalingPlanName":"shop","ScalingInstructions":[` + read("") + `]}`, "ApplicationSource must be a JSON object"},
		{"both sources", "CreateScalingPlan", createSource(`{"CloudFormationStackARN":"arn:aws:cloudformation:us-east-1:000000000000:stack/s/1","TagFilters":[{"Key":"a"}]}`), "exactly one"},
		{"empty source", "CreateScalingPlan", createSource(`{}`), "exactly one"},
		{"stack other region", "CreateScalingPlan", createSource(`{"CloudFormationStackARN":"arn:aws:cloudformation:us-west-2:000000000000:stack/s/1"}`), "stack ARN in this region"},
		{"stack other account", "CreateScalingPlan", createSource(`{"CloudFormationStackARN":"arn:aws:cloudformation:us-east-1:123456789012:stack/s/1"}`), "stack ARN"},
		{"empty tag filters", "CreateScalingPlan", createSource(`{"TagFilters":[]}`), "1 to 50"},
		{"duplicate tag key", "CreateScalingPlan", createSource(`{"TagFilters":[{"Key":"a"},{"Key":"a"}]}`), "cannot repeat the key"},
		{"duplicate tag value", "CreateScalingPlan", createSource(`{"TagFilters":[{"Key":"a","Values":["x","x"]}]}`), "cannot repeat a value"},
		{"empty tag key", "CreateScalingPlan", createSource(`{"TagFilters":[{"Key":""}]}`), "TagFilter Key"},
		{"long tag value", "CreateScalingPlan", createSource(`{"TagFilters":[{"Key":"a","Values":["` + strings.Repeat("v", 257) + `"]}]}`), "TagFilter value"},
		{"tag control character", "CreateScalingPlan", createSource(`{"TagFilters":[{"Key":"a\u0001"}]}`), "TagFilter Key"},
		{"tag unknown field", "CreateScalingPlan", createSource(`{"TagFilters":[{"Key":"a","Other":1}]}`), "Unsupported field"},
		{"too many tag values", "CreateScalingPlan", createSource(`{"TagFilters":[{"Key":"a","Values":[` + strings.TrimSuffix(strings.Repeat(`"v",`, 21), ",") + `]}]}`), "0 to 20"},
		{"no instructions", "CreateScalingPlan", create(), "1 to 50"},
		{"instruction not object", "CreateScalingPlan", create(`1`), "JSON object"},
		{"ec2 namespace", "CreateScalingPlan", create(instruction(`"dynamodb"`, `"autoscaling"`)), "Only DynamoDB table targets"},
		{"unknown namespace", "CreateScalingPlan", create(instruction(`"dynamodb"`, `"lambda"`)), "ServiceNamespace must be"},
		{"index target", "CreateScalingPlan", create(instruction(`table/orders`, `table/orders/index/by-date`)), "global secondary index"},
		{"bad resource", "CreateScalingPlan", create(instruction(`table/orders`, `cluster:orders`)), "table/<table-name>"},
		{"short table name", "CreateScalingPlan", create(instruction(`table/orders`, `table/ab`)), "table/<table-name>"},
		{"index dimension", "CreateScalingPlan", create(instruction(`dynamodb:table:ReadCapacityUnits`, `dynamodb:index:ReadCapacityUnits`)), "ScalableDimension must be"},
		{"min zero", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"MinCapacity":0`)), "MinCapacity"},
		{"min fraction", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"MinCapacity":1.5`)), "MinCapacity"},
		{"min string", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"MinCapacity":"1"`)), "MinCapacity must be an integer"},
		{"max above bound", "CreateScalingPlan", create(instruction(`"MaxCapacity":5`, `"MaxCapacity":40001`)), "MaxCapacity"},
		{"max below min", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"MinCapacity":6`)), "greater than or equal"},
		{"predictive mode", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"PredictiveScalingMode":"ForecastOnly","MinCapacity":1`)), "Predictive scaling applies only"},
		{"load metric", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"PredefinedLoadMetricSpecification":{"PredefinedLoadMetricType":"ASGTotalCPUUtilization"},"MinCapacity":1`)), "Predictive scaling"},
		{"unknown instruction field", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"Weight":1,"MinCapacity":1`)), "Unsupported field ScalingInstruction.Weight"},
		{"bad update behavior", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"ScalingPolicyUpdateBehavior":"Merge","MinCapacity":1`)), "ScalingPolicyUpdateBehavior"},
		{"bad disable flag", "CreateScalingPlan", create(instruction(`"MinCapacity":1`, `"DisableDynamicScaling":"yes","MinCapacity":1`)), "DisableDynamicScaling must be a boolean"},
		{"no target tracking", "CreateScalingPlan", create(strings.Replace(read(""), read("")[strings.Index(read(""), `"TargetTrackingConfigurations"`):len(read(""))-1], `"TargetTrackingConfigurations":[]`, 1)), "1 to 10"},
		{"missing target tracking", "CreateScalingPlan", create(`{"ServiceNamespace":"dynamodb","ResourceId":"table/orders","ScalableDimension":"dynamodb:table:ReadCapacityUnits","MinCapacity":1,"MaxCapacity":5}`), "TargetTrackingConfigurations"},
		{"duplicate metric", "CreateScalingPlan", create(strings.Replace(read(""), `}]}`, `},{"PredefinedScalingMetricSpecification":{"PredefinedScalingMetricType":"DynamoDBReadCapacityUtilization"},"TargetValue":60}]}`, 1)), "unique scaling metric"},
		{"customized metric", "CreateScalingPlan", create(read(`,"CustomizedScalingMetricSpecification":{"MetricName":"m","Namespace":"n","Statistic":"Sum"}`)), "CustomizedScalingMetricSpecification is unsupported"},
		{"warmup", "CreateScalingPlan", create(read(`,"EstimatedInstanceWarmup":60`)), "EstimatedInstanceWarmup"},
		{"resource label", "CreateScalingPlan", create(instruction(`"DynamoDBReadCapacityUtilization"`, `"DynamoDBReadCapacityUtilization","ResourceLabel":"app/x"`)), "ResourceLabel"},
		{"wrong metric", "CreateScalingPlan", create(instruction(`"DynamoDBReadCapacityUtilization"`, `"DynamoDBWriteCapacityUtilization"`)), "must be DynamoDBReadCapacityUtilization"},
		{"missing metric", "CreateScalingPlan", create(instruction(`"PredefinedScalingMetricSpecification":{"PredefinedScalingMetricType":"DynamoDBReadCapacityUtilization"},`, ``)), "requires PredefinedScalingMetricSpecification"},
		{"unknown metric field", "CreateScalingPlan", create(instruction(`"DynamoDBReadCapacityUtilization"`, `"DynamoDBReadCapacityUtilization","Other":1`)), "Unsupported field"},
		{"missing target value", "CreateScalingPlan", create(instruction(`,"TargetValue":50`, ``)), "TargetValue is required"},
		{"target value too large", "CreateScalingPlan", create(instruction(`"TargetValue":50`, `"TargetValue":3e108`)), "2^360"},
		{"bad cooldown", "CreateScalingPlan", create(read(`,"ScaleInCooldown":-1`)), "ScaleInCooldown"},
		{"bad scale-in flag", "CreateScalingPlan", create(read(`,"DisableScaleIn":1`)), "DisableScaleIn"},
		{"duplicate instruction", "CreateScalingPlan", create(read(""), read("")), "cannot repeat"},
		{"update wrong version", "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":2}`, "only valid ScalingPlanVersion is 1"},
		{"update missing version", "UpdateScalingPlan", `{"ScalingPlanName":"shop"}`, "ScalingPlanVersion"},
		{"update empty instructions", "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1,"ScalingInstructions":[]}`, "1 to 50"},
		{"delete version string", "DeleteScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":"1"}`, "ScalingPlanVersion"},
		{"delete bad name", "DeleteScalingPlan", `{"ScalingPlanName":"","ScalingPlanVersion":1}`, "ScalingPlanName"},
		{"names and sources", "DescribeScalingPlans", `{"ScalingPlanNames":["a"],"ApplicationSources":[` + aspTags("a") + `]}`, "cannot be combined"},
		{"version without names", "DescribeScalingPlans", `{"ScalingPlanVersion":1}`, "requires ScalingPlanNames"},
		{"describe wrong version", "DescribeScalingPlans", `{"ScalingPlanNames":["a"],"ScalingPlanVersion":3}`, "only valid"},
		{"too many names", "DescribeScalingPlans", `{"ScalingPlanNames":["a","b","c","d","e","f","g","h","i","j","k"]}`, "1 to 10"},
		{"bad listed name", "DescribeScalingPlans", `{"ScalingPlanNames":["a|b"]}`, "vertical bars"},
		{"bad listed source", "DescribeScalingPlans", `{"ApplicationSources":[{}]}`, "exactly one"},
		{"empty sources", "DescribeScalingPlans", `{"ApplicationSources":[]}`, "1 to 10"},
		{"max results zero", "DescribeScalingPlans", `{"MaxResults":0}`, "MaxResults"},
		{"max results high", "DescribeScalingPlanResources", `{"ScalingPlanName":"shop","ScalingPlanVersion":1,"MaxResults":51}`, "MaxResults"},
		{"resources wrong version", "DescribeScalingPlanResources", `{"ScalingPlanName":"shop","ScalingPlanVersion":0}`, "only valid"},
		{"resources bad name", "DescribeScalingPlanResources", `{"ScalingPlanName":7,"ScalingPlanVersion":1}`, "ScalingPlanName"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			if message := h.fails(t, 400, "ValidationException", c.op, c.body); !strings.Contains(message, c.contains) {
				t.Fatalf("message %q does not contain %q", message, c.contains)
			}
		})
	}
	h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlans", `{"NextToken":""}`)
	h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlans", `{"NextToken":7}`)
	if read, write := h.units(t, "us-east-1", "orders"); read != 1 || write != 1 || len(aspPlans(t, h, `{}`)) != 0 {
		t.Fatal("rejected input changed state")
	}
	// Explicit dynamic-scaling opt-out accepts an empty tracking list and
	// still validates any supplied configuration.
	h.must(t, "CreateScalingPlan", create(`{"ServiceNamespace":"dynamodb","ResourceId":"table/orders","ScalableDimension":"dynamodb:table:WriteCapacityUnits","MinCapacity":2,"MaxCapacity":2,"DisableDynamicScaling":true,"TargetTrackingConfigurations":[{"PredefinedScalingMetricSpecification":{"PredefinedScalingMetricType":"DynamoDBWriteCapacityUtilization"},"TargetValue":-1.5e3,"DisableScaleIn":true,"ScaleInCooldown":0,"ScaleOutCooldown":2147483647}]}`))
	stored := aspPlans(t, h, `{}`)[0].(map[string]any)["ScalingInstructions"].([]any)[0].(map[string]any)["TargetTrackingConfigurations"].([]any)[0].(map[string]any)
	if stored["TargetValue"] != -1500.0 || stored["DisableScaleIn"] != true || stored["ScaleInCooldown"] != 0.0 || stored["ScaleOutCooldown"] != 2147483647.0 {
		t.Fatal(stored)
	}
	if _, write := h.units(t, "us-east-1", "orders"); write != 2 {
		t.Fatal(write)
	}
}

func TestAutoScalingPlansWireBoundaries(t *testing.T) {
	h := aspSetup(t)
	serve := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.g.ServeHTTP(w, r)
		return w
	}
	expect := func(label string, w *httptest.ResponseRecorder, status int, errorType string) {
		t.Helper()
		var decoded map[string]string
		if json.Unmarshal(w.Body.Bytes(), &decoded) != nil || w.Code != status || decoded["__type"] != errorType {
			t.Fatal(label, w.Code, w.Body.String())
		}
	}
	r := aspRequest("DescribeScalingPlans", `{}`)
	r.Method = "PUT"
	expect("method", serve(r), 400, "ValidationException")
	r = aspRequest("DescribeScalingPlans", `{}`)
	r.URL.Path = "/plans"
	expect("path", serve(r), 400, "ValidationException")
	r = aspRequest("DescribeScalingPlans", `{}`)
	r.URL.RawQuery = "x=1"
	expect("query", serve(r), 400, "ValidationException")
	expect("media", serve(aspRequest("DescribeScalingPlans", `{}`, "Content-Type", "application/json")), 400, "ValidationException")
	expect("region", serve(aspRequest("DescribeScalingPlans", `{}`, "X-Simulator-Region", "mars-1")), 400, "ValidationException")
	if w := serve(aspRequest("DescribeScalingPlans", `{}`, "X-Simulator-Service", "autoscaling-plans")); w.Code != 200 {
		t.Fatal("explicit service selection rejected", w.Code, w.Body.String())
	}
	r = aspRequest("DescribeScalingPlans", `{}`)
	r.Header.Set("X-Amz-Target", aspTargetPrefix+"DescribeScalingPlans")
	r.Header.Set("X-Simulator-Service", "logs")
	w := httptest.NewRecorder()
	h.plans.ServeHTTP(w, r, "us-east-1", func(string, string) error { return nil })
	expect("service mismatch", w, 400, "ValidationException")
	expect("session token", serve(aspRequest("DescribeScalingPlans", `{}`, "X-Amz-Security-Token", "session")), 400, "ValidationException")
	credential := "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261111/us-east-1/autoscaling-plans/aws4_request, SignedHeaders=host, Signature=dummy"
	if w := serve(aspRequest("DescribeScalingPlans", `{}`, "Authorization", credential)); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	expect("real key", serve(aspRequest("DescribeScalingPlans", `{}`, "Authorization", strings.Replace(credential, "TCSIMTEST", "AKIAEXAMPLE", 1))), 403, "AccessDeniedException")
	expect("scope request", serve(aspRequest("DescribeScalingPlans", `{}`, "Authorization", strings.Replace(credential, "aws4_request", "aws4", 1))), 403, "AccessDeniedException")
	expect("scope disagreement", serve(aspRequest("DescribeScalingPlans", `{}`, "Authorization", credential, "X-Simulator-Region", "eu-west-1")), 400, "ValidationException")
	if w := serve(aspRequest("DescribeScalingPlans", `{}`, "Authorization", strings.Replace(credential, "us-east-1", "eu-west-1", 1))); w.Code != 200 {
		t.Fatal("credential scope region was not used", w.Code, w.Body.String())
	}
	expect("unknown operation", serve(aspRequest("ListScalingPlans", `{}`)), 501, "UnknownOperationException")
	w = serve(aspRequest("GetScalingPlanResourceForecastData", `{}`))
	expect("forecast", w, 501, "UnknownOperationException")
	if !strings.Contains(w.Body.String(), "CloudWatch load history") {
		t.Fatal(w.Body.String())
	}
	expect("large body", serve(aspRequest("DescribeScalingPlans", `{"NextToken":"`+strings.Repeat("x", aspRequestBytes)+`"}`)), 400, "ValidationException")
	expect("utf8", serve(aspRequest("DescribeScalingPlans", "{\"NextToken\":\"\xff\"}")), 400, "ValidationException")
	expect("duplicate key", serve(aspRequest("DescribeScalingPlans", `{"MaxResults":1,"MaxResults":2}`)), 400, "ValidationException")
	expect("null", serve(aspRequest("DescribeScalingPlans", `{"NextToken":null}`)), 400, "ValidationException")
	expect("trailing", serve(aspRequest("DescribeScalingPlans", `{}{}`)), 400, "ValidationException")
	expect("array", serve(aspRequest("DescribeScalingPlans", `[]`)), 400, "ValidationException")
	expect("empty", serve(aspRequest("DescribeScalingPlans", ``)), 400, "ValidationException")
	for _, key := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Target", "X-Amz-Security-Token"} {
		r := aspRequest("DescribeScalingPlans", `{}`)
		r.Header.Add(key, "one")
		r.Header.Add(key, "two")
		if key == "X-Amz-Target" {
			r.Header.Del(key)
			r.Header.Add(key, aspTargetPrefix+"DescribeScalingPlans")
			r.Header.Add(key, aspTargetPrefix+"DeleteScalingPlan")
		}
		expect("duplicate "+key, serve(r), 400, "ValidationException")
	}
	w = httptest.NewRecorder()
	h.plans.ServeHTTP(w, aspRequest("DescribeScalingPlans", `{}`), "us-east-1", nil)
	expect("missing authorizer", w, 500, "InternalServiceException")
}

func TestAutoScalingPlansGatewaySelectorsAndMissingDependencies(t *testing.T) {
	h := aspSetup(t)
	h.g.CatalogOwns = func(*http.Request) bool { return true }
	h.g.Catalog = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("Auto Scaling Plans fell through to the catalog") })
	credential := "AWS4-HMAC-SHA256 Credential=TCSIMTEST/20261111/us-east-1/autoscaling-plans/aws4_request, SignedHeaders=host, Signature=dummy"
	for _, selector := range [][]string{{"X-Amz-Target", aspTargetPrefix + "DescribeScalingPlans"}, {"X-Simulator-Service", "autoscaling-plans"}, {"Authorization", credential}} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/x-amz-json-1.1")
		r.Header.Set(selector[0], selector[1])
		if selector[0] != "X-Amz-Target" {
			r.Header.Set("X-Amz-Target", "")
		}
		if !isAutoScalingPlans(r) {
			t.Fatal("selector not recognized", selector)
		}
	}
	for _, headers := range [][]string{{"X-Amz-Target", "Logs_20140328.DescribeLogGroups"}, {"X-Simulator-Service", "logs"}, {"Authorization", strings.Replace(credential, "autoscaling-plans", "dynamodb", 1)}} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set(headers[0], headers[1])
		if isAutoScalingPlans(r) {
			t.Fatal("foreign selector claimed", headers)
		}
	}
	if code, _, w := h.call(t, "DescribeScalingPlans", `{}`); code != 200 {
		t.Fatal(w.Body.String())
	}
	for label, gateway := range map[string]*Gateway{"store": {Identities: h.ids}, "identities": {AutoScalingPlans: h.plans}} {
		w := httptest.NewRecorder()
		gateway.ServeHTTP(w, aspRequest("DescribeScalingPlans", `{}`))
		if w.Code < 400 {
			t.Fatal("missing dependency allowed", label)
		}
	}
	if _, err := NewAutoScalingPlansStore(h.db.Database(), nil); err == nil {
		t.Fatal("missing DynamoDB dependency accepted")
	}
	other, err := core.New(filepath.Join(t.TempDir(), "other.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherDynamo, err := NewDynamoDBStore(other.Database())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewAutoScalingPlansStore(h.db.Database(), otherDynamo); err == nil {
		t.Fatal("DynamoDB store of another database accepted")
	}
}

func TestAutoScalingPlansPaginationTokensAreBoundToQueryAndGeneration(t *testing.T) {
	h := aspSetup(t)
	for _, name := range []string{"t-one", "t-two", "t-three"} {
		h.table(t, "us-east-1", name, "PROVISIONED", 1, 1)
	}
	for i, name := range []string{"charlie", "alpha", "bravo"} {
		table := []string{"t-one", "t-two", "t-three"}[i]
		h.must(t, "CreateScalingPlan", aspCreateJSON(name, aspTags(name), aspInstructionJSON(table, autoScalingReadDimension, 1, 5, ""), aspInstructionJSON(table, autoScalingWriteDimension, 1, 5, "")))
	}
	first := h.must(t, "DescribeScalingPlans", `{"MaxResults":2}`)
	names := func(page map[string]any) []string {
		var out []string
		for _, plan := range page["ScalingPlans"].([]any) {
			out = append(out, plan.(map[string]any)["ScalingPlanName"].(string))
		}
		return out
	}
	token, _ := first["NextToken"].(string)
	if strings.Join(names(first), ",") != "alpha,bravo" || token == "" {
		t.Fatal(first)
	}
	second := h.must(t, "DescribeScalingPlans", `{"MaxResults":2,"NextToken":"`+token+`"}`)
	if strings.Join(names(second), ",") != "charlie" || second["NextToken"] != nil {
		t.Fatal(second)
	}
	// Tokens are live positions: another page size may reuse them.
	if page := h.must(t, "DescribeScalingPlans", `{"MaxResults":50,"NextToken":"`+token+`"}`); len(names(page)) != 1 {
		t.Fatal(page)
	}
	h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlans", `{"ScalingPlanNames":["alpha"],"NextToken":"`+token+`"}`)
	h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlans", `{"NextToken":"`+token+`"}`, "X-Simulator-Region", "us-west-2")
	for _, forged := range []string{"v2." + token[3:], "v1.!!", "v1." + strings.Repeat("a", 1100), aspToken("alpha", "other"), "v1.eyJhIjoieCJ9"} {
		h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlans", `{"NextToken":"`+forged+`"}`)
	}
	if page := h.must(t, "DescribeScalingPlans", `{"ScalingPlanNames":["charlie","alpha","missing"]}`); strings.Join(names(page), ",") != "alpha,charlie" {
		t.Fatal(page)
	}
	if page := h.must(t, "DescribeScalingPlans", `{"ApplicationSources":[`+aspTags("bravo")+`,`+aspTags("nobody")+`]}`); strings.Join(names(page), ",") != "bravo" {
		t.Fatal(page)
	}
	resources := h.must(t, "DescribeScalingPlanResources", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"MaxResults":1}`)
	resourceToken, _ := resources["NextToken"].(string)
	if len(resources["ScalingPlanResources"].([]any)) != 1 || resourceToken == "" || resources["ScalingPlanResources"].([]any)[0].(map[string]any)["ScalableDimension"] != autoScalingReadDimension {
		t.Fatal(resources)
	}
	next := h.must(t, "DescribeScalingPlanResources", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"MaxResults":1,"NextToken":"`+resourceToken+`"}`)
	if len(next["ScalingPlanResources"].([]any)) != 1 || next["NextToken"] != nil || next["ScalingPlanResources"].([]any)[0].(map[string]any)["ScalableDimension"] != autoScalingWriteDimension {
		t.Fatal(next)
	}
	h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlanResources", `{"ScalingPlanName":"bravo","ScalingPlanVersion":1,"NextToken":"`+resourceToken+`"}`)
	h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlanResources", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"NextToken":"`+token+`"}`)
	// A shrunken live plan yields an empty page rather than an invented entry.
	h.must(t, "UpdateScalingPlan", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"ScalingInstructions":[`+aspInstructionJSON("t-two", autoScalingReadDimension, 1, 5, "")+`]}`)
	if late := h.must(t, "DescribeScalingPlanResources", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"NextToken":"`+resourceToken+`"}`); len(late["ScalingPlanResources"].([]any)) != 0 {
		t.Fatal(late)
	}
	// A recreated plan with the same name is a new generation.
	// The clock does not advance: the generation, not the time, separates them.
	h.must(t, "DeleteScalingPlan", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1}`)
	h.must(t, "CreateScalingPlan", aspCreateJSON("alpha", aspTags("alpha"), aspInstructionJSON("t-two", autoScalingReadDimension, 1, 5, "")))
	h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlanResources", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"NextToken":"`+resourceToken+`"}`)
	var generation string
	if err := h.db.Database().QueryRow(`SELECT generation FROM aws_autoscaling_plans WHERE region='us-east-1' AND name='alpha'`).Scan(&generation); err != nil || len(generation) != 32 {
		t.Fatal(generation, err)
	}
	if page := h.must(t, "DescribeScalingPlanResources", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"NextToken":"`+aspToken("1", aspBinding("us-east-1", "alpha", generation))+`"}`); len(page["ScalingPlanResources"].([]any)) != 0 {
		t.Fatal(page)
	}
	for _, after := range []string{"0", "01", "x"} {
		forged := aspToken(after, aspBinding("us-east-1", "alpha", generation))
		h.fails(t, 400, "InvalidNextTokenException", "DescribeScalingPlanResources", `{"ScalingPlanName":"alpha","ScalingPlanVersion":1,"NextToken":"`+forged+`"}`)
	}
}

func TestAutoScalingPlansDerivedStatusFollowsTableGeneration(t *testing.T) {
	h := aspSetup(t)
	h.table(t, "us-east-1", "orders", "PROVISIONED", 1, 1)
	h.must(t, "CreateScalingPlan", aspCreateJSON("shop", aspTags("shop"), aspInstructionJSON("orders", autoScalingReadDimension, 3, 9, `,"DisableDynamicScaling":true`)))
	if plan := aspPlans(t, h, `{}`)[0].(map[string]any); plan["StatusCode"] != "Active" {
		t.Fatal(plan)
	}
	if _, err := h.dynamo.DeleteTable("us-east-1", "orders"); err != nil {
		t.Fatal(err)
	}
	inactive := func(label, message string) {
		t.Helper()
		resource := aspResources(t, h, "shop")[0].(map[string]any)
		plan := aspPlans(t, h, `{}`)[0].(map[string]any)
		if resource["ScalingStatusCode"] != "Inactive" || resource["ScalingStatusMessage"] != message || plan["StatusCode"] != "ActiveWithProblems" {
			t.Fatal(label, resource, plan)
		}
	}
	inactive("deleted table", autoScalingInactiveMessage)
	h.table(t, "us-east-1", "orders", "PROVISIONED", 20, 1)
	inactive("recreated table generation", autoScalingRecreatedMessage)
	if read, _ := h.units(t, "us-east-1", "orders"); read != 20 {
		t.Fatal("a stale plan changed a new table generation", read)
	}
	h.must(t, "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1,"ScalingInstructions":[`+aspInstructionJSON("orders", autoScalingReadDimension, 3, 9, `,"DisableDynamicScaling":true`)+`]}`)
	if resource := aspResources(t, h, "shop")[0].(map[string]any); resource["ScalingStatusCode"] != "Active" {
		t.Fatal(resource)
	}
	if read, _ := h.units(t, "us-east-1", "orders"); read != 9 {
		t.Fatal(read)
	}
}

func TestAutoScalingPlansStorageFaultsFailClosed(t *testing.T) {
	h := aspSetup(t)
	db := h.db.Database()
	h.table(t, "us-east-1", "orders", "PROVISIONED", 1, 1)
	h.must(t, "CreateScalingPlan", aspCreateJSON("shop", aspTags("shop"), aspInstructionJSON("orders", autoScalingReadDimension, 1, 9, "")))
	// Plan quota counts every region in the database.
	for i := 1; i < maxAutoScalingPlans; i++ {
		if _, err := db.Exec(`INSERT INTO aws_autoscaling_plans VALUES(?,?,?,?,?,?,?)`, "eu-west-1", "filler"+jsonInt(i), "tags\x00filler"+jsonInt(i), `{"TagFilters":[{"Key":"filler"}]}`, 1, 1, "filler"); err != nil {
			t.Fatal(err)
		}
	}
	h.table(t, "us-east-1", "audit", "PROVISIONED", 1, 1)
	h.fails(t, 400, "LimitExceededException", "CreateScalingPlan", aspCreateJSON("extra", aspTags("extra"), aspInstructionJSON("audit", autoScalingReadDimension, 5, 9, "")))
	if read, _ := h.units(t, "us-east-1", "audit"); read != 1 {
		t.Fatal("quota refusal changed capacity", read)
	}
	if _, err := db.Exec(`DELETE FROM aws_autoscaling_plans WHERE region='eu-west-1'`); err != nil {
		t.Fatal(err)
	}
	// Corrupted stored documents never produce invented output.
	if _, err := db.Exec(`UPDATE aws_autoscaling_plan_resources SET instruction='{'`); err != nil {
		t.Fatal(err)
	}
	h.fails(t, 500, "InternalServiceException", "DescribeScalingPlans", `{}`)
	h.fails(t, 500, "InternalServiceException", "DescribeScalingPlanResources", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	if _, err := db.Exec(`UPDATE aws_autoscaling_plan_resources SET instruction=? `, `{"ServiceNamespace":"dynamodb","ResourceId":"table/other","ScalableDimension":"dynamodb:table:ReadCapacityUnits","MinCapacity":1,"MaxCapacity":2,"TargetTrackingConfigurations":[]}`); err != nil {
		t.Fatal(err)
	}
	h.fails(t, 500, "InternalServiceException", "DescribeScalingPlans", `{}`)
	if _, err := db.Exec(`UPDATE aws_autoscaling_plan_resources SET instruction=? `, `{"ServiceNamespace":"dynamodb","ResourceId":"table/orders","ScalableDimension":"dynamodb:table:ReadCapacityUnits","MinCapacity":1,"MaxCapacity":2,"TargetTrackingConfigurations":[{"TargetValue":1}]}`); err != nil {
		t.Fatal(err)
	}
	h.fails(t, 500, "InternalServiceException", "DescribeScalingPlanResources", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	if failure := aspInvalid("plain message"); failure.Error() != "plain message" {
		t.Fatal(failure)
	}
	// Trusted native callers cannot request an empty or oversized page.
	for _, limit := range []int{0, maxAutoScalingPageSize + 1} {
		if _, _, err := h.plans.Plans(context.Background(), "us-east-1", ASPPlanQuery{Limit: limit}); aspCode(err) != "ValidationException" {
			t.Fatal(limit, err)
		}
		if _, _, err := h.plans.Resources(context.Background(), "us-east-1", "shop", "", limit); aspCode(err) != "ValidationException" {
			t.Fatal(limit, err)
		}
	}
	if _, err := db.Exec(`UPDATE aws_autoscaling_plans SET source='['`); err != nil {
		t.Fatal(err)
	}
	h.fails(t, 500, "InternalServiceException", "DescribeScalingPlans", `{}`)
	if _, err := db.Exec(`UPDATE aws_dynamodb_tables SET schema='{' WHERE name='audit'`); err != nil {
		t.Fatal(err)
	}
	h.fails(t, 500, "InternalServiceException", "CreateScalingPlan", aspCreateJSON("extra", aspTags("extra"), aspInstructionJSON("audit", autoScalingReadDimension, 5, 9, "")))
	if _, err := db.Exec(`DROP TABLE aws_autoscaling_plan_resources`); err != nil {
		t.Fatal(err)
	}
	h.fails(t, 500, "InternalServiceException", "DeleteScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	h.fails(t, 500, "InternalServiceException", "UpdateScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1,"ScalingInstructions":[`+aspInstructionJSON("orders", autoScalingReadDimension, 1, 9, "")+`]}`)
	h.fails(t, 500, "InternalServiceException", "DescribeScalingPlanResources", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`)
	if _, err := db.Exec(`DROP TABLE aws_autoscaling_plans`); err != nil {
		t.Fatal(err)
	}
	for op, body := range map[string]string{
		"CreateScalingPlan":            aspCreateJSON("extra", aspTags("extra"), aspInstructionJSON("orders", autoScalingReadDimension, 5, 9, "")),
		"UpdateScalingPlan":            `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`,
		"DescribeScalingPlans":         `{}`,
		"DescribeScalingPlanResources": `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`,
	} {
		h.fails(t, 500, "InternalServiceException", op, body)
	}
}

func TestAutoScalingPlansStartupRefusesOrphanedResources(t *testing.T) {
	h := aspSetup(t)
	db := h.db.Database()
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO aws_autoscaling_plan_resources VALUES('us-east-1','ghost',0,'dynamodb','table/orders','dynamodb:table:ReadCapacityUnits','{}','x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAutoScalingPlansStore(db, h.dynamo); err == nil || !strings.Contains(err.Error(), "orphaned") {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM aws_autoscaling_plan_resources`).Scan(&count); err != nil || count != 1 {
		t.Fatal("startup refusal modified data", count, err)
	}
}
