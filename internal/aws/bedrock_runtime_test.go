package aws

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/inference"
)

// Every prompt and model reply in these tests is synthetic. The recording
// backend and fake Ollama server are test doubles for the backend boundary;
// they are not evidence of behavior with a real model, Apple hardware or
// Claude Code.

const (
	bedrockTestModel = "anthropic.claude-3-haiku-20240307-v1:0"
	bedrockTestPath  = "/model/anthropic.claude-3-haiku-20240307-v1%3A0/"
	bedrockTestAuth  = "AWS4-HMAC-SHA256 Credential=TCSIMEXAMPLE/20261111/us-east-1/bedrock/aws4_request, SignedHeaders=host;x-amz-date, Signature=0"
)

type recordingBackend struct {
	mu       sync.Mutex
	requests []inference.Request
	result   inference.Result
	err      error
	started  chan struct{}
	release  chan struct{}
}

func (b *recordingBackend) Name() string { return "recording" }
func (b *recordingBackend) ValidModel(name string) bool {
	return name == "local-a" || name == "local-b"
}
func (b *recordingBackend) Generate(ctx context.Context, request inference.Request) (inference.Result, error) {
	b.mu.Lock()
	b.requests = append(b.requests, request)
	started, release := b.started, b.release
	b.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return inference.Result{}, ctx.Err()
		}
	}
	return b.result, b.err
}
func (b *recordingBackend) calls() []inference.Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]inference.Request(nil), b.requests...)
}

type bedrockHarness struct {
	gateway    *Gateway
	identities *core.Identities
	database   string
	server     *httptest.Server
}

func newBedrockHarness(t *testing.T, backend inference.Backend, timeout time.Duration, concurrency int) *bedrockHarness {
	t.Helper()
	database := filepath.Join(t.TempDir(), "bedrock.db")
	store, err := core.New(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	gateway := &Gateway{Store: store, Identities: core.NewIdentities(store)}
	if backend != nil {
		runtime, err := NewBedrockRuntime(BedrockRuntimeConfig{Backend: backend, Models: map[string]string{bedrockTestModel: "local-a", "amazon.nova-micro-v1:0": "local-b"}, Timeout: timeout, Concurrency: concurrency})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(runtime.Close)
		gateway.BedrockRuntime = runtime
	}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	return &bedrockHarness{gateway: gateway, identities: gateway.Identities, database: database, server: server}
}

type bedrockReply struct {
	status int
	header http.Header
	body   map[string]any
	raw    []byte
}

func (h *bedrockHarness) call(t *testing.T, method, path string, headers map[string]string, body string) bedrockReply {
	t.Helper()
	request, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", bedrockTestAuth)
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		if value == "" {
			request.Header.Del(name)
		} else {
			request.Header.Set(name, value)
		}
	}
	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	reply := bedrockReply{status: response.StatusCode, header: response.Header, raw: data}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &reply.body); err != nil {
			t.Fatalf("non-JSON body %q", data)
		}
	}
	if response.Header.Get("X-Amzn-RequestId") == "" {
		t.Fatal("missing request ID")
	}
	return reply
}

func expectBedrockError(t *testing.T, reply bedrockReply, status int, code string) {
	t.Helper()
	if reply.status != status || reply.header.Get("X-Amzn-ErrorType") != code || reply.body["message"] == "" {
		t.Fatalf("got %d %s %v; wanted %d %s", reply.status, reply.header.Get("X-Amzn-ErrorType"), reply.body, status, code)
	}
}

const anthropicBody = `{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"system":"synthetic system","temperature":0.5,"top_p":0.75,"top_k":20,"messages":[{"role":"user","content":"synthetic hello"},{"role":"assistant","content":[{"type":"text","text":"synthetic reply"}]},{"role":"user","content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}]}`
const converseBody = `{"messages":[{"role":"user","content":[{"text":"synthetic converse"}]}],"system":[{"text":"rule one"},{"text":"rule two"}],"inferenceConfig":{"maxTokens":32,"temperature":0,"topP":1}}`

