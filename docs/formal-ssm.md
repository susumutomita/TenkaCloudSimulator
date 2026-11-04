# SSM latest-parameter finite contract

`formal/SSMParameterState.tla` is a bounded safety model of the **existing native
SSM authorization gate plus ParameterStore's latest-row operations**. Reviewed
source baseline: `6ffbd1881dad3589469fdf1809654b7483bd6ac4`.

This registry integration is **stacked on unmerged formal PR #310**, exact base
`ebf5b5d29c3616c57afcd6a6542829d8a741218c`. It must not be treated as a
main-based or merged change. The applicability record deliberately uses that
immutable dependency base; regenerate it against the actual reviewed base after
any dependency rebase, squash merge or restacking. Source/model/native test bytes
from the standalone SSM candidate are preserved. The model adds no
runtime API, stored history, authorization implementation or new dependency.
`ParameterStore` itself is a trusted internal storage adapter; authorization is
performed by `Gateway.serveSSM` before its native operations. This model does
not imply that direct internal store calls enforce IAM.

## Primary contract references

References were browsed on **2026-11-04**. The official AWS SDK Go v2
[SSM service metadata](https://raw.githubusercontent.com/aws/aws-sdk-go-v2/main/service/ssm/api_client.go)
declares service API version **2014-11-06** and JSON 1.1. That version is service
metadata, not an extra request-body field. The simulator uses root POST with
`X-Amz-Target: AmazonSSM.<Operation>`.

| Official source | Relevant contract | Bounded local correspondence / gap |
|---|---|---|
| [PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html) | Creates/updates parameters; Overwrite defaults false; updating yields a new version; an unqualified read uses the latest value | Existing `(region,name)` row is refused without Overwrite; accepted writes start at version 1 or increment the latest row. The simulator retains only that row, not AWS's retained version history |
| [GetParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameter.html) | Reads the requested parameter; AWS additionally accepts labels, versions and some ARN/shared-parameter forms | Local Get uses exact validated name within the selected region. Labels, `name:version`, ARN selectors and shared cross-account parameters are rejected and are not modeled |
| [DeleteParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DeleteParameter.html) | Deletes a named parameter; missing names fail; AWS documents a wait before same-name recreation | Local Delete removes the exact regional row and missing rows fail. Local recreation is immediate and starts again at version 1; AWS's 30-second recreation guidance is not implemented or modeled |
| [SSM API reference](https://docs.aws.amazon.com/systems-manager/latest/APIReference/Welcome.html) | Reference document reports publication date October 7, 2026 when retrieved | Retrieval/publication dates identify the reference examined, not a claim that every AWS behavior or future revision is supported |

Only validated Standard `String`/`StringList` latest-row inputs are abstracted.
SecureString/KMS, Advanced tier, AMI validation and other unsupported settings
are not converted into successful model transitions.

## Bounds and properties

The domain is **two regions × two names, two abstract values, and latest-row
versions 1–2**. This is four possible parameter addresses, not four teams.
Boolean allow and explicit-deny inputs represent an already evaluated identity
policy; the model does not parse IAM or match ARNs. `Permitted` requires allow
and absence of explicit Deny.

An otherwise successful overwrite at version 2 is outside the finite explored
prefix. It is disabled as an **exploration cutoff**, not represented as an AWS
error or runtime version limit. Delete/recreate, denied requests, reads and
no-overwrite refusal are still explored. No constraint, view or fairness option
silently filters the state graph. Safety checks do not establish liveness.

`last.before` is a single ghost pre-event snapshot used to check frame conditions.
It is not stored API history, a version-selector implementation or a promise
that old values can be fetched.

| Invariant | What is checked in this finite abstraction |
|---|---|
| `TypeOK` | Latest entries and event observations remain in the declared finite domains |
| `DeniedPreservesState` | Missing allow or explicit Deny preserves every modeled row, returns no value and reports no successful write version |
| `OverwriteRefusal` | An existing entry without Overwrite is unchanged and reports the modeled already-exists result |
| `PutVersion` | A successful write stores the requested abstract value, starts at 1 or advances the previous latest version by one, and reports that same version |
| `DeleteEffect` | A successful deletion leaves its requested entry absent |
| `OtherKeysUnchanged` | An operation cannot mutate other region/name addresses |
| `ReadIsolation` | An authorized Get observes exactly the requested region/name, including absence, value and latest version, without mutation |

## Source and existing test correspondence

The focused `internal/server` tests whose names begin `TestSSM` were executed
normally and with `-race`, using `GOMAXPROCS=1` and Go `-p 1`. This includes the
two existing SSM tests and the new test below. The listed CloudFormation tests
were source-read only, not re-executed. No full Go suite, Lambda guest VM,
browser or live-AWS run is implied by this task or by TLC results.

`TestSSMFormalLatestRowIsolationAndExplicitDeny` in
`internal/server/ssm_model_contract_test.go` exercises the actual native HTTP
handler with four regional/name cells and distinct values/versions (1, 2, 3,
and 4). An identity has both Allow and explicit Deny for Put/Get/Delete. After
each rejected overwrite, Delete and Get, administrator reads verify **all four**
values, versions, exact names and regional ARNs remain correct. The same test
checks no-overwrite refusal, an accepted single-cell overwrite, regional
Delete, and immediate recreation starting at 1. Runtime versions above 2 also
show that the model's finite cutoff is not an API limit. This test connects
selected finite properties to actual code behavior; it is still not refinement
verification or an exhaustive runtime/concurrency proof.

| Source seam | Existing concrete test names | Evidence scope and gap |
|---|---|---|
| `internal/aws/ssm.go`: `serveSSM` authorizes before Put/Get/Delete branches | `internal/server/ssm_test.go`: `TestSSMLifecycleAndIAM` | The existing reader Put refusal preserves the later expected version. New `TestSSMFormalLatestRowIsolationAndExplicitDeny` additionally checks explicit-Deny Put/Delete/Get after valid Allow, with every distinct cell unchanged after each refusal |
| `internal/aws/parameters.go`: `Put` checks existing regional/name row before INSERT/UPDATE; transaction rollback handles refused overwrite | `TestSSMLifecycleAndIAM` | Create reports 1; duplicate no-overwrite fails; read still reports original value/version; accepted overwrite reports 2 |
| `ParameterStore.Put`: omitted description preserves stored metadata | `TestSSMLifecycleAndIAM` | Existing runtime assertion covers description preservation. Description/type/timestamp are deliberately not represented in this small model |
| `ParameterStore.GetContext` and `ListContext`: WHERE region, then exact name match | `TestSSMLifecycleAndIAM`; `TestSSMPaginationAndValidation` | Existing tests cover a foreign-region absence, multiple names, missing names and invalid selectors. New `TestSSMFormalLatestRowIsolationAndExplicitDeny` checks distinct values/versions and regional ARNs for every region/name combination |
| `ParameterStore.Delete`: DELETE WHERE region and name; zero affected rows fail | `TestSSMLifecycleAndIAM` | Deleting east does not delete the same name in Tokyo; repeated deletion and subsequent east read fail. New `TestSSMFormalLatestRowIsolationAndExplicitDeny` also checks immediate recreation version 1 and all other cells remaining unchanged |
| Shared native store persistence and ownership use | `internal/aws/cloudformation_test.go`: `TestStackPersistenceRollbackAndQuotas`, `TestStackDeletionValidatesResourceClaims` | These two source-read-only tests concern retained SSM rows across restart, quotas and CFN claims. CFN ownership, 1000-row quota, persistence and crash behavior are outside this model |

`GetParameters`, `DescribeParameters`, path recursion, pagination/token scope,
all JSON validation, policy snapshot changes, real concurrent interleavings,
SQL failures/crashes, host/team authentication and SQLite implementation details
remain outside the abstraction. This is neither a live AWS differential test
nor a refinement proof from Go/SQL into TLA+.

Focused runtime commands also completed successfully (no production Go edits):

```sh
GOMAXPROCS=1 go test -p 1 ./internal/server -run '^TestSSM' -count=1 -timeout=40s
GOMAXPROCS=1 go test -p 1 -race ./internal/server -run '^TestSSM' -count=1 -timeout=45s
```

Observed package execution times were 1.555s normally and 11.065s with race.
These targeted results do not imply full-module coverage, security, E2E or CI
success.

## Actual TLC and mutation sensitivity

The pinned JAR was checked against `formal/tla2tools.sha256`. Executed with
Java `-Xmx512m -XX:ActiveProcessorCount=1`, TLC `-workers 1`, copied model/config
in isolated scratch directories, and all seven named invariants enabled.

- Positive model: exit **0**, exhaustive completion, **6,679,297 generated /
  58,001 distinct states**.
- Five negative controls: each exit **12**, a state trace, and the **specified
  named invariant violation**. A parser error, Java/runtime exception, timeout
  or an unrelated invariant is not accepted as a negative-control success.

| Mutation | Required counterexample |
|---|---|
| Ignore explicit Deny in the permission predicate | `DeniedPreservesState` |
| Accept an existing Put without Overwrite | `OverwriteRefusal` |
| Keep the old version on accepted overwrite | `PutVersion` |
| Read the other region for the requested name | `ReadIsolation` |
| Read the other name in the requested region | `ReadIsolation` |

The first draft's positive run raised a non-record `None.version` evaluation
error. The cutoff was corrected to an explicit `IF` guard; that failed run was
not counted as a sensitivity counterexample. The results above are from the
corrected, completed runs.

To run the positive model with an existing checksum-verified absolute
`TLA_JAR` path, copy the two files to a scratch directory and run there:

```sh
SSM_MODEL_TMP=$(mktemp -d)
cp formal/SSMParameterState.tla formal/SSMParameterState.cfg "$SSM_MODEL_TMP/"
(
  cd "$SSM_MODEL_TMP"
  java -Xmx512m -XX:ActiveProcessorCount=1 -cp "$TLA_JAR" tlc2.TLC \
    -workers 1 -cleanup -config SSMParameterState.cfg SSMParameterState
)
```

This candidate registers `SSMParameterState` and all five exact mutation edits
in `formal/models.json`, alongside the unchanged original five models. The
runner and applicability checker implementations remain unchanged. The gate's
inventory self-test now requires the exact six model IDs; additional negative
tests reject a missing/changed SSM config, dirty config input after HEAD capture,
and a counterexample for a different invariant. Their classifier fixtures are
not passed off as actual TLC runs.

`formal/applicability.json` classifies and hashes every changed blob relative to
the stacked dependency base. Native test/model/config paths are bound to the
seven named SSM properties and their limits. Registration and a valid declared
scope do not themselves prove refinement, human acceptance, hosted CI success,
repository merge protection or main integration. Exact-HEAD applicability and
the complete registered model/sensitivity suite must pass for the final
candidate before claiming a required-gate result.
