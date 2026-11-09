# Durable guest lease contract model

`DurableGuestLease` is a finite safety model of the opt-in registry boundary in
`internal/aws/ec2_guest_lease.go` and the trusted backend contract in
`internal/workload/guestlease/lease.go`. It is not an implementation of a backend
and does not enable public EC2 or SSM operations. Fixture state-machine tests are
not VM or backend durability evidence.

## Bounds and state

The three seeds start at the acquire, contain and release transaction boundaries.
Each has one target record, an independent retained peer lease/tombstone, three
possible record keys and a quota of two representing the production quota of 1024
retained records. Acquisition starts with an unused target slot. A competing
admission captures its own snapshot, then attempts an insert into a fresh or
already retained key. Both primary and competing commits check current rows for
capacity and uniqueness. Accepted inserts change the retained-record map; failed
inserts leave it unchanged. Atomic guarded commits abstract serialized database
writes, not a proof of SQLite isolation. The stale-snapshot control deliberately
uses the old count after a competing insert has consumed the final slot. These
are separate bounded boundary scenarios, not a theorem that the implementation
refines their composition. Request identity is constructed independently from the
durable record and observations. Owner/account, region, instance, deployment,
profile incarnation, record fence, operation and lease have mismatching inputs.

`work` and `ackWork` are uncommitted database work; `db` is the committed record.
Crash/reopen discards local transaction and observation activity but preserves
committed records, backend effects and delayed deliveries. One dispatch can leave
two identical delayed continuations. A backend effect is independent of its
registry acknowledgement. Observations read backend state, including a separate
unknown result, rather than treating the desired action as completed work. An
independent observation envelope can carry a wrong fence, operation, action,
request lease, result lease or result state. The `obs-owner` case is an envelope
from another owner's fence: the actual backend Intent has no Owner field. Owner,
region, deployment and profile are retained local authority, checked before the
backend call and never replaced from a reply. A dishonest backend returning the
correct fence for another physical guest violates the backend mapping contract;
this model does not invent a runtime Owner-field validation to detect it.

One captured reconciliation snapshot can be delayed while a second reconciliation
wins and its caller advances to the next operation. `ConcurrentAdvance` abstracts
that other caller's successful acknowledgement, intent commit and backend effect;
its individual database steps are represented by the ordinary path for each
operation seed. The losing snapshot still attempts its exact-record CAS. Thus
invocations are not assumed to serialize, but arbitrary multi-caller histories,
SQL isolation and the atomicity of this summarized competing path are outside
scope. The retained peer is immutable background state, not a second independently
executing lifecycle.

## Source correspondence and safety properties

- `Acquire`, `guestLeaseWrite`, `Commit` and `dispatch`: durable intent precedes
  Dispatch. Evidence compares the actual persisted record and record incarnation
  against the independently constructed request, not a committed/durable flag; no external call occurs in the caller's database transaction.
  Rollback cannot expose work and reopen cannot remove durable obligations.
- `Get`, `List`, `guestLeaseRead`: exact stored authority and canonical record
  identity are abstract record equality. Get/List perform no backend dispatch.
  Concrete JSON canonicalization, pagination and SQL constraints need native tests.
- `Advance`, `replace`: operation and exact expected-record CAS reject stale
  snapshots. Profile recreation cannot replace the stored profile incarnation;
  cleanup remains possible without consulting the current profile.
- `Reconcile`: only authoritative, matching acquired/contained/released evidence
  permits an acknowledgement. Unknown acquisition remains pending. Observation
  failure, uniqueness failure or final acknowledgement failure leaves the prior
  durable obligation intact. Dispatch acknowledgement alone never proves stopped
  or released state.
- The unique retained lease constraint includes retired peer leases. Tombstones
  cannot be pruned or replaced, and continue to count toward admission quota.
  Competing admission attempts exercise quota and reuse refusal against retained
  rows. The model introduces no production pruning or replacement API.

The configuration lists 24 invariants. In particular `ReadFence`, `CASFence`,
`FailedACKRetainsPending`, `ObservationEnvelopeExact`, `ObservationEvidence`,
`AuthoritativeObservation` and `UnknownNeverRetires` distinguish requested identity, actual observation and
committed acknowledgement. `CleanupSurvivesProfile` is a one-step enabled-cleanup
contract, not eventual progress or a fairness assumption.

## Conditional backend obligations

The backend must durably bind the complete payload to `(Fence, Operation)`, reject
conflicting payloads, suppress duplicate effects and maintain monotone fences
against queued and in-flight older work. Lease-to-guest mappings must be immutable
and nonaliasing even after retirement. Authoritative observations must describe
completed physical work, with containment/release fencing delayed revival.

`BackendDuplicateSuppression`, `BackendMonotone`, `BackendImmutableMapping` and
`BackendPayloadIdentity` explicitly state these assumptions as properties of the
modeled backend. `NoFalseStopped` and authoritative lifecycle publication depend
on this contract. Backend mutation controls deliberately violate it to expose
counterexamples; they do not show that the registry can contain a malicious or
nonconforming backend. No Go driver, QEMU process, transport cache or requested
operation is certified as satisfying this contract by registration of this model.
Actual backend persistence/atomicity, physical containment, SQL/crash refinement,
random fence collision resistance and unbounded concurrency remain outside scope.
There is no liveness theorem, automatic retry, absence-based retirement, lease
replacement or recovery garbage collection.

## Designated counterexample controls and registration

The registry designates 32 controls for uncommitted dispatch, omitted durable
intent writes with commit flags intact, calls inside a transaction, redispatch
after reopen, rollback publication, lost durable records, each read fence, stale
CAS, failed final ACK, retired-lease aliasing, every observation-envelope field,
request-echo observations, duplicate effects, stale continuation revival, backend
mapping aliasing, conflicting payload acceptance, primary/competing quota bypass,
stale admission snapshots, retired/live-record replacement and profile-dependent
cleanup refusal. The retired-record control overwrites key 2 and must first fail
`TombstoneRetained`. The separate live-record control overwrites existing key 1
without consuming another slot and must first fail `NoReplacement`; it does not
change the tombstone. These distinguish permanent retirement from the broader
no-replacement rule within the same two-record bound.
The exact source edits and expected first invariant are in `formal/models.json`.
These are executable control specifications, not assertions of executed results.

The model/config and exact two-file source scope are additive to the previous
64-model registry. `scripts/formal_model_gate.py` protects model presence,
invariants, controls and exact source scope. `scripts/test-formal-gate.py` adds
removal, source-scope, domain and literal-anchor negative checks without replacing
prior tests. Use the repository's `TLA_JAR=... make formal` gate for baseline and
all designated controls, and the existing pure gate test command for registry
validation. Applicability declarations are maintained independently by the
integration owner. Registration and source inspection alone are not a passing
TLC, native-test or release result.
