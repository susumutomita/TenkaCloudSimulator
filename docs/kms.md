# KMS: bounded local keys

This increment implements eight native KMS JSON 1.1 operations: `CreateKey`,
`DescribeKey`, `ListKeys`, `EnableKey`, `DisableKey`, `GetKeyPolicy`, `Encrypt`,
and `Decrypt`. It is an independent learning simulator. Its key storage and
ciphertext are **not AWS KMS, an HSM, an at-rest encryption guarantee, or an AWS
portable ciphertext format**. No AWS credentials or network access are required.

Send `POST /_simulator/providers/aws/` with
`Content-Type: application/x-amz-json-1.1` and
`X-Amz-Target: TrentService.<Operation>`. `X-Simulator-Region` selects one of the
four supported local regions (default `us-east-1`). SDKs use dummy `TCSIM...`
SigV4 credentials with service `kms`; real credentials and session tokens are
rejected. The CLI's local access token is still required. `server.New` alone is
an unauthenticated composition API; hosts must wrap it in `NewAccess`.

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
resource are supplied by this increment. The isolated UI source is not available
until shared navigation/capability/assets integration is completed and tested.
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
Selected metadata DTO fields adapt MIT-licensed Kumo at the pinned commit in
[provenance](../third_party/kumo-kms.json); original rights are retained in
[KUMO-LICENSE](../third_party/KUMO-LICENSE.txt) and the embedded legal mirror.
Kumo's context-ignoring cryptography and LocalStack's Python runtime were not
copied. Authenticated AWS console visual equivalence is unverified.

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
