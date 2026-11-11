package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const bedrockCLIModel = "anthropic.claude-3-haiku-20240307-v1:0"

func parsedBedrockFlags(t *testing.T, args ...string) (bedrockCLIFlags, error) {
	t.Helper()
	set := flag.NewFlagSet("bedrock-test", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	values, err := registerBedrockFlags(set)
	if err != nil {
		t.Fatal(err)
	}
	if err = set.Parse(args); err != nil {
		return bedrockCLIFlags{}, err
	}
	return *values, nil
}

func TestBedrockFlagsDefaultDisabledAndRejectOrphanOptions(t *testing.T) {
	if _, err := registerBedrockFlags(nil); err == nil {
		t.Fatal("nil flag set accepted")
	}
	values, err := parsedBedrockFlags(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []bool{false, true} {
		if runtime, err := validateBedrockFlags(values, unsafe); runtime != nil || err != nil {
			t.Fatal("Bedrock Runtime became enabled by default")
		}
	}
	for _, orphan := range []string{"--bedrock-ollama-url=" + "http://127.0.0.1:11434", "--bedrock-model=a.b=c", "--bedrock-runtime-timeout=5s", "--bedrock-runtime-concurrency=1"} {
		values, err := parsedBedrockFlags(t, orphan)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = validateBedrockFlags(values, false); err == nil || !strings.Contains(err.Error(), "require --bedrock-runtime-backend") {
			t.Fatalf("%s silently ignored: %v", orphan, err)
		}
	}
	for _, malformed := range [][]string{{"--bedrock-runtime-timeout=soon"}, {"--bedrock-runtime-concurrency=two"}} {
		if _, err := parsedBedrockFlags(t, malformed...); err == nil {
			t.Fatalf("%v parsed", malformed)
		}
	}
}

func TestBedrockFlagsBuildExplicitOllamaRuntime(t *testing.T) {
	values, err := parsedBedrockFlags(t, "--bedrock-runtime-backend=ollama", "--bedrock-model="+bedrockCLIModel+"=llama3.2:1b", "--bedrock-model=amazon.nova-micro-v1:0=qwen2.5:0.5b")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := validateBedrockFlags(values, false)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.BackendName() != "ollama" || runtime.Timeout() != defaultBedrockTimeout || runtime.Concurrency() != defaultBedrockConcurrency || len(runtime.ModelIDs()) != 2 {
		t.Fatal("default Ollama runtime mismatch")
	}
	values, err = parsedBedrockFlags(t, "--bedrock-runtime-backend=ollama", "--bedrock-ollama-url=http://[::1]:11500/", "--bedrock-model="+bedrockCLIModel+"=llama3.2:1b", "--bedrock-runtime-timeout=90s", "--bedrock-runtime-concurrency=1")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = validateBedrockFlags(values, false)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.Timeout() != 90*time.Second || runtime.Concurrency() != 1 {
		t.Fatal("explicit limits ignored")
	}
}

func TestBedrockFlagsRefuseUnsafeUnavailableAndAmbiguousConfiguration(t *testing.T) {
	model := "--bedrock-model=" + bedrockCLIModel + "=llama3.2:1b"
	for _, test := range []struct {
		args   []string
		unsafe bool
		want   string
	}{
		{[]string{"--bedrock-runtime-backend=ollama", model}, true, "authenticated access"},
		{[]string{"--bedrock-runtime-backend=apple", model}, false, "Apple Foundation Models backend is unavailable"},
		{[]string{"--bedrock-runtime-backend=apple", "--bedrock-ollama-url=http://127.0.0.1:11434", model}, false, "applies only"},
		{[]string{"--bedrock-runtime-backend=", model}, false, "must be ollama or apple"},
		{[]string{"--bedrock-runtime-backend=openai", model}, false, "must be ollama or apple"},
		{[]string{"--bedrock-runtime-backend=ollama"}, false, "at least one --bedrock-model"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-model=" + bedrockCLIModel}, false, "MODEL_ID=LOCAL_MODEL"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-model==llama3"}, false, "MODEL_ID=LOCAL_MODEL"},
		{[]string{"--bedrock-runtime-backend=ollama", model, model}, false, "more than once"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-model=" + bedrockCLIModel + "=gpt-oss:120b-cloud"}, false, "non-local"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-model=Bad=llama3"}, false, "invalid Bedrock model ID"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-ollama-url=http://localhost:11434", model}, false, "numeric loopback"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-ollama-url=http://192.168.1.2:11434", model}, false, "numeric loopback"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-ollama-url=https://127.0.0.1:11434", model}, false, "numeric loopback"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-ollama-url=", model}, false, "numeric loopback"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-runtime-timeout=500ms", model}, false, "timeout"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-runtime-timeout=6m", model}, false, "timeout"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-runtime-concurrency=0", model}, false, "concurrency"},
		{[]string{"--bedrock-runtime-backend=ollama", "--bedrock-runtime-concurrency=9", model}, false, "concurrency"},
	} {
		values, err := parsedBedrockFlags(t, test.args...)
		if err != nil {
			t.Fatal(err)
		}
		if runtime, err := validateBedrockFlags(values, test.unsafe); runtime != nil || err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%v: %v", test.args, err)
		}
	}
}

