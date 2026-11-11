package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOllama is a test double for the Ollama /api/chat HTTP boundary. Every
// payload is synthetic; it is not evidence of behavior with a real model.
func fakeOllama(t *testing.T, handler http.HandlerFunc) (*Ollama, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	backend, err := NewOllama(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backend.Close)
	return backend, server
}

func chatReply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = io.WriteString(w, body)
}

func syntheticRequest() Request {
	temperature, topP, topK := 0.25, 0.5, 7
	return Request{Model: "synthetic:latest", System: "synthetic system", MaxTokens: 12, Temperature: &temperature, TopP: &topP, TopK: &topK,
		Messages: []Message{{RoleUser, "synthetic one"}, {RoleAssistant, "synthetic two"}, {RoleUser, "synthetic three"}}}
}

func TestParseOllamaURLAcceptsOnlyNumericLoopbackHTTP(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1:11434":          "127.0.0.1:11434",
		"http://127.0.0.1:11434/":         "127.0.0.1:11434",
		"http://127.3.4.5:80":             "127.3.4.5:80",
		"http://[::1]:11434":              "[::1]:11434",
		"http://[::ffff:127.0.0.1]:11434": "127.0.0.1:11434",
	} {
		if got, err := ParseOllamaURL(raw); err != nil || got != want {
			t.Fatalf("%s: got %q %v", raw, got, err)
		}
	}
	for _, raw := range []string{"", "127.0.0.1:11434", "https://127.0.0.1:11434", "http://localhost:11434", "http://10.0.0.1:11434", "http://0.0.0.0:11434",
		"http://[::]:11434", "http://127.0.0.1", "http://127.0.0.1:", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:011434", "http://127.0.0.1:x",
		"http://user@127.0.0.1:11434", "http://127.0.0.1:11434/api", "http://127.0.0.1:11434?x=1", "http://127.0.0.1:11434/?", "http://127.0.0.1:11434#x",
		"http://127.1:11434", "http://[fe80::1%25lo]:11434", "http://%31%32%37.0.0.1:11434", "http:127.0.0.1:11434", "http://127.0.0.1:11434/%2F"} {
		if _, err := ParseOllamaURL(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
		if _, err := NewOllama(raw); err == nil {
			t.Fatalf("constructed %q", raw)
		}
	}
}

func TestValidOllamaModelRefusesRemoteCloudTags(t *testing.T) {
	for _, name := range []string{"llama3.2:1b", "qwen2.5:0.5b-instruct-q4_K_M", "library/llama3", "hf.co/org/model:Q4_K_M", "phi3"} {
		if !ValidOllamaModel(name) || !(&Ollama{}).ValidModel(name) {
			t.Fatalf("refused %s", name)
		}
	}
	for _, name := range []string{"", ":latest", "a b", "../x", "a/b/c/d", "gpt-oss:120b-cloud", "model:cloud", "MODEL:CLOUD", "x:" + strings.Repeat("a", 129), "http://x", "a?b"} {
		if ValidOllamaModel(name) {
			t.Fatalf("accepted %q", name)
		}
	}
}

func TestOllamaSendsBoundedNonStreamingChatAndReturnsActualText(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("http_proxy", "http://127.0.0.1:1")
	var seen map[string]any
	backend, _ := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected backend request %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Error(err)
		}
		chatReply(w, `{"model":"synthetic:latest","message":{"role":"assistant","content":"synthetic answer"},"done":true,"done_reason":"length","prompt_eval_count":9,"eval_count":12}`)
	})
	if backend.Name() != "ollama" || !strings.HasPrefix(backend.Address(), "127.0.0.1:") {
		t.Fatal("backend identity mismatch")
	}
	result, err := backend.Generate(context.Background(), syntheticRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result != (Result{Text: "synthetic answer", Stop: StopLength, InputTokens: 9, OutputTokens: 12}) {
		t.Fatalf("result mismatch %+v", result)
	}
	encoded, _ := json.Marshal(seen)
	want := `{"messages":[{"content":"synthetic system","role":"system"},{"content":"synthetic one","role":"user"},{"content":"synthetic two","role":"assistant"},{"content":"synthetic three","role":"user"}],"model":"synthetic:latest","options":{"num_predict":12,"temperature":0.25,"top_k":7,"top_p":0.5},"stream":false}`
	if string(encoded) != want {
		t.Fatalf("backend request %s", encoded)
	}
	minimal := Request{Model: "synthetic:latest", MaxTokens: 1, Messages: []Message{{RoleUser, "only"}}}
	if _, err = backend.Generate(context.Background(), minimal); err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(seen)
	if string(encoded) != `{"messages":[{"content":"only","role":"user"}],"model":"synthetic:latest","options":{"num_predict":1},"stream":false}` {
		t.Fatalf("minimal backend request %s", encoded)
	}
}

