package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// policy is a deterministic authorizer: deny lists exact action/resource pairs.
type policy struct {
	mu    sync.Mutex
	deny  map[string]bool
	calls []string
	err   error
}

func (p *policy) authorize(_ context.Context, principal, action, resource string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, principal+" "+action+" "+resource)
	if p.err != nil {
		return "", p.err
	}
	if p.deny[action] || p.deny[action+" "+resource] {
		return "explicit-deny", nil
	}
	return "allowed", nil
}

type harness struct {
	t      *testing.T
	engine *Engine
	db     *sql.DB
	policy *policy
	now    time.Time
}

func newHarness(t *testing.T, services ...*Service) *harness {
	t.Helper()
	store, err := core.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &harness{t: t, db: store.Database(), policy: &policy{deny: map[string]bool{}}, now: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
	if len(services) == 0 {
		services = Services()
	}
	engine, err := New(store.Database(), Config{Regions: []string{"us-east-1", "ap-northeast-1"}, Account: "000000000000", DefaultPrincipal: "simulator-admin", Authorize: h.policy.authorize, Now: func() time.Time { return h.now }}, services)
	if err != nil {
		t.Fatal(err)
	}
	h.engine = engine
	return h
}

type reply struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r reply) code() string {
	if t, ok := r.body["__type"].(string); ok {
		return t
	}
	return r.header.Get("X-Amzn-ErrorType")
}

func (h *harness) do(req *http.Request) reply {
	h.t.Helper()
	if !h.engine.Owns(req) {
		h.t.Fatalf("engine does not own %s %s", req.Method, req.URL)
	}
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)
	out := reply{status: recorder.Code, header: recorder.Header(), raw: recorder.Body.String()}
	decoder := json.NewDecoder(bytes.NewReader(recorder.Body.Bytes()))
	decoder.UseNumber()
	_ = decoder.Decode(&out.body)
	return out
}

func sign(req *http.Request, service, region string) {
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMEXAMPLE/20261106/"+region+"/"+service+"/aws4_request, SignedHeaders=host, Signature=0")
}

// json1 sends one JSON-protocol operation for an engine-owned target.
func (h *harness) json1(model, op string, input any) reply {
	h.t.Helper()
	b := h.binding(model)
	data, _ := json.Marshal(input)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(data))
	req.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+"."+op)
	req.Header.Set("Content-Type", "application/x-amz-json-"+b.model.Metadata.JSONVersion)
	sign(req, b.signingName(), "us-east-1")
	return h.do(req)
}

