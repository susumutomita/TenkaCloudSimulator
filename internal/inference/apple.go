package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
	"unicode/utf8"
)

// ErrAppleUnavailable preserves the standalone binary's opt-in boundary.
var ErrAppleUnavailable = &Error{Kind: Unavailable, Message: "the Apple Foundation Models backend is unavailable: explicitly configure an optional native helper; no other backend is selected"}

// AppleFoundationModels never discovers or builds native code implicitly.
func AppleFoundationModels() (Backend, error) { return nil, ErrAppleUnavailable }

// Apple invokes one operator-selected executable, without a shell, socket,
// persistent session, prompt arguments or inherited environment. The helper is
// trusted native code with the operator's authority, not a guest sandbox.
type Apple struct{ helper string }

// NewAppleFoundationModels probes the optional helper before startup. Default
// builds still use CGO_ENABLED=0 and require no Swift runtime or helper.
func NewAppleFoundationModels(helper string) (*Apple, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, failure(Unavailable, 0, "The Apple Foundation Models backend requires an Apple silicon Mac.")
	}
	if !filepath.IsAbs(helper) {
		return nil, failure(Unavailable, 0, "The Apple helper must be an explicit absolute executable path.")
	}
	info, err := os.Stat(helper)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, failure(Unavailable, 0, "The configured Apple helper is missing or is not an executable file.")
	}
	a := &Apple{helper: helper}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = a.call(ctx, "--check", nil)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Apple) Name() string { return "apple" }

// ValidModel accepts only the on-device system model, never PCC or model URLs.
func (a *Apple) ValidModel(name string) bool { return name == "system" }

type appleMessage struct {
	Role Role   `json:"role"`
	Text string `json:"text"`
}

type appleRequest struct {
	Version     int            `json:"version"`
	Model       string         `json:"model"`
	System      string         `json:"system"`
	Messages    []appleMessage `json:"messages"`
	MaxTokens   int            `json:"maxTokens"`
	Temperature *float64       `json:"temperature,omitempty"`
	TopP        *float64       `json:"topP,omitempty"`
	TopK        *int           `json:"topK,omitempty"`
}

type appleReply struct {
	Version int     `json:"version"`
	Error   string  `json:"error,omitempty"`
	Result  *Result `json:"result,omitempty"`
}

func (a *Apple) Generate(ctx context.Context, request Request) (Result, error) {
	if !utf8.ValidString(request.System) || !a.ValidModel(request.Model) || request.MaxTokens < 1 || request.MaxTokens > 8192 || len(request.Messages) == 0 || len(request.Messages)%2 == 0 {
		return Result{}, failure(Unrepresentable, 0, "The Apple request requires model system, alternating user/assistant turns and maxTokens 1–8192.")
	}
	if request.TopP != nil && request.TopK != nil {
		return Result{}, failure(Unrepresentable, 0, "Apple supports topP or topK sampling, not both together.")
	}
	if (request.Temperature != nil && !(*request.Temperature >= 0 && *request.Temperature <= 1)) ||
		(request.TopP != nil && !(*request.TopP > 0 && *request.TopP <= 1)) ||
		(request.TopK != nil && !(*request.TopK >= 1 && *request.TopK <= 500)) {
		return Result{}, failure(Unrepresentable, 0, "Apple requires temperature 0–1, topP greater than 0 and at most 1, and topK 1–500.")
	}
	translated := appleRequest{Version: 1, Model: request.Model, System: request.System, MaxTokens: request.MaxTokens, Temperature: request.Temperature, TopP: request.TopP, TopK: request.TopK}
	for i, message := range request.Messages {
		want := RoleUser
		if i%2 == 1 {
			want = RoleAssistant
		}
		if message.Role != want || !utf8.ValidString(message.Text) {
			return Result{}, failure(Unrepresentable, 0, "The Apple request has invalid conversation turns.")
		}
		translated.Messages = append(translated.Messages, appleMessage{Role: message.Role, Text: message.Text})
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(translated) != nil {
		return Result{}, failure(Unrepresentable, 0, "The Apple request options cannot be encoded.")
	}
	if body.Len() > MaxBackendRequestBytes {
		return Result{}, failure(TooLarge, 0, "The Apple request exceeds the native helper input bound.")
	}
	// Also bound direct library use; the shared Bounded wrapper supplies the
	// operator's shorter timeout, concurrency and shutdown cancellation.
	bounded, cancel := context.WithTimeout(ctx, MaxTimeout)
	defer cancel()
	reply, err := a.call(bounded, "--generate", body.Bytes())
	if err != nil {
		return Result{}, err
	}
	result := reply.Result
	if result == nil || result.Stop != StopEnd || result.InputTokens < 0 || result.OutputTokens < 0 || result.OutputTokens >= request.MaxTokens || !utf8.ValidString(result.Text) {
		return Result{}, failure(Unrepresentable, 0, "The Apple reply cannot truthfully represent a completed text turn below the requested token limit.")
	}
	return *result, nil
}

type appleOutput struct {
	buffer   bytes.Buffer
	cancel   context.CancelFunc
	overflow bool
}

func (b *appleOutput) Write(p []byte) (int, error) {
	if len(p) > MaxBackendResponseBytes-b.buffer.Len() {
		b.overflow = true
		b.cancel()
		return 0, errors.New("native output limit exceeded")
	}
	return b.buffer.Write(p)
}

func (a *Apple) call(ctx context.Context, mode string, input []byte) (appleReply, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := appleOutput{cancel: cancel}
	cmd := exec.CommandContext(runCtx, a.helper, mode)
	cmd.Env = []string{}
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return appleReply{}, ended(ctx, MaxTimeout)
		}
		if output.overflow {
			return appleReply{}, failure(BackendFailure, 0, "The Apple helper exceeded the output bound.")
		}
		return appleReply{}, failure(Unavailable, 0, "The configured Apple helper failed; no other backend is selected.")
	}
	var reply appleReply
	decoder := json.NewDecoder(bytes.NewReader(output.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(output.buffer.Bytes()) || decoder.Decode(&reply) != nil || decoder.Decode(new(any)) != io.EOF || reply.Version != 1 {
		return appleReply{}, failure(BackendFailure, 0, "The Apple helper returned an invalid protocol response.")
	}
	if reply.Error != "" {
		// Only fixed codes cross the boundary. Never echo framework errors,
		// stderr or arbitrary helper output: they may contain prompt text.
		kind, reason := BackendFailure, "generation failed"
		switch reply.Error {
		case "deviceNotEligible", "appleIntelligenceNotEnabled", "modelNotReady", "unsupportedOS":
			kind, reason = Unavailable, reply.Error
		case "busy":
			kind, reason = Busy, "model busy"
		case "timeout":
			kind, reason = Timeout, "model deadline exceeded"
		case "unsupported", "tokenLimit", "contextLimit":
			kind, reason = Unrepresentable, reply.Error
		}
		return appleReply{}, failure(kind, 0, "Apple Foundation Models: %s; no fallback is selected.", reason)
	}
	if mode == "--check" && reply.Result != nil {
		return appleReply{}, failure(BackendFailure, 0, "The Apple availability check returned an unexpected generation result.")
	}
	return reply, nil
}
