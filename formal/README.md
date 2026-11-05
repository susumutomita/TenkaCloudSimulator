# Finite state model

Current registry: 37 models / 245 literal-protected controls
(282 required executions). All main34 model objects, TLA/CFG bytes, properties
and210 controls are preserved. SakuraDiskMetadata/SakuraDiskAdmission retain
22 controls; SakuraStartupAdmission adds13 more. They cover bounded metadata,
fixed binding/issued-ID/peer frames, validated Actor mapping/admission and parsed
opt-in startup order. All three scoped baselines and35 controls passed separately;
the current integrated immutable282-case gate is still pending. Startup's first
TLC enumeration failure and its equivalent324-valuation correction are preserved.
No CLI string/regex refinement, DB lease, Icon resource, constructor atomicity,
physical storage/VM or Go implementation proof is implied. See [Sakura scope](../docs/sakura.md).
The prior native projection/routing model bounds and properties remain intact.
IAMManagedPolicies retains its four API-reachable seeds and two-request suffixes;
the earlier four-request exploration timed out and is not claimed.
Registration is source evidence, not itself evidence of successful execution. Dated receipts below
refer to their recorded inputs; later revisions require their own checks. These
finite models do not prove Go/SQL/HTTP/UI refinement or complete cloud compatibility.

Legacy integration sections below retain historical counts and status statements
for traceability. Their word “current” refers to the recorded integration, not
today's registry. The authoritative current inventory is the 37/245 registry
above; proof claims always require evidence for the exact revision.

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
has19 models/98 controls in main before this KMS restack. Individual finite runs and the final immutable integrated
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

All98 established sensitivity-control IDs (including36 earlier baseline controls) have literal per-model minimum sets. Real-filesystem tests remove each control individually and require rejection. This protects the checked inventory, not semantic completeness or a Go implementation proof.


## Bounded KMS key-state registration

KMSKeyState adds two fixed team/key namespaces sharing one home region, two request
regions, two contexts/blob slots and an abstract two-encryption counter ceiling.
Eight named controls exercise missing permission/team/region/state/context/key
binding guards and the counter ceiling. Create/SetEnabled/Encrypt/Decrypt are
atomic successful-event transitions; rejection frames, actual AES/nonce behavior,
policy/JSON parsing, metadata/list outputs, SQL write acknowledgment/rollback,
constructor wiring and host authentication remain native/integration obligations.
The existing ten model entries and their36 controls are preserved; the new model
and all four invariants are required by the loader and removal-negative fixtures.
The registry is canonical; the old standalone KMS fragment is preserved outside
production history as a handoff artifact and removed from the current tree.
The immutable91172d0 source passed12 baselines/48 required controls and34 exact
changed-blob declarations. Native/SDK/browser/whole-quality results are separate
in [KMS service evidence](../docs/kms.md); current hosted CI remains required.


## KMS protocol admission and policy snapshot

KMSProtocolAdmission independently normalizes KMS/other/missing target, service
hint and signing-scope metadata, credential-format/region consistency, four identity
lookup outcomes and one immutable permission snapshot. One optional live-policy
change after lookup must not replace the captured request permit. Five safety
properties plus TypeOK reject KMS-to-S3 fallback, authority inferred from wire
metadata,500-to403 outage masking, missing identity success and missing/conflicting
KMS target success. Rejected/error replies expose no abstract resource data or
mutation. Four controls remove signing intent, mask an outage, ignore the target
or read live policy instead of the snapshot. Type/parse errors are not acceptable
counterexamples. Actual91172d0 runs produced all four named invariant violations
with exit12 and transition traces; the atomic model baseline and eight controls
also passed their intended checks.
This is not raw-header/SigV4 parsing, local token authentication, real IAM/SQL,
other AWS branch authorization or a Go refinement proof. Atomic KMSKeyState and
its eight controls remain unchanged; protocol claims are not assigned to that
successful-event model. Real Gateway metadata/conflict cases, identity DB failure
frames/retry and a real body-read-barrier snapshot test provide separate native
bridge obligations. The registry is12/48, preserving all earlier models/invariants/controls.
Native bridge results are recorded separately in [KMS](../docs/kms.md).
Documentation-only children do not change model bytes; each final HEAD still
requires its own immutable input/applicability gate before publication.


