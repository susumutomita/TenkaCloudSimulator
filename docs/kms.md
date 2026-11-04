# KMS: bounded local keys

This local module supplies eight bounded native KMS JSON 1.1 operations: `CreateKey`,
`DescribeKey`, `ListKeys`, `EnableKey`, `DisableKey`, `GetKeyPolicy`, `Encrypt`,
and `Decrypt`. It is an independent learning simulator. Its key storage and
ciphertext are **not AWS KMS, an HSM, an at-rest encryption guarantee, or an AWS
portable ciphertext format**. No AWS credentials or network access are required.

After root configures the shared AWS Gateway with this module, send
`POST /_simulator/providers/aws/` with
`Content-Type: application/x-amz-json-1.1` and
`X-Amz-Target: TrentService.<Operation>`. `X-Simulator-Region` selects one of the
four supported local regions (default `us-east-1`). SDKs use dummy `TCSIM...`
SigV4 credentials with service `kms`; real credentials and session tokens are
rejected. The host's local access token must still be required. `server.New` alone is
an unauthenticated composition API; hosts must wrap it in `NewAccess`. The refreshed private source now initializes KMS in regular server.New and
advertises exactly these eight operations. Native fixtures also configure
Gateway.KMS directly. The regular server and actual CLI/SDK checks are recorded below; rebuilt console
interaction and whole-CI remain separate gates. Source presence alone is not
successful integration.
An explicitly unconfigured store still returns501.

## Keys and identity permissions

Create with `{}` or an optional `Description`. The supported defaults are
`KeySpec: SYMMETRIC_DEFAULT`, `KeyUsage: ENCRYPT_DECRYPT`, `Origin: AWS_KMS`, and
`MultiRegion: false`; different values fail. Returned key metadata includes a
UUID `KeyId`, local `Arn`, creation timestamp, `KeyState`, `Enabled`, and the
fixed symmetric configuration. Key ID and full same-region/account key ARN are
accepted; aliases and cross-account identifiers are not.

`CreateKey` and `ListKeys` require their `kms:<Operation>` IAM permission on `*`.
All other operations require their exact key ARN. Explicit Deny wins. For
`Decrypt` without `KeyId`, authorization uses the key embedded in the local
blob; an explicitly provided `KeyId` must match. A wrong key denied by IAM fails
before that key is read. Principal selection is exercise context, not
participant authentication or team isolation.

`GetKeyPolicy` accepts an optional `PolicyName`, whose default and sole valid
value is `default`, and returns a JSON policy string plus `PolicyName`.
Programmatic creation uses the immutable AWS-style default account-root
statement that **enables delegation through identity IAM**. It does not grant
all identities access. Custom policies, policy mutation, policy conditions,
grants, and cross-account resource-policy evaluation are not implemented and
are rejected. The local administrator remains a simulator operator.

`EnableKey`/`DisableKey` return HTTP 200 with an empty body. Metadata and policy
remain readable while disabled. Both cryptographic operations reject disabled
keys; re-enabling restores access to existing ciphertexts.

## Real local authenticated encryption

The implementation uses Go standard-library AES-256-GCM with
`cipher.NewGCMWithRandomNonce`. Each encryption uses a generated 96-bit nonce;
nonce, authenticated ciphertext and 128-bit tag are stored in a bounded opaque
local blob with a version, region and key identifier. Canonical JSON additional
authenticated data binds the format, full local key ARN, region, and exact
case-sensitive `EncryptionContext` map. Map order is immaterial; absent and
empty context are equivalent. A different context, region, key, or modified blob
cannot successfully decrypt. Context is not a secret; do not place credentials
in it. Neither plaintext nor key material is returned by metadata APIs or put in
errors/logs. `Decrypt` intentionally returns the requested plaintext.

Plaintext is 1–4096 bytes. JSON binary fields use base64 strings; SDKs expose
binary values. `EncryptionAlgorithm` is omitted or `SYMMETRIC_DEFAULT` only.
Successful responses include the full key ARN and algorithm. Key material,
state and an encryption counter persist in the team's SQLite database. State
checking and crypto/counter operations share a short transaction, serializing
Disable with crypto. The counter refuses encryption after `2^32` messages per
key, as required by the standard random-nonce API.