func TestOllamaMapsBackendResponsesToTypedFailures(t *testing.T) {
	cases := []struct {
		name, contentType, body string
		status                  int
		kind                    Kind
	}{
		{"missing model", "application/json", `{"error":"model not found"}`, 404, ModelMissing},
		{"busy", "application/json", `{"error":"busy"}`, 503, Busy},
		{"rate", "application/json", `{}`, 429, Busy},
		{"server error", "application/json", `{"error":"synthetic"}`, 500, BackendFailure},
		{"redirect is not followed", "text/html", ``, 302, BackendFailure},
		{"non json", "text/plain", `{}`, 200, BackendFailure},
		{"malformed", "application/json", `{"done":`, 200, BackendFailure},
		{"trailing", "application/json", `{"done":true}{}`, 200, BackendFailure},
		{"error field", "application/json", `{"error":"synthetic","done":true}`, 200, BackendFailure},
		{"remote host", "application/json", `{"remote_host":"https://example.invalid","message":{"role":"assistant","content":"x"},"done":true,"done_reason":"stop"}`, 200, Unrepresentable},
		{"remote model", "application/json", `{"remote_model":"x","message":{"role":"assistant","content":"x"},"done":true,"done_reason":"stop"}`, 200, Unrepresentable},
		{"not done", "application/json", `{"message":{"role":"assistant","content":"x"},"done":false,"done_reason":"stop"}`, 200, BackendFailure},
		{"no message", "application/json", `{"done":true,"done_reason":"stop"}`, 200, BackendFailure},
		{"wrong role", "application/json", `{"message":{"role":"user","content":"x"},"done":true,"done_reason":"stop"}`, 200, BackendFailure},
		{"no content", "application/json", `{"message":{"role":"assistant"},"done":true,"done_reason":"stop"}`, 200, BackendFailure},
		{"thinking", "application/json", `{"message":{"role":"assistant","content":"x","thinking":"hidden"},"done":true,"done_reason":"stop"}`, 200, Unrepresentable},
		{"tool calls", "application/json", `{"message":{"role":"assistant","content":"","tool_calls":[{}]},"done":true,"done_reason":"stop"}`, 200, Unrepresentable},
		{"images", "application/json", `{"message":{"role":"assistant","content":"","images":["x"]},"done":true,"done_reason":"stop"}`, 200, Unrepresentable},
		{"unknown reason", "application/json", `{"message":{"role":"assistant","content":"x"},"done":true,"done_reason":"load"}`, 200, BackendFailure},
		{"missing reason", "application/json", `{"message":{"role":"assistant","content":"x"},"done":true}`, 200, BackendFailure},
		{"oversized", "application/json", `{"message":{"role":"assistant","content":"` + strings.Repeat("x", MaxBackendResponseBytes) + `"},"done":true,"done_reason":"stop"}`, 200, BackendFailure},
	}
	var redirected atomic.Int32
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			backend, _ := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/chat" {
					redirected.Add(1)
				}
				w.Header().Set("Content-Type", test.contentType)
				if test.status == 302 {
					w.Header().Set("Location", "/elsewhere")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			_, err := backend.Generate(context.Background(), syntheticRequest())
			if KindOf(err) != test.kind {
				t.Fatalf("kind %d wanted %d (%v)", KindOf(err), test.kind, err)
			}
			if test.status != 200 && StatusOf(err) != test.status {
				t.Fatalf("status %d", StatusOf(err))
			}
			if strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "hidden") {
				t.Fatal("backend body or prompt leaked into the error message")
			}
		})
	}
	if redirected.Load() != 0 {
		t.Fatal("redirect target was requested")
	}
}

func TestOllamaZeroAndMissingCountsAndEmptyContent(t *testing.T) {
	backend, _ := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		chatReply(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop","prompt_eval_count":-1}`)
	})
	result, err := backend.Generate(context.Background(), syntheticRequest())
	if err != nil || result != (Result{Stop: StopEnd}) {
		t.Fatalf("result %+v %v", result, err)
	}
}

func TestOllamaRefusesIncompleteRequestsWithoutContactingBackend(t *testing.T) {
	var calls atomic.Int32
	backend, _ := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	valid := syntheticRequest()
	for _, mutate := range []func(*Request){
		func(r *Request) { r.Model = "cloud:cloud" },
		func(r *Request) { r.Messages = nil },
		func(r *Request) { r.MaxTokens = 0 },
		func(r *Request) { r.Messages = []Message{{"system", "x"}} },
		func(r *Request) { r.Messages = []Message{{RoleUser, strings.Repeat("x", MaxBackendRequestBytes)}} },
	} {
		request := valid
		mutate(&request)
		if _, err := backend.Generate(context.Background(), request); KindOf(err) != BackendFailure {
			t.Fatalf("incomplete request kind %d", KindOf(err))
		}
	}
	if calls.Load() != 0 {
		t.Fatal("backend was contacted")
	}
}