func TestBedrockRuntimeRoutingPrecedesControlPlaneCatalog(t *testing.T) {
	cases := []struct {
		service, auth, path string
		want                bool
	}{
		{"", bedrockTestAuth, "/model/x.y/invoke", true},
		{"", bedrockTestAuth, "/async-invoke", true},
		{"", bedrockTestAuth, "/async-invoke/arn", true},
		{"", bedrockTestAuth, "/guardrail/abc/version/1/apply", true},
		{"", bedrockTestAuth, "/guardrails", false},
		{"", bedrockTestAuth, "/inference-profiles", false},
		{"bedrock", "", "/model/x.y/converse", true},
		{"bedrock", "", "/guardrails", false},
		{"bedrock-runtime", "", "/", true},
		{"bedrock-runtime", strings.Replace(bedrockTestAuth, "/bedrock/", "/s3/", 1), "/anything", true},
		{"s3", bedrockTestAuth, "/model/x.y/invoke", false},
		{"", strings.Replace(bedrockTestAuth, "/bedrock/", "/s3/", 1), "/model/x.y/invoke", false},
		{"", "", "/model/x.y/invoke", false},
		{"", "Bearer synthetic-key", "/model/x.y/invoke", true},
		{"", "Bearer synthetic-key", "/guardrails", false},
	}
	for _, test := range cases {
		r := httptest.NewRequest("POST", "http://localhost"+test.path, nil)
		if test.service != "" {
			r.Header.Set("X-Simulator-Service", test.service)
		}
		if test.auth != "" {
			r.Header.Set("Authorization", test.auth)
		}
		if got := isBedrockRuntime(r); got != test.want {
			t.Fatalf("%+v: got %v", test, got)
		}
	}
}

func TestBedrockRuntimeDisabledAndUnsupportedOperationsFailExplicitly(t *testing.T) {
	disabled := newBedrockHarness(t, nil, 0, 0)
	for _, operation := range []string{"invoke", "converse"} {
		reply := disabled.call(t, "POST", bedrockTestPath+operation, nil, converseBody)
		expectBedrockError(t, reply, 501, "UnknownOperationException")
		if !strings.Contains(reply.body["message"].(string), "disabled") {
			t.Fatal("disabled reason missing")
		}
	}
	backend := &recordingBackend{}
	enabled := newBedrockHarness(t, backend, time.Second, 1)
	for _, harness := range []*bedrockHarness{disabled, enabled} {
		for _, route := range [][2]string{{"POST", bedrockTestPath + "invoke-with-response-stream"}, {"POST", bedrockTestPath + "converse-stream"}, {"POST", bedrockTestPath + "invoke-with-bidirectional-stream"},
			{"POST", bedrockTestPath + "count-tokens"}, {"POST", "/async-invoke"}, {"GET", "/async-invoke"}, {"GET", "/async-invoke/arn%3Aaws%3Abedrock"}, {"POST", "/guardrail/abc/version/DRAFT/apply"}} {
			reply := harness.call(t, route[0], route[1], nil, `{}`)
			expectBedrockError(t, reply, 501, "UnknownOperationException")
		}
		for _, route := range [][2]string{{"GET", bedrockTestPath + "invoke"}, {"POST", bedrockTestPath + "unknown"}, {"POST", bedrockTestPath + "invoke/"}, {"POST", "/model//invoke"},
			{"DELETE", "/async-invoke"}, {"POST", "/guardrail/abc/apply"}} {
			expectBedrockError(t, harness.call(t, route[0], route[1], nil, `{}`), 404, "UnknownOperationException")
		}
		r := httptest.NewRequest("POST", "http://localhost/", nil)
		r.Header.Set("X-Simulator-Service", "bedrock-runtime")
		w := httptest.NewRecorder()
		harness.gateway.ServeHTTP(w, r)
		if w.Code != 404 || w.Header().Get("X-Amzn-ErrorType") != "UnknownOperationException" {
			t.Fatal("explicit bedrock-runtime selection fell through to another service")
		}
	}
	if len(backend.calls()) != 0 {
		t.Fatal("unsupported operation reached the backend")
	}
}

