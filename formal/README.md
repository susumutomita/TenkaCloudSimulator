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

The executable inventory is now `formal/models.json`; all registered models, including the original five,
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
models and their controls remain mandatory. SSM adds one baseline and five
negative controls; the complete combined inventory is stated below.

See [the SSM contract and source/test correspondence](../docs/formal-ssm.md).
The native HTTP integration test uses distinct values and runtime versions
1–4; there is no version-history/selector, live-AWS differential, liveness or
Go refinement claim. New candidate execution and hosted CI must be checked separately; model checking is not Go refinement.


## Startup token visibility

`StartupTokenVisibility.tla` models one fixed configured token file, two abstract
token values, three initial file states and at most two historical emissions.
It checks existing-file identity, private creation, invalid-file refusal and
emission-time auth/explicit-show/TTY/CI-presence guards. Removing the CI or TTY
guard must violate `HistoryVisibility`; overwriting an existing token must
violate `FileIdentityFrozen`. Original model/config and exact mutation anchors
are preserved.

`source_scope` binds CLI/launcher code and its real PTY/native tests. It does not
prove terminal syscalls, OS ACLs, entropy, crash safety, build freshness, process
shutdown or all Go behavior. The metadata-only `go.mod` annotation change uses
the separate narrow recognizer and is not bound to token-state invariants.

## Browser session revocation

BrowserSessions is required alongside StartupTokenVisibility, SSMParameterState and the original five models. Its two clients, three fresh browser
credentials, one operator and one participant parent, clock0..3 and TTL2/1 bound
login→cookie use→logout/replay→parent expiry/invalidation. Five named mutants
retain a revoked session, ignore parent validity, accept raw operator cookies,
clear other sessions or remove SDK authority; each must violate its declared
invariant. Existing five models and protected invariants remain unchanged.

The registry is the only executable inventory; there is no checked-in companion
registration fragment. access.go is the runtime seam; main.jsx is the account
menu's caller, and access_browser_test.go/console_logout.py hold separate native
and actual browser assertions. Listing those paths binds source review, not Go
refinement or a UI/TLS/cryptography proof. See
[session contract](../docs/console-sessions.md) for the independent checks.

Startup, SSM and Browser inventory/property removal are rejected. Source
integration is not a new-head TLC or runtime execution result.

## SQS lifecycle and physical storage budget

SQSLifecycle bounds two valid queue scopes, one queue/message lifetime per scope,
two deliveries and a coarse clock; its eleven mutations cover parent lifetime,
IAM/scope, delay/retention eligibility, atomic failure and receipt rotation.
SQSStorageBudget separately checks two trusted queue scopes, up to three legacy
rows plus one fresh ID, abstract payload weights 1–2, caps of two rows/three
units and preserved legacy over-cap state. Its seven mutations exercise row/byte
admission, expired foreign charging, scoped purge, failure rollback, live-row
preservation and available legacy maintenance. The 729 unique-ID initial row
sets use an equivalent seed-assignment product, avoiding enumeration of every
subset of the larger record universe. Accounting invariants apply to accepted
admissions; failures independently preserve the complete prior physical state.

The earlier BrowserSessions/SQS integration contributed ten baselines and thirty-six mandatory mutants; the current union is listed below.
All original, SSM and Startup required properties remain protected. Model/source
integration must pass the immutable-head gate separately for each new head. These finite abstractions do not
prove Go/SQLite refinement, actual byte/JSON/Unicode arithmetic, filesystem size,
concurrency scheduling, persistence/crashes, distributed delivery or liveness.
See [SQS limits and concrete test boundaries](../docs/sqs.md).

BrowserSessions, SQSLifecycle and SQSStorageBudget remain mandatory in the larger current registry. A source integration is not a new-head TLC or Go execution result.


## Multi-cloud storage integration models

The registry adds ProviderActor (7 properties/6 sensitivity controls),
GCPStorageState (10 properties/8 controls), and AzureBlobState (10 properties/11
controls), plus ProjectAuthority (10 properties/6 controls), BlobRange (11 properties/8 controls), and ProviderPath (finite carrier properties/7 controls), retaining all existing models/controls. The current source registry
has16 models/82 controls. Individual finite runs and the final immutable integrated
run are recorded separately; registration alone is not a successful TLC run.

ProviderActor abstracts post-authentication emission, selected identity and
participant binding. GCPStorageState abstracts bounded state/checksum-independent
generation and authorization decisions; AzureBlobState abstracts owner/budget,
transaction frames, current data and conditional Put. New factory initialization,
URI parsing, HTTP Range, UI/SDK behavior, credential cryptography and SQL behavior
require native/integration tests and are not automatically covered by leaf models.
ProjectAuthority is registered as a source candidate for fixed GCP project-owner bindings, refusal frames and actual-owner decisions. Its execution is recorded separately; it does not prove the SQL implementation, authentication extractor, growing project roster, Azure RBAC or API success/liveness.

Azure's finite owner function uses the exact `Owners <- AccountOwners` CFG binding.
The parser permits only that binding for AzureBlobState, requires its presence,
and rejects duplicate/other bindings, invariant substitution, views and constraints.
Registry metadata cannot grant a general substitution exception. The original
model/config bytes and finite state space are preserved.


BlobRange checks normalized finite response selection (body length0–3, starts/ends0–4,
checksum limit2 abstract units), authorization/condition gates, bounds/refusals,
selected bytes and persistent frame. It does not parse HTTP headers or prove real
MD5 correctness/Go slicing/SQL behavior. Its initial lexical parse failure was
retained; the corrected same-domain model explored9,600 distinct states and all8
specified mutants reached their intended invariant counterexamples.

ProviderPath checks13 finite decoded/escaped carrier pairs, known/unknown/encoded
provider modes and opaque contexts. It preserves carrier pairs and contexts; it
does not claim that all distinct URI representations must name different logical
objects, parse Go percent encoding, or prove the outer server dispatch ordering.
Its independent baseline explored13,104 states and all7 specified mutants failed
at their intended invariants. Final immutable integrated checking is separate.

Typed Azure listing XML is a state-stuttering projection of already-authorized,
bounded page data. The state models' frame properties apply only to persistence;
XML order, escaping, disclosure, actual SDK parsing and SQL initialization are not
proved by this association. Independent serializer byte-goldens and native HTTP
checks cover the wire format separately.