func (h *harness) rest(model, method, path string, input any) reply {
	h.t.Helper()
	b := h.binding(model)
	var body []byte
	if input != nil {
		body, _ = json.Marshal(input)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	sign(req, b.signingName(), "us-east-1")
	return h.do(req)
}

func (h *harness) binding(model string) *binding {
	for _, b := range h.engine.bindings {
		if b.spec.Model == model {
			return b
		}
	}
	h.t.Fatalf("no binding %s", model)
	return nil
}

func expect(t *testing.T, r reply, status int, code string) {
	t.Helper()
	if r.status != status || code != "" && r.code() != code {
		t.Fatalf("want %d %s, got %d %s: %s", status, code, r.status, r.code(), r.raw)
	}
}

func str(v any) string { s, _ := v.(string); return s }

func TestEveryReviewedServiceBindsToItsOfficialModel(t *testing.T) {
	h := newHarness(t)
	if len(h.engine.bindings) != len(Services()) {
		t.Fatalf("bound %d of %d services", len(h.engine.bindings), len(Services()))
	}
	for _, b := range h.engine.bindings {
		if b.model.Source.Version != "1.41.6" || len(b.model.Source.SHA256) != 64 || b.model.Source.License != "Apache-2.0" {
			t.Fatalf("%s lacks pinned official provenance: %+v", b.spec.Model, b.model.Source)
		}
		if len(b.spec.Bounds) == 0 {
			t.Fatalf("%s must document its bounded differences", b.spec.Model)
		}
	}
	if len(embeddedModels()) != len(Services()) {
		t.Fatalf("embedded models %v do not match services", embeddedModels())
	}
}

func TestCodeConnectionsLifecycleProjectsOfficialShapes(t *testing.T) {
	h := newHarness(t)
	created := h.json1("codeconnections", "CreateConnection", map[string]any{"ConnectionName": "demo", "ProviderType": "GitHub", "Tags": []any{map[string]any{"Key": "team", "Value": "a"}}})
	expect(t, created, 200, "")
	arn := str(created.body["ConnectionArn"])
	if !strings.HasPrefix(arn, "arn:aws:codeconnections:us-east-1:000000000000:connection/") || created.header.Get("Content-Type") != "application/x-amz-json-1.0" {
		t.Fatalf("unexpected create %v %v", created.body, created.header)
	}
	got := h.json1("codeconnections", "GetConnection", map[string]any{"ConnectionArn": arn})
	expect(t, got, 200, "")
	connection := got.body["Connection"].(map[string]any)
	if connection["ConnectionStatus"] != "PENDING" || connection["OwnerAccountId"] != "000000000000" || connection["ProviderType"] != "GitHub" {
		t.Fatalf("connection projection %v", connection)
	}
	if _, leaked := connection["Tags"]; leaked {
		t.Fatal("Connection shape has no Tags member; it must not be projected")
	}
	expect(t, h.json1("codeconnections", "CreateConnection", map[string]any{"ConnectionName": "both", "ProviderType": "GitHub", "HostArn": "arn:aws:codeconnections:us-east-1:000000000000:host/x/abc"}), 400, "ValidationException")
	expect(t, h.json1("codeconnections", "CreateConnection", map[string]any{"ConnectionName": "host-missing", "HostArn": "arn:aws:codeconnections:us-east-1:000000000000:host/x/abc"}), 400, "ResourceNotFoundException")
	host := h.json1("codeconnections", "CreateHost", map[string]any{"Name": "ghe", "ProviderType": "GitHubEnterpriseServer", "ProviderEndpoint": "https://ghe.example"})
	expect(t, host, 200, "")
	hostArn := str(host.body["HostArn"])
	expect(t, h.json1("codeconnections", "CreateHost", map[string]any{"Name": "ghe", "ProviderType": "GitHubEnterpriseServer", "ProviderEndpoint": "https://ghe.example"}), 400, "")
	viaHost := h.json1("codeconnections", "CreateConnection", map[string]any{"ConnectionName": "on-host", "HostArn": hostArn})
	expect(t, viaHost, 200, "")
	filtered := h.json1("codeconnections", "ListConnections", map[string]any{"HostArnFilter": hostArn})
	if list := filtered.body["Connections"].([]any); len(list) != 1 || list[0].(map[string]any)["ConnectionName"] != "on-host" {
		t.Fatalf("host filter %v", filtered.body)
	}
	updated := h.json1("codeconnections", "UpdateHost", map[string]any{"HostArn": hostArn, "ProviderEndpoint": "https://new.example"})
	expect(t, updated, 200, "")
	if h.json1("codeconnections", "GetHost", map[string]any{"HostArn": hostArn}).body["ProviderEndpoint"] != "https://new.example" {
		t.Fatal("update did not persist")
	}
	expect(t, h.json1("codeconnections", "UpdateHost", map[string]any{"HostArn": hostArn, "ProviderEndpoint": "https://new.example", "Name": "x"}), 400, "ValidationException")
	expect(t, h.json1("codeconnections", "TagResource", map[string]any{"ResourceArn": arn, "Tags": []any{map[string]any{"Key": "x", "Value": "y"}}}), 200, "")
	expect(t, h.json1("codeconnections", "UntagResource", map[string]any{"ResourceArn": arn, "TagKeys": []any{"team"}}), 200, "")
	tags := h.json1("codeconnections", "ListTagsForResource", map[string]any{"ResourceArn": arn}).body["Tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["Key"] != "x" {
		t.Fatalf("tags %v", tags)
	}
	expect(t, h.json1("codeconnections", "DeleteConnection", map[string]any{"ConnectionArn": arn}), 200, "")
	expect(t, h.json1("codeconnections", "GetConnection", map[string]any{"ConnectionArn": arn}), 400, "ResourceNotFoundException")
	expect(t, h.json1("codeconnections", "ListTagsForResource", map[string]any{"ResourceArn": arn}), 400, "ResourceNotFoundException")
	expect(t, h.json1("codeconnections", "ListRepositoryLinks", map[string]any{}), 501, "UnknownOperationException")
	expect(t, h.json1("codeconnections", "NoSuchOperation", map[string]any{}), 400, "UnknownOperationException")
}

func TestJSONInputIsValidatedAgainstOfficialConstraints(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		name  string
		input any
	}{
		{"unknown member", map[string]any{"ConnectionName": "a", "ProviderType": "GitHub", "Extra": 1}},
		{"missing required", map[string]any{"ProviderType": "GitHub"}},
		{"enum", map[string]any{"ConnectionName": "a", "ProviderType": "Mercurial"}},
		{"too long", map[string]any{"ConnectionName": strings.Repeat("a", 33), "ProviderType": "GitHub"}},
		{"wrong type", map[string]any{"ConnectionName": 3, "ProviderType": "GitHub"}},
		{"null", map[string]any{"ConnectionName": nil, "ProviderType": "GitHub"}},
		{"list type", map[string]any{"ConnectionName": "a", "ProviderType": "GitHub", "Tags": "x"}},
		{"element type", map[string]any{"ConnectionName": "a", "ProviderType": "GitHub", "Tags": []any{"x"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expect(t, h.json1("codeconnections", "CreateConnection", c.input), 400, "ValidationException")
		})
	}
	expect(t, h.json1("codeconnections", "ListConnections", map[string]any{"MaxResults": "many"}), 400, "ValidationException")
	expect(t, h.json1("codeconnections", "ListConnections", map[string]any{"MaxResults": 0}), 400, "ValidationException")
	b := h.binding("codeconnections")
	req := httptest.NewRequest("POST", "/", strings.NewReader("{not json"))
	req.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+".ListConnections")
	expect(t, h.do(req), 400, "SerializationException")
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+".ListConnections")
	expect(t, h.do(req), 400, "SerializationException")
	req = httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat(" ", maxRequestBytes+1)))
	req.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+".ListConnections")
	expect(t, h.do(req), 400, "ValidationException")
	if len(h.policy.calls) != 0 {
		t.Fatalf("invalid requests must fail before authorization: %v", h.policy.calls)
	}
	var count int
	if err := h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources").Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid requests changed state: %d %v", count, err)
	}
}

