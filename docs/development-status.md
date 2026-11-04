# Development roster

**Snapshot: 2026-11-04 07:36 UTC. Update owner: the integration owner (`root`).**
This is the single active-assignment roster, maintained at claims, handoffs and
integration milestones. It is a reviewed snapshot, not a live agent counter;
follow each linked PR for its current HEAD and checks. The [parallel development
rules](contributing/parallel-development.md) and [shared contracts](design/0005-parallel-service-contracts.md)
apply. The [258-unit service backlog](https://github.com/susumutomita/TenkaCloudSimulator/issues/52)
is a plan, not a second active roster or an implementation count.

Verified public main baseline: `8cc07ca2e8da0c9d6a9df804d903c8a11727d96f`.
API Gateway [PR45](https://github.com/susumutomita/TenkaCloudSimulator/pull/45)
and workflow/roster [PR46](https://github.com/susumutomita/TenkaCloudSimulator/pull/46)
are merged. Main [CI41785567246](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41785567246)
and [CodeQL41785567251](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41785567251)
succeeded. Six internal worker assignments plus root fit this environment's seven
agent slots; that does not mean all workers run commands at every instant or
that external agents consume these local slots. Heavy Go/WASI runs are reserved
separately to avoid CPU contention in fixed-deadline tests.

## Internal assignments and handoffs

| Owner / boundary | Issue / PR | Actual evidence and state | Dependency / next action |
|---|---|---|---|
| `root`: shared integration, publication and final review | [37](https://github.com/susumutomita/TenkaCloudSimulator/issues/37), PR45/46 merged; [PR303](https://github.com/susumutomita/TenkaCloudSimulator/pull/303) in review | Native16-operation REST/Lambda increment is in main. API-only CFN is a separate public candidate; final metadata/test HEAD must pass its own CI/review before merge | Own shared Gateway/server/OpenAPI/navigation/assets/CI/catalog integration; recheck exact HEAD, unresolved findings and main CI |
| `waf_service`: WAF backend, then read-only API-only CFN review | [38](https://github.com/susumutomita/TenkaCloudSimulator/issues/38), [Draft PR50](https://github.com/susumutomita/TenkaCloudSimulator/pull/50) | Native/backend/UI and original-handler Count→Block HTTP checks pass locally. Additional local fix is preserved; independent follow-up verification was stopped by a platform risk classification. Draft is not merge-ready | Do not retry or reroute the stopped verification. Source/coverage/CI and final review remain required; no completed-WAF or full-WP claim |
| `cfn_adapters`: full CFN lifecycle and owned HTTP-scope port | [39](https://github.com/susumutomita/TenkaCloudSimulator/issues/39), [44](https://github.com/susumutomita/TenkaCloudSimulator/issues/44) | Full local candidate `0e8e025` passes race/build and 93.95% statements, but unfiltered source 88.59% fails 90%. Separate local scope port `918b29c` has focused tests; neither is merged | Full controller is frozen. Same-stack generation/config scope needs native invocation/atomic route integration; do not publish private progress logs/history |
| `cfn_callback`: callback primitive and independently publishable CFN API slice | [40](https://github.com/susumutomita/TenkaCloudSimulator/issues/40), [39](https://github.com/susumutomita/TenkaCloudSimulator/issues/39), [PR303](https://github.com/susumutomita/TenkaCloudSimulator/pull/303) | Primitive local `70f90cc`; WAF finite model `0c93a94` / API docs `450f6f5` handed off. Clean API-only branch starts from public `8cc07ca` and contains no WAF/custom controller/private history. Its actual two-team SDK/HTTP/restart/delete and17 E2E pass | Explicit compiler-file handoff: this worker owns the API-only branch; the full-controller branch remains frozen. Final test/docs-only HEAD needs complete hosted coverage/review. Keep the branches separate |
| `grader_http`: scoped HTTP execution and native Lambda activation | [41](https://github.com/susumutomita/TenkaCloudSimulator/issues/41) | Local executor4f8e3b8 passes actual urllib→native API→saved-role SSM/S3, two-outer capacity/sticky-error and child cancellation tests. Native SDK/CFN activation is being integrated on an isolated branch | No guest/environment-derived authority. Capture enrollment after native creation, validate scope and route in one Tx, release before guest and reject sticky infrastructure errors after Run. No full WP result |
| `waf_console`: Cloudscape WAF UI and native browser helpers | [42](https://github.com/susumutomita/TenkaCloudSimulator/issues/42), Draft PR50 | Local UI and actual native create/update/stale-lock/reload/keyboard/delete, PC/390px reflow/axe and original-handler Count/leak→Block403/safe200 pass | Shared root navigation/assets are integrated separately. Authenticated AWS visual equivalence is unverified; no main-merge claim |
| `issue_plan`: original WP preflight/oracles and independent read-only reviews | [43](https://github.com/susumutomita/TenkaCloudSimulator/issues/43) | Local444a8c0 prepares original-source guards and a two-process blocked preflight. Full host ledger/browser is not run. Earlier CFN state findings were independently reproduced and fixed; a later WAF follow-up turn was stopped | Preserve unexecuted/stopped status. Never substitute a test-only host, altered grader, or projected template for the original full lifecycle |

Local short SHAs are handoff references, not public merge evidence. There is one
writer per active file/worktree; a frozen earlier branch is not permission to
publish or overwrite it. PR24/27 are unrelated and remain outside these claims.

## External assignments: public evidence versus owner reports

The snapshot reads found Issues47/48/49, no corresponding public service PR,
and DynamoDB/SNS branches still at `bdff8b7`. Branch creation alone is not a
pushed implementation. Local work may exist but is not inferred here.

| Service | Issue / owner evidence | Actual start / implementation evidence | Handoff |
|---|---|---|---|
| SQS | [48](https://github.com/susumutomita/TenkaCloudSimulator/issues/48); personal owner unknown | Issue created; branch/commit/PR and local progress unconfirmed | [Composition boundary](https://github.com/susumutomita/TenkaCloudSimulator/issues/48#issuecomment-6623866640), [current API limits/compatibility](https://github.com/susumutomita/TenkaCloudSimulator/issues/48#issuecomment-6625188406) |
| SNS | [49](https://github.com/susumutomita/TenkaCloudSimulator/issues/49); Grok Bot is user-reported, GitHub assignee empty | `feat/sns-basics-20261104` is at base `bdff8b7`. Latest user report is survey-only with 9% usage and reset 11/5 13:29 JST; earlier implementation-start language conflicts. Actual implementation start remains UNKNOWN | [Fanout/transaction boundary](https://github.com/susumutomita/TenkaCloudSimulator/issues/49#issuecomment-6623912371), [confirmation/delivery semantics](https://github.com/susumutomita/TenkaCloudSimulator/issues/49#issuecomment-6625191525) |
| DynamoDB | [47](https://github.com/susumutomita/TenkaCloudSimulator/issues/47); Muse is user-reported, GitHub assignee empty | `feat/dynamodb-basics-20261104` is at base `bdff8b7`. No pushed implementation delta observed; local progress UNKNOWN | [Composition boundary](https://github.com/susumutomita/TenkaCloudSimulator/issues/47#issuecomment-6623865340), [item-size contract](https://github.com/susumutomita/TenkaCloudSimulator/issues/47#issuecomment-6625190075) |

Service-owned files/tests stay on dedicated branches. The integration owner is
the final writer of shared `internal/aws/ssm.go`, `internal/aws/s3.go`,
`internal/server/server.go` and root OpenAPI. Propose minimal additive hunks in a
Draft PR; preserve existing dispatch, IAM and native SQLite state. SNS→SQS must
agree atomicity/retry behavior without nested store transactions. Inspect the
service backlog before opening another service issue.

## Completion boundaries

- Only hello-world has completed the real host import→deploy→participant browser→score→cleanup path. API support, projected tests and local commits do not enable other catalog problems.
- Current TenkaCloud main remains `f99b3d9`; its native CFN host engine lacks original WP multi-flag deploy/recover/private-output/flag-ID submission/ledger integration. Simulator work does not silently modify that repository.
- Challenge main remains `a749b142`. Original WP WebAclId uses WebACL Ref (`name|id|scope`) but its SDK Id use requires source-owner resolution. Do not falsify simulator Ref or alter original bytes unnoticed.
- CFN-created IAM roles remain legacy identity aggregates, absent from native IAM Get/List/role pickers; metadata-only CFN trust is not native trust evaluation. This migration is still pending.
- Only scoped documented services, one process/SQLite/token authority per team and bounded saved-role execution are claimed. No real AWS sign-in/resource creation, external delivery, deployment or public release has been performed.
- Failed/stopped public writes stay stopped, with safe evidence outside the published tree. Do not republish a denied payload through this roster, docs, another API or agent. Whole-source 90% floors and exact-HEAD review/CI remain required.