func TestBedrockInvokeModelTranslatesAnthropicMessagesExactly(t *testing.T) {
	backend := &recordingBackend{result: inference.Result{Text: "synthetic <answer> & more", Stop: inference.StopLength, InputTokens: 11, OutputTokens: 64}}
	harness := newBedrockHarness(t, backend, time.Second, 1)
	reply := harness.call(t, "POST", bedrockTestPath+"invoke", map[string]string{"Accept": "application/json"}, anthropicBody)
	if reply.status != 200 || reply.header.Get("Content-Type") != "application/json" || reply.header.Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatalf("invoke reply %d %v", reply.status, reply.body)
	}
	if reply.header.Get("X-Amzn-Bedrock-Input-Token-Count") != "11" || reply.header.Get("X-Amzn-Bedrock-Output-Token-Count") != "64" || reply.header.Get("X-Amzn-Bedrock-Invocation-Latency") == "" {
		t.Fatal("token or latency headers missing")
	}
	if !bytes.Contains(reply.raw, []byte(`"synthetic <answer> & more"`)) {
		t.Fatal("reply text was HTML-escaped")
	}
	id, _ := reply.body["id"].(string)
	if !strings.HasPrefix(id, "msg_bdrk_") || len(id) != len("msg_bdrk_")+24 {
		t.Fatalf("message id %q", id)
	}
	delete(reply.body, "id")
	encoded, _ := json.Marshal(reply.body)
	want := `{"content":[{"text":"synthetic \u003canswer\u003e \u0026 more","type":"text"}],"model":"anthropic.claude-3-haiku-20240307-v1:0","role":"assistant","stop_reason":"max_tokens","stop_sequence":null,"type":"message","usage":{"input_tokens":11,"output_tokens":64}}`
	if string(encoded) != want {
		t.Fatalf("invoke body %s", encoded)
	}
	calls := backend.calls()
	temperature, topP, topK := 0.5, 0.75, 20
	if len(calls) != 1 {
		t.Fatal("backend call count")
	}
	got := calls[0]
	if got.Model != "local-a" || got.System != "synthetic system" || got.MaxTokens != 64 || *got.Temperature != temperature || *got.TopP != topP || *got.TopK != topK ||
		len(got.Messages) != 3 || got.Messages[1] != (inference.Message{Role: inference.RoleAssistant, Text: "synthetic reply"}) || got.Messages[2].Text != "line one\nline two" {
		t.Fatalf("translated request %+v", got)
	}
	backend.result = inference.Result{Stop: inference.StopEnd}
	reply = harness.call(t, "POST", bedrockTestPath+"invoke", map[string]string{"Content-Type": "", "Accept": "*/*"}, `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":[{"role":"user","content":"x"}],"system":[{"type":"text","text":"s"}]}`)
	if reply.status != 200 || reply.body["stop_reason"] != "end_turn" || len(reply.body["content"].([]any)) != 0 {
		t.Fatalf("empty end_turn reply %v", reply.body)
	}
	if last := backend.calls()[1]; last.Temperature != nil || last.TopP != nil || last.TopK != nil || last.System != "s" {
		t.Fatal("absent options were invented")
	}
}

func TestBedrockConverseTranslatesTextBlocksAndARNModelIDs(t *testing.T) {
	backend := &recordingBackend{result: inference.Result{Text: "synthetic converse reply", Stop: inference.StopEnd, InputTokens: 5, OutputTokens: 4}}
	harness := newBedrockHarness(t, backend, time.Second, 1)
	arn := url.PathEscape("arn:aws:bedrock:us-east-1::foundation-model/" + bedrockTestModel)
	reply := harness.call(t, "POST", "/model/"+arn+"/converse", nil, converseBody)
	if reply.status != 200 {
		t.Fatalf("converse %d %v", reply.status, reply.body)
	}
	metrics := reply.body["metrics"].(map[string]any)
	if _, ok := metrics["latencyMs"].(float64); !ok {
		t.Fatal("latency metric missing")
	}
	delete(reply.body, "metrics")
	encoded, _ := json.Marshal(reply.body)
	if string(encoded) != `{"output":{"message":{"content":[{"text":"synthetic converse reply"}],"role":"assistant"}},"stopReason":"end_turn","usage":{"inputTokens":5,"outputTokens":4,"totalTokens":9}}` {
		t.Fatalf("converse body %s", encoded)
	}
	got := backend.calls()[0]
	if got.Model != "local-a" || got.System != "rule one\nrule two" || got.MaxTokens != 32 || *got.Temperature != 0 || *got.TopP != 1 || got.TopK != nil || got.Messages[0].Text != "synthetic converse" {
		t.Fatalf("translated converse %+v", got)
	}
	backend.result = inference.Result{Stop: inference.StopLength}
	reply = harness.call(t, "POST", "/model/amazon.nova-micro-v1%3A0/converse", nil, `{"messages":[{"role":"user","content":[{"text":"x"}]},{"role":"assistant","content":[{"text":"y"}]},{"role":"user","content":[{"text":"z"}]}],"system":[]}`)
	if reply.status != 200 || reply.body["stopReason"] != "max_tokens" {
		t.Fatalf("converse length %v", reply.body)
	}
	if last := backend.calls()[1]; last.Model != "local-b" || last.MaxTokens != BedrockDefaultMaxTokens || last.System != "" {
		t.Fatalf("default converse request %+v", last)
	}
}

