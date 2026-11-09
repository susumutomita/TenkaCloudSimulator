package guestcommand

import "context"

// DurableIntent is supplied only by a trusted provider after committing intent.
// Token identifies one target operation forever; Command groups target operations.
// Lease and LeaseFence identify the already acquired guest incarnation. Request
// is the SHA-256 digest of immutable provider authority, request and target leases.
// ScriptJSON is an ordered JSON string array, not host shell arguments.
// TimeoutSeconds is the requested delivery window, not proof of runtime expiry
// or cancellation. Execution must independently obey guestcommand runtime bounds.
type DurableIntent struct {
	Token, Command, Lease, LeaseFence, Request, ScriptJSON string
	TimeoutSeconds                                         int
	RequestedAt                                            int64 // UTC Unix milliseconds
}

type DurableObservation struct {
	Intent DurableIntent
	// State is Unknown, InProgress, Success, Failed, TimedOut or Rejected.
	// Unknown and transport errors never prove the guest stopped.
	State                 string
	StartedAt, FinishedAt int64
	ExitCode              int
	Stdout, Stderr        string
	// Truncation or uncertain cancellation cannot be reported as Success.
	OutputTruncated, CleanupRequired bool
}

// DurableBackend is a prerequisite contract, not an implementation or a bridge
// to Driver. Driver's process-local Handle cannot provide this contract.
//
// Dispatch must durably bind Token to the entire exact intent BEFORE any effect,
// reject conflicting reuse, and suppress duplicate effects including delayed
// continuations across restart. It must atomically fence execution against the
// authoritative guestlease incarnation and containment/release high-water mark.
// A held lease observed by the provider does not authorize a later stale effect.
// No intent may select a transport, host path, identity, or replacement guest.
//
// Observe is bounded, side-effect-free and authoritative. Echoing the intent or
// an in-memory fixture does not establish completion. Terminal observations must
// be immutable, including timestamps, exit code and bounded UTF-8 output. Success
// requires known exit code zero, complete output and no cleanup obligation.
// Timeout/transport loss without verified exit requires containment; it must not
// be disguised as cancellation or terminal TimedOut. All calls honor context.
// There is intentionally no production backend in this prerequisite.
type DurableBackend interface {
	Dispatch(context.Context, DurableIntent) error
	Observe(context.Context, DurableIntent) (DurableObservation, error)
}
