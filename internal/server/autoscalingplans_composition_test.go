package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func autoScalingComposedCall(t *testing.T, h http.Handler, target, body, token string, headers ...string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("POST", "http://127.0.0.1/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", target)
	if token != "" {
		r.Header.Set("X-Simulator-Token", token)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	decoded := map[string]any{}
	if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
		// Access middleware refusals are plain text, never provider JSON.
		if w.Code < 400 {
			t.Fatal(target, w.Code, w.Body.String())
		}
		decoded["raw"] = w.Body.String()
	}
	return w.Code, decoded
}

func autoScalingComposedServer(t *testing.T, path, token string) (http.Handler, func() error) {
	t.Helper()
	h, closeStore, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccess(h, token, false)
	if err != nil {
		t.Fatal(err)
	}
	return access, closeStore
}

func TestAutoScalingPlansCompositionTokenPersistenceIsolationAndCapabilities(t *testing.T) {
	path := filepath.Join(t.TempDir(), "autoscaling.db")
	token := strings.Repeat("c", 64)
	access, closeStore := autoScalingComposedServer(t, path, token)
	table := `{"TableName":"orders","AttributeDefinitions":[{"AttributeName":"id","AttributeType":"S"}],"KeySchema":[{"AttributeName":"id","KeyType":"HASH"}],"BillingMode":"PROVISIONED","ProvisionedThroughput":{"ReadCapacityUnits":1,"WriteCapacityUnits":100}}`
	plan := `{"ScalingPlanName":"shop","ApplicationSource":{"TagFilters":[{"Key":"app","Values":["shop"]}]},"ScalingInstructions":[{"ServiceNamespace":"dynamodb","ResourceId":"table/orders","ScalableDimension":"dynamodb:table:WriteCapacityUnits","MinCapacity":2,"MaxCapacity":10,"TargetTrackingConfigurations":[{"PredefinedScalingMetricSpecification":{"PredefinedScalingMetricType":"DynamoDBWriteCapacityUtilization"},"TargetValue":70}]}]}`
	if code, _ := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService.CreateScalingPlan", plan, ""); code != 401 {
		t.Fatal("unauthenticated request reached the provider", code)
	}
	if code, body := autoScalingComposedCall(t, access, "DynamoDB_20120810.CreateTable", table, token, "Content-Type", "application/x-amz-json-1.0"); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService.CreateScalingPlan", plan, token); code != 200 || body["ScalingPlanVersion"] != 1.0 {
		t.Fatal(code, body)
	}
	if err := closeStore(); err != nil {
		t.Fatal(err)
	}
	access, closeStore = autoScalingComposedServer(t, path, token)
	defer closeStore()
	code, described := autoScalingComposedCall(t, access, "DynamoDB_20120810.DescribeTable", `{"TableName":"orders"}`, token, "Content-Type", "application/x-amz-json-1.0")
	throughput := described["Table"].(map[string]any)["ProvisionedThroughput"].(map[string]any)
	if code != 200 || throughput["ReadCapacityUnits"] != 1.0 || throughput["WriteCapacityUnits"] != 10.0 {
		t.Fatal("DynamoDB does not observe the bounded capacity after restart", code, described)
	}
	code, plans := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", `{}`, token)
	if listed := plans["ScalingPlans"].([]any); code != 200 || len(listed) != 1 || listed[0].(map[string]any)["StatusCode"] != "ActiveWithProblems" {
		t.Fatal(code, plans)
	}
	// The provider-prefixed route reaches the same state.
	r := httptest.NewRequest("POST", "http://127.0.0.1/_simulator/providers/aws/", strings.NewReader(`{"ScalingPlanName":"shop","ScalingPlanVersion":1}`))
	r.Header.Set("Content-Type", "application/x-amz-json-1.1")
	r.Header.Set("X-Amz-Target", "AnyScaleScalingPlannerFrontendService.DescribeScalingPlanResources")
	r.Header.Set("X-Simulator-Token", token)
	w := httptest.NewRecorder()
	access.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"PartiallyActive"`) {
		t.Fatal(w.Code, w.Body.String())
	}

	// A participant token binds a principal that cannot widen its authority.
	operator := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		r.Header.Set("X-Simulator-Token", token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		access.ServeHTTP(w, r)
		return w
	}
	if w := operator("PUT", "/_simulator/v1/iam/principals/viewer/policy", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"autoscaling-plans:Describe*","Resource":"*"}]}`); w.Code >= 300 {
		t.Fatal(w.Code, w.Body.String())
	}
	issued := operator("POST", "/_simulator/v1/access", `{"Principal":"viewer"}`)
	var participant struct{ AccessToken string }
	if issued.Code != 200 || json.Unmarshal(issued.Body.Bytes(), &participant) != nil {
		t.Fatal(issued.Code, issued.Body.String())
	}
	if code, _ := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", `{}`, participant.AccessToken); code != 200 {
		t.Fatal("participant describe denied", code)
	}
	for _, target := range []string{"DeleteScalingPlan", "UpdateScalingPlan"} {
		if code, body := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService."+target, `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`, participant.AccessToken); code != 403 || body["__type"] != "AccessDeniedException" {
			t.Fatal(target, code, body)
		}
	}
	if code, _ := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService.DeleteScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`, participant.AccessToken, "X-Simulator-Principal", "simulator-admin"); code != 403 {
		t.Fatal("participant selected the administrator", code)
	}

	// A second team has its own process state, database and token authority.
	otherToken := strings.Repeat("d", 64)
	other, closeOther := autoScalingComposedServer(t, filepath.Join(t.TempDir(), "other.db"), otherToken)
	defer closeOther()
	if code, _ := autoScalingComposedCall(t, other, "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", `{}`, token); code != 401 {
		t.Fatal("cross-team token accepted", code)
	}
	if code, body := autoScalingComposedCall(t, other, "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", `{}`, otherToken); code != 200 || len(body["ScalingPlans"].([]any)) != 0 {
		t.Fatal("team state leaked", code, body)
	}

	capabilities := httptest.NewRequest("GET", "http://127.0.0.1/_simulator/v1/capabilities", nil)
	capabilities.Header.Set("X-Simulator-Token", token)
	w = httptest.NewRecorder()
	access.ServeHTTP(w, capabilities)
	var caps struct {
		Services map[string]struct {
			Operations     []string `json:"operations"`
			Console        bool     `json:"console"`
			Targets        []string `json:"targets"`
			ForecastData   bool     `json:"forecastData"`
			CloudFormation bool     `json:"cloudFormation"`
		} `json:"services"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	asp := caps.Services["autoscaling-plans"]
	if strings.Join(asp.Operations, ",") != "CreateScalingPlan,DescribeScalingPlans,DescribeScalingPlanResources,UpdateScalingPlan,DeleteScalingPlan" || asp.Console || asp.ForecastData || asp.CloudFormation || len(asp.Targets) != 2 {
		t.Fatal(w.Body.String())
	}
	if code, body := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService.DeleteScalingPlan", `{"ScalingPlanName":"shop","ScalingPlanVersion":1}`, token); code != 200 {
		t.Fatal(code, body)
	}
	if code, body := autoScalingComposedCall(t, access, "AnyScaleScalingPlannerFrontendService.DescribeScalingPlans", `{}`, token); code != 200 || len(body["ScalingPlans"].([]any)) != 0 {
		t.Fatal(code, body)
	}
}