func TestCredentialsRegionsAndScopesAreChecked(t *testing.T) {
	h := newHarness(t)
	b := h.binding("ecr")
	send := func(mutate func(*http.Request)) reply {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{"repositoryName":"rr"}`))
		req.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+".CreateRepository")
		mutate(req)
		return h.do(req)
	}
	expect(t, send(func(r *http.Request) {
		r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKIAREAL/20261106/us-east-1/ecr/aws4_request")
	}), 403, "AccessDeniedException")
	expect(t, send(func(r *http.Request) { r.Header.Set("X-Amz-Security-Token", "t") }), 400, "InvalidParameterException")
	expect(t, send(func(r *http.Request) {
		sign(r, "ecr", "us-east-1")
		r.Header.Set("X-Simulator-Region", "ap-northeast-1")
	}), 400, "InvalidParameterException")
	expect(t, send(func(r *http.Request) { sign(r, "ecr", "mars-1") }), 400, "InvalidParameterException")
	expect(t, send(func(r *http.Request) { r.Header.Set("X-Simulator-Region", "ap-northeast-1") }), 200, "")
	expect(t, send(func(r *http.Request) {}), 200, "")
	tokyo := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	tokyo.Header.Set("X-Amz-Target", b.model.Metadata.TargetPrefix+".DescribeRepositories")
	sign(tokyo, "ecr", "ap-northeast-1")
	if repos := h.do(tokyo).body["repositories"].([]any); len(repos) != 1 || !strings.Contains(str(repos[0].(map[string]any)["repositoryArn"]), "ap-northeast-1") {
		t.Fatalf("regions must be isolated: %v", repos)
	}
	expect(t, h.json1("ecr", "DescribeRepositories", map[string]any{"registryId": "123456789012"}), 400, "InvalidParameterException")
	if got := h.json1("ecr", "DescribeRepositories", map[string]any{"registryId": "000000000000"}); len(got.body["repositories"].([]any)) != 1 {
		t.Fatalf("account member %v", got.body)
	}
}

func TestAuthorizationPrecedesDisclosureAndMutation(t *testing.T) {
	h := newHarness(t)
	h.policy.deny["ecr:CreateRepository"] = true
	expect(t, h.json1("ecr", "CreateRepository", map[string]any{"repositoryName": "denied"}), 403, "AccessDeniedException")
	delete(h.policy.deny, "ecr:CreateRepository")
	created := h.json1("ecr", "CreateRepository", map[string]any{"repositoryName": "kept"})
	expect(t, created, 200, "")
	arn := str(created.body["repository"].(map[string]any)["repositoryArn"])
	if created.header.Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatal("decision header missing")
	}
	h.policy.deny["ecr:DeleteRepository "+arn] = true
	expect(t, h.json1("ecr", "DeleteRepository", map[string]any{"repositoryName": "kept"}), 403, "AccessDeniedException")
	h.policy.deny["ecr:DeleteRepository arn:aws:ecr:us-east-1:000000000000:repository/ghost"] = true
	// A denied caller cannot learn whether a resource exists.
	expect(t, h.json1("ecr", "DeleteRepository", map[string]any{"repositoryName": "ghost"}), 403, "AccessDeniedException")
	h.policy.deny["ecr:TagResource"] = true
	expect(t, h.json1("ecr", "TagResource", map[string]any{"resourceArn": arn, "tags": []any{map[string]any{"Key": "a", "Value": "b"}}}), 403, "AccessDeniedException")
	expect(t, h.json1("ecr", "TagResource", map[string]any{"resourceArn": "arn:aws:ecr:us-east-1:000000000000:repository/none", "tags": []any{}}), 403, "AccessDeniedException")
	h.policy.deny["ecr:DescribeRepositories"] = true
	expect(t, h.json1("ecr", "DescribeRepositories", map[string]any{}), 403, "AccessDeniedException")
	h.policy.err = errors.New("policy store unavailable")
	expect(t, h.json1("ecr", "PutImageScanningConfiguration", map[string]any{"repositoryName": "kept", "imageScanningConfiguration": map[string]any{"scanOnPush": true}}), 500, "ServerException")
	h.policy.err = nil
	var count int
	_ = h.db.QueryRow("SELECT count(*) FROM aws_catalog_resources").Scan(&count)
	if count != 1 {
		t.Fatalf("denied requests must not change state, got %d rows", count)
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	req.Header.Set("X-Amz-Target", h.binding("ecr").model.Metadata.TargetPrefix+".DescribeRepositories")
	req.Header.Set("X-Simulator-Principal", "team-a")
	h.do(req)
	if last := h.policy.calls[len(h.policy.calls)-1]; !strings.HasPrefix(last, "team-a ecr:DescribeRepositories arn:aws:ecr:us-east-1:000000000000:repository/*") {
		t.Fatalf("principal/resource mismatch: %s", last)
	}
}

func TestStepFunctionsDefinitionsConflictsAndUpdates(t *testing.T) {
	h := newHarness(t)
	definition := `{"StartAt":"A","States":{"A":{"Type":"Pass","End":true}}}`
	role := "arn:aws:iam::000000000000:role/sfn"
	created := h.json1("stepfunctions", "CreateStateMachine", map[string]any{"name": "flow", "definition": definition, "roleArn": role, "tags": []any{map[string]any{"key": "k", "value": "v"}}})
	expect(t, created, 200, "")
	if created.body["creationDate"] != json.Number("1793966400") {
		t.Fatalf("epoch seconds expected, got %v", created.body["creationDate"])
	}
	arn := str(created.body["stateMachineArn"])
	expect(t, h.json1("stepfunctions", "CreateStateMachine", map[string]any{"name": "flow", "definition": definition, "roleArn": role}), 400, "StateMachineAlreadyExists")
	for _, bad := range []string{"{}", "[]", `{"StartAt":"B","States":{"A":{"Type":"Pass"}}}`, `{"StartAt":"A","States":{"A":{"Type":"Teleport"}}}`} {
		expect(t, h.json1("stepfunctions", "CreateStateMachine", map[string]any{"name": "bad", "definition": bad, "roleArn": role}), 400, "InvalidDefinition")
	}
	described := h.json1("stepfunctions", "DescribeStateMachine", map[string]any{"stateMachineArn": arn})
	revision := str(described.body["revisionId"])
	if described.body["status"] != "ACTIVE" || described.body["type"] != "STANDARD" || len(revision) != 36 {
		t.Fatalf("describe %v", described.body)
	}
	expect(t, h.json1("stepfunctions", "DescribeStateMachine", map[string]any{"stateMachineArn": arn, "includedData": "ALL_DATA"}), 400, "ValidationException")
	h.now = h.now.Add(time.Minute)
	updated := h.json1("stepfunctions", "UpdateStateMachine", map[string]any{"stateMachineArn": arn, "definition": definition})
	expect(t, updated, 200, "")
	if updated.body["updateDate"] != json.Number("1793966460") || str(updated.body["revisionId"]) == revision {
		t.Fatalf("update %v", updated.body)
	}
	tags := h.json1("stepfunctions", "ListTagsForResource", map[string]any{"resourceArn": arn}).body["tags"].([]any)
	if tags[0].(map[string]any)["key"] != "k" {
		t.Fatalf("lowercase tag members %v", tags)
	}
	expect(t, h.json1("stepfunctions", "StartExecution", map[string]any{"stateMachineArn": arn}), 501, "UnknownOperationException")
	expect(t, h.json1("stepfunctions", "DeleteStateMachine", map[string]any{"stateMachineArn": arn}), 200, "")
	expect(t, h.json1("stepfunctions", "DescribeStateMachine", map[string]any{"stateMachineArn": arn}), 400, "StateMachineDoesNotExist")
}

func TestPaginationTokensAreBoundToTheRequest(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		expect(t, h.json1("stepfunctions", "CreateActivity", map[string]any{"name": fmt.Sprintf("a%d", i)}), 200, "")
	}
	var names []string
	token := ""
	for pages := 0; pages < 10; pages++ {
		input := map[string]any{"maxResults": 2}
		if token != "" {
			input["nextToken"] = token
		}
		page := h.json1("stepfunctions", "ListActivities", input)
		expect(t, page, 200, "")
		for _, item := range page.body["activities"].([]any) {
			names = append(names, str(item.(map[string]any)["name"]))
		}
		token = str(page.body["nextToken"])
		if token == "" {
			break
		}
	}
	if strings.Join(names, ",") != "a0,a1,a2,a3,a4" {
		t.Fatalf("pages %v", names)
	}
	first := h.json1("stepfunctions", "ListActivities", map[string]any{"maxResults": 1})
	stale := str(first.body["nextToken"])
	expect(t, h.json1("stepfunctions", "ListStateMachines", map[string]any{"nextToken": stale}), 400, "InvalidToken")
	expect(t, h.json1("stepfunctions", "ListActivities", map[string]any{"nextToken": "!!"}), 400, "InvalidToken")
	expect(t, h.json1("stepfunctions", "ListActivities", map[string]any{"maxResults": 1001}), 400, "")
}

func TestAmplifyRESTRoutingParentsAndChildren(t *testing.T) {
	h := newHarness(t)
	app := h.rest("amplify", "POST", "/apps", map[string]any{"name": "site", "tags": map[string]any{"a": "b"}})
	expect(t, app, 200, "")
	body := app.body["app"].(map[string]any)
	appID := str(body["appId"])
	if body["defaultDomain"] != appID+".amplifyapp.com" || body["platform"] != "WEB" || body["tags"].(map[string]any)["a"] != "b" {
		t.Fatalf("app %v", body)
	}
	expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": "s", "oauthToken": "secret"}), 400, "BadRequestException")
	expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": "s", "basicAuthCredentials": "dXNlcjpwYXNz"}), 400, "BadRequestException")
	expect(t, h.rest("amplify", "POST", "/apps", map[string]any{"name": "s", "enableBasicAuth": true}), 400, "BadRequestException")
	branch := h.rest("amplify", "POST", "/apps/"+appID+"/branches", map[string]any{"branchName": "main", "stage": "PRODUCTION"})
	expect(t, branch, 200, "")
	if b := branch.body["branch"].(map[string]any); b["branchArn"] != "arn:aws:amplify:us-east-1:000000000000:apps/"+appID+"/branches/main" || b["displayName"] != "main" {
		t.Fatalf("branch %v", b)
	}
	expect(t, h.rest("amplify", "POST", "/apps/"+appID+"/branches", map[string]any{"branchName": "main"}), 400, "BadRequestException")
	expect(t, h.rest("amplify", "POST", "/apps/dmissing/branches", map[string]any{"branchName": "x"}), 404, "NotFoundException")
	expect(t, h.rest("amplify", "GET", "/apps/"+appID+"/branches/main", nil), 200, "")
	expect(t, h.rest("amplify", "GET", "/apps/"+appID+"/branches/feature%2Fx", nil), 404, "NotFoundException")
	listed := h.rest("amplify", "GET", "/apps/"+appID+"/branches?maxResults=10", nil)
	if len(listed.body["branches"].([]any)) != 1 {
		t.Fatalf("branches %v", listed.body)
	}
	expect(t, h.rest("amplify", "GET", "/apps?bogus=1", nil), 400, "BadRequestException")
	expect(t, h.rest("amplify", "GET", "/apps?maxResults=1&maxResults=2", nil), 400, "BadRequestException")
	expect(t, h.rest("amplify", "DELETE", "/apps/"+appID, nil), 400, "BadRequestException")
	expect(t, h.rest("amplify", "POST", "/apps/"+appID, map[string]any{"description": "updated"}), 200, "")
	expect(t, h.rest("amplify", "POST", "/apps/"+appID, map[string]any{"repository": "https://example.invalid"}), 400, "BadRequestException")
	expect(t, h.rest("amplify", "POST", "/apps/"+appID, map[string]any{"appId": "other"}), 400, "BadRequestException")
	arn := "arn:aws:amplify:us-east-1:000000000000:apps/" + appID
	escaped := strings.NewReplacer(":", "%3A", "/", "%2F").Replace(arn)
	expect(t, h.rest("amplify", "POST", "/tags/"+escaped, map[string]any{"tags": map[string]any{"c": "d"}}), 200, "")
	expect(t, h.rest("amplify", "DELETE", "/tags/"+escaped+"?tagKeys=a&tagKeys=c", nil), 200, "")
	if tags := h.rest("amplify", "GET", "/tags/"+escaped, nil).body["tags"].(map[string]any); len(tags) != 0 {
		t.Fatalf("untag %v", tags)
	}
	expect(t, h.rest("amplify", "GET", "/webhooks/x", nil), 501, "UnknownOperationException")
	expect(t, h.rest("amplify", "PATCH", "/nowhere", nil), 404, "UnknownOperationException")
	expect(t, h.rest("amplify", "DELETE", "/apps/"+appID+"/branches/main", nil), 200, "")
	deleted := h.rest("amplify", "DELETE", "/apps/"+appID, nil)
	expect(t, deleted, 200, "")
	if deleted.body["app"].(map[string]any)["name"] != "site" {
		t.Fatalf("delete output %v", deleted.body)
	}
}

func TestIdempotencyTokensReplayAndRejectMismatch(t *testing.T) {
	// Exercise the generic token path through a synthetic model with an
	// idempotency-token member.
	h := syntheticHarness(t)
	first := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "w", "ClientToken": "t1", "Size": 3})
	expect(t, first, 200, "")
	replay := h.json1("synthetic", "CreateWidget", map[string]any{"Name": "w", "ClientToken": "t1", "Size": 3})
	expect(t, replay, 200, "")
	if first.body["WidgetArn"] != replay.body["WidgetArn"] {
		t.Fatalf("replay created a new resource: %v %v", first.body, replay.body)
	}
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "w", "ClientToken": "t1", "Size": 4}), 400, "IdempotentParameterMismatchException")
	expect(t, h.json1("synthetic", "DeleteWidget", map[string]any{"WidgetId": str(first.body["WidgetId"])}), 200, "")
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "w", "ClientToken": "t1", "Size": 3}), 409, "ConflictException")
	h.now = h.now.Add(25 * time.Hour)
	expect(t, h.json1("synthetic", "CreateWidget", map[string]any{"Name": "w", "ClientToken": "t1", "Size": 3}), 200, "")
}