func TestBedrockCLIRefusalHappensBeforeStartupFiles(t *testing.T) {
	directory := t.TempDir()
	state, token := filepath.Join(directory, "state.db"), filepath.Join(directory, "token")
	for _, args := range [][]string{
		{"--bedrock-runtime-backend=apple", "--bedrock-model=" + bedrockCLIModel + "=x"},
		{"--bedrock-runtime-backend=ollama", "--bedrock-ollama-url=http://10.0.0.1:11434", "--bedrock-model=" + bedrockCLIModel + "=x"},
		{"--unsafe-no-auth", "--bedrock-runtime-backend=ollama", "--bedrock-model=" + bedrockCLIModel + "=x"},
	} {
		err := run(context.Background(), append([]string{"--listen", "127.0.0.1:0", "--state", state, "--token-file", token}, args...), io.Discard)
		if err == nil {
			t.Fatalf("%v started", args)
		}
		for _, path := range []string{state, token} {
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatal("refused configuration created a startup file")
			}
		}
	}
}

// TestBedrockCLIActualRunServesConfiguredModel drives the real CLI, access
// token boundary and HTTP listener against a synthetic Ollama test double.
func TestBedrockCLIActualRunServesConfiguredModel(t *testing.T) {
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"message":{"role":"assistant","content":"synthetic cli reply"},"done":true,"done_reason":"stop","prompt_eval_count":2,"eval_count":3}`)
	}))
	defer ollama.Close()
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "token")
	ctx, cancel := context.WithCancel(context.Background())
	output := &sakuraStartupOutput{ready: make(chan string, 1)}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--listen", "127.0.0.1:0", "--token-file", tokenPath, "--bedrock-runtime-backend", "ollama", "--bedrock-ollama-url", ollama.URL, "--bedrock-model", bedrockCLIModel + "=synthetic:latest"}, output)
	}()
	var origin string
	select {
	case origin = <-output.ready:
	case err := <-done:
		t.Fatalf("CLI exited before readiness: %v", err)
	case <-time.After(15 * time.Second):
		t.Fatal("CLI startup deadline")
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSpace(string(data))
	invoke := func(withToken bool) (int, map[string]any) {
		request, _ := http.NewRequest("POST", origin+"/model/anthropic.claude-3-haiku-20240307-v1%3A0/converse", strings.NewReader(`{"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`))
		request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIMEXAMPLE/20261111/us-east-1/bedrock/aws4_request, SignedHeaders=host, Signature=0")
		request.Header.Set("Content-Type", "application/json")
		if withToken {
			request.Header.Set("X-Simulator-Token", token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(response.Body).Decode(&body)
		return response.StatusCode, body
	}
	if status, _ := invoke(false); status != 401 {
		t.Fatalf("unauthenticated inference status %d", status)
	}
	status, body := invoke(true)
	if status != 200 || !strings.Contains(func() string { encoded, _ := json.Marshal(body); return string(encoded) }(), "synthetic cli reply") {
		t.Fatalf("authenticated inference %d %v", status, body)
	}
	request, _ := http.NewRequest("GET", origin+"/_simulator/v1/capabilities", nil)
	request.Header.Set("X-Simulator-Token", token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var capabilities struct {
		Services map[string]map[string]any `json:"services"`
	}
	_ = json.NewDecoder(response.Body).Decode(&capabilities)
	response.Body.Close()
	runtime := capabilities.Services["bedrock-runtime"]
	if runtime["enabled"] != true || runtime["backend"] != "ollama" || strings.Contains(func() string { encoded, _ := json.Marshal(runtime); return string(encoded) }(), ollama.URL) {
		t.Fatalf("capabilities %v", runtime)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("CLI did not stop")
	}
}
