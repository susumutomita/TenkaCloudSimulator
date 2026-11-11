package aws

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/inference"
)

// Bedrock Runtime bounds. They are local admission limits, not AWS quotas.
const (
	BedrockRuntimeRequestBytes = 1 << 20
	bedrockMaxMessages         = 500
	bedrockMaxBlocks           = 64
	BedrockMaxOutputTokens     = 8192
	BedrockDefaultMaxTokens    = 1024
	bedrockMaxTopK             = 500
	bedrockMaxModels           = 32
	bedrockModelLabelBytes     = 2048
	bedrockAnthropicVersion    = "bedrock-2023-05-31"
	bedrockInvokeAction        = "bedrock:InvokeModel"
	bedrockDeadlineSlack       = 10 * time.Second
)

// Operator-mapped Bedrock model IDs follow the official foundation-model ID
// form, including geography-prefixed inference profile IDs. They never
// contain a path separator, so a mapped ID cannot select another route.
var bedrockModelID = regexp.MustCompile(`^[a-z0-9-]{1,63}([.][a-z0-9-]{1,63}){1,3}([:][a-z0-9-]{1,63}){0,2}$`)

// ValidBedrockModelID reports whether id can be mapped by the operator.
func ValidBedrockModelID(id string) bool { return bedrockModelID.MatchString(id) }

// BedrockRuntimeConfig is trusted operator configuration. Clients choose only
// among Models keys; they never supply a backend URL or backend model name.
type BedrockRuntimeConfig struct {
	Backend     inference.Backend
	Models      map[string]string
	Timeout     time.Duration
	Concurrency int
}

// BedrockRuntime serves bounded, text-only, non-streaming InvokeModel
// (Anthropic Messages body) and Converse through one explicitly selected
// local backend. It stores and logs no prompt or response.
type BedrockRuntime struct {
	backend *inference.Bounded
	models  map[string]string
	base    context.Context
	stop    context.CancelFunc
}

// NewBedrockRuntime validates and copies the operator configuration.
func NewBedrockRuntime(config BedrockRuntimeConfig) (*BedrockRuntime, error) {
	if config.Backend == nil {
		return nil, errors.New("Bedrock Runtime requires an explicitly selected backend")
	}
	if len(config.Models) == 0 || len(config.Models) > bedrockMaxModels {
		return nil, fmt.Errorf("Bedrock Runtime requires 1–%d operator model mappings", bedrockMaxModels)
	}
	models := make(map[string]string, len(config.Models))
	for id, backendModel := range config.Models {
		if !ValidBedrockModelID(id) {
			return nil, fmt.Errorf("invalid Bedrock model ID %q", id)
		}
		if !config.Backend.ValidModel(backendModel) {
			return nil, fmt.Errorf("invalid or non-local %s model name for %s", config.Backend.Name(), id)
		}
		models[id] = backendModel
	}
	bounded, err := inference.NewBounded(config.Backend, config.Timeout, config.Concurrency)
	if err != nil {
		return nil, err
	}
	base, stop := context.WithCancel(context.Background())
	return &BedrockRuntime{backend: bounded, models: models, base: base, stop: stop}, nil
}

// Close cancels in-flight generations. It is safe to call more than once.
func (b *BedrockRuntime) Close() { b.stop() }

// BackendName names the selected backend kind.
func (b *BedrockRuntime) BackendName() string { return b.backend.Name() }

