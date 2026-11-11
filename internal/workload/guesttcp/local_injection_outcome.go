package guesttcp

import "sync/atomic"

// Private per-invocation provenance, never a provider-supplied authority bit.
type localInjectionKey struct{}
type localInjectionToken struct {
	used      atomic.Bool
	completed atomic.Bool
	eligible  atomic.Bool
}
type localInjectionCompleted struct {
	token *localInjectionToken
	cause error
}

func (e *localInjectionCompleted) Error() string { return e.cause.Error() }
func (e *localInjectionCompleted) Unwrap() error { return e.cause }

// A provider must propagate this exact callback error for the narrow exception.
type ownedCallbackError struct{ cause error }

func (e *ownedCallbackError) Error() string { return e.cause.Error() }
func (e *ownedCallbackError) Unwrap() error { return e.cause }