**Storage limitation:** the private SQLite table stores raw key material.
Protect the database, backups and host filesystem. The simulator does not
supply a production secret manager, secure memory erasure, rotation, key
export/import, or protection against a host/database administrator. Allocate
separate process/database/token authorities per team. Reopening the same team
DB preserves keys and ciphertext usability; another team's DB does not have
those keys.

## Limits, errors and unsupported scope

Local limits (not AWS quota claims): 1000 keys per process/database, a 64 KiB
JSON request, at most 100 context entries and 8192 bytes of canonical serialized
UTF-8 context. Description is at most 8192 Unicode characters. `ListKeys`
returns region-local key ID/ARN pairs, supports `Limit` 1–1000 (default 100),
and uses an opaque region-bound `NextMarker` when `Truncated` is true.

Bad JSON, duplicate keys, unknown/case-mismatched fields, nulls, malformed
Unicode, invalid base64, unsupported algorithms/options and invalid IDs fail
with `ValidationException` (HTTP 400). A missing key yields `NotFoundException`;
a disabled key yields `DisabledException`; an explicitly different decryption
key yields `IncorrectKeyException`; invalid/tampered/context-mismatched local
ciphertext yields `InvalidCiphertextException`. IAM denial is HTTP 403.
Unimplemented operations return HTTP 501; they never silently succeed.

No asymmetric/HMAC/data keys, aliases, tags, custom key policies/stores,
CloudTrail/CloudWatch, rotation, scheduled deletion, grants, multi-region keys,
external key material, AWS portable blobs, AWS eventual consistency, or CFN
resource are supplied by this increment. A prepared isolated Cloudscape component targets these same native APIs for the
regional key list, creation, metadata, read-only default policy, enable/disable
and explicitly identified local encryption test. Shared navigation/component wiring and canonical asset rebuild are now recorded
below. Actual browser verification remains pending; a built bundle is not
evidence that the new view is usable.
No existing Challenge problem is enabled merely by these operations.

## Wire examples

With the host's authenticated HTTP helper (never put access tokens in examples):

1. `TrentService.CreateKey`: `{}` → `KeyMetadata.KeyId` and `KeyMetadata.Arn`.
2. `TrentService.Encrypt`:
   `{"KeyId":"<KeyId>","Plaintext":"aGVsbG8=","EncryptionContext":{"purpose":"demo"}}`
   → `CiphertextBlob` (base64), `KeyId` (ARN), `EncryptionAlgorithm`.
3. `TrentService.Decrypt`:
   `{"CiphertextBlob":"<returned base64>","EncryptionContext":{"purpose":"demo"}}`
   → `Plaintext: "aGVsbG8="`. Changing purpose to `other` is an error.
4. `TrentService.DisableKey`: `{"KeyId":"<KeyId>"}` → empty success.
   The same Decrypt now fails with `DisabledException`.
5. `TrentService.EnableKey`: the same key request → empty success, then the
   original Decrypt works again. Do not delete the team database as cleanup if
   its keys must remain usable; scheduled deletion is unsupported.

## Sources and reuse