// ModelIDs lists the operator-mapped Bedrock model IDs.
func (b *BedrockRuntime) ModelIDs() []string {
	ids := make([]string, 0, len(b.models))
	for id := range b.models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Timeout is the per-request generation deadline.
func (b *BedrockRuntime) Timeout() time.Duration { return b.backend.Timeout() }

// Concurrency is the number of simultaneous generations admitted.
func (b *BedrockRuntime) Concurrency() int { return b.backend.Concurrency() }

func (b *BedrockRuntime) generate(ctx context.Context, request inference.Request) (inference.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	release := context.AfterFunc(b.base, cancel)
	defer release()
	return b.backend.Generate(ctx, request)
}

// bedrockRuntimePath reports whether an escaped path addresses an official
// Bedrock Runtime route prefix. The control plane uses plural /guardrails/.
func bedrockRuntimePath(path string) bool {
	return strings.HasPrefix(path, "/model/") || path == "/async-invoke" || strings.HasPrefix(path, "/async-invoke/") || strings.HasPrefix(path, "/guardrail/")
}

// isBedrockRuntime recognizes Bedrock Runtime intent before the Bedrock
// control-plane catalog, which shares the "bedrock" signing name. An explicit
// "bedrock-runtime" selection never falls through to another service.
func isBedrockRuntime(r *http.Request) bool {
	service := r.Header.Get("X-Simulator-Service")
	if service == "bedrock-runtime" {
		return true
	}
	authorization := r.Header.Get("Authorization")
	signed := r53SigningService(authorization)
	// Bedrock API keys use bearer authentication without a signing scope.
	// Recognize them so they receive an explicit Bedrock refusal.
	bearer := strings.HasPrefix(authorization, "Bearer ")
	if service != "" && service != "bedrock" || signed != "" && signed != "bedrock" || service == "" && signed == "" && !bearer {
		return false
	}
	return bedrockRuntimePath(r.URL.EscapedPath())
}

// bedrockRuntimeRoute resolves the official operation and its model label.
func bedrockRuntimeRoute(r *http.Request) (operation, label string, ok bool) {
	segments := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	switch {
	case len(segments) == 3 && segments[0] == "model" && segments[1] != "" && r.Method == http.MethodPost:
		operations := map[string]string{"invoke": "InvokeModel", "converse": "Converse", "invoke-with-response-stream": "InvokeModelWithResponseStream", "converse-stream": "ConverseStream", "invoke-with-bidirectional-stream": "InvokeModelWithBidirectionalStream", "count-tokens": "CountTokens"}
		operation, ok = operations[segments[2]]
		if !ok {
			return "", "", false
		}
		label, err := url.PathUnescape(segments[1])
		if err != nil || label == "" {
			return "", "", false
		}
		return operation, label, true
	case len(segments) == 1 && segments[0] == "async-invoke" && r.Method == http.MethodPost:
		return "StartAsyncInvoke", "", true
	case len(segments) == 1 && segments[0] == "async-invoke" && r.Method == http.MethodGet:
		return "ListAsyncInvokes", "", true
	case len(segments) == 2 && segments[0] == "async-invoke" && segments[1] != "" && r.Method == http.MethodGet:
		return "GetAsyncInvoke", "", true
	case len(segments) == 5 && segments[0] == "guardrail" && segments[1] != "" && segments[2] == "version" && segments[3] != "" && segments[4] == "apply" && r.Method == http.MethodPost:
		return "ApplyGuardrail", "", true
	}
	return "", "", false
}

type bedrockError struct {
	code    string
	status  int
	message string
	extra   map[string]any
}

func bedrockInvalid(format string, args ...any) *bedrockError {
	return &bedrockError{code: "ValidationException", status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

func bedrockWriteError(w http.ResponseWriter, failure *bedrockError) {
	body := map[string]any{"message": failure.message}
	for key, value := range failure.extra {
		body[key] = value
	}
	w.Header().Set("X-Amzn-ErrorType", failure.code)
	writeBedrockJSON(w, failure.status, body)
}

func bedrockRequestID() string {
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	encoded := hex.EncodeToString(random)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// bedrockRuntimeRegion validates dummy TCSIM credentials and Region scope.
func bedrockRuntimeRegion(r *http.Request) (string, *bedrockError) {
	region := r.Header.Get("X-Simulator-Region")
	if r.Header.Get("X-Amz-Security-Token") != "" {
		return "", bedrockInvalid("Session tokens are not supported.")
	}
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		_, scope, ok := strings.Cut(authorization, "Credential=")
		scope, _, _ = strings.Cut(scope, ",")
		parts := strings.Split(strings.TrimSpace(scope), "/")
		if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") || !ok || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[3] != "bedrock" || parts[4] != "aws4_request" {
			return "", &bedrockError{code: "AccessDeniedException", status: http.StatusForbidden, message: "Use unsigned requests or dummy TCSIM SigV4 credentials with the bedrock signing name; Bedrock API keys are not supported."}
		}
		if region != "" && region != parts[2] {
			return "", bedrockInvalid("Region header and credential scope disagree.")
		}
		region = parts[2]
	}
	if region == "" {
		region = "us-east-1"
	}
	for _, supported := range Regions {
		if region == supported {
			return region, nil
		}
	}
	return "", bedrockInvalid("Unsupported simulator region.")
}

// bedrockResolveModel accepts a mapped-form model ID or its foundation-model
// ARN in the request Region. Other ARN kinds are not modeled.
func bedrockResolveModel(label, region string) (string, bool) {
	if len(label) > bedrockModelLabelBytes {
		return "", false
	}
	if strings.HasPrefix(label, "arn:") {
		prefix := "arn:aws:bedrock:" + region + "::foundation-model/"
		if !strings.HasPrefix(label, prefix) {
			return "", false
		}
		label = strings.TrimPrefix(label, prefix)
	}
	return label, ValidBedrockModelID(label)
}

func bedrockMediaType(value string, accept bool) bool {
	if value == "" || accept && value == "*/*" {
		return true
	}
	media, _, err := mime.ParseMediaType(value)
	return err == nil && media == "application/json"
}

func bedrockInferenceError(err error, modelID string) *bedrockError {
	message := err.Error()
	switch inference.KindOf(err) {
	case inference.Unavailable:
		return &bedrockError{code: "ServiceUnavailableException", status: http.StatusServiceUnavailable, message: message}
	case inference.ModelMissing:
		return &bedrockError{code: "ResourceNotFoundException", status: http.StatusNotFound, message: message}
	case inference.Timeout:
		return &bedrockError{code: "ModelTimeoutException", status: http.StatusRequestTimeout, message: message}
	case inference.Busy:
		return &bedrockError{code: "ThrottlingException", status: http.StatusTooManyRequests, message: message}
	case inference.Canceled:
		return &bedrockError{code: "ServiceUnavailableException", status: http.StatusServiceUnavailable, message: message}
	}
	failure := &bedrockError{code: "ModelErrorException", status: http.StatusFailedDependency, message: message, extra: map[string]any{"resourceName": modelID}}
	if status := inference.StatusOf(err); status > 0 {
		failure.extra["originalStatusCode"] = status
	}
	return failure
}

func (g *Gateway) serveBedrockRuntime(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Amzn-RequestId", bedrockRequestID())
	if failure := g.bedrockRuntime(w, r); failure != nil {
		bedrockWriteError(w, failure)
	}
}

func (g *Gateway) bedrockRuntime(w http.ResponseWriter, r *http.Request) *bedrockError {
	for _, name := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Target", "X-Amz-Security-Token", "Content-Type", "Accept"} {
		if len(r.Header.Values(name)) > 1 {
			return bedrockInvalid("Duplicate %s headers are unsupported.", name)
		}
	}
	operation, label, ok := bedrockRuntimeRoute(r)
	if !ok {
		return &bedrockError{code: "UnknownOperationException", status: http.StatusNotFound, message: "No official Amazon Bedrock Runtime operation matches this method and path."}
	}
	if operation != "InvokeModel" && operation != "Converse" {
		return &bedrockError{code: "UnknownOperationException", status: http.StatusNotImplemented, message: "The simulator does not implement Amazon Bedrock Runtime " + operation + "; only non-streaming text InvokeModel and Converse are available."}
	}
	runtime := g.BedrockRuntime
	if runtime == nil {
		return &bedrockError{code: "UnknownOperationException", status: http.StatusNotImplemented, message: "Amazon Bedrock Runtime inference is disabled in this simulator process. The operator must explicitly select a local backend; there is no cloud or synthetic fallback."}
	}
	if r.Header.Get("X-Amz-Target") != "" {
		return bedrockInvalid("Amazon Bedrock Runtime REST/JSON does not accept a JSON protocol target.")
	}
	region, failure := bedrockRuntimeRegion(r)
	if failure != nil {
		return failure
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return bedrockInvalid("Query parameters are not supported for %s.", operation)
	}
	for name := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-amzn-bedrock-") {
			return bedrockInvalid("Unsupported option header %s: trace, guardrail, latency and service-tier options are not implemented.", name)
		}
	}
	if !bedrockMediaType(r.Header.Get("Content-Type"), false) || operation == "Converse" && r.Header.Get("Content-Type") == "" {
		return bedrockInvalid("Content-Type must be application/json.")
	}
	if !bedrockMediaType(r.Header.Get("Accept"), true) {
		return bedrockInvalid("Accept must be application/json.")
	}
	modelID, valid := bedrockResolveModel(label, region)
	if !valid {
		return bedrockInvalid("The provided model identifier is invalid. Use an operator-mapped model ID or its foundation-model ARN in the request Region.")
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	resource := "arn:aws:bedrock:" + region + "::foundation-model/" + modelID
	decision := "allowed"
	if principal != core.Administrator {
		if g.Identities == nil {
			return &bedrockError{code: "AccessDeniedException", status: http.StatusForbidden, message: "Bedrock Runtime identity authority is unavailable."}
		}
		var err error
		decision, err = g.Identities.DecisionContext(r.Context(), principal, bedrockInvokeAction, resource)
		if err != nil {
			return &bedrockError{code: "InternalServerException", status: http.StatusInternalServerError, message: "Identity state could not be read."}
		}
	}
	w.Header().Set("X-Simulator-IAM-Decision", decision)
	if decision != "allowed" {
		return &bedrockError{code: "AccessDeniedException", status: http.StatusForbidden, message: "IAM evaluation: " + decision + " for " + principal + " (" + bedrockInvokeAction + " on " + resource + ")."}
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, BedrockRuntimeRequestBytes))
	if err != nil {
		return bedrockInvalid("Request bodies are limited to %d bytes.", BedrockRuntimeRequestBytes)
	}
	backendModel, mapped := runtime.models[modelID]
	if !mapped {
		return &bedrockError{code: "ResourceNotFoundException", status: http.StatusNotFound, message: "Model " + modelID + " is not mapped to a local backend model by the simulator operator."}
	}
	var request inference.Request
	if operation == "InvokeModel" {
		request, failure = parseAnthropicInvoke(data)
	} else {
		request, failure = parseConverse(data)
	}
	if failure != nil {
		return failure
	}
	request.Model = backendModel
	deadline := time.Now().Add(runtime.Timeout() + bedrockDeadlineSlack)
	controller := http.NewResponseController(w)
	// Extend the listener's fixed deadlines for this bounded request only. A
	// writer that cannot extend them keeps the host's shorter deadline.
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	started := time.Now()
	result, err := runtime.generate(r.Context(), request)
	if err != nil {
		return bedrockInferenceError(err, modelID)
	}
	latency := time.Since(started).Milliseconds()
	if operation == "InvokeModel" {
		writeAnthropicResult(w, modelID, result, latency)
	} else {
		writeConverseResult(w, result, latency)
	}
	return nil
}

