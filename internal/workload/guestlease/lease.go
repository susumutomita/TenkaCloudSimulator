// Package guestlease defines a trusted backend boundary, not a VM implementation.
// Callers must persist an Intent before Dispatch and must never replay Dispatch
// after an uncertain result or restart. Backend implementations must retain their
// own durable mapping from Fence to an immutable guest identity. No participant
// supplied path, socket, VM identifier or provider resource ID crosses this API.
package guestlease

import "context"

type Action string

const (
	Acquire Action = "acquire"
	Contain Action = "contain"
	Release Action = "release"
)

// Fence is a random lease incarnation; Operation is monotonically increasing.
// Lease is a backend-generated opaque handle, empty until acquisition is observed.
type Intent struct {
	Fence     string
	Operation uint64
	Action    Action
	Lease     string
}
type Disposition int

const (
	Uncertain Disposition = iota
	Accepted
	// Rejected guarantees Dispatch neither performed nor queued any side effect.
	Rejected
)

type State string

const (
	Unknown   State = "unknown"
	Acquired  State = "acquired"
	Contained State = "contained"
	Released  State = "released"
)

type Observation struct {
	Intent Intent
	Lease  string
	State  State
}

// Dispatch must durably record the full request keyed by (Fence, Operation)
// before accepting or queueing work. The identity is immutable: conflicting
// Action or Lease values for that key must be rejected without additional effect.
// An identical delayed request must not create a second guest or repeat effects.
// This is a backend obligation, not an exactly-once guarantee from this registry.
//
// Backend execution is monotone per Fence, including queued work and in-flight
// continuations. Before every effect it must fence against newer operations.
// Containment/release acknowledgement must also fence older work that could
// resume later; an old acquire/contain continuation cannot revive a released
// guest, downgrade containment, or attach another guest. Durable high-water marks
// and terminal tombstones must survive backend restart.
//
// Each Fence has one immutable backend-created lease-to-guest mapping. Neither a
// lease nor its underlying guest may alias another Fence, be reassigned after
// release, or be replaced during recovery. Observe must consult that mapping and
// actual completed backend work, not synthesize a state from the requested action.
//
// Observe is authoritative and side-effect free. Released proves no live guest
// and no delayed dispatch can create/revive it. Contained proves no guest work
// can execute until a new authorized operation (none is supplied by this slice).
// Errors, timeout, missing transport, or a process-local cache cannot establish
// containment or release. Echoing a request alone is not authoritative evidence.
// Implementations must bound work and respect context cancellation.
type Backend interface {
	Dispatch(context.Context, Intent) (Disposition, error)
	Observe(context.Context, Intent) (Observation, error)
}