Current main-preserving KMS restack:18 baselines/94 controls, retaining every main16/82 entry and adding only the two owned KMS models. The current merged source has not run TLC, native/browser or whole-profile acceptance. Earlier911/8ccc receipts do not establish current-head success.

## Current SQS console main integration

Mainfcf contributes18 baselines/94 mandatory controls, including the two
merged KMS models. SQSConsoleEpoch adds one baseline/four literal-protected
controls:19 baselines/98 controls,117 planned executions. Every existing
main model/property/bound/control/config and removal fixture is retained.
This source union has not run TLC; old99/base5c97 actual103 success is historical.
The SQS epoch model covers bounded accepted replies/private receipt custody,
not native receipt validity, authentication, React/DOM/thread execution or
Go/SQLite refinement. See [correspondence](../docs/formal-sqs-console.md).

## Current Secrets mutation/read integration

The current registry preserves those 26 main models and 149 controls and adds only
SecretsMutationRefresh (five properties/four named controls): 27 models, 153
controls, 180 planned executions. Every original model/config/property/control is
retained. The new property mapping separates acknowledged writes from read-only
metadata recovery; it does not prove React/HTTP/native idempotency or ambiguous
write outcomes. [Contract](../docs/formal-secrets-mutation-refresh.md). Full new-HEAD
immutable applicability/TLC and whole-quality/hosted CI remain required.


## Azure properties lifecycle candidate

Historical registration at `8e656eb` preserved 27 prior models/153 controls
and added AzurePropertiesEpoch (nine invariants/five controls), producing
28 baselines + 158 controls = 186 executions. The merged `ce622018` subsequently
passed the full 186-case gate and its main CI. Later Container Properties adds
AzurePropertiesTarget without changing those TLA/CFG files, bounds, properties
or controls. Use the current inventory above and exact-revision CI; older
receipts do not certify later source changes.

The model abstracts component lifecycle/request epochs, successful HEAD publication,
close/open clearing and locale-stable state; it does not prove JavaScript, native
HTTP parsing, Go/SQLite authorization, SDK compatibility or full Azure parity.
New immutable formal/application checks and hosted gates remain required.


## Azure Properties typed targets

The Container Properties extension preserves every maince622 model/config/control
and adds `AzurePropertiesTarget`: complete kind/parent/name target identity,
read-only HEAD route carriers and exact container/blob displayed field sets.
All seven invariants and six literal controls are mandatory. A pinned scoped run
passed the baseline (85 generated / 20 distinct states) and all six expected
semantic counterexamples. Registration plus this scoped result is not a full
193-execution or implementation/refinement proof.

[Source correspondence and limits](../docs/formal-azure-properties-target.md) explain
ghost capture provenance, the separately checked lifecycle/epoch assumptions and
concrete native/browser obligations.


## Independent REST route and proxy-event boundaries

RESTRouteMatching checks nine pre-tokenized template cases and eleven request
segment carriers in one evaluation step: literal constraints, exact or nonempty
greedy cardinality, literal score, actual captures (including final duplicate
name wins), and rejected-result shape. Its independent accepted-pair/value oracle
covers root, interior empty/dot/escaped data, invalid unnamed captures and
nonterminal greedy templates. It does not parse raw template/path bytes or prove
SplitPath, URL decoding, method/ANY selection, global best-route tie rules,
provider dispatch, deployment routing or Go refinement.

RESTProxyEventBoundary checks finite canonical header/query classes, representative
local credential/signature suppression, accepted application Bearer/multi-value
preservation, actual path versus resource template, read-only event results,
malformed-query refusal and symbolic raw/final-encoded-size admission. The two-unit
size bound is a threshold abstraction, not concrete256KiB enumeration. It does
not prove Go JSON allocation, total memory bounds, raw UTF8/query/header parsing,
case-fold/sorting, all prefix strings, host token authentication, snapshots of
mutable native inputs or implementation refinement. Native tests independently
cover those concrete contracts. The model emits no guest event on refusal.