func writeAnthropicResult(w http.ResponseWriter, modelID string, result inference.Result, latency int64) {
	stop := "end_turn"
	if result.Stop == inference.StopLength {
		stop = "max_tokens"
	}
	content := []map[string]string{}
	if result.Text != "" {
		content = append(content, map[string]string{"type": "text", "text": result.Text})
	}
	random := make([]byte, 12)
	_, _ = rand.Read(random)
	w.Header().Set("X-Amzn-Bedrock-Input-Token-Count", strconv.Itoa(result.InputTokens))
	w.Header().Set("X-Amzn-Bedrock-Output-Token-Count", strconv.Itoa(result.OutputTokens))
	w.Header().Set("X-Amzn-Bedrock-Invocation-Latency", strconv.FormatInt(latency, 10))
	writeBedrockJSON(w, http.StatusOK, map[string]any{
		"id": "msg_bdrk_" + hex.EncodeToString(random), "type": "message", "role": "assistant", "model": modelID,
		"content": content, "stop_reason": stop, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": result.InputTokens, "output_tokens": result.OutputTokens},
	})
}

func writeConverseResult(w http.ResponseWriter, result inference.Result, latency int64) {
	stop := "end_turn"
	if result.Stop == inference.StopLength {
		stop = "max_tokens"
	}
	content := []map[string]string{}
	if result.Text != "" {
		content = append(content, map[string]string{"text": result.Text})
	}
	writeBedrockJSON(w, http.StatusOK, map[string]any{
		"output":     map[string]any{"message": map[string]any{"role": "assistant", "content": content}},
		"stopReason": stop,
		"usage":      map[string]int{"inputTokens": result.InputTokens, "outputTokens": result.OutputTokens, "totalTokens": result.InputTokens + result.OutputTokens},
		"metrics":    map[string]int64{"latencyMs": latency},
	})
}
