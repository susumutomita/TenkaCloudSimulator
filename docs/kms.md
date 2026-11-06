# KMS: local symmetric keys and customer aliases

Twelve native JSON1.1 operations share the team's actual SQLite state with SDK
clients and the embedded Cloudscape console. Use the authenticated [CLI](start.md).
Embedding hosts must wrap the unauthenticated `server.New` composition API with
`NewAccess`. Allocate a separate process/database/token authority per team.

POST to `/` or `/_simulator/providers/aws/` with Content-Type
`application/x-amz-json-1.1` and X-Amz-Target `TrentService.<Operation>`.
The four supported regions default to `us-east-1`. Local unsigned or dummy
TCSIM-prefixed SDK credentials select protocol/region; they are not authentication.
Real AWS credentials and session tokens are rejected. An exercise principal does
not establish authentication or team isolation.

This is an independent learning simulator. SQLite stores raw key material: it
is not an HSM, secure memory or an at-rest protection guarantee. Use dummy data.
The versioned local ciphertext cannot be used with AWS KMS.

## Operations and permissions

| Operation | Native state/result | Required local identity permission |
| --- | --- | --- |
| CreateKey | New Enabled symmetric key, committed before acknowledgment | `kms:CreateKey` on `*` |
| DescribeKey | Metadata only; Enabled or Disabled | `kms:DescribeKey` on concrete key ARN |
| ListKeys | Region-local UUID/ARN page; default100, range1–1000 | `kms:ListKeys` on `*` |
| EnableKey | Set Enabled; empty200 | `kms:EnableKey` on concrete key ARN |
| DisableKey | Set Disabled; empty200 | `kms:DisableKey` on concrete key ARN |
| GetKeyPolicy | Read immutable `default` policy | `kms:GetKeyPolicy` on concrete key ARN |
| Encrypt | Enabled key only; ciphertext and exact-one counter increment in one transaction | `kms:Encrypt` on resolved concrete key ARN |
| Decrypt | Enabled key only; authenticated key/region/context; no persistent mutation | `kms:Decrypt` on selected or ciphertext concrete key ARN |
| CreateAlias | Create a unique customer alias; empty200 | `kms:CreateAlias` on alias ARN **and** target key ARN |
| ListAliases | Region-local customer aliases; optional concrete KeyId/ARN filter | `kms:ListAliases` on `*` |
| UpdateAlias | Retarget without renaming or changing key material; empty200 | `kms:UpdateAlias` on alias ARN **and both old/new key ARNs** |
| DeleteAlias | Remove only the alias; key/ciphertexts remain; empty200 | `kms:DeleteAlias` on alias ARN **and** current key ARN |

Explicit Deny wins. An alias-metadata grant does not grant cryptographic access;
resolving an alias does not require an invented DescribeKey/GetKey permission.
The immutable account-root key-policy statement enables delegation through the
local identity matcher, not access for every identity. Full AWS key-policy,
condition/ABAC, grants and cross-account evaluation are not implemented.

## Keys, crypto and alias resolution

CreateKey accepts `{}` or Description. The supported configuration is
`SYMMETRIC_DEFAULT`, `ENCRYPT_DECRYPT`, `AWS_KMS`, `MultiRegion:false`.
GetKeyPolicy accepts only the optional/default PolicyName `default`.
Enable/Disable leave metadata readable; disabled keys reject crypto until enabled.

Concrete UUIDs and same-region/account key ARNs work for existing key operations.
Encrypt and Decrypt additionally accept customer alias names or alias ARNs.
Alias resolution, concrete-key authorization, material/state read, crypto/counter
checks and commit occur in the **same** transaction. Callbacks use the captured
caller policy, not nested store transactions. DescribeKey, EnableKey, DisableKey
and GetKeyPolicy do **not** accept alias identifiers in this bounded implementation.

Decrypt without KeyId follows the UUID embedded in the local ciphertext. An
explicit alias follows its current target: after retargeting, old ciphertext
fails with IncorrectKeyException if the caller can use that selected target;
an unauthorized selection fails AccessDenied first. The old concrete key can
still decrypt its ciphertext with the matching context. Alias deletion does not
remove keys, material or encryption counters.

The native format uses Go AES-256-GCM/NewGCMWithRandomNonce, a generated96-bit
nonce and128-bit tag. AAD binds version, full concrete key ARN, region and the
case-sensitive EncryptionContext string map; absent/empty context are equivalent.
Wrong key/region/context or modified data fails. The mutable alias string is not
used as ciphertext identity. Each key refuses encryption after `2^32` messages.
No key material is returned by metadata APIs or included in errors. Decrypt
intentionally returns plaintext; context is not secret storage.

## Customer aliases and console

API names include `alias/`, are case-sensitive, and use ASCII letters/numbers,
`/`, `_`, `-`, with total length at most256. `alias/aws/` is reserved. Targets
must be existing same-region/account concrete customer keys, not aliases.
Both Enabled and Disabled supported keys permit alias management. UpdateAlias
cannot rename; delete/create is a separate pair of operations.

The console follows the public AWS guide's **Customer managed keys → key detail
→ Aliases** create/delete location. Enter a suffix: the console adds `alias/`
automatically. Entering `alias/example` therefore produces `alias/alias/example`.
The target picker reads real ListKeys/DescribeKey and requires those separate
read permissions. They do not enlarge alias/crypto permissions.