Protocol and limits follow the bounded portions of the official
[CreateKey](https://docs.aws.amazon.com/kms/latest/APIReference/API_CreateKey.html),
[Encrypt](https://docs.aws.amazon.com/kms/latest/APIReference/API_Encrypt.html),
[Decrypt](https://docs.aws.amazon.com/kms/latest/APIReference/API_Decrypt.html),
[GetKeyPolicy](https://docs.aws.amazon.com/kms/latest/APIReference/API_GetKeyPolicy.html),
and [default policy](https://docs.aws.amazon.com/kms/latest/developerguide/key-policy-default.html)
documentation. The cryptographic primitive is Go's
[NewGCMWithRandomNonce](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce).
The preserved historical candidate selected metadata DTO fields from MIT-licensed
Kumo at the pinned commit in
[provenance](../third_party/kumo-kms.json); original rights are retained in
[KUMO-LICENSE](../third_party/KUMO-LICENSE.txt) and the embedded legal mirror.
New development treats Kumo as a design reference only; no Kumo engine/codebase,
submodule or new dependency is introduced. Existing notices are retained, not
removed based on assumptions. Kumo storage/cryptography and LocalStack's Python
runtime are not used. Authenticated AWS console visual equivalence is unverified.

## Console correspondence and remaining gaps

The official [symmetric-key creation guide](https://docs.aws.amazon.com/kms/latest/developerguide/create-symmetric-cmk.html)
and [creation permissions](https://docs.aws.amazon.com/kms/latest/developerguide/create-keys.html)
are the public references for the `Customer managed keys`, `Configure key`,
`Symmetric`, `Encrypt and decrypt`, and key-policy labels. We have not accessed
an authenticated AWS console or verified pixel-for-pixel current layouts.

| Flow | Bounded local implementation | Remaining AWS-console difference |
|---|---|---|
| Customer managed keys → Create key | Cloudscape table/form; symmetric configuration and description | AWS console requires an alias and includes labels, administrators and usage-permission stages; aliases/tags/custom policy editing are intentionally unsupported here |
| Key details | Key ID/ARN, state, region, usage/spec/origin, description | No rotation, grants, CloudTrail, usage metrics or deletion |
| Key policy | Actual native GetKeyPolicy; immutable JSON read-only | Uses the programmatic default policy, not AWS console-generated administrator/user statements |
| Disable/Enable | Actual native operations and state refresh; crypto buttons honor returned state | AWS eventual consistency and full action-menu arrangement are not reproduced |
| Local encryption test | Actual Encrypt/Decrypt, context error, base64 blob | Clearly identified simulator learning tool; not represented as an AWS console feature |

These differences must stay visible until their underlying APIs, authorization
and UI are implemented and verified together. No form creates a decorative
alias or silently grants key use.

## Operation, state and permission contract (official references checked 2026-11-04)

The following table describes this module's finite subset, not full AWS KMS.
Key ARN below means the actual same-region/account key, including one recovered
from the local ciphertext when Decrypt omits KeyId. AWS key-policy/grant/cross-account
semantics extend beyond the local immutable delegation policy and identity matcher.

| Native operation | Local state/result | Permission and resource | Primary specification |
|---|---|---|---|
| CreateKey | New symmetric key Enabled; random material and counter0 committed before acknowledgment | kms:CreateKey on * | [CreateKey](https://docs.aws.amazon.com/kms/latest/APIReference/API_CreateKey.html) |
| DescribeKey | Metadata read, enabled or disabled; no material | kms:DescribeKey on key ARN | [DescribeKey](https://docs.aws.amazon.com/kms/latest/APIReference/API_DescribeKey.html) |
| ListKeys | Region-local ID/ARN page; default100, range1–1000 | kms:ListKeys on * | [ListKeys](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListKeys.html) |
| EnableKey | Disabled/Enabled → Enabled; empty200 body | kms:EnableKey on key ARN | [EnableKey](https://docs.aws.amazon.com/kms/latest/APIReference/API_EnableKey.html) |
| DisableKey | Enabled/Disabled → Disabled; empty200 body | kms:DisableKey on key ARN | [DisableKey](https://docs.aws.amazon.com/kms/latest/APIReference/API_DisableKey.html) |
| GetKeyPolicy | Read immutable default policy; disabled allowed | kms:GetKeyPolicy on key ARN | [GetKeyPolicy](https://docs.aws.amazon.com/kms/latest/APIReference/API_GetKeyPolicy.html) |
| Encrypt | Enabled only; local blob plus counter+1 in same transaction | kms:Encrypt on key ARN | [Encrypt](https://docs.aws.amazon.com/kms/latest/APIReference/API_Encrypt.html) |
| Decrypt | Enabled only; exact authenticated key/region/context; no persistent mutation | kms:Decrypt on key ARN | [Decrypt](https://docs.aws.amazon.com/kms/latest/APIReference/API_Decrypt.html) |

AWS documents cross-account DescribeKey/Encrypt/Decrypt and aliases in several
operations; this module explicitly excludes them. Current AWS Encrypt/Decrypt
also offer DryRun, grants and recipient/attestation options; unknown fields are
rejected. These are unsupported, not silently ignored. The original Issue117
plan mentions aliases, grants and scheduled deletion, none of which this smaller
increment implements. No key deletion API is invented: test-owned temporary DB
cleanup is distinct from deleting a key with live ciphertext dependencies.

## Dependencies and independent test/model evidence

Mandatory host dependencies are the existing provider SQLite connection, request
region/identity snapshot and root Access token boundary. Core IAM decision logic
is the existing limited identity matcher, not a new AWS authorization engine.
AES-GCM uses only the Go standard library; no external KMS, HSM, process, network
or SDK runtime is required. S3 SSE-KMS, SSM SecureString, Secrets Manager encryption,
Lambda guest KMS and CloudFormation KMS resources are separate unimplemented
integrations. Enabling this native module does not enable any existing problem.

The KMSKeyState model bounds two keys/teams, two request regions, two context
values, two blob slots and two encryptions per key. Eight controls target permission,
team, region, enabled encrypt/decrypt, context/key binding and the abstract counter
ceiling. It describes only atomic successful state events: rejected response frames,
SQL RowsAffected/commit failures, payload/header/JSON parsing, ciphertext bytes,
cryptography and probability, pagination, host authentication and read-only metadata
outputs are not proved. The canonical model registry and four required properties are integrated in
source; the final exact HEAD must pass the immutable applicability and TLC gates.
A registry declaration alone is not an executed proof.

This source refresh normally merges main b61d029 into a separate candidate while
preserving the original5862047 checkout. The initial merge preserved all five shared conflict files from main. A later
authorized source phase adds regular server initialization/capabilities, minimal
KMS navigation and eight header-target body templates while preserving the
existing IAM alias and all43 OpenAPI operation IDs. Generated assets are not
rebuilt yet; existing SQS/API/logout state and routes are preserved. New native tests use
independent literal operation/action/resource names, full persisted-key fingerprints,
actual SQLite pure RAISE(IGNORE) failures and rejected transport frames. Expected
single-row writes now require one affected row before any key/state/ciphertext
success; a missing database refuses construction. These fault tests concern
nonstandard SQL fault fixtures, not an ordinary API exploit or protection against
an arbitrary host/database administrator. At native checkpoint f018bc6, all16 TestKMS functions passed normally (1.223s),
under race (10.312s), and package vet passed (1CPU/-p1/offline/timeout60s).
Subsequent dispatch and regular-server checks are recorded below. New model
registration and actual console browser interaction remain separate gates.
Old-candidate results are not fresh whole-integration evidence.

| Actual operation group | Finite model correspondence | Native obligations outside the model |
|---|---|---|
| CreateKey | Create + AuthorizedEvents/TypeOK | Native defaults, random key/UUID, IAM action *, quota, one-row write/commit acknowledgment |
| EnableKey/DisableKey | SetEnabled + AuthorizedEvents; later crypto checks EnabledCrypto | Exact key ARN, empty wire body, failed/ignored SQL update frames |
| Encrypt | Encrypt + TypeOK count0..2, EnabledCrypto, AuthorizedEvents | Actual2^32 ceiling, nonce construction, AES-GCM/AAD bytes, ciphertext format and counter commit |
| Decrypt | Decrypt + BoundContextAndKey, EnabledCrypto, AuthorizedEvents | Actual tag verification, case-sensitive context JSON, no plaintext on denial/error, counter/material read invariance |
| DescribeKey/ListKeys/GetKeyPolicy | No separate read-output transition | Independent native permission/resource checks, pagination, schema/redaction, disabled-state metadata/default policy |

The canonical registry includes kms_types.go for abstract key/region/context
binding helpers and shared initialization/dispatch declarations. Its explicit
limits exclude parser/crypto/read-output/host-authentication proofs. The prior
standalone fragment is preserved outside the source tree, leaving one canonical
registry. Twelve baselines and48 named controls are registered; existing ten
models and36 controls remain intact. Current-head execution is still pending.


## Source composition and bounded API reference

Regular server.New uses the same provider SQLite connection for KMS and closes
it with the existing team lifecycle. Existing Access remains mandatory in the CLI;
no new login path, principal bypass, AWS account configuration or public endpoint
is added. Capabilities explicitly list eight operations and unsupported scope.
The console imports the isolated Cloudscape view with region/principal/navigation
remount keys and the existing authenticated request helper; logout/account/API
reference controls retain their prior source. The view is not an AWS-authenticated
console capture or a promise of exact current layout.

The existing root POST OpenAPI operation now contains eight named KMS JSON1.1 body
examples with exact per-target request/response schema mapping. The IAM Query
provider alias and all43 existing operation IDs remain unchanged: eight target
headers are not misrepresented as eight distinct path/method operations. Select
X-Amz-Target manually to match the body; choosing a body example has not been
verified to synchronize headers automatically. Create a key first, use its actual
returned identifier, then copy actual local Encrypt output for Decrypt. The
valid-base64 documentation placeholder is deliberately not decryptable.
Parsed schema/source checks, served examples, SDK sends and canonical bundle/NOTICE
rebuild have separate results below. Browser operations, complete coverage/security/CI
and all12/48 formal runs remain distinct pending gates for this composition.


## Protocol admission/refusal and snapshot contract (prepared, not verified)

The independent KMSProtocolAdmission model represents normalized selection,
metadata consistency, identity lookup and a captured permit before executing one
request. KMS signing scope is a routing hint only, never authenticated authority.
An identity-store failure/cancellation is an internal500; a missing identity is403.
A live policy change after snapshot must affect the next request, not silently
replace the current captured permit. Missing/conflicting targets cannot become
S3 success. Error/denial replies preserve the abstract resource and disclose none.
Four new controls accompany five safety invariants plus TypeOK. Raw header/SDK
signature parsing, host token authentication, SQL implementation and other-service
behavior are not formally established; actual Gateway/native tests are separate.
The atomic KMS model's8 controls do not prove these protocol/refusal properties.

At pre-fix checkpoint ebb6f78, actual tests reproduced both dispatch findings:
identity SQLite outage became403, while signed KMS intent without/conflicting
target could return S3/SQS success or the wrong service error. Those failures are
preserved. The minimal a6f3e0e correction uses PolicyContext with request context,
separates missing identity403 from internal500 and recognizes KMS signing scope
as routing metadata without granting authorization. All19 AWS KMS test functions
passed normally (1.260s) and under race (10.174s); the regular-server Access,
provider-alias, two-DB/token and persistence test passed normally (1.388s) and
under race (10.499s), with both-package vet passing. The body-read barrier proves
captured permission is stable across later grant/revocation; the next request
sees the changed policy. This is not full IAM, SigV4 or tenant-isolation proof.
A same-view console refresh guard now rejects older errors, with prepared delayed
old-error/current-error/recovery browser assertions; these are also unexecuted.


## Canonical assets and actual SDK checkpoints

At driver checkpoint8a1fbb1, an offline CGO0 current-source binary and existing
boto3 1.42.0 exercised all eight operations through two real CLI processes/DBs,
actual local token/role IAM, context/key/region/tamper refusal, disabled state and
restart. Eight clients had42 calls,42 sends and42 responses, with positive equal
counts per client. A numeric-loopback/exact-origin guard runs before SDK send;
proxy use is disabled. No overlay, old binary, dependency install, actual AWS,
browser or guest Python runtime was used. Temporary fixture processes were reaped.

The canonical Cloudscape build used Node24.19.0 and a byte-identical package/lock
pair from the existing verified physical dependency tree. All19,261 entries,
including16,047 file hashes/modes and3 internal relative symlinks, matched before
and after copying/building. No install or lifecycle script ran. The canonical
builder recorded22 actual dependency notice packages; original notices remained
unchanged, and the asset/provenance checker passed. Browser operation and visual
fidelity are not inferred from these asset checks. At canonical checkpoint c8cdb8f, the extended driver read the actual embedded
specification and executed all eight body examples against the same restarted
SQLite through official SDK methods, substituting only the actual returned key
identifier/ciphertext. The documented placeholder was explicitly rejected;
pagination was bounded and exact, and Disable/Enable state was read back. Eight
clients had57 positive equal calls/sends/responses. Both actual CLI processes
and the restart process completed/reaped cleanly. This is not Scalar browser
interaction, automatic header synchronization or a whole-CI result.