func TestBedrockRuntimeRejectsUnsupportedInputBeforeTheBackend(t *testing.T) {
	backend := &recordingBackend{}
	harness := newBedrockHarness(t, backend, time.Second, 1)
	invoke := []string{
		``, `[]`, `{"anthropic_version":"bedrock-2023-05-31"}{}`, "{\"anthropic_version\":\"bedrock-2023-05-31\",\"max_tokens\":1,\"messages\":[{\"role\":\"user\",\"content\":\"\xff\"}]}",
		`{"max_tokens":1,"messages":[{"role":"user","content":"x"}]}`,
		`{"anthropic_version":"bedrock-2023-05-31","messages":[{"role":"user","content":"x"}]}`,
		`{"anthropic_version":"bedrock-2023-05-31","max_tokens":1}`,
		`{"anthropic_version":1,"max_tokens":1,"messages":[{"role":"user","content":"x"}]}`,
		`{"anthropic_version":"2023-06-01","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`,
	}
	base := func(extra string) string {
		return `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":[{"role":"user","content":"x"}]` + extra + `}`
	}
	for _, extra := range []string{`,"max_tokens":2`, `,"messages":[{"role":"user","content":"y"}]`, `,"tools":[]`, `,"tool_choice":{}`, `,"stop_sequences":["x"]`, `,"stream":true`, `,"thinking":{}`, `,"metadata":{}`, `,"anthropic_beta":[]`, `,"system":null`,
		`,"temperature":1.5`, `,"temperature":"0.5"`, `,"top_p":-0.1`, `,"top_k":0`, `,"top_k":501`, `,"top_k":1.5`, `,"system":[]`, `,"system":[{"type":"image"}]`, `,"system":7`} {
		invoke = append(invoke, base(extra))
	}
	message := func(messages string) string {
		return `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":` + messages + `}`
	}
	for _, messages := range []string{`[]`, `{}`, `[1]`, `[{"role":"user"}]`, `[{"content":"x"}]`, `[{"role":"system","content":"x"}]`, `[{"role":7,"content":"x"}]`,
		`[{"role":"assistant","content":"x"},{"role":"user","content":"y"}]`, `[{"role":"user","content":"x"},{"role":"assistant","content":"y"}]`,
		`[{"role":"user","content":""}]`, `[{"role":"user","content":"  "}]`, `[{"role":"user","content":[]}]`, `[{"role":"user","content":[{"type":"image","source":{}}]}]`,
		`[{"role":"user","content":[{"type":"text"}]}]`, `[{"role":"user","content":[{"type":"text","text":"x","cache_control":{}}]}]`, `[{"role":"user","content":[{"type":1,"text":"x"}]}]`,
		`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x"}]}]`, `[{"role":"user","content":7}]`, `[{"role":"user","content":"x","name":"y"}]`} {
		invoke = append(invoke, message(messages))
	}
	for _, maxTokens := range []string{`0`, `8193`, `"1"`, `1.5`, `1e2`, `-1`} {
		invoke = append(invoke, `{"anthropic_version":"bedrock-2023-05-31","max_tokens":`+maxTokens+`,"messages":[{"role":"user","content":"x"}]}`)
	}
	for _, body := range invoke {
		expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"invoke", nil, body), 400, "ValidationException")
	}
	converse := []string{``, `{}`, `{"messages":null}`, `{"modelId":"x","messages":[]}`,
		`{"messages":[{"role":"user","content":"x"}]}`,
		`{"messages":[{"role":"user","content":[]}]}`,
		`{"messages":[{"role":"user","content":[{}]}]}`,
		`{"messages":[{"role":"user","content":[7]}]}`,
		`{"messages":[{"role":"user","content":[{"text":"x","image":{}}]}]}`,
		`{"messages":[{"role":"user","content":[{"text":" "}]}]}`,
		`{"messages":[{"role":"user","content":[{"text":"x"}]},{"role":"user","content":[{"text":"y"}]}]}`,
		`{"messages":[{"role":"user","content":[{"text":"x"}]},{"role":"assistant","content":[{"text":"y"}]}]}`,
		`{"messages":[{"role":"user","content":[{"text":"x"}],"extra":1}]}`,
		`{"messages":[{"role":"user"}]}`,
		`{"messages":[{"role":"robot","content":[{"text":"x"}]}]}`,
	}
	valid := `"messages":[{"role":"user","content":[{"text":"x"}]}]`
	for _, extra := range []string{`"toolConfig":{}`, `"guardrailConfig":{}`, `"additionalModelRequestFields":{}`, `"promptVariables":{}`, `"additionalModelResponseFieldPaths":[]`,
		`"requestMetadata":{"k":"v"}`, `"performanceConfig":{"latency":"standard"}`, `"serviceTier":{"type":"default"}`, `"system":[{"cachePoint":{"type":"default"}}]`, `"system":[{"guardContent":{}}]`,
		`"system":{}`, `"inferenceConfig":{"stopSequences":["x"]}`, `"inferenceConfig":{"maxTokens":0}`, `"inferenceConfig":{"maxTokens":9000}`, `"inferenceConfig":{"temperature":2}`,
		`"inferenceConfig":{"topP":"1"}`, `"inferenceConfig":[]`, `"inferenceConfig":{"maxTokens":9000,"maxTokens":5}`, `"system":[{"text":"a","text":"b"}]`,
		`"messages":[{"role":"user","content":[{"text":"y"}]}]`, `"inferenceConfig":{"maxTokens":5}x`, `"inferenceConfig":{7:1}`, `"inferenceConfig":{"maxTokens":5`} {
		converse = append(converse, `{`+valid+`,`+extra+`}`)
	}
	for _, block := range []string{`{"image":{"format":"png","source":{"bytes":"eA=="}}}`, `{"document":{}}`, `{"video":{}}`, `{"toolUse":{}}`, `{"toolResult":{}}`, `{"guardContent":{}}`,
		`{"cachePoint":{"type":"default"}}`, `{"reasoningContent":{}}`, `{"citationsContent":{}}`, `{"searchResult":{}}`, `{"text":7}`} {
		converse = append(converse, `{"messages":[{"role":"user","content":[`+block+`]}]}`)
	}
	for _, body := range converse {
		expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"converse", nil, body), 400, "ValidationException")
	}
	if len(backend.calls()) != 0 {
		t.Fatal("rejected input reached the backend")
	}
}

