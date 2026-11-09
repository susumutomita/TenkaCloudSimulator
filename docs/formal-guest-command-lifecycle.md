# Bounded guest-command observation lifecycle

`formal/GuestCommandLifecycle.tla` is a finite safety model of an exclusively
owned, already-authorized QGA transport and its command observation driver.
It is not a guest process model and does not enable a Battle capability.

## Bounds and assumptions

The checked configuration has two sequential, never-reused job identities, one
active operation and two admitted status polls per job (the Go limit is 1024).
There are four start outcomes: definitely unsent, explicit rejection, accepted,
and unknown; four poll outcomes: pending, exited, truncated output, and error.
Reply correlation and abandonment timing are nondeterministic Booleans/events.
Two jobs expose stale callbacks after rejection or observed exit and admission of
a later command. No arbitrary-history induction or liveness is claimed.

The source scope is exactly `internal/workload/qga/client.go`,
`internal/workload/qga/protocol.go`, and
`internal/workload/guestcommand/driver.go`. This correspondence is a reviewed
abstraction, not a mechanically proved Go refinement. The QGA parser supplies
classified observations; its actual JSON, framing, synchronization nonce, signed
exit values, byte counts and transmission accounting are not executed by TLC.
Assumed opaque unique identities stand for driver/job handles and correlated
requests, not PID uniqueness, cryptographic strength or authority discovery.
The supplied transport's authorization and exclusive ownership are preconditions.

## Correspondence

- `Start` models driver admission and installation of a new active job before the
  transport call. `ExclusiveAdmission` and `NoClosedAdmission` forbid concurrent
  or closed-driver admission.
- `ResolveStart` models QGA transmission classification and driver publication.
  Explicit rejection also abstracts local invalid-command rejection, the two
  reusable error classes. `unsent` denotes other definitely-unsent failures:
  these close the driver but require no guest cleanup. `RejectedCannotRun`,
  `CleanupOnlyPossible`, `StartEvidence`, `CorrelationGuard` and `UnsentCloses`
  check these distinctions. `mayRun` means a command could have been accepted,
  not an assertion that a process is currently running.
- `Abandon` combines deadline expiry, cancellation, control-channel closure and
  driver close for an active job. While Start is in flight it only closes
  admission and records abort. Classification must return before cleanup is
  asserted. After acceptance it records uncertainty and cleanup, never a fake
  stopped/exited result. `StartingAbortDefersCleanup`, `AbandonDoesNotStop`,
  `CleanupRequired` and `QuarantineSticky` express these obligations.
- Deadline expiry is an abstract event, including the driver's synchronous
  checks when Start/Poll returns. A trace in which a deadline has expired maps
  that event before result publication. There is no clock variable, timer
  scheduling proof, elapsed-time guarantee or verification of absolute deadline
  arithmetic. The concrete driver starts its absolute deadline before sending
  and does not renew it on a pending result; code tests check that boundary.
- `BeginPoll`/`PollResult` split the RPC to expose cancellation while waiting.
  `PollBound` checks admitted-call counting. Rate limiting is abstracted as
  stuttering; concurrent/foreign-handle refused polls also do not change state.
  Known correlated exit or truncated output retires the active job, backed by
  `ExitEvidence` plus `TerminalPublicationEvidence`. The latter independently
  requires a newly published terminal observation to name the polling job, have
  an accepted/polling predecessor, and match an actually correlated exited or
  truncated event outcome. The branch-assigned proof flag alone is insufficient.
  Truncated output models `OutputUnavailable`, not success with
  an invented exit code. A late result can clear private in-flight bookkeeping
  but cannot change an uncertain public observation (`TerminalObservationsSticky`).
- `StaleAbandonFrame` checks the job identity fence: a delayed timer or old
  handle cannot abandon a later active job. Invalid foreign handles are outside
  the finite identity domain and their rejection is a source/test obligation.
  `CloseIdle` closes admission without changing completed observations.

## Sensitivity and policy

The registry requires all seventeen invariants, the exact three-file source scope
and seventeen designated mutants. Each mutant must produce TLC exit 12 and its
named invariant counterexample, not merely a parse error or any failure.
Controls remove admission guards, exit evidence, cleanup classification,
quarantine stickiness, poll bounds, the stale-job fence, late-result fencing,
start/poll correlation, abort handling, unsent-channel closure and fictitious
process termination on abandon. `PublishPendingAsExit` replaces the correlated
pending-result branch with fabricated exit phase/proof publication;
the independent event-evidence invariant rejects it. Review exposed that this
mutant passed the earlier sixteen properties, so the earlier baseline was not
evidence against this specific modeling defect. The late-result
mutant also clears cleanup to model an erroneous reconciliation rather than
being detected only as an inconsistent cleanup bit. Negative registry tests
reject removal of the model, any required property, source-scope shrink/expansion,
mutation-anchor drift and deletion of required controls. Existing models,
properties, controls and thresholds are preserved.

## Verification and exclusions

Run the registered baseline and all seventeen controls through `make formal`
with the repository-pinned TLC tool. Formal applicability binds exact changed
blobs; it establishes declaration consistency, not semantic proof of Go code.
Native/race, SDK/browser and complete problem acceptance remain separate gates.

No Go refinement, byte-parser verification, nonce strength, wall-clock
scheduling, VM/process containment, persistent intent/restart recovery, PTY,
Session Manager, SSM or Hello World Battle availability is proved. Closing the
stream does not kill the guest. Cleanup requires a trusted lifecycle component
and immutable guest lease, neither supplied nor modeled here.
