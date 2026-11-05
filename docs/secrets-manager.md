# Secrets Manager: local training subset

> Main refresh candidate: current mainfcf8 (KMS, S3 regression and login improvements merged) plus Secrets18aa is source-prepared only. Every validation receipt below describes the original isolated62ba/runtime source; this new21-model/108-control,29-case integrated candidate has not yet run Go, SDK, UI, formal, coverage or hosted CI.

Seven native Secrets Manager operations, the Cloudscape console, SDK clients and
the embedded API reference use the same SQLite state. This is an independent,
bounded training implementation, not a complete AWS service or a hosted release.

**Use dummy values only. SecretString is plaintext in the private database.**
There is no KMS/envelope encryption, HSM, secure-erasure or production-vault
claim. Every response carries `X-Simulator-Encryption: none`. Names/descriptions
are ordinary metadata, not protected value fields; do not put secrets in them.

## Connect and use the API

Start the normal authenticated local binary as described in [startup](start.md),
sign in with its local access token, then choose **Services → Secrets Manager**.
Hosts allocate a separate process, SQLite file and token authority for each team.
A selected IAM principal is exercise context, not authentication or tenant
isolation. Participant tokens cannot select the administrator.

The native endpoint is root `POST /`, content type
`application/x-amz-json-1.1`, with exact
`X-Amz-Target: secretsmanager.<Operation>`. API version is **2017-10-17** and
SigV4 signing service is `secretsmanager`. SDK wire credentials must be dummy
`TCSIM...` values; they do not authenticate the caller or validate a real AWS
signature. The outer local access-token middleware remains required.

The embedded reference at `/_simulator/docs/` contains seven named request and
response examples under the existing shared-root operation. They are **static
examples**, not a view of executed state. The request examples have been run
against the actual binary and checked against SDK state. A reproducible
[two-team boto3 lifecycle](../test/e2e/secrets_manager.py) demonstrates endpoint,
dummy credentials, token injection and exact loopback-origin transport guards;
[the example runner](../test/e2e/secrets_manager_docs.py) executes the served
OpenAPI requests. Host Python is development/test tooling, not a simulator
runtime dependency.

| Operation | Accepted request subset | Result / IAM resource |
| --- | --- | --- |
| CreateSecret | Name, optional Description, SecretString, ClientRequestToken | ARN/Name/VersionId; exact generated ARN |
| DescribeSecret | SecretId: exact name or full local ARN | Value-free metadata and labeled version IDs; resolved ARN |
| ListSecrets | MaxResults 1–100, NextToken, SortBy created-date/name, SortOrder asc/desc | Value-free metadata; `secretsmanager:ListSecrets` on `*` |
| PutSecretValue | SecretId, SecretString, ClientRequestToken; VersionStages absent or `[AWSCURRENT]` | New immutable version/current transition; resolved ARN |
| GetSecretValue | SecretId, optional VersionId and/or VersionStage AWSCURRENT/AWSPREVIOUS | The only operation returning SecretString; resolved ARN |
| UpdateSecretVersionStage | SecretId, VersionStage=AWSCURRENT, MoveToVersionId; matching RemoveFromVersionId when moving | Current/previous promotion; resolved ARN |
| DeleteSecret | Full local ARN, ForceDeleteWithoutRecovery=true | Immediate local deletion; request ARN, including idempotent absence |

Unknown operations return501; malformed/unsupported fields and incompatible
selectors return400; IAM refusal returns403; storage/policy failures return a
sanitized500. Missing/expired/foreign host credentials return401 at the outer
access layer. Duplicate protocol/authority headers, duplicate JSON keys, null or
wrong field types, invalid Unicode scalars and oversized bodies are rejected.
Signed Secrets requests with missing/foreign targets do not fall through to S3.

## Values, identity, versions and limits

Names use1–512 ASCII letters/digits and `/_+=.@-`. Physical ARNs are
`arn:aws:secretsmanager:<region>:000000000000:secret:<name>-<six random characters>`.
The four simulator regions are supported. Names resolve only in the selected
region; full ARNs match account, region and suffix exactly, with no partial-ARN
or former-name fallback. Recreating a deleted name obtains a never-reused ARN.

SecretString is1–65536 UTF-8 bytes and transport JSON is capped at512KiB.
ClientRequestToken/VersionId is32–64 characters; SDKs can generate tokens, while
raw HTTP clients must supply one for creation/value updates. Description is at
most2048 characters. A name ending in hyphen-plus-six characters is permitted;
matching does not rely on partial ARNs.

A known token with identical bytes is an immutable retry/no-op; different bytes
for that token fail. Create on an existing name with a fresh token fails: use
PutSecretValue for another version. New current versions move the previous
current to AWSPREVIOUS and remove that label from its old owner. Unlabeled
retained versions remain readable by VersionId; Describe/List show only labeled
versions. Supplying both ID and stage requires them to select the same version.
Promotion checks the current label's owner; same-owner promotion is a no-op.
Retries do not rewrite values, labels or timestamps.

