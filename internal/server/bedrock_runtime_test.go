package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/inference"
)

// blockingBackend is a synthetic test double; it never runs a model.
type blockingBackend struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingBackend) Name() string                { return "blocking" }
func (b *blockingBackend) ValidModel(name string) bool { return name == "local" }
func (b *blockingBackend) Generate(ctx context.Context, _ inference.Request) (inference.Result, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return inference.Result{}, ctx.Err()
}

func bedrockCapabilities(t *testing.T, s *httptest.Server) map[string]any {
	t.Helper()
	var manifest struct {
		Services map[string]map[string]any `json:"services"`
	}
	if err := json.Unmarshal(status(t, s, "GET", "/_simulator/v1/capabilities", "", 200, nil), &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest.Services["bedrock-runtime"]
}

func TestBedrockRuntimeIsDisabledByDefaultBesideTheControlPlane(t *testing.T) {
	s := instance(t)
	capabilities := bedrockCapabilities(t, s)
	if capabilities["enabled"] != false || capabilities["streaming"] != false || capabilities["backend"] != nil || capabilities["models"] != nil {
		t.Fatalf("default capabilities %v", capabilities)
	}
	auth := map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=TCSIMEXAMPLE/20261111/us-east-1/bedrock/aws4_request, SignedHeaders=host, Signature=0", "Content-Type": "application/json"}
	response, body := call(t, s, "POST", "/model/anthropic.claude-3-haiku-20240307-v1%3A0/converse", `{"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`, auth)
	if response.StatusCode != 501 || response.Header.Get("X-Amzn-ErrorType") != "UnknownOperationException" || !strings.Contains(string(body), "disabled") {
		t.Fatalf("disabled runtime %d %s", response.StatusCode, body)
	}
	status(t, s, "GET", "/guardrails", "", 200, auth)
}

func TestBedrockRuntimeCompositionReportsTheRuntimeAndLeavesItCallerOwned(t *testing.T) {
	backend := &blockingBackend{started: make(chan struct{})}
	runtime, err := aws.NewBedrockRuntime(aws.BedrockRuntimeConfig{Backend: backend, Models: map[string]string{"amazon.nova-micro-v1:0": "local"}, Timeout: time.Minute, Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultProviderConfig()
	options.BedrockRuntime = runtime
	handler, closeState, err := NewWithProviders(":memory:", options)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(handler)
	defer s.Close()
	capabilities := bedrockCapabilities(t, s)
	encoded, _ := json.Marshal(capabilities)
	if capabilities["enabled"] != true || capabilities["backend"] != "blocking" || capabilities["concurrency"] != float64(1) || capabilities["timeoutSeconds"] != float64(60) || !strings.Contains(string(encoded), `"models":["amazon.nova-micro-v1:0"]`) || strings.Contains(string(encoded), `"local"`) {
		t.Fatalf("enabled capabilities %s", encoded)
	}
	done := make(chan int, 1)
	go func() {
		response, _ := call(t, s, "POST", "/model/amazon.nova-micro-v1%3A0/converse", `{"messages":[{"role":"user","content":[{"text":"synthetic"}]}]}`, map[string]string{"X-Simulator-Service": "bedrock-runtime", "Content-Type": "application/json"})
		done <- response.StatusCode
	}()
	<-backend.started
	// The caller owns the runtime: closing the composition leaves it open,
	// and closing the runtime cancels the generation.
	if err := closeState(); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		t.Fatalf("closing the composition ended a caller-owned generation: %d", code)
	case <-time.After(100 * time.Millisecond):
	}
	runtime.Close()
	select {
	case code := <-done:
		if code != 503 {
			t.Fatalf("closed runtime status %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("closing the runtime did not cancel the generation")
	}
}