Both exact source scopes are protected: path.go for route matching and event.go
for event projection. Generic RESTDeploymentSnapshot and ProviderPath properties
are not substituted for these contracts. All fourteen properties and sixteen
literal sensitivity IDs are mandatory. Each scoped negative actually exited12
with its exact named semantic invariant and a transition trace; both baselines
completed (100 and145 distinct states). These scoped results and unchanged
model/CFG relocation bytes do not certify later rewritten Go files or the new
full230-case/current-HEAD integration. CHECK_DEADLOCK FALSE permits the deliberate
one-evaluation horizon; there are no liveness claims.

The CFGs were normalized mechanically from the independently checked named-list
continuation into canonical INVARIANT lines. Ordered properties, constants and
behavior declarations remain identical; both TLA files remain byte-identical.
The two baselines and all16 controls were rerun after this CFG-byte change and
passed. The original strict CFG parser is unchanged; no named-continuation,
view/constraint/substitution exception was added.

## Explicit native wire-object projection

NativeWireProjection binds only internal/aws/wire_json_projection.go. Three fixed
ordered field positions, independent provider omission flags and abstract value
kinds zero/false/nil/empty/scalar/bad exercise successful complete projection,
exact omission/order/kind identity, omitted bad-value skipping and encoding-error
return with no published partial prefix. The provider chooses omission; this
model does not determine each caller's contract. It does not prove actual JSON
escaping/bytes/types, map-key order, timestamps, Go encoder behavior, memory,
XML/IAM, persistence or full cloud serialization. Existing IAM/API/KMS lifecycle
models do not thereby acquire serialization guarantees.

The baseline completed1,729 distinct states; six semantic controls each exited12
with the named expected invariant and a real transition trace. FirstBad identifies
the partial-prefix negative only, not a new native implementation. These scoped
results are independent model evidence, not the historical230-case gate or the current282-case gate
or a refinement proof. The entire original30/175 inventory, timeouts and tool pin
remain unchanged. All three exact native source scopes and all new properties
and control IDs are protected by the loader and failure tests.


## Secret version metadata listing

SecretVersionListing adds nine invariants and thirteen literal-protected semantic
controls, preserving all prior33 registry objects and197 controls and their
TLA/CFG bytes. The combined inventory is34 baselines +210 controls =244 required
executions. Registration and earlier independent scoped results are not a
successful immutable244-case integration gate or hosted-CI result.

Four-transition suffixes cover four ordered symbolic version keys, two database
authorities/regions, two physical generations and page size one. List permission
is independent of value-read permission. The finite query resolves the parent
before saved-ARN authorization; only an admitted request scans version/stage
metadata and validates its continuation. Cursor predicates bind database key,
region, domain, physical generation, IncludeDeprecated and expiry. Filtering
precedes the page limit, resume positions advance strictly, and reads preserve
version/stage state. Add/promote/delete/recreate/reopen expose live keyset changes;
this is not a stable snapshot or a proof of eventual termination.

Correspondence is limited to ListSecretVersionIDs metadata enumeration/cursor
logic in `secrets_manager_versions.go` and the exact added operation dispatch in
`secret_http.go`; the other seven operations do not acquire new guarantees. Native
store and HTTP tests named in source_scope separately exercise denied frames,
physical recreation, database reopen, malformed requests and fault recovery.
The public reference is [AWS ListSecretVersionIds](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_ListSecretVersionIds.html),
reviewed2026-11-05. No server capability or generic Gateway parsing guarantee is
inferred from this state projection.

The token validity/key/expiry fields are abstractions, not HMAC, time or encoding
proofs. SQL isolation/commit faults, concrete IAM/host authentication, JSON/UTF8/ARN
parsing, actual timestamp/UUID order, variable page sizes, secret-value redaction,
retention, UI and Go refinement remain native/integration obligations. All thirteen
controls must fail their exact named semantic invariant with a behavioral trace;
syntax/type/config failures or timeouts are not accepted counterexamples.