Local limits per database are100 active secrets,100 versions per secret,
10000 total retained versions,64MiB retained value bytes and10000 lifetime ARN
reservations. These are local bounds, not AWS account quotas or limits on the
whole SQLite file/process memory. Valid immutable retries still work at quota.
Force deletion atomically cascades versions/stages but retains ARN reservations.
Deleting an absent old ARN does not affect a recreated name. Deletion removes
rows, not guaranteed copies in SQLite free pages or process/browser memory.

Listing tokens are HMAC-bound to database, region, sort settings and position,
expire after24 hours and survive a normal database reopen. Default local order
is ascending created-date, with ARN tie-breaking. Invalid/tampered/expired or
foreign-scope tokens fail without mutation. The installed test boto3 model lacks
optional SortBy; SDK tests use explicit SortOrder/default CreatedDate, while
native tests exercise name sorting. No dependency installation or backend
relaxation hides that tooling limitation.

## Persistence, authorization and disclosure

The five provider tables separate metadata from values:

- `aws_secret_settings`: private persistent cursor key.
- `aws_secret_arns`: issued/retired physical identity reservations, no values.
- `aws_secrets`: region/name/ARN/description/timestamps, no values.
- `aws_secret_versions`: immutable value/version rows with parent foreign keys.
- `aws_secret_stages`: unique current/previous ownership referencing real versions.

The gateway loads/validates an immutable policy snapshot before opening the
single-connection store transaction. Its authorizer is pure: it evaluates that
snapshot against the actual resolved ARN, never performs a second DB lookup or
external I/O inside the transaction, and authorizes before reading a value.
Unsupported IAM policy fields remain rejected; this is the existing limited IAM
evaluator, not full AWS IAM. Missing-name lookup returns ResourceNotFound before
inventing an ARN, so existence/error ordering may differ from AWS.

Metadata DTOs never contain SecretString. Generic formatting/JSON/slog of private
material redacts it; only the successful authorized Get writer explicitly emits
raw bytes. Errors, headers and metadata do not copy request values. A late
staging-metadata failure after a value read returns no partial material or HTTP
value; a same-token retry does not acknowledge incomplete metadata. The successful
Get response uses `Cache-Control: no-store`.

Mutation statements verify expected affected-row counts before commit. Real
SQLite `RAISE(IGNORE)`, ABORT and deferred-FK commit failures preserve all five
private tables and allow an exact retry after repair. Tests also cover unavailable
tables, corrupt identity/timestamps, missing-current labels, policy DB outages,
canceled requests, quota refusal and restart. This is persistence failure
atomicity, not an HTTP exploit claim or a SQL crash/refinement proof.

## Console flow and public AWS comparison

The console lists/describes metadata without implicit retrieval. Retrieve secret
value is explicit; Hide, navigation, identity/region changes, forms and unmount
clear visible values and invalidate late replies. Replies must match the selected
ARN. Drafts stay in component memory, retain an idempotency token across a failed
write and clear after success/cancel/navigation. Editing starts with an empty
draft. No browser-storage or memory-zeroization guarantee is implied.

Public guides were checked2026-11-04. No authenticated AWS screen or real AWS
resource was accessed, so pixel-for-pixel identity is not claimed.

