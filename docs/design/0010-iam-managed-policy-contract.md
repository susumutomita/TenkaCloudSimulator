# IAM managed policies: first implementation contract

Status: approved P0 design for [Issue53](https://github.com/susumutomita/TenkaCloudSimulator/issues/53), dated **2026-11-05**, based on main `4a649814bee73b06ee82aecc6ca952dfe6bc8137`. This document specifies work to implement and verify. Registration, source code or a successful response alone does not establish acceptance. No actual AWS calls, sign-in or resources were used to establish this contract.

## Operations and wire format

The increment has exactly **12** operations. It does not include ListEntitiesForPolicy, user/group attachments, AWS managed policies, boundaries, STS, advanced trust or policy conditions. Use the existing IAM root POST Query protocol, `Version=2010-05-08`, namespace `https://iam.amazonaws.com/doc/2010-05-08/`, and native IAM XML error/response envelopes. Query parses form encoding once; SDK dummy wire credentials do not replace local access authentication.

| Official operation | Required inputs | Supported optional inputs | Successful result |
|---|---|---|---|
| [CreatePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreatePolicy.html) | PolicyName, PolicyDocument | Path, Description | Policy metadata; creates default v1 |
| [GetPolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_GetPolicy.html) | PolicyArn | — | Policy metadata, including Description |
| [ListPolicies](https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListPolicies.html) | Scope=Local, required by this subset | PathPrefix, Marker, MaxItems | Policies/member, IsTruncated, optional Marker |
| [DeletePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeletePolicy.html) | PolicyArn | — | ResponseMetadata only |
| [CreatePolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreatePolicyVersion.html) | PolicyArn, PolicyDocument | SetAsDefault | PolicyVersion metadata, no Document |
| [GetPolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_GetPolicyVersion.html) | PolicyArn, VersionId | — | PolicyVersion including encoded Document |
| [ListPolicyVersions](https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListPolicyVersions.html) | PolicyArn | Marker, MaxItems | Versions/member, no Document; IsTruncated, optional Marker |
| [SetDefaultPolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_SetDefaultPolicyVersion.html) | PolicyArn, VersionId | — | ResponseMetadata only |
| [DeletePolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeletePolicyVersion.html) | PolicyArn, VersionId | — | ResponseMetadata only |
| [AttachRolePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_AttachRolePolicy.html) | RoleName, PolicyArn | — | ResponseMetadata only |
| [DetachRolePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DetachRolePolicy.html) | RoleName, PolicyArn | — | ResponseMetadata only |
| [ListAttachedRolePolicies](https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListAttachedRolePolicies.html) | RoleName | PathPrefix, Marker, MaxItems | AttachedPolicies/member with PolicyName and PolicyArn; IsTruncated, optional Marker |

GetPolicyVersion documents use RFC3986 URL encoding, including `%20` for spaces; some SDKs decode automatically. Do not double-decode input JSON or return raw/encoded documents from metadata-only calls. ListPolicies omits Description. Response RequestId and IAM namespace remain compatible with the existing role APIs. See the primary [Policy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_Policy.html) and [PolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_PolicyVersion.html) shapes.

## Explicitly bounded support

- Only customer policies in the local account `000000000000` exist. ARN shape is `arn:aws:iam::000000000000:policy/<name>`, with no region. IAM is global within a team database; process/database/token authorities provide team isolation. Do not derive account or authority from caller-provided labels, signature text or arbitrary ARN namespaces.
- Only policy Path `/` is supported, defaulting when omitted. Nonroot Path is explicitly unsupported501. PathPrefix accepts omission, empty or `/`; nonroot prefixes are unsupported501. PolicyName is1–128 ASCII alphanumeric or `_+=,.@-`, unique without case distinctions. Preserve original spelling and canonical saved ARN; do not normalize resource names as filesystem paths. Role names retain the narrower existing native subset.
- AWS ListPolicies defaults omitted Scope to All. This increment **rejects omitted Scope, All and AWS with501**, requiring explicit Local. It does not silently change the AWS default or return an invented AWS catalog. Any supplied OnlyAttached or PolicyUsageFilter, including false or PermissionsPolicy, is unsupported501. Policy tags and tag operations are unsupported.
- PolicyDocument permits tab, LF, CR and characters U+0020–U+00FF. The local raw document limit is32KiB of UTF-8. Apply the6144-character managed-policy limit to the rune count after `json.Compact`, retaining whitespace inside JSON strings. The existing128KiB total URL-encoded Query body cap remains. These bounds are narrower than the official131072-character document wire allowance; do not advertise complete AWS input compatibility. Use `core.ParsePolicy` and reject unsupported fields rather than accepting unevaluated permission semantics.
- Store at most1000 policy claims, **including retired claims**, and20000 attachment edges. Each policy retains at most5 versions. Each role has at most20 distinct managed attachments, separately from the existing10-inline-policy limit. The combined effective role policy remains at most100 statements and32KiB. These are explicit local bounds; the current official managed-policy role quota is20 by default, adjustable to25. See [IAM quotas](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_iam-quotas.html).
- Description is immutable, at most1000 characters. List metadata must not expose version documents. PermissionsBoundaryUsageCount is zero because boundaries are unsupported; AttachmentCount counts only implemented native-role edges. Never represent metadata-only CFN references as actual attachments.
- Lists use MaxItems default100, range1–1000. Marker is opaque and bound to operation, filters and relevant saved entity generation. IsTruncated requires a continuation Marker. Return deterministic local ordering without claiming AWS ordering or a multi-page snapshot. Wrong-action/filter/entity markers must fail before returning unrelated rows.

## Policy versions, ownership and effective permissions

CreatePolicy stores fresh PolicyId/generation and v1 as default. Subsequent versions receive monotonically increasing vN identifiers; deleting an older version does not renumber or recycle IDs. CreatePolicyVersion defaults SetAsDefault to false. The API version identifier grammar is `v[1-9][0-9]*(\.[A-Za-z0-9-]*)?`; well-formed nonexistent identifiers return NoSuchEntity. See [versioning](https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_managed-versioning.html).

DeletePolicyVersion rejects the current default with DeleteConflict409. DeletePolicy requires no attachments and no nondefault versions; successful deletion removes the remaining default. It never detaches roles or silently deletes other versions. Role deletion rejects remaining managed attachments. Retain existing native-role tombstone ownership. Attachment edges bind both role and policy generations, preventing deleted/recreated names or ARNs from adopting old authority.

For existing valid role and policy parents, repeated Attach is a no-op, and Detach of an absent edge is a no-op. These are deliberate local contracts, **not verified AWS differential behavior**; the retrieved primary API pages do not specify the absent-edge case. Missing role or policy is NoSuchEntity404. Duplicate CreatePolicy is EntityAlreadyExists409. No ClientToken or retry deduplication is added to version creation.

`iamSyncPolicy` must be the common materializer for inline and managed grants. Its union contains all inline statements and every attached managed policy's default version. Preserve explicit Deny precedence. Every inline Put/Delete, Attach/Detach and default switch uses this union; an inline update must never erase managed grants. Managed permissions do not alter the role trust document.

CreatePolicyVersion(SetAsDefault=true) and SetDefaultPolicyVersion update the default pointer and **every attached role aggregate in one transaction**. Any limit, corrupt row, query, ignored mandatory write, cancellation or commit failure rolls back the full version/default/edge/aggregate frame. Do not trim statements, update only some roles or acknowledge missing mandatory rows. Intentional duplicate/no-op paths remain distinct from mandatory row mutations. Retired legacy ownership and unrelated roles/teams must remain unchanged.

Existing CloudFormation `ManagedPolicyArns` references remain **metadata-only**. Do not migrate legacy CFN roles into native ownership or grant their referenced AWS policy documents. Lambda caller authorization snapshots the caller policy for CreateFunction/PassRole. The saved execution-role bridge fixes principal/region and authorizes each delegated operation against the current effective aggregate; it does **not** freeze that policy for the complete guest invocation.

## Admission and producer contracts

The managed Query branch belongs after existing root-method/body/auth/Version/duplicate/field checks but before the old unconditional RoleName guard. Preserve the nine existing native role/inline operations. A missing managed store must not disable old role APIs. Capture the caller decision before opening the store transaction. For AttachRolePolicy, DetachRolePolicy and ListAttachedRolePolicies, resolve the saved role ARN and authorize it, then pass that exact nonempty RoleId as the store expectedRoleID argument. Inside the same store transaction, compare the currently validated live RoleId before any policy read, no-op acknowledgement, mutation or list emission; a different incarnation fails NoSuchEntity with no state change. This binds admission to the previously authorized immutable role ARN without a nested database lookup. The optional zero-argument store port is for trusted in-process host composition only, never native Query admission. A deterministic real-store delete/recreate boundary test and an AST composition guard cover this seam; neither is a full HTTP concurrency proof.

The official [IAM service-authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_iam.html) assigns policy resources to policy/version operations, role resources to Attach/Detach/ListAttachedRolePolicies and `*` to ListPolicies. `iam:PolicyARN` is a condition key, not an extra independent policy-resource permission. Unsupported Conditions stay rejected by the core grammar. CreatePolicyVersion may set its default using that action; do not require an additional SetDefault action.

Use existing IAM error handling: invalid/malformed inputs400, explicit denial403, missing404, duplicate/delete conflict/quota409, unsupported501, infrastructure/corrupt state sanitized ServiceFailure500. Do not convert database errors into denial, not-found or success. Exact validation precedence and existing old-operation codes must be preserved by tests.

ListPolicyVersions returns **parent policy metadata, items and error** from the same read transaction. ListAttachedRolePolicies returns **parent role metadata, items and error** from the same transaction. Query uses those parent generations to validate cursors; it must not perform a separate lookup that races recreation. Store and Query owners agree the exact exported names/types before coding; this document does not impose a competing DTO set. All database calls carry the request context.

## Ownership and acceptance

| Owner | Exclusive responsibility |
|---|---|
| grader_http | Managed store, versions/attachments and shared role aggregate/ownership seams and native fault tests |
| team_host | Query admission/XML and actual local official-SDK acceptance |
| waf_console | Customer-policy/role-permission Cloudscape child and client; no fake AWS catalog |
| cfn_adapters | Finite lifecycle/aggregate models and meaningful controls, with explicit source correspondence |
| cfn_callback | This contract and primary-spec research |
| root | Shared composition/capabilities/OpenAPI/navigation/assets, integration, review and publication |

New managed-policy implementation is original TenkaCloud code. Preserve existing Role wire adaptation's Kumo MIT copyright and notice; do not replace its provenance or adopt an emulator engine. Do not edit external Issue51 S3 multipart work. Tests may call existing S3 APIs as permission oracles.

Acceptance requires two authenticated local CLI processes with separate persistent DB/token authorities: create/attach to two native roles; verify effective Allow and inline Deny; create nondefault without changing authority; default flip across both roles; quota/fault rollback; detach/version/delete cleanup; restart persistence and unaffected peer/role state. Query tests cover literal plus/percent/document encoding, omitted-vs-explicit unsupported filters, pagination generation mismatch, exact response omissions and malformed/duplicate fields. The console must use the same native state and clear stale scoped responses. Official SDK success must be observed, not inferred from DTOs or stubs.

Finite models must cover stored aggregate/default/edge effects and meaningful injected defects, not just altered ghost bookkeeping. They do not prove JSON/UTF-8/IAM parsing, real AWS equivalence, crypto, SQL or React refinement. Whole race/build, one fresh profile passing both90% floors, mandatory SDK/browser tests, formal/application gates, licenses/security and exact-HEAD CI remain required before completion. This P0 document itself claims none of those executions.
