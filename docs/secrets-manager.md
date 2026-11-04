# Secrets Manager: bounded local subset

Status: **local source candidate**, Issue [266](https://github.com/susumutomita/TenkaCloudSimulator/issues/266).
The preserved implementation is `96259aa423f105f83c276b14eca57e0af0d626c2`.
It was normally merged with public main `b61d0299a2139661f01ef29ae5826e4c070cc7bf`
in local integration commit `31f740a`; no rebase, force push or publication was
performed. This is not a complete AWS service, public release or Challenge claim.

The seven store operations, private native JSON adapter, value-free metadata,
version immutability, label transitions and dummy-value redaction source are
preserved. Shared-file merge conflicts retain current main. As a result, this
integration tree **does not initialize Secrets storage in the CLI server**, add
its UI to the service menu, embed its console bundle, register its OpenAPI
operations/E2E helpers, or register its three finite models. A direct native
Gateway with no Secrets store fails closed; source files are not an enabled
service. Root owns those integration changes and their final validation.

Exact prior shared deltas and all three merge blobs were archived outside the
repository for review; generated bundles must be regenerated from the final
combined source instead of restoring an old bundle over current main. The old
worktree and commit remain intact. No new SDK/dependency installation, real AWS
operation, release, merge or issue comment is part of this phase.

The saved handoff reports earlier selected native tests, actual two-team
CLI/boto3/browser checks and eight registered baselines with nineteen intended
counterexamples at the prior candidate. Those are **historical reports**, not
current-HEAD test or CI results. This integration phase ran all sixteen
`TestSecret` native tests normally (1.527s) and under race (10.907s), and
`go vet ./internal/aws` passed. It has not run CLI/SDK/browser, full repository
coverage or TLC checks. The selected native tests do not establish server
integration or a whole-repository coverage pass.

Five real SQLite `RAISE(IGNORE)` controls failed on the preserved source:
initial current-label insert, previous-label insert, new current-label insert,
metadata timestamp update and parent deletion incorrectly acknowledged success.
The fix checks directly affected rows before transaction commit: inserts and
metadata updates require one; deletion requires one for an existing physical
ARN or zero for an already absent ARN. All five now reject the ignored write,
roll back all five Secrets tables, and allow clean corrected retries.
Same-token retries and repeated deletion remain idempotent. This is a local
persistence failure-atomicity correction, not an HTTP exploit claim.

## Scope and explicit storage boundary

The proposed first increment is seven native operations over **dummy SecretString
values**. Native Go/SQLite state, SDK, console and later integrations must share
one backend. Runtime must not require Python, Node, Docker, AWS or network KMS.

KMS117 is not public main and is not an implicit dependency. The initial contract
rejects every supplied `KmsKeyId`, including aliases and the AWS managed-key name.
It does **not** perform AWS envelope encryption. Proposed local values are
plaintext in the private simulator database, with an explicit dummy-only UI and
documentation boundary and `X-Simulator-Encryption: none` response header.
No UI key selector may display `aws/secretsmanager` as an operative encryption
choice. Root accepted this training-only boundary; real local encryption/key
custody would require a separately agreed dependency and truthful UI behavior.

## Verified wire contract

API version: **2017-10-17**. Root POST, `application/x-amz-json-1.1`, exact target
`secretsmanager.<Operation>`, signing service `secretsmanager`, simulator dummy
credentials only. The existing local access-token middleware remains mandatory.
The official Welcome page contains Query/XML/Organizations boilerplate; the
individual API HTTP examples and pinned AWS SDK Go client instead establish JSON
1.1. Do not route Secrets Manager requests through the CloudFormation Query path.

Primary SDK pin: `aws/aws-sdk-go-v2` commit
`71605dd1c260cb5fd015ad142a714dc03479b49d`,
`service/secretsmanager/api_client.go`: `ServiceAPIVersion` is 2017-10-17 and the
client selects `awsjson.New11(schemas.Secretsmanager)`.

| Operation | First request subset | Result and authority |
| --- | --- | --- |
| CreateSecret | Name, optional Description, required SecretString and ClientRequestToken | Stable ARN/Name/VersionId; exact generated secret ARN |
| DescribeSecret | SecretId: exact name or full local ARN | Metadata and labeled version IDs only; resolved exact ARN |
| ListSecrets | MaxResults 1–100, NextToken, SortBy created-date/name, SortOrder asc/desc | Metadata only; `secretsmanager:ListSecrets` on `*` |
| PutSecretValue | SecretId, SecretString, ClientRequestToken; VersionStages absent or exactly `[AWSCURRENT]` | Immutable new version/current-label transition; resolved exact ARN |
| GetSecretValue | SecretId, optional VersionId and/or VersionStage AWSCURRENT/AWSPREVIOUS | Only this authorized operation returns SecretString; resolved exact ARN |
| UpdateSecretVersionStage | SecretId, VersionStage=AWSCURRENT, MoveToVersionId, matching RemoveFromVersionId when moving | Atomic rollback/promotion; resolved exact ARN |
| DeleteSecret | **Full local ARN**, ForceDeleteWithoutRecovery=true | Atomic immediate local deletion and idempotent absent-ARN success; request ARN |

These are narrower than AWS: metadata-only creation, SecretBinary, custom labels,
AWSPENDING/rotation, binary encodings, partial/cross-account ARNs, tags, policies,
replicas, generated passwords, recovery windows, RestoreSecret, asynchronous
deletion, version garbage collection and CloudFormation are unsupported. Unknown
fields and operations must fail explicitly. Name-based deletion is deliberately
excluded initially so absence never requires inventing a resource ARN for IAM.
The console deletes the full ARN it actually described.

Secret names use the AWS 1–512 ASCII letter/digit and `/_+=.@-` set. A name ending
in a hyphen and six characters is not itself invalid; the simulator does not use
partial-ARN matching. ARN format is
`arn:aws:secretsmanager:<region>:000000000000:secret:<name>-<six random characters>`.
Only the simulator's four regions/current account are supported. Recreating a
name must issue a never-reused ARN; retired ARN reservations remain durable.

SecretString is 1–65536 UTF-8 bytes. Validate exact JSON types, null/duplicate keys,
payload bounds and Unicode scalar validity rather than silently replacing an
unpaired surrogate inside a value. JSON transport can be capped at 512 KiB to
allow a fully escaped valid value. ClientRequestToken/VersionId is 32–64
characters; SDKs auto-generate the token, raw HTTP clients must supply one.

The installed development boto3 model predates the current optional SortBy field.
The actual SDK test therefore specifies SortOrder and uses the documented default
CreatedDate sort key. Name sorting is covered by native store/private-protocol
tests; the pinned current AWS Go SDK reference includes SortBy. No dependency
install, test skip or relaxed backend behavior was used to conceal that tool gap.

Create retries with an existing token and identical value must be no-ops;
different data for a known version fails. Creating an already named secret with a
fresh token is conservatively rejected; new versions use PutSecretValue. Put
retries never move staging labels, change timestamps or overwrite version data.
On a new current version, move the old current version to AWSPREVIOUS and remove
that label from its old owner. Keep unlabeled versions retrievable by VersionId;
Describe/List expose only versions with labels. Get with both ID and stage must
require them to refer to the same version. Promotion requires the current owner
to match RemoveFromVersionId; a true same-owner request is a no-op. Initial scope
does not remove AWSCURRENT without a replacement or assign AWSPREVIOUS directly.

Force deletion of an already absent full ARN succeeds without fabricated
resource creation; return only supplied/known identifier metadata. An old ARN
must never resolve by its former name to a new secret. RecoveryWindowInDays,
omitted force, force=false and name-only deletion fail without mutation.
The local immediate deletion is an explicit deviation from AWS background
deletion. Local reads/listings are immediately consistent; AWS listings can lag.
AWS documents CreatedDate as the default sort key but does not specify a default
direction in the checked pages. The simulator will document ascending as its
local default and tests will specify direction when ordering matters.

## State, IAM and redaction contract

The protected value channel is `SecretString`/`secretMaterial`. This is not
automatic data-loss prevention for arbitrary text: names and descriptions are
metadata, so callers must not put secret values there. Redaction does not claim
encryption at rest, memory zeroization, SQLite free-page erasure or protection
from a host account that can read the private database.

Provider-owned tables are proposed as:

- `aws_secret_arns`: persistent issued/retired ARN reservations, not values.
- `aws_secrets`: region/name/ARN/generation/description/timestamps, no value.
- `aws_secret_versions`: immutable version ID, value and creation timestamp,
  foreign-keyed to the physical secret ARN.
- `aws_secret_stages`: one owner per `(region, ARN, stage)`, foreign-keyed to an
  actual version; initial stage set is AWSCURRENT/AWSPREVIOUS.
- A per-database cursor key for scope-bound durable listing tokens.

All mutation/lookup authorization must occur against the exact physical ARN in
the same short transaction as the operation. **The authorizer must be pure.**
Gateway loads and validates an immutable identity-policy snapshot **before**
entering the store. Inside the transaction, the callback uses `core.Evaluate`
and writes the IAM decision header only; it must never call DecisionContext or
perform another database lookup on the single connection. Policy/DB failures
must remain sanitized 500 errors, not be collapsed into AccessDenied.

For a name that does not exist, resolution currently returns generic
ResourceNotFound without constructing a prospective ARN or reading a value.
Existence/error ordering can therefore differ from AWS authorization behavior;
this is a bounded compatibility difference, not a claim of full IAM error
equivalence. Full ARN lookup never falls back to the name.

Resolve full ARNs exactly, including region/account/suffix. Resolve names only in
the selected region. Authorize before reading any value. Values are separate
from metadata DTOs; list/describe/create/put/stage/delete responses, errors,
headers, logs, traces and artifacts must never copy SecretString. The sole
GetSecretValue response uses `Cache-Control: no-store`; the console reveals it
only after an explicit retrieval action, clears it on navigation/identity/region
change and never persists it in browser storage. Creating/updating clears the
draft after success. Default formatting/JSON encoding of the private material
type must redact; authorized value serialization must be an explicit separate
code path. Never include raw request fields in errors or SQL failure messages.

Proposed simulator quotas: 100 active secrets, 100 retained versions per secret,
10000 total versions, 64 MiB retained value bytes, 10000 lifetime ARN reservations
per database. These are local bounds, not AWS account quotas. Idempotent retries
must still work at quota. Force deletion cascades versions/stages atomically but
preserves ARN reservations. SQL/cancellation/quota failures leave names,
versions, labels and values unchanged. Team hosts allocate separate processes,
databases and token authorities; a selected principal is not tenant identity.

## Upstream design references; no engine adoption

Kumo pin: `ce37a8fb406ecb96b5619204f645c26302b651a5`, MIT, copyright2025 sivchari.
Full existing `third_party/KUMO-LICENSE.txt` and embedded original permission
notice must be preserved. Its `internal/service/secretsmanager/types.go` supplies
useful field/lifecycle reference; `storage.go` cannot be copied wholesale:
Create ignores the caller token; Put overwrites the version map entry for a
reused token and rotates labels even for a retry; repeated rotation can leave
several versions bearing AWSPREVIOUS; partial-ARN fallback can resolve a name
without physical-identity protection. No UpdateSecretVersionStage store method
was present in that pin. SQLite/immutability/label uniqueness/authority therefore
are implemented independently here with explicit detectors. No Kumo engine, fork,
submodule, service implementation or dependency is imported by this candidate.
Do not import its broad DTO containing
SecretString into metadata responses.

LocalStack public snapshot `8b9a79f05846835cf4dff63ab7eefdde9df83783` has Apache-2.0
licensing. Its `localstack-core/localstack/services/secretsmanager/provider.py`
delegates values/versions to moto and integrates KMS/replication. Review is
reference-only: no LocalStack/moto code, runtime, image or dependency is copied.
The public snapshot license is not permission for current proprietary images.
No upstream program was executed. P0 adds no dependency or borrowed runtime.

## Acceptance obligations and archived predecessor evidence

Before integration, native tests must demonstrate create→describe/list with no
value→Put B→Get current B/previous A→promote A→force delete, including same-token
same-value no-op, same-token different-value rejection and three successive
versions with exactly one current/previous owner. Verify both ID/stage selectors,
metadata-only rejected input, 64 KiB/Unicode boundaries, unsupported fields,
idempotency at quota, database trigger failures, canceled transactions and exact
rollback of values plus labels. Secret sentinels must be absent from all other
responses, errors, formatting, stdout/stderr and metadata artifacts.

Real binary SDK tests use two simultaneous authenticated teams, participant
policies and separate persisted files. Same names/tokens across teams must not
share values or cursors. Restart preserves ARN/version/labels. Denial blocks Get
and mutation; old-ARN permissions cannot access a recreated name. The console
must execute real API calls, assert native SDK state after Enter and cancellation,
keep values out of screenshots, check stale reveal responses after navigation,
PC/390px reflow, keyboard and canonical axe, with zero external requests.

A new bounded model should cover immutable version records, unique labels,
current→previous moves, retry no-op/conflict, exact-generation authority and
atomic deletion. Mutants must expose overwrite-on-retry, duplicate previous-label
owners, stale-ARN adoption and orphaned versions/stages. A separate view/reveal
epoch model is needed if async response acceptance can retain a secret after
navigation. These are finite abstractions, not encryption/SQL/React refinement
proofs. Root must register applicable models and run both global 90% floors,
required CI/E2E/security/license gates before any later merge. Current integration
full integration checks remain unexecuted. The following UI and model details describe saved source
and predecessor reports; they are not enabled or verified on this integration
HEAD. No actual AWS, publication, release or deployment was performed.

Public console reference: the official flow is Store a new secret → Other type
of secret → key/value or Plaintext → name/description → rotation off → review →
Store. Use official Cloudscape components and these labels where implemented;
disable unsupported database/rotation/KMS choices honestly. Authenticated AWS
screens have not been observed, so no visual identity claim is justified.

In the archived predecessor candidate, the console was composed through the normal service menu and region/principal/
navigation lifetime key, with a canonical pinned Cloudscape bundle in the Go
binary. Prior handoff reports record actual browser checks against that prior
binary; this integration retains main assets and has not rerun them. It never retrieves a
value while listing or describing metadata. Explicit Retrieve secret value
requests AWSCURRENT; Hide, navigation, opening a form and unmount invalidate
pending results and clear the displayed value. An accepted reply must carry the
selected ARN. Drafts remain only in component memory, survive a failed write
with the same request token for an idempotent retry, and clear on successful
write/cancel/navigation. This does not promise memory zeroization or protection
from a browser extension/devtools. Editing a new version starts with an empty
draft rather than implicitly loading the saved value. The clearly labeled
simulator rollback helper uses UpdateSecretVersionStage; the public AWS guide
describes CLI label moves, so it is not claimed as the actual AWS console flow.

`SecretsRevealEpoch` separately models explicit retrieve, Hide/navigation,
unmount, response ARN binding and historical acceptance context with two
resources and two request/navigation epochs. `SecretsVersionLifecycle` also
checks that every active resource has exactly one current label. The source
and nine intended mutants have run with the pinned TLC JAR, one worker and a
512MiB Java heap. Lifecycle normal checks463,761 distinct states (4,261,018
generated), and reveal normal checks60 distinct states (164 generated). All nine
mutants produce their intended invariant violation: conflicting-version
overwrite, missing delete cascade, duplicate previous label, missing Create
permission, missing Read team/generation binding, missing reply epoch/ARN guard
and retained visible value after unmount. An initial mutant had a shadowed
variable semantic error; that run was rejected, corrected and rerun. Parse
errors/timeouts are never counted as counterexamples. The unchanged two model sources are now registered alongside the new channel
model in the predecessor history. The latest saved handoff reports all eight
registered baselines and nineteen counterexamples passing at `96259aa`; this
phase has not independently rerun that record. Current main model registration
was retained, so these three Secrets models require a new integration and exact
HEAD gate. These predecessor checks establish
only the documented finite model properties. `test/e2e/secrets_manager_console.py` passed the actual
browser/SDK wizard, metadata-only reads, genuine delayed Get rejection after
Hide and list navigation, explicit reveal/edit, version rollback and keyboard
deletion with native SDK state assertions. A separately authenticated participant
can list/describe metadata but receives a native403 on Retrieve and never sees
the value. PC1440 and mobile390 reflow pass, including the mobile pair-entry
form; all five canonical axe screens have zero violations, with zero page
errors/external requests. The checker initially found unnamed notification
dismiss buttons; explicit accessible names fixed the product issue. Screenshots are captured only after values
are hidden, and no browser trace/HAR with dummy or user values is produced.

### Disclosure-channel correspondence and limits

The new `SecretsDisclosureChannels` model separates four operation contexts and
four sinks, with at most two emissions. `AcquireRaw` represents the authorized
Get **call site**, not a permission check inside `secretMaterial.plaintext()`.
That accessor returns its bytes; the sole production plaintext writer in
`secret_http.go` runs only after `SecretStore.Get` resolves the saved ARN,
authorizes it and succeeds. `RawRequiresAuthorizedGet` and
`RawEmissionRequiresGet` describe this call-site obligation.
`MetadataLogErrorRedacted` and `DeniedNoRaw` describe private DTO/formatter/log/
error output. Controls remove raw-access operation or permission guards, leak a
private sink, retain raw state across contexts, or bypass the Get writer guard.
The latest predecessor handoff reports these controls included in the eight
baseline/nineteen-counterexample pass at `96259aa`. Current integration TLC has
not run; source/anchor validation is not counterexample evidence.

| Channel | Concrete path | Required actual evidence |
|---|---|---|
| Private material / generic formatting | `secret_material.go` String/GoString/LogValue/MarshalJSON and private pointer indirection | `TestSecretMaterialGenericFormattingAndLoggingCannotExposeValues`: exported/private holders, JSON, slog and an unprotected negative control |
| Metadata DTO | `SecretMetadata`, `SecretDescription`, `SecretListEntry` omit value fields | `TestSecretMetadataUsesValueFreeNativeWireShapes` and actual Describe/List responses |
| Authorized Get writer | `secret_http.go` Get success explicitly calls plaintext after native Get | `TestSecretHTTPSevenOperationLifecycleRevealsOnlyAuthorizedGet` and participant IAM native/browser checks |
| Denied Get before value access | `secret_values.go` saved-ARN resolve/pure authorize before querying values | `TestSecretGetAuthorizesSavedARNBeforeReadingAnyValue` uses a storage fault to distinguish denial from value access |
| Sanitized errors | `secretHTTPError` fixed service errors and generic infrastructure failure | `TestSecretHTTPRejectsUnsupportedOrAmbiguousWritesWithoutDisclosure` and corrupted-policy500 test |

Proposed model source scopes describe these specific confidentiality/authority
boundaries. They are not registered in this current integration yet. They do
not prove Go reflection, JSON encoding, SQL, encryption, browser memory or
whole-program noninterference. Unknown implementation paths still fail the
applicability checker; no prose or directory-wide exemption was added. Stateful
UI is bound to its reveal model rather than classified as presentation. Shared
dispatch/server bindings concern only Secrets composition, preserving the other
native APIs.

Primary references checked 2026-11-04; API version 2017-10-17. No real AWS
comparison or authenticated AWS console observation was performed.

Sources: [CreateSecret](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_CreateSecret.html),
[PutSecretValue](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_PutSecretValue.html),
[GetSecretValue](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_GetSecretValue.html),
[DescribeSecret](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_DescribeSecret.html),
[ListSecrets](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_ListSecrets.html),
[UpdateSecretVersionStage](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_UpdateSecretVersionStage.html),
[DeleteSecret](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_DeleteSecret.html),
[IAM resource/action reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_secretsmanager.html),
[console creation guide](https://docs.aws.amazon.com/secretsmanager/latest/userguide/create_secret.html),
[pinned SDK protocol](https://github.com/aws/aws-sdk-go-v2/blob/71605dd1c260cb5fd015ad142a714dc03479b49d/service/secretsmanager/api_client.go),
[pinned Kumo store](https://github.com/sivchari/kumo/blob/ce37a8fb406ecb96b5619204f645c26302b651a5/internal/service/secretsmanager/storage.go),
[pinned LocalStack provider](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/localstack-core/localstack/services/secretsmanager/provider.py).
