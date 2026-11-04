# Finite state model

`ObjectStore.tla` models two buckets and two keys, shared object ownership, create/put/delete transitions, and an abstract identity decision `allow /\ ~deny`. `ObjectStore.cfg` checks type correctness, no orphan objects, no unauthorized mutation, and explicit-deny precedence.

Run `TLA_JAR=/path/to/tla2tools.jar make formal`. The jar is TLC 1.7.4 from the official tlaplus/tlaplus release and checked against `tla2tools.sha256`. The script also removes the nonempty-delete guard in a temporary mutant and requires TLC to detect a NoOrphans violation. No Java/TLC dependency is needed at application runtime.

Correspondence: Create → Store.Create; Put → Store.Put; DeleteObject → Store.Delete; DeleteBucket → Store.DeleteBucket; object parent relation → SQLite FK and transaction; authorization predicate → selected principal policy decision before provider operations. Go tests exercise those concrete paths and concurrency.

The model abstracts object bytes, time, limits, policy patterns/ARN/action matching, policy document parsing, SQLite internals, crashes, external-process writers, and HTTP/UI behavior. It verifies 125 distinct reachable states for this finite configuration, not an unbounded theorem or refinement proof. There are no liveness claims. Policy state in the model is a two-Boolean abstraction; full IAM semantics are not proven.

No `Poteto` toolchain or configuration was found in the cloud checkout or its available repository instructions. That name is not assumed to mean TLC, Apalache, or a proof assistant.

The reported NoOrphans counterexample is **intentional negative control** in ObjectStoreMutant, generated only inside a temporary directory. It is not an unresolved bug in ObjectStore or the Go implementation. The normal model retains `ObjectsFor(b) = {}`; Store.DeleteBucket checks child count inside its transaction and SQLite also has a restrictive FK. The fresh run and concrete lifecycle/concurrent-write tests both confirm nonempty deletion is rejected without changing stored bytes.

## Stack ownership and drift

StackOwnership.tla models two owners and two names. Create reserves a name;
manual deletion removes the resource but preserves its claim; stack cleanup
releases only its own claims/resources. OwnershipExclusive prohibits two owners
from claiming the same name, including drift. The normal finite model must pass;
a mutant that allows reclaiming an absent resource must fail this invariant.
This corresponds to aws_cfn_ownership and transactional create/delete in the Go
AWS provider. It does not prove SQL crash behavior, authentication or IAM semantics.

## Native IAM and legacy identities

NativeIAMOwnership.tla models two case-folded role names, native active/retired claims, inline permissions and legacy identities. Native creation and legacy writes are atomic steps, native deletion preserves its claim, and policy-bearing roles cannot be deleted. TypeOK and OwnershipExclusive must hold; removing the LegacyPut native-claim guard must produce an OwnershipExclusive counterexample. This abstracts SQLite transactions, role case folding, trust/ARN parsing, HTTP authorization and process isolation. Actual concurrent Go/SDK/browser tests verify those paths separately. It is a finite state check, not an implementation refinement proof.

S3Versions.tla separately models enabled versioning, retained versions/delete markers and bucket deletion. Its mutation replaces the empty-history guard with an empty-current-view guard and must violate NoHistoryOrphans. Suspension, payload quotas, SQL failures and concrete IDs remain native test responsibilities.

## REST deployment snapshots

RESTDeploymentSnapshot.tla bounds one API, two deployment IDs, two handler configurations and one stage. Mutable routing edits or removal preserve frozen snapshots, stage references cannot dangle, and API deletion removes its dependent snapshots/stage. A mutation that rewrites old snapshots when draft configuration changes must violate SnapshotsFrozen. The fifth normal model and fifth mutation control run in the same checked-TLC workflow. This abstracts SQL, permissions, actual handlers, crashes and team isolation; native HTTP/SDK/Python and two-process tests separately check those paths. It is not an implementation refinement or liveness proof.

## Required gate and applicability

The executable inventory is now `formal/models.json`; all five existing models
and their exact sensitivity mutations remain mandatory. See
[formal validation](../docs/formal-validation.md) for exact changed-blob records,
negative controls, evidence and the distinction between a workflow check,
human scope review and separately configured repository merge protection.
New runtime/state transitions are not covered by the existing five models merely
because this suite passes. No new service, callback or Python/HTTP state-machine
proof is implied by this inventory.


## SSM latest parameter state

`SSMParameterState.tla` is the sixth registered finite model. It bounds two
regions by two names, two validated abstract values and latest-row versions
1–2, with a success-prefix cutoff rather than an API version cap. It checks
explicit-Deny preservation, no-overwrite refusal, successful version advance,
exact-key deletion/frame conditions and region/name read isolation. Five
specified mutations must fail the appropriate named property. Existing five
models and their controls remain mandatory; the combined inventory is six
positive models and ten negative controls (sixteen TLC cases).

See [the SSM contract and source/test correspondence](../docs/formal-ssm.md).
The native HTTP integration test uses distinct values and runtime versions
1–4; there is no version-history/selector, live-AWS differential, liveness or
Go refinement claim. This candidate incorporated main `55a1119` and PR #314 main `2d8a41a` by normal
merge after PR #310 merged; its SSM additions still require their own exact-HEAD
checks and PR acceptance. The original standalone/stacked commits are retained.