func TestBedrockRuntimeRejectsUnsupportedTransportOptions(t *testing.T) {
	backend := &recordingBackend{result: inference.Result{Text: "x", Stop: inference.StopEnd}}
	harness := newBedrockHarness(t, backend, time.Second, 1)
	invoke := bedrockTestPath + "invoke"
	body := `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`
	for _, test := range []struct {
		path    string
		headers map[string]string
		status  int
		code    string
	}{
		{invoke, map[string]string{"X-Amzn-Bedrock-Trace": "ENABLED"}, 400, "ValidationException"},
		{invoke, map[string]string{"X-Amzn-Bedrock-GuardrailIdentifier": "abc"}, 400, "ValidationException"},
		{invoke, map[string]string{"X-Amzn-Bedrock-PerformanceConfig-Latency": "standard"}, 400, "ValidationException"},
		{invoke, map[string]string{"X-Amzn-Bedrock-Service-Tier": "default"}, 400, "ValidationException"},
		{invoke, map[string]string{"Content-Type": "text/plain"}, 400, "ValidationException"},
		{invoke, map[string]string{"Content-Type": "application/json; charset"}, 400, "ValidationException"},
		{invoke, map[string]string{"Accept": "text/html"}, 400, "ValidationException"},
		{bedrockTestPath + "converse", map[string]string{"Content-Type": ""}, 400, "ValidationException"},
		{invoke + "?trace=1", nil, 400, "ValidationException"},
		{invoke + "?", nil, 400, "ValidationException"},
		{invoke, map[string]string{"X-Amz-Target": "AmazonBedrockRuntime.InvokeModel"}, 400, "ValidationException"},
		{invoke, map[string]string{"X-Amz-Security-Token": "x"}, 400, "ValidationException"},
		{invoke, map[string]string{"Authorization": strings.Replace(bedrockTestAuth, "TCSIMEXAMPLE", "AKIAEXAMPLE", 1)}, 403, "AccessDeniedException"},
		{invoke, map[string]string{"Authorization": "Bearer token"}, 403, "AccessDeniedException"},
		{invoke, map[string]string{"X-Simulator-Region": "us-west-2"}, 400, "ValidationException"},
		{invoke, map[string]string{"Authorization": strings.Replace(bedrockTestAuth, "us-east-1", "sa-east-1", 1)}, 400, "ValidationException"},
		{"/model/" + url.PathEscape("arn:aws:bedrock:us-west-2::foundation-model/"+bedrockTestModel) + "/invoke", nil, 400, "ValidationException"},
		{"/model/" + url.PathEscape("arn:aws:bedrock:us-east-1:000000000000:inference-profile/us."+bedrockTestModel) + "/invoke", nil, 400, "ValidationException"},
		{"/model/Not_A_Model/invoke", nil, 400, "ValidationException"},
		{"/model/" + strings.Repeat("a", 2049) + "/invoke", nil, 400, "ValidationException"},
		{"/model/meta.llama3-8b-instruct-v1%3A0/invoke", nil, 404, "ResourceNotFoundException"},
	} {
		expectBedrockError(t, harness.call(t, "POST", test.path, test.headers, body), test.status, test.code)
	}
	request, _ := http.NewRequest("POST", harness.server.URL+invoke, strings.NewReader(body))
	request.Header.Add("Authorization", bedrockTestAuth)
	request.Header.Add("Authorization", bedrockTestAuth)
	response, err := harness.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatal("duplicate authority headers accepted")
	}
	oversized := `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":[{"role":"user","content":"` + strings.Repeat("x", BedrockRuntimeRequestBytes) + `"}]}`
	expectBedrockError(t, harness.call(t, "POST", invoke, nil, oversized), 400, "ValidationException")
	unsigned := harness.call(t, "POST", invoke, map[string]string{"Authorization": "", "X-Simulator-Service": "bedrock-runtime", "X-Simulator-Region": "ap-northeast-1"}, body)
	if unsigned.status != 200 {
		t.Fatalf("unsigned first-party selection %d %v", unsigned.status, unsigned.body)
	}
	if len(backend.calls()) != 1 {
		t.Fatal("rejected transport options reached the backend")
	}
}

