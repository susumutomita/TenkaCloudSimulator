# Service specification and verification traceability

## Current public source snapshot: 2026-11-08

Baseline: [`835853a2eddf95c85a9a8bbb9ad3a6d9ba0b7152`](https://github.com/susumutomita/TenkaCloudSimulator/commit/835853a2eddf95c85a9a8bbb9ad3a6d9ba0b7152).
This is an index of bounded contracts and source evidence, not a service completion
percentage or a claim that every named test was rerun by this documentation task.
Use the [development roster](development-status.md) for dated integration records
and the [README service table](../README.md#supported-services) for entry points.
The November 4 matrix below is historical and must not override this snapshot.

| Contract at this baseline | Current source and acceptance references | Explicit boundary |
|---|---|---|
| Native AWS services: S3, SSM, IAM, Lambda, CloudFormation, API Gateway, SNS, SQS, Logs, Secrets Manager, KMS and DynamoDB | [Service table](../README.md#supported-services), [backlog acceptance](acceptance/backlog-services.md), [IAM policies](formal-iam-managed-policies.md), and the service-specific guides linked there; implementation and tests in `internal/aws/` and `test/e2e/` | Each guide defines operations, fields, quotas and exclusions. API support does not imply a console, CloudFormation adapter or completed Challenge. Dummy practice data only. |
| Native STS and CloudFormation event lifecycle | [STS contract](sts-native.md), [STS model](sts-formal-candidate.md), [event model](formal-cfn-event-history.md), `internal/aws/sts_*.go`, `internal/aws/cloudformation_events*.go` | Local SigV4 is verified only on the documented native surfaces. No real AWS identity, general federation or full asynchronous AWS lifecycle. |
| Battle value intrinsics | [Bounded intrinsics](cloudformation-intrinsics.md), `internal/aws/cloudformation_intrinsics*.go`, [PR #443](https://github.com/susumutomita/TenkaCloudSimulator/pull/443) | Select/GetAZs/Base64 and escaped scalar Sub do not enable the original full Battle stack. EC2, InstanceProfile, public SSM AMI lookups and Linux execution remain unsupported. |
| CloudFormation local SSM parameter values | [Typed parameter contract](cloudformation-ssm-parameters.md), [snapshot model](formal-cfn-parameter-snapshot.md), `internal/aws/cloudformation_ssm_parameters.go`, [PR #445](https://github.com/susumutomita/TenkaCloudSimulator/pull/445) | Two typed carriers resolve local Standard String values with scoped permission checks and saved snapshots. An Image ID carrier does not verify an AMI, execute EC2, fetch public AWS parameters or enable full Battle. |
| AWS model-driven catalog | [Catalog contract](aws-catalog.md), [generated service table](aws-catalog-services.md), `internal/aws/catalog/selection.json`, `internal/aws/catalog/*_test.go` | Configuration metadata for 112 additional AWS services. No workload execution, data planes, external delivery or unlisted operations. |
| Google Cloud and Azure storage | [Cross-provider official SDK acceptance](acceptance/crossprovider-official-storage.md), [Cloud Storage](gcp-storage.md), [Blob Storage](azure-blob.md), [provider console acceptance](acceptance/crossprovider-console.md) | Bounded object/blob state and provider-specific authority in synthetic scopes, not general cloud tenancy or the full provider APIs. |
| Google Cloud catalog | [Catalog contract](gcp-catalog.md), `internal/gcp/catalog/selection.json`, `internal/gcp/catalog/*_test.go` | Configuration metadata for 40 APIs from pinned discovery documents. No cloud workload execution. |
| Azure Resource Manager catalog | [ARM contract](azure-catalog.md), `internal/azure/arm/selection.json`, `internal/azure/arm/*_test.go` | Eleven provider namespaces including resource groups, with synthetic subscription/tenant scope. No Azure RBAC, Entra tokens, data planes or real provisioning. |
| Sakura | [Disk subset](sakura.md), [SDK compatibility](sakura-sdk-compatibility.md) | Explicit opt-in local Disk configuration metadata only. Not Server, Switch/NIC, object storage, a VM or block storage implementation. |
| Startup, console sessions and API reference | [Startup](start.md), [browser sessions](console-sessions.md), [token-file login](login-token-file.md), [API reference](api-reference.md) | Present in this base; the November 4 pending-PR descriptions below are historical. Authenticated provider-console equivalence and actual Dia/1Password behavior remain unverified. |
| Standalone release pipeline | [Release contract](releases.md), [PR #444](https://github.com/susumutomita/TenkaCloudSimulator/pull/444) | Pipeline source is merged. This document does not assert a published release, tag, signing/notarization or deployment. |

The executable [formal registry](../formal/models.json) contains **58 baselines
and 489 required mutation controls (547 executions)** at this exact base.
[Formal validation](formal-validation.md) describes the bounds, implementation
scope, sensitivity requirements and immutable-input applicability policy. These
are finite abstraction checks, not Go refinement proofs or real-cloud differential
tests. A model count is not a count of completed services. The November 4 total
of five models is preserved only as historical evidence.

All code, assets, dependencies, model/config files and workflows in this PR match
the baseline. Only the two documentation files and their applicability record
change. The current-HEAD whole CI, both coverage floors, SDK/browser/platform
checks and Formal validation must pass before merge; subsequent main checks are
separate evidence. For unserved products and why they remain excluded, use the
[coverage assessment](coverage-assessment.md).

Separate team processes, databases and token authorities remain required. Neither
catalog metadata nor a successful API call establishes original problem grading.
No real cloud calls or authenticated cloud-console comparison were performed for
this documentation update.

## Historical matrix: 2026-11-04 at `2d8a41a0`

Everything from this heading onward describes the named November 4 revision.
Words such as "main", "current", "pending" and "next" are relative to that date,
not today's branch or assignments. Later native multi-cloud storage, service
catalogs, STS, managed policies, startup/session features and additional formal
models supersede the relevant older exclusions. Relative guide links open the
maintained guides; the immutable baseline identifies the historical code.

**Public snapshot: 2026-11-04 12:35 UTC. Baseline
`2d8a41a0cb576bd42561a7162d35129f44979c08`.** This matrix describes that public
main source, not a union of unpublished service branches. The linked [development roster](development-status.md) now contains later
public integration records; it is not frozen to this historical baseline. Local-only implementation histories and executor receipts are not
part of this publication.

Main [ci41828639809](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41828639809),
[Formal41828639605](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41828639605)
and [Push on main41828638968](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41828638968)
succeeded. This documentation update executed no Go, SDK, browser, TLC or cloud
checks; named tests below are source pointers, not new execution results.

**`actualAWSComparison=false` throughout.** Public official specifications and
local tests establish limited compatibility, not observed equivalence to an
authenticated AWS console or real AWS endpoint. API versions below identify wire
protocols, not the latest document revision. No Azure/GCP native service is
implemented in this main. Only hello-world has actual host
import→deploy→participant operation→score→cleanup evidence.

### Evidence and model boundary

Main registers **five** finite models: ObjectStore, S3Versions, StackOwnership,
NativeIAMOwnership and RESTDeploymentSnapshot. PR310 added the immutable registry
and changed-blob applicability gate; PR314 added narrow unchanged-module
annotation classification. Neither change adds a service or proves Go refinement.
Finite checks require declared bounds and intended invariant-breaking mutants.
Unknown runtime changes still need a relevant model; prose is not an exemption.
Workflow runs do not establish configured repository branch protection.

Public candidates remain separate from main. Historical CI on base55a or6ff
cannot validate restacked source on2d8. Use the linked roster for exact public
HEADs, successful/failed/running workflows and pending integration. Do not add
candidate model counts, restricted coverage or local source receipts to main's
totals; do not treat code-scanning success as a passing normal CI.

### Implemented main contracts at `2d8a41a0`

All main services use the same provider SQLite state, local host authentication
and bounded identity-policy evaluator. SDK signatures are dummy `TCSIM` scope
selection, not real SigV4 verification. Separate teams require separate
processes, databases and token authorities; principal selection is not tenancy.

#### S3

**Wire/subset:** path-style REST/XML (no date-version query). ListBuckets,
CreateBucket, HeadBucket, GetBucketLocation, DeleteBucket, ListObjectsV2,
PutObject, GetObject, HeadObject, DeleteObject; Get/PutBucketVersioning,
ListObjectVersions; Get/Put/DeletePublicAccessBlock; Get/Put/DeleteBucketTagging.
Four local regions. Object keys are at most1024 UTF8 bytes. Version ID and
key/version-marker selectors, prefix/delimiter/max-keys/url encoding are bounded;
versioning Status is Enabled/Suspended; public access has four Boolean fields;
bucket tags are at most50 key/value entries. See [full subset](s3.md).

**Source:** `internal/aws/s3.go`, `s3_versions.go`, `s3_configuration.go`,
`s3_list_versions.go`, `s3_tags.go`; current object relation `internal/core/store.go`.
Create→put/replace→marker/version deletion→empty-history bucket deletion must be
atomic; failure cannot orphan retained history or change current view partially.
Explicit Deny must precede mutation.

**Models:** ObjectStore:2 buckets×2 keys, Boolean allow/deny; TypeOK,
NoOrphans, NoUnauthorizedMutation, DenyWins. S3Versions:1 bucket×2 keys with
history length0..3 per key; NoHistoryOrphans and CurrentMatchesLatest. Empty-history guard
mutants must fail. Suspension, quotas, XML, payload bytes and SQL errors are not
modeled.

**Named tests:** `TestRelationshipAndAtomicConditions` (`internal/core/store_test.go`),
`TestS3SuspensionReplacesNullAndRetainsVersions`,
`TestS3VersionTransactionsAndConditionalWrites` (`internal/aws/s3_versions_test.go`),
`TestS3FailedNullMutationPreservesHistoryAndCurrent` (`s3_faults_test.go`),
`TestS3VersionsNativeProtocolAndIAM` (`internal/server/s3_versions_test.go`).
Evidence: main CI; wire and actual browser/SDK entrypoints live in `test/e2e/check.py`.

**Unsupported/unmodeled:** multipart, ACL/bucket policies, Object Lock, lifecycle,
replication, MFA Delete, account-wide public-block policy; propagation is immediate.
**Next meaningful check:** model Suspended null replacement plus failed-write
rollback/quota accounting; mutate replacement to drop a numbered version and
require retained-history preservation to fail. Keep SQL-fault tests independent.

Primary specs: [REST API](https://docs.aws.amazon.com/AmazonS3/latest/API/Welcome.html),
[PutBucketVersioning](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketVersioning.html),
[ListObjectVersions](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectVersions.html).

#### Systems Manager Parameter Store

**Wire/subset:** JSON1.1 POST, `AmazonSSM.<operation>` (service model2014-11-06).
PutParameter:Name/Value/Type/Description/Overwrite/Tier/DataType; GetParameter:
Name/WithDecryption; GetParameters:Names/WithDecryption; DeleteParameter:Name;
DescribeParameters:MaxResults/NextToken; GetParametersByPath:Path/Recursive/
MaxResults/NextToken/WithDecryption. Standard String/StringList/text only;
SecureString/KMS, labels, version and ARN selectors are unsupported, not decrypted.

**Source:** `internal/aws/ssm.go`, `parameters.go`. Create/overwrite increments saved
version; unauthorized or invalid writes leave the old value. Region/path and
per-operation IAM resource checks control retrieval and deletion.
**Model:** none dedicated. StackOwnership only abstracts stack resource claims;
it does not prove parameter versions or pagination.
**Tests:** `TestSSMLifecycleAndIAM`, `TestSSMPaginationAndValidation`
(`internal/server/ssm_test.go`), `TestStackPersistenceRollbackAndQuotas`
(`internal/aws/cloudformation_test.go`). Evidence: main CI and hello-world local
host lifecycle, not other Challenge completion.
**Next:** two parameters/two versions/two principals model, overwrite rejection
preserves value/version and denied reads reveal no value. Add cursor/query-change
and overwrite/rollback contract tests; do not call generic ObjectStore a SSM proof.
Primary: [PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html),
[GetParametersByPath](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParametersByPath.html).

#### IAM roles and limited identity policies

**Wire/subset:** Query/XML Version2010-05-08, root POST selected by IAM scope.
Create/Get/List/DeleteRole; Put/Get/DeleteRolePolicy; ListRolePolicies;
UpdateAssumeRolePolicy. Root path and restricted role names; Description and
MaxSessionDuration metadata, canonical JSON service-principal trust with
sts:AssumeRole and explicit Deny. Inline policy supports bounded Action/Resource
matching, not Conditions. List roles/policies uses Marker/MaxItems. [Details](iam.md).

**Source:** `internal/aws/iam_query.go`, `iam_roles.go`, `iam_legacy.go`,
`internal/core/policy.go`. Create/retire/native claim and inline-policy aggregate
change atomically. Native and legacy identities cannot adopt each other's names;
a retired native claim prevents trust bypass through a legacy identity.
**Model:** NativeIAMOwnership:2 case-folded names, active/retired claims,
legacy identities, permission Boolean; TypeOK/OwnershipExclusive; legacy guard
removal mutant. Does not model trust parsing or request IAM evaluation.
**Tests:** `TestNativeIAMAndLegacyIdentityCreationAreAtomic`,
`TestNativeIAMRoleTrustPolicyAndRetiredOwnership` (`iam_roles_test.go`),
`TestNativeIAMStorageFailuresDoNotCommitPartialChanges` (`iam_faults_test.go`),
`TestNativeIAMQueryLifecyclePaginationAndAuthorization` (`iam_query_test.go`),
`TestUnsupportedPolicyNeverSilentlyAccepted` (`internal/core/policy_test.go`). Main CI.
**Unsupported:** STS/users/groups/managed policies/SCP/boundaries/advanced trust;
CFN roles remain legacy aggregates and do not appear in native role picker.
**Next:** role revoke/delete→next Lambda invoke denied model, including stale
role reference/recreated RoleId; mutate trust recheck removal. Bind to actual
saved-role execution tests, not just role CRUD.
Primary: [CreateRole](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreateRole.html),
[execution role](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html).

#### Lambda

**Wire/subset:** REST/JSON2015-03-31. Create/List/GetFunctionConfiguration,
UpdateFunctionCode, Invoke(RequestResponse/DryRun), DeleteFunction; bounded
AddPermission/GetPolicy/RemovePermission for API Gateway service and local API
SourceArn. Python3.12 ZIP, Handler/Role/Timeout/MemorySize128/Environment/Tags;
RevisionId protects code updates. [Exact bounds](lambda.md).
**Source:** `internal/aws/lambda.go`, `lambda_permissions*.go`, `lambda_bridge.go`,
`internal/workload/`; native role trust and iam:PassRole are checked separately.
Saved execution role, function region and trusted callback determine service
authority. Invocation caller cannot supply SDK credentials/identity/network.
SDK bridge in main is SSM get_parameter/get_parameters and S3 get/put/delete_object.
**Models:** no dedicated runtime, role-capture or cancellation model. REST snapshot
and native IAM models cover only their own abstract state.
**Tests:** `TestLambdaUpdateCannotRestoreStaleConfiguration`
(`lambda_store_test.go`), `TestLambdaCapabilityUsesSavedRoleAndNativeState`
(`lambda_bridge_test.go`), `TestSDKCancellationDoesNotWaitForBusyDatabaseOrStartLateMutation`
(`lambda_bridge_cancel_test.go`), `TestActualLambdaSDKUsesExecutionRoleAndSharedSSMAndS3`
(`internal/server/lambda_bridge_test.go`). Main CI; actual fixed CPython/WASI runs,
not fabricated responses.
**Unsupported:** versions/aliases, async, images/layers, VPC, CloudWatch delivery,
configuration update, general boto3/network. Context, language and memory/time
bounds deliberately differ from AWS. API response success is not grading success.
**Next:** bounded capture→policy revoke→queued/cancelled callback state model:
no late mutation, no caller authority substitution, no result after sticky host
failure. Pair mutation controls with actual guest/database-lock tests.
Primary: [CreateFunction](https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunction.html),
[Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html).

#### CloudFormation

**Wire/subset:** Query/XML Version2010-05-15; CreateStack/DescribeStacks/ListStacks/
DeleteStack, inline JSON/YAML TemplateBody, String parameters, supported Ref/
GetAtt/Sub/DependsOn and bounded Outputs. Synchronous local lifecycle differs
from AWS asynchronous creation. Types: IAM::Role (legacy aggregate),
SSM::Parameter, S3::Bucket, inline Python Lambda::Function, ApiGateway::RestApi/
Resource/Method/Deployment/Stage and Lambda::Permission. [API property subset](cloudformation-rest.md).
**Source:** `internal/aws/cloudformation.go`, `s3_cloudformation.go`,
`lambda_cloudformation.go`, `apigateway_cloudformation.go`. Create/claims share a
transaction; delete checks current ownership/fingerprint and underlying IAM
before reverse cleanup. Drift/SQL failure cannot delete unrelated native rows.
**Model:** StackOwnership:2 owners×2 names; TypeOK/OwnershipExclusive, absent-but-
claimed resources remain reserved; reclaim mutant. It abstracts all SQL,
resource properties, reverse cleanup and actual CFN asynchronous states.
**Tests:** `TestStackDeletionValidatesResourceClaims` (`cloudformation_test.go`),
`TestDelayedDeleteCannotRemoveReclaimedResources` (`stack_concurrency_test.go`),
`TestCFNAPISharedStateRollbackDriftAndIsolation`,
`TestCFNAPITransactionHelpersNeverCommitOrRollbackCaller` (`apigateway_cloudformation_test.go`),
`TestCFNAPIPersistenceFailuresAreAtomic` (`apigateway_cloudformation_faults_test.go`),
`test/e2e/cloudformation_rest.py` two real local processes/restart/HTTP/cleanup. Main CI.
**Unsupported:** UpdateStack/change sets/custom resources/WAF/events/Retain,
service-role assumption and full catalog grading. Only hello-world has host
import→start→operate→score→cleanup evidence; API support does not enable others.
**Next:** model reverse cleanup with missing/changed/unowned child and injected
failure; assert all-or-nothing ownership release and no foreign deletion.
Primary: [CreateStack](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStack.html),
[DependsOn](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-attribute-dependson.html).

#### API Gateway REST

**Wire/subset:** REST/JSON model2015-07-09 (paths have no date prefix).16 operations:
Create/Get/List/DeleteRestApi; Create/Get/List/DeleteResource; Put/GetMethod;
PutIntegration; Create/GetDeployment; Create/Get/DeleteStage. Explicit REGIONAL,
NONE, POST AWS_PROXY local Lambda; name/description/tags, pathPart,
requestParameters for supported proxy path and stage variables only.
[Methods/fields/limits](apigateway.md). Lambda service grant is separate.
**Source:** `internal/aws/apigateway_{control,resources,deployments,routing,invoke}.go`,
`apirouting/`. Draft route→immutable deployment→stage→saved-role guest invocation.
More-specific path cannot fall back to broader ANY on missing method. Local
transport strips simulator/cookie/AWS credentials and returns nonexecuting
attachments; it is not anonymous AWS internet application hosting.
**Model:** RESTDeploymentSnapshot:1 API×2 deployments×2 handler values×1 stage;
TypeOK/SnapshotsFrozen/NoStageOrphans; rewrite-old-snapshot mutant. Does not model
wire parsing, route precedence, service grants, team authentication or guest code.
**Tests:** `TestPrivateRESTDeploymentFreezesIntegratedMethodsAndStages`,
`TestPrivateRESTStageRoutingUsesFrozenSpecificResourceBeforeMethod`
(`apigateway_store_test.go`), `TestPrivateRESTInvokesActualSavedRolePythonAfterServicePermission`
(`apigateway_invoke_test.go`), `TestRESTAndLambdaPolicyRequireConfiguredParticipantAuthority`
(`apigateway_dispatch_test.go`), `TestRESTDamagedPersistedDocumentsFailRatherThanInventState`
(`apigateway_damaged_state_test.go`). Main CI and `test/e2e/apigateway.py`.
**Unsupported:** v2/authorizers/API keys/usage plans/binary/mapping/cache/domains/
VPC links/stage editing/method deletion/WAF. **Next:** route specificity+ANY and
permission revocation model; mutate broad-route fallback and stale service grant
acceptance, check actual repeated-query and active-content response negatives.
Primary: [CreateDeployment](https://docs.aws.amazon.com/apigateway/latest/api/API_CreateDeployment.html),
[Lambda proxy contract](https://docs.aws.amazon.com/apigateway/latest/developerguide/set-up-lambda-proxy-integrations.html).

### Public increments awaiting integration

| Public work | Boundary before a support claim changes |
|---|---|
| [Startup PR308](https://github.com/susumutomita/TenkaCloudSimulator/pull/308) | Fresh current-base build, authenticated persistent state/token handling, actual TTY/CI suppression, signals and finite visibility model. Cross-build is not macOS/Windows execution. |
| [DynamoDB PR309](https://github.com/susumutomita/TenkaCloudSimulator/pull/309) | Resolve failed whole CI; validate item-size/key/value rules, table generations/ownership, atomic persistence and a relevant model with actual native SDK/2team cases. No DynamoDB support in main yet. |
| [API docs PR311](https://github.com/susumutomita/TenkaCloudSimulator/pull/311) | Preserve real Scalar requests, supported wire examples/errors, full response access and offline/keyboard/mobile behavior after current-base integration. |
| [Browser logout PR312](https://github.com/susumutomita/TenkaCloudSimulator/pull/312) | Browser credential revocation, parent/session separation, replay denial, two teams and actual account-menu keyboard flows remain distinct from host session authentication. |
| [SSM PR313](https://github.com/susumutomita/TenkaCloudSimulator/pull/313) | Proposed dedicated latest-row/authorization model and native contract tests require their own current-HEAD CI. A dedicated SSM model is not counted in main until merge. |
| [SQS PR315](https://github.com/susumutomita/TenkaCloudSimulator/pull/315) | Resolve failed native/formal CI; establish supported queue/receipt operations, timeout/cleanup transitions, isolated real state and explicit unsupported behavior. |
| [WAF Draft PR50](https://github.com/susumutomita/TenkaCloudSimulator/pull/50) | Held/unmerged status only; no new verification or main-support claim. |
| [Azure product plan316](https://github.com/susumutomita/TenkaCloudSimulator/issues/316) | Dated product inventory, not implemented services; classify product/tool/suite/lifecycle, tenant/subscription/RG/RBAC and many-to-many RP dependencies before defining bounded APIs and portal/Fluent flows. |

### Acceptance before changing support claims

Record immutable source/model/config hashes, exact operation/field subsets and
primary references. Require meaningful positive, authorization/wrong-scope,
malformed-input, persistence/restart, rollback, two-team, cleanup and UI/API shared
state tests. Model changed state/authority/dependencies with explicit limits and
intended counterexamples. Human semantic correspondence review remains necessary;
a model alone does not establish its relationship to every implementation path.

Use ordinary merge integration to preserve current public contracts, shared
service state, notices and distinct models. Require fresh whole-module race,
statement and unfiltered-source90%, real SDK/browser/axe/no-external-request,
license/security, cross-build and exact-HEAD hosted CI/review. Restricted profiles
cannot fill omitted whole coverage. Verify subsequent main CI separately.
Unsupported operations fail explicitly; no additional Challenge problem is
enabled solely by API200, metadata CRUD or a screenshot. Separate
process/database/token authorities remain the isolation unit, and actual AWS
comparison stays unverified until permitted and executed.
