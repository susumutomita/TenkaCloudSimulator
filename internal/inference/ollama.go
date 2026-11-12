package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultOllamaURL is Ollama's documented local listener.
const DefaultOllamaURL = "http://127.0.0.1:11434"

// Ollama transport bounds.
const (
	MaxBackendRequestBytes  = 2 << 20
	MaxBackendResponseBytes = 1 << 20
	maxBackendHeaderBytes   = 16 << 10
	backendDialTimeout      = 5 * time.Second
)

// Backend model names are operator configuration. Ollama ":cloud" and
// "-cloud" tags run on a remote service, so they are refused here; the
// simulator cannot otherwise prove where an Ollama daemon executes a model.
var ollamaModelName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}(/[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}){0,2}(:[a-zA-Z0-9][a-zA-Z0-9._-]{0,127})?$`)

// ValidOllamaModel reports whether name is an acceptable local model tag.
func ValidOllamaModel(name string) bool {
	if !ollamaModelName.MatchString(name) {
		return false
	}
	lower := strings.ToLower(name)
	return !strings.HasSuffix(lower, ":cloud") && !strings.HasSuffix(lower, "-cloud")
}

// Ollama calls one numeric-loopback Ollama daemon over plain HTTP. It never
// uses an environment proxy, follows redirects, resolves names, pulls models
// or dials any address other than the configured one.
type Ollama struct {
	address string
	base    string
	client  *http.Client
}

// ParseOllamaURL accepts only http://<numeric loopback IP>:<port> with an
// optional trailing slash: no name resolution, credentials, path, query,
// fragment or TLS endpoint that could leave the host.
func ParseOllamaURL(raw string) (address string, err error) {
	invalid := fmt.Errorf("the Ollama URL must be http://<numeric loopback IP>:<port>, for example %s", DefaultOllamaURL)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return "", invalid
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || port == "" {
		return "", invalid
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
		return "", invalid
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || strings.Contains(host, "%") {
		return "", invalid
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// NewOllama validates raw and prepares a dedicated, proxy-free transport.
func NewOllama(raw string) (*Ollama, error) {
	address, err := ParseOllamaURL(raw)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: backendDialTimeout, Control: func(_, dialed string, _ syscall.RawConn) error {
		if dialed != address {
			return errors.New("refusing to dial an address other than the configured loopback Ollama listener")
		}
		return nil
	}}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		},
		ForceAttemptHTTP2:      false,
		DisableCompression:     true,
		MaxIdleConns:           MaxConcurrency,
		MaxIdleConnsPerHost:    MaxConcurrency,
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: maxBackendHeaderBytes,
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Ollama{address: address, base: "http://" + address, client: client}, nil
}

// Name identifies the backend kind.
func (o *Ollama) Name() string { return "ollama" }

// ValidModel accepts local Ollama model tags and refuses remote cloud tags.
func (o *Ollama) ValidModel(name string) bool { return ValidOllamaModel(name) }

// Address is the validated numeric loopback host:port.
func (o *Ollama) Address() string { return o.address }

// Close releases idle backend connections.
func (o *Ollama) Close() { o.client.CloseIdleConnections() }

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ollamaRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options"`
}
type ollamaResponse struct {
	Message *struct {
		Role      string            `json:"role"`
		Content   *string           `json:"content"`
		Thinking  string            `json:"thinking"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
		Images    []json.RawMessage `json:"images"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount *int   `json:"prompt_eval_count"`
	EvalCount       *int   `json:"eval_count"`
	RemoteModel     string `json:"remote_model"`
	RemoteHost      string `json:"remote_host"`
	Error           string `json:"error"`
}

// Generate sends one non-streaming /api/chat request. Ollama error bodies are
// not echoed because they may quote client input.
func (o *Ollama) Generate(ctx context.Context, request Request) (Result, error) {
	if !ValidOllamaModel(request.Model) || len(request.Messages) == 0 || request.MaxTokens < 1 {
		return Result{}, failure(BackendFailure, 0, "The local inference request is incomplete.")
	}
	messages := make([]ollamaMessage, 0, len(request.Messages)+1)
	if request.System != "" {
		messages = append(messages, ollamaMessage{Role: "system", Content: request.System})
	}
	for _, message := range request.Messages {
		if message.Role != RoleUser && message.Role != RoleAssistant {
			return Result{}, failure(BackendFailure, 0, "The local inference request has an invalid role.")
		}
		messages = append(messages, ollamaMessage{Role: string(message.Role), Content: message.Text})
	}
	options := map[string]any{"num_predict": request.MaxTokens}
	if request.Temperature != nil {
		options["temperature"] = *request.Temperature
	}
	if request.TopP != nil {
		options["top_p"] = *request.TopP
	}
	if request.TopK != nil {
		options["top_k"] = *request.TopK
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	// Keep prompt text byte-for-byte; HTML escaping would inflate <, > and &.
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(ollamaRequest{Model: request.Model, Messages: messages, Stream: false, Options: options}); err != nil {
		return Result{}, failure(BackendFailure, 0, "The local inference request could not be encoded.")
	}
	body := encoded.Bytes()
	if len(body) > MaxBackendRequestBytes {
		return Result{}, failure(TooLarge, 0, "The translated request exceeds the %d-byte local backend transport limit.", MaxBackendRequestBytes)
	}
	outbound, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Result{}, failure(BackendFailure, 0, "The local inference request could not be prepared.")
	}
	outbound.Header.Set("Content-Type", "application/json")
	outbound.Header.Set("Accept", "application/json")
	response, err := o.client.Do(outbound)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, failure(Canceled, 0, "The local backend request ended before a response.")
		}
		return Result{}, failure(Unavailable, 0, "The configured local Ollama backend is unreachable.")
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxBackendResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, failure(Canceled, response.StatusCode, "The local backend response ended early.")
		}
		return Result{}, failure(BackendFailure, response.StatusCode, "The local backend response could not be read.")
	}
	if len(data) > MaxBackendResponseBytes {
		return Result{}, failure(BackendFailure, response.StatusCode, "The local backend response exceeds %d bytes.", MaxBackendResponseBytes)
	}
	switch {
	case response.StatusCode == http.StatusNotFound:
		return Result{}, failure(ModelMissing, response.StatusCode, "The mapped local model is not available from Ollama; the simulator never downloads models.")
	case response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusTooManyRequests:
		return Result{}, failure(Busy, response.StatusCode, "The local Ollama backend is busy.")
	case response.StatusCode < 200 || response.StatusCode > 299:
		return Result{}, failure(BackendFailure, response.StatusCode, "The local Ollama backend returned HTTP %d.", response.StatusCode)
	}
	if media := response.Header.Get("Content-Type"); media != "" && !strings.HasPrefix(strings.ToLower(media), "application/json") {
		return Result{}, failure(BackendFailure, response.StatusCode, "The local backend returned a non-JSON response.")
	}
	var decoded ollamaResponse
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&decoded) != nil || decoder.Decode(new(json.RawMessage)) != io.EOF {
		return Result{}, failure(BackendFailure, response.StatusCode, "The local backend returned malformed JSON.")
	}
	if decoded.Error != "" {
		return Result{}, failure(BackendFailure, response.StatusCode, "The local Ollama backend reported an error.")
	}
	if decoded.RemoteHost != "" || decoded.RemoteModel != "" {
		return Result{}, failure(Unrepresentable, response.StatusCode, "The Ollama backend reported remote execution, which this local adapter refuses.")
	}
	if !decoded.Done || decoded.Message == nil || decoded.Message.Role != "assistant" || decoded.Message.Content == nil {
		return Result{}, failure(BackendFailure, response.StatusCode, "The local backend returned an incomplete chat response.")
	}
	if decoded.Message.Thinking != "" || len(decoded.Message.ToolCalls) != 0 || len(decoded.Message.Images) != 0 {
		return Result{}, failure(Unrepresentable, response.StatusCode, "The local model returned reasoning, tool-call or image output, which this text-only adapter cannot represent.")
	}
	var stop StopReason
	switch decoded.DoneReason {
	case "stop":
		stop = StopEnd
	case "length":
		stop = StopLength
	default:
		return Result{}, failure(BackendFailure, response.StatusCode, "The local backend returned an unsupported completion reason.")
	}
	result := Result{Text: *decoded.Message.Content, Stop: stop}
	if decoded.PromptEvalCount != nil && *decoded.PromptEvalCount >= 0 {
		result.InputTokens = *decoded.PromptEvalCount
	}
	if decoded.EvalCount != nil && *decoded.EvalCount >= 0 {
		result.OutputTokens = *decoded.EvalCount
	}
	return result, nil
}