func TestBedrockRuntimeEvaluatesInvokeModelPermissionBeforeMapping(t *testing.T) {
	backend := &recordingBackend{result: inference.Result{Text: "x", Stop: inference.StopEnd}}
	harness := newBedrockHarness(t, backend, time.Second, 1)
	body := `{"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`
	participant := map[string]string{"X-Simulator-Principal": "learner"}
	expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"converse", participant, body), 403, "AccessDeniedException")
	allowed := "arn:aws:bedrock:us-east-1::foundation-model/" + bedrockTestModel
	policy := core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"bedrock:InvokeModel"}, Resource: core.StringList{allowed}}}}
	if err := harness.identities.Set("learner", policy); err != nil {
		t.Fatal(err)
	}
	if reply := harness.call(t, "POST", bedrockTestPath+"converse", participant, body); reply.status != 200 || reply.header.Get("X-Simulator-IAM-Decision") != "allowed" {
		t.Fatalf("allowed participant %d", reply.status)
	}
	denied := harness.call(t, "POST", "/model/amazon.nova-micro-v1%3A0/invoke", participant, `{}`)
	expectBedrockError(t, denied, 403, "AccessDeniedException")
	if denied.header.Get("X-Simulator-IAM-Decision") != "implicitDeny" || !strings.Contains(denied.body["message"].(string), "bedrock:InvokeModel") {
		t.Fatal("denial does not name the evaluated action")
	}
	unmapped := harness.call(t, "POST", "/model/meta.llama3-8b-instruct-v1%3A0/converse", participant, body)
	expectBedrockError(t, unmapped, 403, "AccessDeniedException")
	policy.Statement = append(policy.Statement, core.Statement{Effect: "Deny", Action: core.StringList{"bedrock:*"}, Resource: core.StringList{allowed}})
	if err := harness.identities.Set("learner", policy); err != nil {
		t.Fatal(err)
	}
	expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"converse", participant, body), 403, "AccessDeniedException")
	if len(backend.calls()) != 1 {
		t.Fatal("denied requests reached the backend")
	}
	harness.gateway.Identities = nil
	expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"converse", participant, body), 403, "AccessDeniedException")
}

func TestBedrockRuntimeIdentityStateFailureIsInternal(t *testing.T) {
	harness := newBedrockHarness(t, &recordingBackend{}, time.Second, 1)
	if err := harness.gateway.Store.Close(); err != nil {
		t.Fatal(err)
	}
	expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"converse", map[string]string{"X-Simulator-Principal": "learner"}, converseBody), 500, "InternalServerException")
}

