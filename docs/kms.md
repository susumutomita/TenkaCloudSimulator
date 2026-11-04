# KMS: bounded local keys

Eight native KMS JSON1.1 operations and their embedded Cloudscape workflow share
the team's SQLite state. The regular server initializes KMS; use the authenticated
CLI described in [startup](start.md). Direct server.New is a composition API and
must be wrapped in NewAccess by embedding hosts. An unconfigured Gateway returns501.

Send POST to `/` or `/_simulator/providers/aws/`, Content-Type
`application/x-amz-json-1.1`, and X-Amz-Target `TrentService.<Operation>`.
Unsigned local requests or dummy TCSIM-prefixed SDK credentials scoped to `kms`
are accepted only behind the host's local authentication. Real AWS credentials
and session tokens are rejected. The four supported local regions default to
us-east-1. Wire service/credential metadata selects protocol, never authority.

This is an independent learning simulator: raw key material persists in SQLite.
It is not AWS KMS, an HSM, an at-rest protection guarantee or an AWS portable
ciphertext format. Use dummy data; no real cloud/network dependency is required.

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
resource are supplied by this increment. The embedded Cloudscape console uses these same native APIs for the regional
key list, creation, metadata, read-only default policy, enable/disable and an
explicitly identified local encryption test. PC1440/390 interaction is verified;
this does not establish authenticated AWS visual equivalence.
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

## Verification and finite model scope

The current implementation has actual native, SDK and browser evidence; the
following are distinct input-scoped results, not a claim that an unpublished
HEAD has passed hosted CI.

| Check | Actual result and scope |
| --- | --- |
| Native and regular server |19 AWS KMS tests and1 regular-server test pass normal/race, with both-package vet. Independent literal IAM/ARN expectations, real SQLIGNORE rollback/full key frames, identity outage500 vs missing403 and body-read policy-snapshot barriers are tested. |
| Whole quality, e094485 | All packages actually ran with race (no cached labels); formatting, vet, assets/rights, module verification and static build pass. One fresh profile:6114/6435 statements95.01%,8016/8848 unfiltered source90.60%, v1.4.0 converter. Both90% floors unchanged; no union/exclusions/timeout changes. |
| Whole conformance,5ca45ea | All23 mandatory cases pass in402.411s, including the KMS SDK and browser children with check=True/no skip/fallback. A separate1-case reflow detector and actual make-start unbuilt/built/rebuild/failure/private-token/TTY/CI/auth/signal-cleanup checks pass. |
| Actual KMS SDK | Eight boto3 clients:57 positive equal calls/sends/responses, numeric-loopback guard, two CLI processes/private DBs, restart, exact IAM/explicit Deny, disabled/context/key/region/tamper refusals. Eight examples fetched from the embedded specification execute against the same SQLite. No header/body auto-synchronization claim. |
| Actual console |1440/390 key create/cancel/detail/policy/crypto/disable-enable with SDK readback; keyboard/reflow/axe, older500 after newer200, visible current500/recovery, region/principal field clearing, IAM403 and browser logout preserving independent SDK. Outside requests/page errors0. |
| Screen audit |50 actual regression screens, zero automated WCAG violations; not full accessibility certification. KMS screens have separate focused interaction evidence. |
| Cross builds | CGO-disabled Linux/Darwin amd64/arm64 and Windows/amd64 compile; foreign execution unverified. |
| Immutable finite TLC,91172d0 |12 baselines and48 exact named mutation counterexamples; each mutant exit12 with intended invariant and transition or initial-state witness. Exact34 changed-blob declarations pass. Fixed jar/512MiB/1worker/60s per case. |

Go/native-test/model/asset bytes and embedded binary are unchanged across these
quality/conformance/formal inputs; subsequent documentation/applicability-only
children retain that scoped evidence. Every final committed HEAD must still
pass its immutable policy/applicability gate. Canonical current hosted CI/security
and final independent publication review remain required. No push/release/merge
or real AWS resource creation is implied by local acceptance.

KMSKeyState abstracts two keys/teams, two request regions/contexts/blob slots and
two encryptions per key; eight controls cover permission/team/region/enabled-state,
context/key binding and the counter ceiling for successful atomic state events.
KMSProtocolAdmission separately abstracts normalized service metadata, credential
format/region, identity lookup outcomes and one captured policy; four controls
cover cross-service fallback, outage classification, required target and policy
snapshot authority. The original10 models/36 controls are preserved, giving12/48.
All48 sensitivity IDs and required invariants have real removal-negative fixtures.

These finite checks are not Go/SQLite/AES/nonce/IAM/SigV4/parser refinement proofs,
read-output proofs or complete AWS compatibility. Native error/rollback/crypto
and transport cases supply separate evidence. The original pre-fix dispatch
counterexamples remain in development history (ebb6f78), corrected at a6f3e0e;
original native provenance is preserved in5862047 and the cited rights files.

Cloudscape assets were canonically rebuilt with Node24.19.0 from byte-identical
package/lock and a fully hashed existing dependency tree;22 notices remain intact.
No install, lifecycle script, external dependency or Kumo engine was introduced.
Screenshots contain dummy data only; focused-scroll full-page captures and later
settled viewport captures are distinct visual artifacts, not new regression proof.
S3 SSE-KMS, SSM SecureString, Secrets Manager encryption, Lambda guest KMS and CFN
KMS resources remain unsupported. No Challenge problem is enabled by this module.
