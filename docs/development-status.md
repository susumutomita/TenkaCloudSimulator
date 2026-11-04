# Development roster

**Snapshot:2026-11-04 05:50 UTC. Update owner: the integration owner.**
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
| Integration owner: native API Gateway and shared integration | [#37](https://github.com/susumutomita/TenkaCloudSimulator/issues/37), [PR45](https://github.com/susumutomita/TenkaCloudSimulator/pull/45) | Public PR open; native16-operation REST/Lambda increment. Reported current HEAD `a4c87e9abf74c7ee3f0c8eb20b17b93c87455523`; exact-HEAD CI/review in progress, merge not confirmed | Own common registration/runtime/server/OpenAPI/navigation/assets/CI/catalog composition. Recheck actual PR HEAD, required checks and reviews before merge, then verify main CI |
| `waf_service`: regional WAF backend | [#38](https://github.com/susumutomita/TenkaCloudSimulator/issues/38); no separate public PR observed | Backend implemented locally; integration work continues in a new worktree. This is not a merge or full-WP claim | Depends on #37 stage execution contract; coordinate native CRUD/IAM/lock tokens, evaluator, CFN/UI schema and notices. Final integrated HEAD/CI remains pending |
| `cfn_adapters`: CFN service adapters and custom orchestration, one owner | [#39](https://github.com/susumutomita/TenkaCloudSimulator/issues/39), [#44](https://github.com/susumutomita/TenkaCloudSimulator/issues/44); no separate public PR observed | Local `163c567` candidate is superseded as an acceptance target: fixes are in progress for failed-create physical identity, custom ownership collisions and SetWebACL authorization | Depends on #37/#38/#40. Same owner controls CFN state machine. Require replacement SHA and focused regressions, then combined gates; do not treat superseded checks as final evidence |
| `cfn_callback`: one-shot response primitive | [#40](https://github.com/susumutomita/TenkaCloudSimulator/issues/40); no separate public PR observed | Primitive local commit `70f90cc` complete at worker scope, already incorporated into CFN candidate as `cc147…`; bounded WAF model work now assigned separately; whole custom lifecycle remains dependent on #44 | Confirm reconciliation/identity/failure integration in final CFN HEAD. Primitive success alone is not completed WP or host scoring |
| `grader_http`: bounded original grader HTTP | [#41](https://github.com/susumutomita/TenkaCloudSimulator/issues/41); no separate public PR observed | Local candidate `d143…`; new RunChild/concurrency work in progress | Depends on #37 execution port and coordinated runtime changes. Actual original grader→API→saved-role effects, nested invocation capacity and isolation still need final integrated evidence |
| `waf_console`: Cloudscape WAF UI | [#42](https://github.com/susumutomita/TenkaCloudSimulator/issues/42); no separate public PR observed | Local UI candidate `e721…`; native browser integration pending | Depends on #38 schema and root navigation/assets. Require actual Count→Block/native readback, desktop/mobile, errors/locking and regression checks. Authenticated AWS visual equivalence has not been verified |
| `issue_plan`: original WP lifecycle oracles and parallel-work rules | [#43](https://github.com/susumutomita/TenkaCloudSimulator/issues/43); no separate public PR observed | Local preflight/oracle commit `444a8c05b2fd17ad0fe3728a09a0fd1347b424ff`:11 guard tests, source/hash/mutation checks and real two-process BLOCKED exit2. Rules/roster are a separate local commit; see Git history | Depends on #38–42/#44 plus actual host multi-flag integration and Challenge contract resolution. Service runner/real HostStore ledger helper are prepared but full execution/browser not passed. Detailed evidence is in the separate worker commit and has not yet been integrated here. |

Abbreviated local SHAs are handoff references, not public merge evidence. The
integration owner must obtain the complete final SHA before picking/reviewing it.
No Issue37–44 is closed in the observed public snapshot. PR45 is open and was no
longer marked Draft when checked; Draft creation is a workflow step, not a claim
that it remains Draft forever. Older open PR24/27 also exist and are not part of
the current worker scope; avoid duplicating or overwriting them.

## External service reservations

These are reserved workstreams, not three confirmed running agents. Except for the user-reported SNS owner, their names, branches, concrete implementation-start acknowledgements and commits are unknown here. No SQS,
SNS or DynamoDB Issue/PR was identified in this repository's observed open list;
that does not establish whether another team has private/local work.

| External scope | Issue / PR / owner | Actual known state | Required handoff and dependencies |
|---|---|---|---|
| SQS | Not identified / not identified / name unknown | Reserved externally; actual start and implementation progress **UNKNOWN** | Confirm owner acknowledgement, Issue, branch/base SHA, file claim, SDK/state/IAM/CFN/UI scope and tests before assigning overlapping work. Shared contracts/integration stay coordinated here |
| SNS | Not identified / not identified / Grok Bot (user-reported) | Latest user report: usage9%, survey only; reported reset11/5 13:29 JST. This is neither a code commit nor proof of implementation start. Earlier start language conflicts with this update; actual implementation start remains **UNKNOWN** | Reconcile with the external owner. Do not convert a utilization/reset report into completion, concurrency availability or a development deadline. Obtain exact scope/commit/evidence before integration |
| DynamoDB | Not identified / not identified / name unknown | Reserved externally; actual start and implementation progress **UNKNOWN** | Confirm owner acknowledgement and nonoverlapping file claim, source/license reuse, native API/state/IAM/CFN/UI acceptance and final SHA before integration |

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
