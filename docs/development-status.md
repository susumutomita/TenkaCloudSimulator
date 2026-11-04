# Development roster

**Snapshot:2026-11-04 06:10 UTC. Update owner: the integration owner.**
Workers report claim/handoff/test/blocker changes to that owner; update this one
page when facts change. It is a manually reviewed snapshot, not a generated live
agent counter. Public state was checked with GitHub open Issues/PRs; local state
below comes from worker handoffs to the integration owner. Personal names not
provided are unknown; role/agent identifiers are used instead.

Use the [parallel development rules](contributing/parallel-development.md) before
starting work and the [shared service contracts](design/0005-parallel-service-contracts.md)
for file boundaries. Do not edit another owner's shared files. Existing user
authorization applies; this roster itself grants no new permission.

## Integration owner and six internal workers

These six workers are acknowledged internal assignments, in addition to the
integration owner. They do not imply that all six are executing a command at
every instant. Native API Gateway is implemented locally and in a public PR;
the dependent WP lifecycle is not complete.

| Owner / scope | Issue / public PR | Actual state and evidence | Dependencies, next action and unknowns |
|---|---|---|---|
| Integration owner: native API Gateway and shared integration | [#37](https://github.com/susumutomita/TenkaCloudSimulator/issues/37), [PR45](https://github.com/susumutomita/TenkaCloudSimulator/pull/45) | PR45 merged as `48a54cf33c639aa39d6b465da81e6a9fc6f59fcc` after all eight checks, code/security completion, bot approval reaction and no unresolved threads at exact `a4c87e9`; main CI41782851655/CodeQL41782851308 are still running | Own common registration/runtime/server/OpenAPI/navigation/assets/CI/catalog composition. Recheck actual PR HEAD, required checks and reviews before merge, then verify main CI |
| `waf_service`: regional WAF backend | [#38](https://github.com/susumutomita/TenkaCloudSimulator/issues/38), [Draft PR50](https://github.com/susumutomita/TenkaCloudSimulator/pull/50) | Native/backend/UI integration is in Draft PR50 at `6726c57`; subsequent API-documentation/formal-gate integration is local with focused race, authority and actual browser checks; original WP HTTP Count/leak→Block403/safe200 check also passes; Lambda-grant removal confirms WAF stops before Lambda authorization. This is not a merge or full-WP claim | Depends on #37 stage execution contract; coordinate native CRUD/IAM/lock tokens, evaluator, CFN/UI schema and notices. Final integrated HEAD/CI remains pending |
| `cfn_adapters`: CFN service adapters and custom orchestration, one owner | [#39](https://github.com/susumutomita/TenkaCloudSimulator/issues/39), [#44](https://github.com/susumutomita/TenkaCloudSimulator/issues/44); no separate public PR observed | Superseded `163c567` is replaced by production fix `0da2546` plus test-only `59e726a`: independent store repro confirms failed-create physical identity, custom ownership collision and callback-less deletion fixes; focused race passes | Depends on #37/#38/#40. Same owner controls CFN state machine. Actual original guest and combined gates are pending the serialized VM test window; do not treat superseded checks as final evidence |
| `cfn_callback`: one-shot response primitive | [#40](https://github.com/susumutomita/TenkaCloudSimulator/issues/40); no separate public PR observed | Primitive local commit `70f90cc` complete at worker scope, already incorporated into CFN candidate as `cc147…`; bounded WAF model commit `0c93a94` passes its baseline and five sensitivity mutants; whole custom lifecycle remains dependent on #44 | Confirm reconciliation/identity/failure integration in final CFN HEAD. Primitive success alone is not completed WP or host scoring |
| `grader_http`: bounded original grader HTTP | [#41](https://github.com/susumutomita/TenkaCloudSimulator/issues/41); no separate public PR observed | Local `d14306e` plus runtime follow-up `0e7b54b`; actual two-outer/child contention and cancellation race checks pass, independent read-only review found no concrete blocker | Depends on #37 execution port and coordinated runtime changes. Actual original grader→API→saved-role effects, nested invocation capacity and isolation still need final integrated evidence |
| `waf_console`: Cloudscape WAF UI | [#42](https://github.com/susumutomita/TenkaCloudSimulator/issues/42), [Draft PR50](https://github.com/susumutomita/TenkaCloudSimulator/pull/50) | Local UI `e721361` integrated into WAF candidate; native browser create/update/stale lock/reload/keyboard/delete, PC/390px reflow and axe pass | Depends on #38 schema and root navigation/assets. Require actual Count→Block/native readback, desktop/mobile, errors/locking and regression checks. Authenticated AWS visual equivalence has not been verified |
| `issue_plan`: original WP lifecycle oracles and parallel-work rules | [#43](https://github.com/susumutomita/TenkaCloudSimulator/issues/43); no separate public PR observed | Local preflight/oracle commit `444a8c05b2fd17ad0fe3728a09a0fd1347b424ff`:11 guard tests, source/hash/mutation checks and real two-process BLOCKED exit2. Rules/roster are in [Draft PR46](https://github.com/susumutomita/TenkaCloudSimulator/pull/46), currently dependent on PR45 | Depends on #38–42/#44 plus actual host multi-flag integration and Challenge contract resolution. Service runner/real HostStore ledger helper are prepared but full execution/browser not passed. Detailed evidence is in the separate worker commit and has not yet been integrated here. |

Abbreviated local SHAs are handoff references, not public merge evidence. The
integration owner must obtain the complete final SHA before picking/reviewing it.
Issue37 closed with PR45. Issues38–44 remain open. PR45 is merged;
Draft creation was a workflow step, not a permanent state. Draft PR46 carries this management documentation and is not a duplicate implementation. Older open PR24/27 also exist and are not part of
the current worker scope; avoid duplicating or overwriting them.

## External service reservations

These are external workstreams, not three confirmed running agents. Public Issues
and branches below were read at this snapshot; owner assignments explicitly
marked user-reported are separate evidence. A branch still at its base commit
shows a reservation, not a pushed implementation. No corresponding external-service public PR was
observed; private/local work remains unknown.

| External scope | Issue / PR / owner | Actual known state | Required handoff and dependencies |
|---|---|---|---|
| SQS | [#48](https://github.com/susumutomita/TenkaCloudSimulator/issues/48) / no public PR observed / name unknown | Issue created with nine Standard JSON operations, visibility/retention and IAM scope. No implementation commit/start acknowledgement observed here | Service-owned `sqs*` files/tests on a dedicated branch; no FIFO/CFN/UI claim. [Shared-file handoff](https://github.com/susumutomita/TenkaCloudSimulator/issues/48#issuecomment-6623866640) is recorded; obtain actual branch/HEAD and tests |
| SNS | [#49](https://github.com/susumutomita/TenkaCloudSimulator/issues/49) / no public PR observed / Grok Bot (user-reported) | Public `feat/sns-basics-20261104` exists at base `bdff8b7`, with no implementation delta observed. Latest user report: usage9%, survey only; reset11/5 13:29 JST. Earlier start language conflicts with this update; actual implementation start remains **UNKNOWN** | Issue49 and branch are created, which is consistent with survey/planning only. Reconcile with the external owner; [shared/fanout handoff](https://github.com/susumutomita/TenkaCloudSimulator/issues/49#issuecomment-6623912371) is recorded. Branch existence and utilization/reset reports do not establish implementation, completion or a deadline. Obtain exact scope/commit/evidence before integration |
| DynamoDB | [#47](https://github.com/susumutomita/TenkaCloudSimulator/issues/47) / no public PR observed / Muse (user-reported; GitHub assignee empty) | Issue created for seven table/item JSON operations; `feat/dynamodb-basics-20261104` currently points to base `bdff8b7`. No pushed implementation delta observed; local progress **UNKNOWN** | Service-owned `dynamodb*` files/tests; CFN/Lambda bridge/console explicitly excluded. [Shared-file handoff](https://github.com/susumutomita/TenkaCloudSimulator/issues/47#issuecomment-6623865340) is recorded; require final SHA, restart/isolation/validation and both coverage gates |

All three external Issue scopes name `internal/aws/ssm.go` dispatch,
`internal/aws/s3.go` Gateway, `internal/server/server.go` initialization/capabilities
and possibly root OpenAPI. The integration owner is the final writer of these
shared composition points, currently also used by WAF. External branches may
propose minimal additive hunks in a Draft PR; they must preserve existing branches,
service dispatch, IAM, SQLite state and API documentation. Hand off constructors,
request scope and exact file hunks instead of editing another owner's worktree.
Recheck current main before starting; do not overwrite another branch or infer
implementation from this plan. Merge only after combined native/authority,
persistence, regression, license/security and exact-HEAD checks.

## Completion blockers and evidence boundary

- Full original WP has **not passed**. No problem is enabled from service source,
  registration, API200, a projection or a prepared test alone.
- Current reviewed TenkaCloud main `f99b3d9a00076a2a5e97f1d4d9b0b20a7e02245c`
  exposes only hello-world/battle in its native CFN engine. Original WP requires
  real CFN multi-flag deploy/recover/redaction/submission/ledger and participant
  browser integration; no test-only replacement engine is accepted.
- Original Challenge `a749b142416770abee3f173673702c55df3856aa` outputs WebAclId
  using WebACL Ref, whose AWS value is `name|id|scope`; the direct SDK Id contract
  needs explicit source-owner resolution. Do not secretly alter original bytes
  or change simulator Ref semantics to hide the mismatch.
- Preserve separate team processes/databases/token authorities, native shared
  state, saved-role authority, explicit cleanup and visible failures. Public
  references/Cloudscape use do not establish authenticated AWS screen parity.
- No release, deployment, AWS sign-in/resource creation or completion of the
  entire service inventory is evidenced by this roster.

Public evidence: [open Issues](https://github.com/susumutomita/TenkaCloudSimulator/issues),
[PR45](https://github.com/susumutomita/TenkaCloudSimulator/pull/45), and the linked
Issue acceptance scopes. Local candidates require independent review and current
exact-HEAD CI; after any authorized merge, confirm the resulting main CI too.
Keep safe local evidence when a public write is blocked; do not reproduce denied
payloads here or use this page as an alternate publication channel.