func TestOllamaUnreachableAndCanceledAreDistinct(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	backend, err := NewOllama("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = backend.Generate(context.Background(), syntheticRequest()); KindOf(err) != Unavailable {
		t.Fatalf("unreachable kind %d", KindOf(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = backend.Generate(ctx, syntheticRequest()); KindOf(err) != Canceled {
		t.Fatalf("canceled kind %d", KindOf(err))
	}
	started := make(chan struct{})
	slow, _ := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"message":`)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel = context.WithCancel(context.Background())
	// Cancel after the headers have had time to arrive, while the body read
	// is blocked. Either phase must report cancellation, never a failure.
	go func() { <-started; time.Sleep(100 * time.Millisecond); cancel() }()
	if _, err = slow.Generate(ctx, syntheticRequest()); KindOf(err) != Canceled || StatusOf(err) != 200 && StatusOf(err) != 0 {
		t.Fatalf("partial body cancel kind %d status %d", KindOf(err), StatusOf(err))
	}
}

func TestOllamaPartialBodyWithoutCancellationIsBackendFailure(t *testing.T) {
	backend, server := fakeOllama(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"message":`)
	})
	_ = server
	if _, err := backend.Generate(context.Background(), syntheticRequest()); KindOf(err) != BackendFailure {
		t.Fatalf("truncated body kind %d", KindOf(err))
	}
}

func TestAppleFoundationModelsIsExplicitlyUnavailable(t *testing.T) {
	backend, err := AppleFoundationModels()
	if backend != nil || !errors.Is(err, ErrAppleUnavailable) || KindOf(err) != Unavailable || !strings.Contains(err.Error(), "no other backend") {
		t.Fatal("Apple backend must fail explicitly without substitution")
	}
}

type scriptedBackend struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
	result  Result
	err     error
}

func (s *scriptedBackend) Name() string                { return "scripted" }
func (s *scriptedBackend) ValidModel(name string) bool { return name == "ok" }
func (s *scriptedBackend) Generate(ctx context.Context, _ Request) (Result, error) {
	s.calls.Add(1)
	if s.started != nil {
		s.started <- struct{}{}
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}
	return s.result, s.err
}

func TestBoundedValidatesLimitsAndDelegates(t *testing.T) {
	backend := &scriptedBackend{result: Result{Text: "x", Stop: StopEnd}}
	for _, test := range []struct {
		backend     Backend
		timeout     time.Duration
		concurrency int
	}{{nil, time.Second, 1}, {backend, time.Second - 1, 1}, {backend, MaxTimeout + 1, 1}, {backend, time.Second, 0}, {backend, time.Second, MaxConcurrency + 1}} {
		if _, err := NewBounded(test.backend, test.timeout, test.concurrency); err == nil {
			t.Fatalf("accepted %+v", test)
		}
	}
	bounded, err := NewBounded(backend, 2*time.Second, 3)
	if err != nil {
		t.Fatal(err)
	}
	if bounded.Name() != "scripted" || !bounded.ValidModel("ok") || bounded.ValidModel("no") || bounded.Timeout() != 2*time.Second || bounded.Concurrency() != 3 {
		t.Fatal("bounded accessors mismatch")
	}
	if result, err := bounded.Generate(context.Background(), Request{}); err != nil || result.Text != "x" {
		t.Fatal("bounded did not delegate", err)
	}
	backend.err = &Error{Kind: ModelMissing, Status: 404, Message: "missing"}
	if _, err := bounded.Generate(context.Background(), Request{}); KindOf(err) != ModelMissing || StatusOf(err) != 404 {
		t.Fatal("typed backend error lost")
	}
	if KindOf(errors.New("plain")) != BackendFailure || StatusOf(errors.New("plain")) != 0 {
		t.Fatal("untyped errors must be backend failures")
	}
}

func TestBoundedRejectsExcessConcurrencyAndEnforcesDeadline(t *testing.T) {
	backend := &scriptedBackend{started: make(chan struct{}, 1), release: make(chan struct{})}
	bounded, err := NewBounded(backend, time.Second, 1)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := bounded.Generate(context.Background(), Request{})
		done <- err
	}()
	<-backend.started
	if _, err = bounded.Generate(context.Background(), Request{}); KindOf(err) != Busy {
		t.Fatalf("excess request kind %d", KindOf(err))
	}
	if backend.calls.Load() != 1 {
		t.Fatal("busy request reached the backend")
	}
	if err = <-done; KindOf(err) != Timeout {
		t.Fatalf("deadline kind %d", KindOf(err))
	}
	backend.started = nil
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err = bounded.Generate(ctx, Request{}); KindOf(err) != Canceled {
		t.Fatalf("canceled kind %d", KindOf(err))
	}
}
