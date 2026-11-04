# Secrets Manager: P0 contract and bounded increment

Status: design/source preparation only. Issue [266](https://github.com/susumutomita/TenkaCloudSimulator/issues/266)
is owned by `cfn_callback`, branch `feat/secrets-manager-native-20261104`, worktree
`/workspace/secrets-manager-native`, public base `6ffbd1881dad3589469fdf1809654b7483bd6ac4`.
Root checked the open Issue and absence of an existing service PR before assigning
this increment. Root owns the shared roster, Gateway, server, navigation,
OpenAPI, generated assets and public actions. No service is registered by this P0.

Source checkpoint: private redacted material, value-free metadata DTOs, immutable
SQL schema and transactional Create are prepared. Focused material/metadata/store
tests pass (4 tests, 1.183 seconds), including reflective formatting, same-token
retry, overwrite denial and late-failure rollback. This does not establish any
HTTP/API/SDK support. Other native operations, protocol, UI and model execution
remain unfinished.

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

## Reuse decision before code

Kumo pin: `ce37a8fb406ecb96b5619204f645c26302b651a5`, MIT, copyright2025 sivchari.
Full existing `third_party/KUMO-LICENSE.txt` and embedded original permission
notice must be preserved. Its `internal/service/secretsmanager/types.go` supplies
useful field/lifecycle reference; `storage.go` cannot be copied wholesale:
Create ignores the caller token; Put overwrites the version map entry for a
reused token and rotates labels even for a retry; repeated rotation can leave
several versions bearing AWSPREVIOUS; partial-ARN fallback can resolve a name
without physical-identity protection. No UpdateSecretVersionStage store method
was present in that pin. SQLite/immutability/label uniqueness/authority therefore
need explicit adaptation and detectors. Do not import its broad DTO containing
SecretString into metadata responses.

LocalStack public snapshot `8b9a79f05846835cf4dff63ab7eefdde9df83783` has Apache-2.0
licensing. Its `localstack-core/localstack/services/secretsmanager/provider.py`
delegates values/versions to moto and integrates KMS/replication. Review is
reference-only: no LocalStack/moto code, runtime, image or dependency is copied.
The public snapshot license is not permission for current proprietary images.
No upstream program was executed. P0 adds no dependency or borrowed runtime.

## Concrete acceptance and formal obligations

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
proofs. Root must register applicable models under307 and run both global90%
floors, full required CI/E2E/security/license gates before merge. Only gofmt,
compilation and the selected pure material/store tests have run; full Go,
SDK, TLC, browser, AWS, publish, release and deployment have not run.

Public console reference: the official flow is Store a new secret → Other type
of secret → key/value or Plaintext → name/description → rotation off → review →
Store. Use official Cloudscape components and these labels where implemented;
disable unsupported database/rotation/KMS choices honestly. Authenticated AWS
screens have not been observed, so no visual identity claim is justified.

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