| Flow | Public AWS guide | Local implemented behavior / difference |
| --- | --- | --- |
| Create | [Secret type → values/encryption → name → rotation → review](https://docs.aws.amazon.com/secretsmanager/latest/userguide/create_secret.html) | Same basic wizard order; other-type dummy text/key-value only. KMS/database/partner/rotation choices unavailable. Store opens details rather than the documented list. |
| Update | [Retrieve then Edit on secret details](https://docs.aws.amazon.com/secretsmanager/latest/userguide/manage_update-secret-value.html) | Explicit Retrieve → Edit → native PutSecretValue; empty new-version draft. |
| Delete | [Actions → schedule deletion and recovery period](https://docs.aws.amazon.com/secretsmanager/latest/userguide/manage_delete-secret.html) | Explicitly force-only deletion with exact-name confirmation; no scheduled deletion/recovery. |
| Labels | API/CLI staging-label management | Current/previous only. The rollback button is labeled a simulator helper, not an AWS console control. |

Unsupported features fail explicitly: metadata-only creation, SecretBinary,
KmsKeyId (including `alias/aws/secretsmanager`), custom/AWSPENDING labels, tags,
resource policies, rotation, replicas, generated passwords, partial/cross-account
ARNs, recovery windows/RestoreSecret, asynchronous deletion, version garbage
collection and CloudFormation. Reads/listing are immediately consistent locally;
AWS listing/deletion timing differs.

## Verification and formal scope

The following are actual local results, separate from hosted GitHub CI:

| Check | Recorded result |
| --- | --- |
| Native contracts |29 selected AWS/server tests normal/race/vet; strict HTTP4 and storage6, plus the late explicit-version outage case |
| Whole Go/race/build | `make before-commit` PASS at `0888924`; unchanged-package Go cache disclosed, changed AWS package reran140.462s |
| One-profile coverage |6270/6615 statements **94.78%**,8193/9091 unfiltered source lines **90.12%**; both90% floors unchanged, no union/exclusions |
| Full E2E/start |24 Conformance cases PASS341.074s, preserving21 plus mandatory Secrets SDK/browser/served examples; actual built/unbuilt/stale/failure/TTY/CI/auth/signal/private-state start checks PASS |
| SDK/console | Two actual CLI processes/SQLite/token authorities, distinct same-name values, IAM revoke/restore,401/403, restart, version/ARN cleanup; PC1440/390, keyboard/logout/cookie replay and genuine stale responses |
| Browser audit |50 baseline screens and separate5 Secrets axe screens: zero violations; hidden-value metadata screenshots only. Automated checks are not full accessibility certification. |
| Cross-compilation | CGO-disabled linux/amd64,linux/arm64,darwin/amd64,darwin/arm64,windows/amd64 PASS; foreign binaries not executed, no Mac used |
| Formal/applicability | At `c97bdde`, immutable13 baselines +50 specified negative controls,19 policy units,21 applicability units and42 exact changed blobs PASS |

The runtime/tests/models/UI/assets used for these results are unchanged by this
document refresh. New commits require their own immutable-head applicability and
TLC execution; earlier receipts never serve as an unexecuted new-head pass.
Hosted exact-HEAD CI and independent publication review are separate checks;
no hosted CI result, public release or merge is asserted here.

The registered Secrets models are `SecretsVersionLifecycle` (six controls),
`SecretsRevealEpoch` (three) and `SecretsDisclosureChannels` (five), alongside the
ten existing models. All50 control identities are required; removing each one is
a negative fixture. Every mutant must exit12 with its named invariant violation.
Forty-eight recorded counterexamples have behavior traces; the existing
`BrowserSessions_RawOperatorCookie` and
`SQSStorageBudget_BlockLegacyMaintenance` instead have explicit violating
initial-state valuations. Parser/type/runtime errors and timeouts are failures.

These bounded models abstract version/label ownership, authority and reveal/
disclosure channels. They do not execute Go/React, prove SQL rollback, cursor
cryptography, quotas, OS authentication, secure erasure, unbounded behavior or
liveness. `AcquireRaw` models the authorized Get call site, not a permission
check in the plaintext accessor. Concrete tests independently cover that boundary
and its failure paths. See [formal inventory](../formal/README.md) and
[exact-blob validation](formal-validation.md).

Historical failures remain evidence, not current blockers: early five ignored
writes falsely acknowledged success and were fixed with row-count checks;
whole-source checkpoints `bb802f2`/`0fe6174` failed at87.38%/89.98% before real
contract tests produced the later90.12% pass. An initial keyboard test used DOM
focus instead of Cloudscape's roving menu selection; the corrected ArrowDown/
Enter test passed without changing product behavior/timeouts. Original recovery
commit `96259aa` and earlier receipts remain preserved; their results are not
substituted for the measured integration checkpoints above.

## References and rights

Primary API references, checked2026-11-04:
[CreateSecret](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_CreateSecret.html),
[DescribeSecret](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_DescribeSecret.html),
[ListSecrets](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_ListSecrets.html),
[PutSecretValue](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_PutSecretValue.html),
[GetSecretValue](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_GetSecretValue.html),
[UpdateSecretVersionStage](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_UpdateSecretVersionStage.html),
[DeleteSecret](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_DeleteSecret.html),
[IAM actions/resources](https://docs.aws.amazon.com/service-authorization/latest/reference/list_secretsmanager.html).
The [pinned official Go SDK](https://github.com/aws/aws-sdk-go-v2/blob/71605dd1c260cb5fd015ad142a714dc03479b49d/service/secretsmanager/api_client.go)
confirms2017-10-17 and JSON1.1; generic Welcome-page Query boilerplate is not the
native protocol source.

Kumo commit `ce37a8fb406ecb96b5619204f645c26302b651a5` (MIT, copyright2025
sivchari) and LocalStack public snapshot `8b9a79f05846835cf4dff63ab7eefdde9df83783`
(Apache-2.0) were design references only. No Kumo/LocalStack/moto engine, service
code, fork, submodule, image or new dependency is adopted by this Secrets
implementation. Existing upstream adaptations elsewhere retain their rights. Existing original
[Kumo permission notice](../third_party/KUMO-LICENSE.txt), embedded mirror and
Cloudscape/Go/runtime notices are retained. The public snapshot license is not a
license for proprietary images. Unlike the referenced in-memory store's token
replacement/partial-ARN behavior, this implementation independently enforces
SQLite immutable versions and retired physical identities.

Pinned source references:
[Kumo store](https://github.com/sivchari/kumo/blob/ce37a8fb406ecb96b5619204f645c26302b651a5/internal/service/secretsmanager/storage.go),
[LocalStack provider](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/localstack-core/localstack/services/secretsmanager/provider.py).
