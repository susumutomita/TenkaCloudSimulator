# Local owner-session lifecycle correspondence

This model corresponds to the gate, transaction, revocation, facade I/O and cleanup methods in `internal/workload/guesttcp/owned_probe_session.go`. The persistent-composition extension adds copied descriptor inspection data without changing these lifecycle transitions. Its expanded frame classifier is separately covered by `OwnedProbeDispatchPolicy`; attachment handoff is covered by `OwnedProbeAttachmentLifecycle`.

The model starts AFTER successful transfer and acquisition of one of eight process-wide slots. It models one retained slot, not global constructor contention or validation rollback. Immutable binding is a symbolic fixed configuration marker, not proof of copied Go slices or physical ownership. Native copying/parser tests remain required.

Enter acquires session gate and checks local terminal/context/deadline state. Expire represents owner invalidation; ChildCancel independently represents the original per-probe context cancellation, with asynchronous Revoke allowed later; no liveness assumed. A single provider transaction retains the gate through callback completion and final return classification. CallbackBegin checks local validity; Apply may still have an effect if revoke races after that check. CallbackEnd and ProviderReturn recheck validity. Acceptance is the final current-check linearization, NOT caller-return-time liveness. Accepted historical evidence may remain visible after later revoke or a retained late callback. Provider physical lease fencing is an external prerequisite and is not established by any model variable.

Observation append is tentative while the gate is held; failed provider/apply/postcheck rolls it back. Evidence reads require the gate free. Provider duplicate and late callbacks poison/revoke without a second modeled apply. A provider rejecting without callback need not revoke; success without callback does. The model abstracts provider panic as the same terminal error cleanup path; a separate native panic regression remains necessary.

Revoke seals facade admission and starts one asynchronous Close independently of the transaction gate. Existing facade reads/writes can finish later; IOStart and revoke serialize via ioMu. CloseReturn does not join: Join needs actual Close completion, zero facade I/O and released transaction gate. Only successful Close releases the retained slot; an error, blocked Close, blocked callback or blocked I/O retains debt. Wait cancellation changes none of this state and is represented as stuttering.

This is intentionally not a proof of frame-byte parsing, TCP semantics, arbitrary provider synchronization, real same-owner physical effect, global resource alias freedom or actual VM cleanup. It cannot qualify an IssuedProbeResource implementation. No production issuer or endpoint is added.

## Recognition is not sealing

ProviderReturn computes outcome under the callback mutex, then releases it before invoking Revoke. Duplicate callback recognition also precedes acquiring ioMu in Revoke. The model represents poison recognition separately from Revoke; facade IOStart remains possible between these steps while local terminal/expiry checks still allow it. FinishReturn waits for terminal seal when transaction classification requires revocation. MisuseReturn similarly waits for sealing, representing the callback mutex held until Revoke returns. A retained late callback may occur after final classification even before the outer function releases its gate; previously accepted historical evidence is not retroactively removed. Native panic/defer paths are still abstracted as error cleanup, not fully enumerated.

## Narrow completed inbound injection

The model also retains the original two-facade-call bound and adds one post-success token replay admission decision, not a second complete transaction.

## Source correspondence

- `owned_probe_session.go` transaction entry claims a private token once and rejects precompleted/reused tokens. `Enter`, `tokenUsed`, and `ReplayPriorSuccess` represent those admission decisions. `tokenState` is original provenance, not a mutable authority identifier. Optional `Configure` preserves original generic no-token schedules.
- `transport_persistent.go` probe receive is the private local-injection producer callsite. `transport.go` `commitLocalInjection` and the local branch of `commitContextMode` create the invocation context and record actual successful synchronous injection. `Apply` and `ProducerPostcheck` are separate, allowing original child cancellation before or after the producer postcheck. The generic transport methods are not covered by this additional scope.
- `local_injection_outcome.go` private token, completion marker and owner callback wrapper are represented by finite identity classes, not provider assertions. `cleanProducer` records the independent raw success/live-transport prerequisite. Wrong-marker identity is an explicit malformed-input overapproximation, not a public construction API.
- `owned_probe_session.go` callback postcheck is `CallbackEnd`; child cancellation may happen between producer and owner postcheck or between owner callback and provider return. `ChoosePropagation` distinguishes exact nil/nil, exact wrapper identity, additional wrapping and unrelated error. Missing callbacks can return too.
- `ProviderReturn` represents the final classification. A safe local completion is still a failed packet result: no accepted observation, no tentative evidence publication and no cleanup release. Owner liveness is tested at this decision, not guaranteed until the caller receives the result. Existing poison remains sticky; later revocation is independent.
- The old gate, asynchronous close initiation, actual close completion, facade drain, and retained cleanup-debt transitions remain. Error recognition does not atomically force revocation, preserving the admitted-I/O window before the actual seal.

## Independent checks and limits

`ExceptionOracle` uses original token provenance, raw completion and clean-producer history independently of the consumer predicate. Controls corrupt producer and consumer together where an isolated guard removal would be redundant. The 29 semantic controls cover lifecycle/debt rules and include prior-success replay, missing provenance, failed injection, mixed transport error, wrong marker, wrapped/unrelated/swallowed callback error, absent original cancellation, owner revocation, outbound/observation misuse, and discarding the valid narrow exception.

The finite malformed direction/kind/provider cases overapproximate the private call graph. Child cancellation is an independent event and does not rebase any clock. Owner and transport sealing are distinct. Admission before apply may be conservatively represented even where concrete child-context validation would reject earlier; this cannot certify concrete early-failure completeness. Exact pointer allocation, atomic CAS refinement, arbitrary error values, parser bytes, external resource exclusivity and provider physical authority require separate source review/native evidence.