**Change target (local API)** is explicitly a simulator API tool: AWS documents
that its console does not offer UpdateAlias. Delete uses an exact typed-name
confirmation and does not imply a key-deletion API. A successful write closes
and clears its form before independent metadata refresh. Failed refresh exposes
only a read-only retry; it does not replay the acknowledged write. Locale changes
retain the draft, while region/principal/key changes retire the old view and
requests. Opaque native error text is not translated as if it were product copy.

## Bounds, pagination and failures

| Bound | Local behavior |
| --- | --- |
| Keys |1000 per process/database |
| Alias rows |1000 total across regions;50 per key; fixed local ceilings |
| ListAliases |Omitted Limit50; explicit1–100; optional concrete KeyId/ARN filter |
| Requests |64KiB JSON; unknown fields, duplicates, nulls and invalid Unicode reject |
| Plaintext/ciphertext |1–4096 bytes plaintext; at most6144 bytes local ciphertext; JSON uses base64 |
| Context |100 entries;8192 bytes canonical UTF-8 JSON |
| Description |8192 Unicode characters |

AWS currently documents an adjustable default of50 aliases per key; the
simulator does not implement quota adjustments or AWS's eventual consistency.
Local alias mutations are immediately atomic. Alias markers are opaque,
persisted HMAC tokens bound to region/filter/limit/revision. Mutation makes old
markers invalid; another team/region or altered options cannot reuse them.
This is a bounded local cursor contract, not an identical AWS marker encoding.

Invalid CreateAlias names return InvalidAliasNameException; malformed fields and
invalid Update/Delete names return ValidationException. Duplicate names return
AlreadyExistsException; missing aliases/keys NotFoundException; stale/tampered
markers InvalidMarkerException; caps LimitExceededException. IAM denial is403;
storage failures are sanitized500 with no partial successful result. Strict
single-row acknowledgments and commit checks precede success. Alias parent FKs
and saved creation-time checks cover the current immutable-UUID key lifecycle;
future DeleteKey/recreation and arbitrary host SQLite tampering are not promised.

No AWS-managed/predefined aliases, alias ABAC, tags, custom policies/stores,
asymmetric/HMAC/data keys, rotation, scheduled deletion, key deletion API,
external material, AWS-portable blobs, HSM protection, S3 SSE-KMS, SSM SecureString,
Secrets encryption, guest-Lambda KMS or CloudFormation KMS resources are supplied.
No Challenge problem is enabled merely by these operations.

## API reference and verification

The embedded root POST has twelve named KMS request templates and per-target
schema mappings. The four alias templates require actual CreateKey output; Update
needs a second real key. Static placeholders are not saved state. Selecting a
body example does not automatically select routing headers. Native SDK children
execute the original eight examples and the four alias examples against the
same SQLite; the alias examples must execute on both supported root routes.

The mandatory SDK/UI/docs children preserve native transport, IAM/explicit Deny,
crypto, two teams/regions, restart and cleanup checks. UI tests include actual
ACK→storage500→read-only retry, older500 after newer200, locale/context clearing,
typed keyboard deletion,1440/390 reflow/axe and browser logout preserving separate
SDK authority. Finite models have a separate [formal contract](../formal/README.md);
the [alias view model](../formal/KMSAliasesViewEpoch.tla) abstracts bounded lifetimes,
request revisions, picker state and ACK/retry behavior. They are not Go/SQL/IAM,
JSON/parser, React/Cloudscape, AES/nonce, unbounded liveness or full AWS proofs.

Every final integration must pass current-HEAD native/SDK/UI/docs, one-profile
coverage floors, required formal controls and hosted CI/security review. A
successful historical/private component run is not a new combined-HEAD gate.
Cross-compilation is not foreign-platform execution; automated axe is not a
complete accessibility or authenticated AWS visual comparison.

## Primary sources and provenance

[CreateAlias](https://docs.aws.amazon.com/kms/latest/APIReference/API_CreateAlias.html),
[ListAliases](https://docs.aws.amazon.com/kms/latest/APIReference/API_ListAliases.html),
[UpdateAlias](https://docs.aws.amazon.com/kms/latest/APIReference/API_UpdateAlias.html),
[DeleteAlias](https://docs.aws.amazon.com/kms/latest/APIReference/API_DeleteAlias.html),
[permissions](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html),
[alias console creation](https://docs.aws.amazon.com/kms/latest/developerguide/alias-create.html),
[retarget limitations](https://docs.aws.amazon.com/kms/latest/developerguide/alias-update.html),
[quotas](https://docs.aws.amazon.com/kms/latest/developerguide/resource-limits.html),
[Encrypt](https://docs.aws.amazon.com/kms/latest/APIReference/API_Encrypt.html),
[Decrypt](https://docs.aws.amazon.com/kms/latest/APIReference/API_Decrypt.html) and
[Go random-nonce GCM](https://pkg.go.dev/crypto/cipher#NewGCMWithRandomNonce)
are specification references, not claims of real-cloud comparison.

The historical Kumo-derived DTO selection is preserved as superseded evidence in
[provenance](../third_party/kumo-kms.json); its original rights remain in
[KUMO-LICENSE](../third_party/KUMO-LICENSE.txt) and the embedded legal mirror.
Current wire output uses independently authored projections documented in the
[accepted source inventory](../third_party/independent-native-source.json).
Aliases/SQLite/authorization/crypto and this UI are original simulator code.
Prior implementation visibility means this is not a clean-room claim. No Kumo
engine, dependency or submodule is introduced.