// fakeOllamaServer is a synthetic test double of the Ollama HTTP API.
func fakeOllamaServer(t *testing.T, handler http.HandlerFunc) *inference.Ollama {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	backend, err := inference.NewOllama(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func newOllamaHarness(t *testing.T, timeout time.Duration, concurrency int, handler http.HandlerFunc) *bedrockHarness {
	t.Helper()
	database := filepath.Join(t.TempDir(), "bedrock.db")
	store, err := core.New(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	runtime, err := NewBedrockRuntime(BedrockRuntimeConfig{Backend: fakeOllamaServer(t, handler), Models: map[string]string{bedrockTestModel: "synthetic:latest"}, Timeout: timeout, Concurrency: concurrency})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	gateway := &Gateway{Store: store, Identities: core.NewIdentities(store), BedrockRuntime: runtime}
	server := httptest.NewServer(gateway)
	t.Cleanup(server.Close)
	return &bedrockHarness{gateway: gateway, identities: gateway.Identities, database: database, server: server}
}

func TestBedrockRuntimeMapsOllamaFailuresWithoutFallback(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		want       int
		code       string
	}{
		{"model not installed", `{"error":"model not found, try pulling it first"}`, 404, 404, "ResourceNotFoundException"},
		{"backend busy", `{"error":"server busy"}`, 503, 429, "ThrottlingException"},
		{"backend error", `{"error":"synthetic failure"}`, 500, 424, "ModelErrorException"},
		{"reasoning output", `{"message":{"role":"assistant","content":"x","thinking":"y"},"done":true,"done_reason":"stop"}`, 200, 424, "ModelErrorException"},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness := newOllamaHarness(t, time.Second, 1, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			reply := harness.call(t, "POST", bedrockTestPath+"converse", nil, converseBody)
			expectBedrockError(t, reply, test.want, test.code)
			if test.code == "ModelErrorException" {
				if reply.body["resourceName"] != bedrockTestModel || reply.body["originalStatusCode"] != float64(test.status) {
					t.Fatalf("model error detail %v", reply.body)
				}
			}
			if strings.Contains(reply.body["message"].(string), "synthetic") {
				t.Fatal("backend body leaked")
			}
		})
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	_ = listener.Close()
	backend, err := inference.NewOllama("http://" + closed)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewBedrockRuntime(BedrockRuntimeConfig{Backend: backend, Models: map[string]string{bedrockTestModel: "synthetic:latest"}, Timeout: time.Second, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	harness := newBedrockHarness(t, nil, 0, 0)
	harness.gateway.BedrockRuntime = runtime
	expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"invoke", nil, `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":[{"role":"user","content":"x"}]}`), 503, "ServiceUnavailableException")
}

func TestBedrockRuntimeTimeoutConcurrencyAndShutdownCancellation(t *testing.T) {
	hold := make(chan struct{})
	entered := make(chan struct{}, 4)
	harness := newOllamaHarness(t, time.Second, 1, func(w http.ResponseWriter, r *http.Request) {
		// Reading the body lets net/http observe the adapter's cancellation.
		_, _ = io.Copy(io.Discard, r.Body)
		entered <- struct{}{}
		select {
		case <-hold:
		case <-r.Context().Done():
		}
	})
	// Registered after the fake server, so it runs before the server closes.
	t.Cleanup(func() { close(hold) })
	done := make(chan bedrockReply, 1)
	go func() { done <- harness.call(t, "POST", bedrockTestPath+"converse", nil, converseBody) }()
	<-entered
	expectBedrockError(t, harness.call(t, "POST", bedrockTestPath+"converse", nil, converseBody), 429, "ThrottlingException")
	expectBedrockError(t, <-done, 408, "ModelTimeoutException")

	backend := &recordingBackend{started: make(chan struct{}, 1), release: make(chan struct{})}
	closing := newBedrockHarness(t, backend, 30*time.Second, 2)
	go func() { done <- closing.call(t, "POST", bedrockTestPath+"converse", nil, converseBody) }()
	<-backend.started
	closing.gateway.BedrockRuntime.Close()
	closing.gateway.BedrockRuntime.Close()
	expectBedrockError(t, <-done, 503, "ServiceUnavailableException")
	backend.mu.Lock()
	backend.started, backend.release = nil, nil
	backend.mu.Unlock()
	before := len(backend.calls())
	for attempt := 0; attempt < 50; attempt++ {
		expectBedrockError(t, closing.call(t, "POST", bedrockTestPath+"converse", nil, converseBody), 503, "ServiceUnavailableException")
	}
	if len(backend.calls()) != before {
		t.Fatal("a closed runtime admitted a new request to the backend")
	}
}

func TestBedrockRuntimeBoundsTheTranslatedRequestNotOnlyTheClientBody(t *testing.T) {
	var calls atomic.Int32
	harness := newOllamaHarness(t, 5*time.Second, 1, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`)
	})
	markup := strings.Repeat("<", 400_000)
	reply := harness.call(t, "POST", bedrockTestPath+"invoke", nil, `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":[{"role":"user","content":"`+markup+`"}]}`)
	if reply.status != 200 || calls.Load() != 1 {
		t.Fatalf("markup prompt under 1 MiB %d %v", reply.status, reply.body)
	}
	// Go JSON escapes U+2028, the largest expansion of a valid UTF-8 body
	// (3 to 6 bytes); a body just under 1 MiB stays within the 2 MiB bound.
	separators := "x" + strings.Repeat("\u2028", (BedrockRuntimeRequestBytes-128)/3)
	reply = harness.call(t, "POST", bedrockTestPath+"invoke", nil, `{"anthropic_version":"bedrock-2023-05-31","max_tokens":1,"messages":[{"role":"user","content":"`+separators+`"}]}`)
	if reply.status != 200 || calls.Load() != 2 {
		t.Fatalf("maximum-expansion prompt under 1 MiB %d %v", reply.status, reply.body)
	}
	// The adapter's own bound stays a client error if it is ever reached.
	if failure := bedrockInferenceError(&inference.Error{Kind: inference.TooLarge, Message: "too large"}, bedrockTestModel); failure.code != "ValidationException" || failure.status != 400 {
		t.Fatalf("translated size failure %+v", failure)
	}
}

func TestBedrockRuntimeReportsAnIncompleteBodySeparately(t *testing.T) {
	harness := newBedrockHarness(t, &recordingBackend{}, time.Second, 1)
	connection, err := net.Dial("tcp", strings.TrimPrefix(harness.server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, _ = io.WriteString(connection, "POST "+bedrockTestPath+"converse HTTP/1.1\r\nHost: localhost\r\nAuthorization: "+bedrockTestAuth+"\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"messages\":")
	_ = connection.(*net.TCPConn).CloseWrite()
	response, err := http.ReadResponse(bufio.NewReader(connection), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != 400 || !bytes.Contains(data, []byte("could not be read completely")) {
		t.Fatalf("incomplete body %d %s", response.StatusCode, data)
	}
}

func TestBedrockRuntimeExtendsListenerDeadlinesForBoundedGeneration(t *testing.T) {
	backend := &recordingBackend{result: inference.Result{Text: "slow synthetic", Stop: inference.StopEnd}, release: make(chan struct{})}
	harness := newBedrockHarness(t, backend, 5*time.Second, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: harness.gateway, ReadTimeout: 200 * time.Millisecond, WriteTimeout: 200 * time.Millisecond}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	go func() { time.Sleep(700 * time.Millisecond); close(backend.release) }()
	request, _ := http.NewRequest("POST", "http://"+listener.Addr().String()+bedrockTestPath+"converse", strings.NewReader(converseBody))
	request.Header.Set("Authorization", bedrockTestAuth)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("long generation was cut by the listener deadline: %v", err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !bytes.Contains(data, []byte("slow synthetic")) {
		t.Fatalf("long generation %d %s", response.StatusCode, data)
	}
}

func TestBedrockRuntimePersistsNoPromptOrResponse(t *testing.T) {
	const prompt, answer = "synthetic-prompt-7f3a91", "synthetic-answer-2c8e44"
	harness := newOllamaHarness(t, time.Second, 1, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"`+answer+`"},"done":true,"done_reason":"stop","prompt_eval_count":1,"eval_count":1}`)
	})
	reply := harness.call(t, "POST", bedrockTestPath+"invoke", nil, `{"anthropic_version":"bedrock-2023-05-31","max_tokens":4,"messages":[{"role":"user","content":"`+prompt+`"}]}`)
	if reply.status != 200 || reply.body["content"].([]any)[0].(map[string]any)["text"] != answer {
		t.Fatalf("invoke %d %v", reply.status, reply.body)
	}
	if err := harness.gateway.Store.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(harness.database + "*")
	if err != nil || len(entries) == 0 {
		t.Fatal("database files missing")
	}
	for _, entry := range entries {
		data, err := os.ReadFile(entry)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(prompt)) || bytes.Contains(data, []byte(answer)) {
			t.Fatalf("%s contains inference text", filepath.Base(entry))
		}
	}
}

func TestNewBedrockRuntimeValidatesOperatorConfiguration(t *testing.T) {
	backend := &recordingBackend{}
	models := map[string]string{bedrockTestModel: "local-a"}
	many := map[string]string{}
	for index := 0; index <= bedrockMaxModels; index++ {
		many["provider.model-"+strings.Repeat("a", index+1)] = "local-a"
	}
	for _, config := range []BedrockRuntimeConfig{
		{Models: models, Timeout: time.Second, Concurrency: 1},
		{Backend: backend, Timeout: time.Second, Concurrency: 1},
		{Backend: backend, Models: many, Timeout: time.Second, Concurrency: 1},
		{Backend: backend, Models: map[string]string{"Bad/ID": "local-a"}, Timeout: time.Second, Concurrency: 1},
		{Backend: backend, Models: map[string]string{"nomodel": "local-a"}, Timeout: time.Second, Concurrency: 1},
		{Backend: backend, Models: map[string]string{bedrockTestModel: "remote"}, Timeout: time.Second, Concurrency: 1},
		{Backend: backend, Models: models, Timeout: 0, Concurrency: 1},
		{Backend: backend, Models: models, Timeout: time.Second, Concurrency: 0},
	} {
		if _, err := NewBedrockRuntime(config); err == nil {
			t.Fatalf("accepted %+v", config)
		}
	}
	input := map[string]string{"us.anthropic.claude-sonnet-4-20250514-v1:0": "local-b", bedrockTestModel: "local-a"}
	runtime, err := NewBedrockRuntime(BedrockRuntimeConfig{Backend: backend, Models: input, Timeout: 3 * time.Second, Concurrency: 4})
	if err != nil {
		t.Fatal(err)
	}
	input[bedrockTestModel] = "local-b"
	if runtime.models[bedrockTestModel] != "local-a" || runtime.BackendName() != "recording" || runtime.Timeout() != 3*time.Second || runtime.Concurrency() != 4 {
		t.Fatal("runtime aliases or misreports configuration")
	}
	if ids := runtime.ModelIDs(); len(ids) != 2 || ids[0] != bedrockTestModel {
		t.Fatalf("model IDs %v", ids)
	}
	for _, id := range []string{"amazon.titan-text-express-v1", "a.b.c.d:1:2", bedrockTestModel} {
		if !ValidBedrockModelID(id) {
			t.Fatalf("refused %s", id)
		}
	}
	for _, id := range []string{"a.b.c.d.e", "UPPER.case", "a..b", "a.b:1:2:3", "a.b/c", ""} {
		if ValidBedrockModelID(id) {
			t.Fatalf("accepted %q", id)
		}
	}
}
