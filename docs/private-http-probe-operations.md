# Process-local HTTP probe operations

`PersistentTransport.StartHTTPObservation` retains a single concrete
`ProbeHTTPObserver` invocation under a nonzero 16-byte `HTTPProbeOperationKey`.
The key correlates work within one persistent NIC. It is not a credential,
physical-owner identity, provider binding, or durable execution identifier.
No endpoint, production capability, Host job metadata, or scoring is provided.

## Admission and lifetime

The NIC owns a map of at most `guestnet.MaxProbeSessions` (1024) handles. A second
transport wrapper around that same NIC cannot create another retention budget.
Handles and failure tombstones are never removed, evicted, renewed, or reused.
The existing TCP session-attempt budget remains independently enforced; legacy
probes and failed operation admissions do not replenish either budget.

A repeated key with the exact original port/path returns the same handle before
examining a replacement context or revalidating NIC admission. A different target
returns `ErrHTTPProbeOperationBinding`. A malformed key, unsupported path, nil
initial context, or full retention budget rejects without recording work.
Targets retained in the map have bounded keys/ports and one of two paths (`/`
or `/healthz`). An unapproved port, unavailable publication owner, revoked NIC,
canceled original context, or exhausted observation capacity becomes a retained
terminal result rather than an opportunity to retry the same key.

The initial start timestamp precedes map admission. Its absolute execution
deadline is the earliest of that timestamp plus eight seconds, the initial
caller deadline, and the NIC deadline. A timer is installed before dispatch;
scheduling delays cannot renew the deadline. The original caller still owns
cancellation of execution. Rediscovery and later waits do not replace that
context, extend a lifetime, cancel work, or execute another observer.
There is no retry or capacity-wait queue. The concrete observer retains the
existing global eight-slot limit shared with legacy HTTP observers.

## Result readiness is not settlement

A snapshot contains immutable target/start/deadline fields, `ResultReady`, the
original observation and `ResultError`, and a separate `Settled` flag.
`WaitResult` returns that snapshot separately from its own wait error. Result
publication occurs before settlement publication. A failed or timed-out wait is
never stored as the original operation result.

After `Observe` returns, the operation checks the private concrete observer's
admission flag. Pre-admission rejection has no worker, so it can settle after
publishing its error. An admitted observer must close its actual worker `done`
channel before the operation can settle. This joins blocked child cleanup and
blocked final same-owner publication without a cleanup timeout. The result may
already report deadline expiry while settlement and observation capacity remain
pending. Eventual worker completion does not overwrite that original failure or
turn it into a late success.

`WaitSettlement` returns nil only after actual settlement. Its own caller may
bound the wait; expiry returns `ErrDrainPending` joined with the context error.
A background-context wait has no extra artificial cleanup deadline. A nil wait
result proves settlement only, not observation success or whole-NIC cleanup.
Normal successful probes leave the shared NIC, passive service, and sibling
work alive. `LookupHTTPObservation` and `Snapshot` never execute work or alter
its lifetime; lookup absence or a read error never proves settlement.

## Synchronization and bounds

The map has a dedicated NIC-local mutex. Target comparison and insertion are
serialized. Caller/NIC deadline capture occurs outside that mutex, followed by
a second identity/quota check before insertion. A racing candidate cannot
construct another observer or replace the winning operation. Constructor admission, context wrapping,
`Observe`, worker joining, and all waits occur outside the map mutex and the
NIC's lifecycle mutex. A recorded handle is visible before observer setup and
dispatch. Each handle has its own state mutex and separate ready/settled channels.
Only the operation's single invocation publishes those terminal transitions.

Pending wrapper work is bounded by retained handles, while actual HTTP workers
are bounded by the shared observer admission pool. A misbehaving owner may keep
an admitted worker and its slot pending indefinitely; this is retained debt,
not proof that cleanup happened. This component does not qualify that owner.

## Future Host adapter obligations

The Host `SimulatorUptimeTransport.probe` contract requires its underlying
promise to remain unsettled until all backend work ends, including after the
Host's own bounded wait expires. An adapter must preserve the original operation
result, join actual settlement, and only then resolve or reject its underlying
promise. It must not complete that promise from `ResultReady`, a settlement-wait
timeout, an unsuccessful status read, or an unknown key.

A remote adapter additionally requires an authenticated provider-to-Host
owner/deployment/lease binding and durable uncertain-start and settlement
recovery. Those do not exist here. Process or NIC recreation loses this map;
unknown then means unknown, not canceled, completed, or safe to start again.
Neither a copied endpoint nor matching correlation strings supplies authority.
No physical-owner qualification, crash-recovery proof, production integration,
or formal coverage is claimed by this process-local component.

## Provider-sealed in-process scope

`BindHTTPProbeOperations` binds one nonzero fixed-size owner/deployment/lease/
backend-incarnation tuple and up to eight named services to the NIC. The trusted
provider chooses service ports from the NIC allowlist and paths from `/` or
`/healthz`. Callers use a service name, not an arbitrary endpoint. The table is
copied and sorted; rewrapping the NIC cannot replace its binding or budget.
Exact registration returns the existing capability, including after revocation
for settlement discovery. A conflicting registration fails. These IDs do not
authenticate a caller or prove ownership of a physical machine.

Scoped `Start` requires an initially finite, live deadline. Exact duplicates
return the original handle without renewing it. Raw and scoped operations
cannot adopt each other's keys, even with the same target. Scoped `Snapshot`
returns the sealed binding, selected service, and actual retained operation.
Unknown or foreign keys remain unknown, never settled. Raw lookup hides scoped
operations. No remote endpoint, registration service, durable store or Host
production adapter is installed by this interface.

After successful child allocation, `RouteReady` records the actual TCP tuple
before Dial sends its first SYN. The tuple remains available even when HTTP
parsing, publication or execution fails; a zero observation is not replaced by
a fabricated successful result. Failure before allocation leaves `RouteReady`
false. Reading a retained tuple is not independent physical-owner evidence,
cleanup certification, or permission to renew a lease.

## Versioned snapshot representation

`WireSnapshot` projects known retained state into version 1 JSON-ready values.
It does not create an HTTP endpoint or grant a consumer authority. The consumer
must independently bind all four nonzero 32-byte identities and the service to
its trusted owner/lease registry. Operation keys are 16-byte lowercase hex;
identity fields are 32-byte lowercase hex. Errors are limited to `canceled`,
`deadline_exceeded` or `probe_failed`, never internal exception text.

Backend timestamps use safe positive integer epoch milliseconds, not the Host
monotonic clock. The execution window remains at most eight seconds. A retained
failure from an already-expired NIC may have a deadline before its start, with
no allocated route; it must remain discoverable for settlement, never scoring.
Successful observations require the actual allocated route, matching service,
valid status, ordered timestamps and receipt strictly before the original
deadline. Millisecond-edge ambiguity rejects conservatively. A ready result
with `settled: false` does not discharge backend work. Missing keys remain
unknown; serialization errors are errors, not an empty successful observation.

The shared `test/fixtures/probe-operation-wire-v1.json` contract vectors cover
unknown, pending, allocated, result-ready/unsettled, success and sanitized
failure states. They are protocol examples, not execution or physical-owner
qualification. Production authentication, original-budget translation and
transport dispatch still need independently verified implementations.
