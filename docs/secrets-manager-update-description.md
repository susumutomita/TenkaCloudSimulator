# Secrets Manager description updates

The native JSON 1.1 `secretsmanager.UpdateSecret` adapter supports one bounded
workflow: replace the description of an existing local secret. This increment
does not implement value updates, KMS key changes, rotation or managed secrets.

## Console workflow

1. Open **Secrets Manager → Edit description** (日本語: **説明を編集**).
   Opening the tab does not retrieve a secret value.
2. Enter the saved, complete physical secret ARN for the selected region. If you
   opened a secret's details first, its ARN is filled in. A name alone is not
   accepted by this form. An update-only participant can enter an ARN even when
   the Secrets list is unavailable.
3. Enter **New description** (**新しい説明**), with at most 2048 Unicode
   characters. Leave it empty to clear the description.
4. Confirm replacement of the description for that ARN, then choose
   **Save description** (**説明を保存**).

The form calls the native `UpdateSecret` API under the current authenticated
identity and region. It does not grant permissions, fetch values, or create a
version. It replaces the description rather than merging it; another writer can
subsequently replace it again. Use dummy practice metadata, never credentials.

After a confirmed save, the console requests `DescribeSecret` to show the current
saved description. This read needs its own permission. If it fails, the console
reports that the description was saved but the refresh failed. **Refresh
description metadata** retries only the read, never the update. The same button
can load metadata changed through an SDK client without submitting the form.

If the write response cannot be confirmed, the console does not automatically
retry or claim that the write failed without changing data. Check the saved
metadata before submitting another update. Navigating away, changing region or
identity, or signing out discards pending view results; aborting a request does
not undo a write already processed by the server. The form prevents duplicate
submissions while its request is pending.

## Wire contract

Send `POST /` with `Content-Type: application/x-amz-json-1.1` and
`X-Amz-Target: secretsmanager.UpdateSecret`:

```json
{"SecretId":"practice/example","Description":"Practice metadata"}
```

- `SecretId` is an existing name or complete physical ARN in the selected local
  region/account. Partial ARNs and cross-region/account selectors are refused.
- `Description` must be present and a string of at most 2048 Unicode scalar
  values. The empty string clears the description. Omission is deliberately
  rejected: this subset has no other update to perform. AWS marks this field
  optional because its operation also supports value and key changes.
- `ClientRequestToken` is optional. If supplied, it must be a valid string of
  32–64 Unicode scalar values. It has no version-creation or reservation effect
  in this metadata-only workflow, including when it equals an existing version
  ID. The AWS parameter describes a new version when a value is supplied; its
  description-update example returns no version ID. This implementation does
  not claim AWS value-update token conflict behavior for metadata-only calls.
  The reference also has general existing-token conflict prose; its application
  without a supplied value is ambiguous. Reuse here is a bounded simulator
  interpretation, not an AWS-equivalence claim.
- Unknown, duplicate, case-mismatched or null fields, malformed JSON/Unicode,
  trailing data and unsupported fields fail closed. `SecretString`,
  `SecretBinary`, `KmsKeyId`, `Type`, rotation fields, tags and version stages
  are refused even when empty. Use the separately supported `PutSecretValue`
  and `UpdateSecretVersionStage` contracts for their bounded workflows.

Success is HTTP 200 with exactly `ARN` and `Name`. It never returns `VersionId`,
secret values, version stages or invented encryption metadata. Responses retain
`Cache-Control: no-store` and `X-Simulator-Encryption: none`.

## Authorization and durability

The required IAM action is `secretsmanager:UpdateSecret`, evaluated against the
resolved saved physical ARN in the same transaction as the write. No additional
value-read permission is required. The gateway supplies its captured policy
snapshot; this operation does not perform nested policy/database lookups.

Only `description` and `changed` in the selected `aws_secrets` row are written.
The timestamp is exposed through `DescribeSecret.LastChangedDate`. An unchanged
description still counts as an authorized update and refreshes that timestamp.
The checked row count and final metadata read must agree before commit. SQL
failure, ignored write, unexpected metadata drift or missing final row rolls the
transaction back. Versions, values, stage bindings, creation dates, retired ARN
reservations and cursor authority are not written or read as value material.
No schema migration or cold-region creation occurs in the request path.

Invalid/unsupported input returns `InvalidParameterException` (400), a valid
missing target returns `ResourceNotFoundException` (400), IAM denial returns
`AccessDeniedException` (403), and database or authority configuration failure
returns a sanitized `InternalServiceError` (500). Scheduled deletion remains
unsupported, so this subset does not manufacture a scheduled-deletion state or
claim AWS `InvalidRequestException` lifecycle parity.

Local practice data is not encrypted. Existing token/authentication and process
per team boundaries still apply. Description is metadata, not a safe place for
credentials. No real AWS resources or outbound cloud requests are used.

## Scope boundaries

The operation is metadata-only. Value updates, rotation, tags, replication, KMS
encryption, resource policies and CloudFormation integration are not supplied by
this contract. Optional token handling is a bounded interpretation rather than a
claim of AWS behavioral equivalence. Console and SDK clients must use the same
native protocol, local authentication and persistence boundaries.

## Official reference

[AWS UpdateSecret API](https://docs.aws.amazon.com/secretsmanager/latest/apireference/API_UpdateSecret.html).
It specifies the request field bounds, required IAM action, metadata example,
conditional `VersionId` response and native error names. The intentionally
narrower omission, selector and unsupported-field behavior is documented above.
