// Package inference owns provider-neutral, bounded local text generation.
//
// It knows no cloud protocol, IAM, problem IDs or scoring. Providers translate
// their wire shapes into Request and map Error kinds to their own envelopes.
// A backend either returns text it actually received from the operator-selected
// local engine or a typed Error; it never substitutes another backend, a
// synthetic answer or a cloud endpoint. Prompts and responses are not persisted
// or logged here.
package inference

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Role is a conversation turn author. System text travels separately.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one text-only conversation turn.
type Message struct {
	Role Role
	Text string
}

// Request is a validated, text-only, non-streaming generation request. Model
// is the operator-mapped backend model name, never a client-supplied URL.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	TopP        *float64
	TopK        *int
}

// StopReason reports why the backend stopped generating.
type StopReason string

const (
	// StopEnd means the model finished its turn.
	StopEnd StopReason = "end"
	// StopLength means MaxTokens was reached.
	StopLength StopReason = "length"
)

// Result is text the local backend actually generated, with its own token
// counts. Counts come from the backend tokenizer, not a cloud model's.
type Result struct {
	Text         string
	Stop         StopReason
	InputTokens  int
	OutputTokens int
}

// Kind classifies a failure so providers can return a truthful error.
type Kind int

const (
	// Unavailable: the selected backend cannot be reached or used.
	Unavailable Kind = iota + 1
	// ModelMissing: the operator-mapped model is not installed locally.
	ModelMissing
	// Timeout: the bounded generation deadline expired.
	Timeout
	// Busy: the bounded concurrency limit, or the backend's own queue, is full.
	Busy
	// BackendFailure: the backend answered with an error or malformed output.
	BackendFailure
	// Unrepresentable: the backend produced output this text-only contract
	// cannot carry (for example reasoning or tool calls).
	Unrepresentable
	// Canceled: the caller or the simulator shutdown canceled the request.
	Canceled
	// TooLarge: the translated backend request exceeds the transport bound.
	TooLarge
)

// Error is a typed backend failure. Message never contains prompt or response
// text; Status is the backend's HTTP status when one was received.
type Error struct {
	Kind    Kind
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// KindOf returns the failure kind, or BackendFailure for an untyped error.
func KindOf(err error) Kind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	return BackendFailure
}

// StatusOf returns the backend HTTP status carried by a typed error, or 0.
func StatusOf(err error) int {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Status
	}
	return 0
}

func failure(kind Kind, status int, format string, args ...any) error {
	return &Error{Kind: kind, Status: status, Message: fmt.Sprintf(format, args...)}
}

// Backend generates text. Implementations must honor ctx cancellation, bound
// what they read and never fall back to a different engine. ValidModel checks
// an operator-supplied backend model name before any request is accepted.
type Backend interface {
	Name() string
	ValidModel(name string) bool
	Generate(ctx context.Context, request Request) (Result, error)
}

// Limits for Bounded. Concurrency rejects instead of queueing.
const (
	MinTimeout     = time.Second
	MaxTimeout     = 5 * time.Minute
	MinConcurrency = 1
	MaxConcurrency = 8
)

// Bounded wraps a backend with a fixed concurrency gate and deadline. A full
// gate fails fast with Busy rather than queueing unbounded work.
type Bounded struct {
	backend Backend
	timeout time.Duration
	slots   chan struct{}
}

// NewBounded validates the operator limits and wraps backend.
func NewBounded(backend Backend, timeout time.Duration, concurrency int) (*Bounded, error) {
	if backend == nil {
		return nil, errors.New("an inference backend is required")
	}
	if timeout < MinTimeout || timeout > MaxTimeout {
		return nil, fmt.Errorf("inference timeout must be between %s and %s", MinTimeout, MaxTimeout)
	}
	if concurrency < MinConcurrency || concurrency > MaxConcurrency {
		return nil, fmt.Errorf("inference concurrency must be between %d and %d", MinConcurrency, MaxConcurrency)
	}
	return &Bounded{backend: backend, timeout: timeout, slots: make(chan struct{}, concurrency)}, nil
}

// Name is the wrapped backend's name.
func (b *Bounded) Name() string { return b.backend.Name() }

// ValidModel delegates operator model-name validation to the backend.
func (b *Bounded) ValidModel(name string) bool { return b.backend.ValidModel(name) }

// Timeout is the per-request generation deadline.
func (b *Bounded) Timeout() time.Duration { return b.timeout }

// Concurrency is the number of simultaneous generations admitted.
func (b *Bounded) Concurrency() int { return cap(b.slots) }

// Generate admits at most Concurrency requests and applies Timeout. A request
// whose context already ended never reaches the backend. Any expired deadline,
// the operator's or the caller's, is reported as Timeout and any cancellation
// as Canceled, even when the backend returned a different error afterward.
func (b *Bounded) Generate(ctx context.Context, request Request) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ended(ctx, b.timeout)
	}
	select {
	case b.slots <- struct{}{}:
	default:
		return Result{}, failure(Busy, 0, "All %d local inference slots are busy; retry later.", cap(b.slots))
	}
	defer func() { <-b.slots }()
	bounded, cancel := context.WithTimeout(ctx, b.timeout)
	defer cancel()
	result, err := b.backend.Generate(bounded, request)
	if bounded.Err() != nil {
		return Result{}, ended(bounded, b.timeout)
	}
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func ended(ctx context.Context, timeout time.Duration) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failure(Timeout, 0, "The local backend did not finish before the request deadline (at most %s).", timeout)
	}
	return failure(Canceled, 0, "The request was canceled before the local backend finished.")
}

// Close releases idle resources of a backend that holds any.
func (b *Bounded) Close() {
	if closer, ok := b.backend.(interface{ Close() }); ok {
		closer.Close()
	}
}
