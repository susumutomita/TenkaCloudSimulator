# Development roster

**Snapshot: 2026-11-04 09:04 UTC. Update owner: the integration owner (`root`).**
This is the single active-assignment roster. Update it at claims, handoffs and
integration milestones; linked PRs and exact-HEAD checks remain authoritative.
Follow the [parallel rules](contributing/parallel-development.md) and
[shared contracts](design/0005-parallel-service-contracts.md). The
[258-unit service backlog](https://github.com/susumutomita/TenkaCloudSimulator/issues/52)
is a plan, not an implementation count or a second active roster.

Verified Simulator main: `6ffbd1881dad3589469fdf1809654b7483bd6ac4`.
[PR45](https://github.com/susumutomita/TenkaCloudSimulator/pull/45),
[PR46](https://github.com/susumutomita/TenkaCloudSimulator/pull/46), and
[PR303](https://github.com/susumutomita/TenkaCloudSimulator/pull/303) are merged.
Main [CI41795143084](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41795143084)
and [CodeQL41795143300](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41795143300)
succeeded on that exact commit. Six active internal workers plus root fit this
environment's seven slots. External agents do not consume these slots. Heavy
Go/WASI/TLC checks use an explicit reservation to preserve deadline-test validity.

## Active internal assignments

| Owner / editing boundary | Issue / PR | Observed state | Dependency / acceptance |
|---|---|---|---|
| `root`: shared integration, roster, final publication/review | PR45/46/303 merged | Public main supports native REST/Lambda and ten CFN resource types; remaining candidates are separate | Final writer for shared dispatch/server/OpenAPI/navigation/assets/CI; exact-HEAD checks, review and main CI before completion |
| `grader_http`: scoped Lambda HTTP and native activation | [41](https://github.com/susumutomita/TenkaCloudSimulator/issues/41) | Local `77d416a` (production unchanged from `e3a0292`) passes full race/build, 18 E2E, statements94.80% and unfiltered source90.22%; independent source review found no additional confirmed blocker | New formal models/registration and final hosted security/CI remain. Same-Tx owned-generation scope, release before guest, sticky host failures and unchanged two-VM capacity; new scope/admission formal models required before merge |
| `cfn_callback`: safe startup | [304](https://github.com/susumutomita/TenkaCloudSimulator/issues/304), `feat/make-start-20261104` | Dedicated main-based source `1a45658`; actual permanent built/unbuilt/rebuild/state/auth/TTY/CI/redirect/shutdown tests and finite startup model/three mutants pass; final hosted gates remain | Own Makefile/start helper/minimal CLI display flag. Persistent DB/private reusable token; display only intentional interactive use, never unintended CI/log output |
| `waf_service`: API-reference usability | [305](https://github.com/susumutomita/TenkaCloudSimulator/issues/305), `feat/scalar-s3-docs-305` | Actual PC/390px audit reproduced misleading static examples. Candidate typed models and original-response reader were visually checked using routed candidate assets; final embedded build and full checks remain | Own embedded documentation/models/reader/tests. Correct operation-specific examples, static/live distinction and actual Send; preserve original XML response and SDK behavior |
| `waf_console`: browser logout | [306](https://github.com/susumutomita/TenkaCloudSimulator/issues/306), `feat/console-logout-20261104` | Local source `dfab916` adds separate browser sessions and account logout; native race, finite model/five mutants and actual keyboard/signout/re-login/replay/two-team/browser-failure checks pass | Separate bounded browser sessions; logout revokes only that browser, preserves operator-file/header SDK access, parent expiry and other sessions; PC/mobile/re-login/replay tests and auth model |
| `cfn_adapters`: effective formal CI | [307](https://github.com/susumutomita/TenkaCloudSimulator/issues/307) | Registry/applicability/16 negative tests prepared; root applied the stable workflow locally, independent review and authoritative TLC checks remain. Existing five-model checks are retained | Stable fail-closed Actions check, exact change evidence and explicit finite bounds. No mathematical-completeness/Go-refinement claim. Repository protection settings remain unchanged |
| `team_host`: TenkaCloud team allocation | TenkaCloud local work, publication not performed | Remote TenkaCloud main `f99b3d9`; dedicated local implementation reuses authenticated SurfaceGateway. Lease crash/concurrent-recovery issues found in review are being fixed before execution; no host PR | Explicit local backend rather than fake AWS accounts; unique process/DB/token/endpoint, authenticated team proxy, restart/cleanup and actual hello-world host lifecycle. Do not modify existing checkouts |

Worker names identify internal assignments, not service completeness. Local short
SHAs are handoff references, not public merge evidence. One writer owns each
active file/worktree. Root composes shared files after handoff; workers do not
replace one another's full OpenAPI, Makefile, navigation or generated bundle.

## Held work and completion boundaries

- [Draft PR50](https://github.com/susumutomita/TenkaCloudSimulator/pull/50)
  remains at public `6726c570`. Additional local WAF work is preserved. A platform
  risk classification stopped independent follow-up verification; it is not
  merge-ready. Do not retry, rephrase or reroute that stopped action. Static
  build/vet/assets are separate evidence and do not replace it.
- Full custom-resource CFN candidate `0e8e025` passed race/build but its source
  coverage88.59% fails90%. It remains frozen and separate from merged API-only
  PR303. Future callback/recovery integration must atomically advance existing
  graph checkpoints without recapturing generations or exposing pending stacks.
- Earlier `issue_plan` work is a held original-source preflight, not an active
  implementation assignment or completed host ledger. Stopped actions remain
  stopped; private progress drafts/history are not republished through docs/PRs.
- Only hello-world has completed the host import/deploy/participant/score/cleanup
  exercise test. Normal automatic host allocation is being implemented separately.
  Other catalog problems remain disabled until their original full lifecycle passes.
- TenkaCloud main `f99b3d9` still lacks original WP multi-flag CFN host integration.
  Challenge main `a749b142` uses WebACL Ref (`name|id|scope`) where its SDK Id use
  needs source-owner resolution. Do not falsify simulator Ref or alter original
  problem bytes silently.
- CFN-created IAM roles are legacy aggregates, absent from native IAM Get/List
  and role pickers; metadata-only CFN trust is not native trust evaluation.
- No authenticated AWS screen, real AWS resource, deployment or public release
  has been used. Public official references do not establish pixel equivalence.
- PR24/27 are unrelated and untouched. Whole-source90% floors and exact-HEAD
  review/CI remain required. Formal checks are finite models, not implementation
  proofs. Required-check/branch-protection enforcement has not been verified or
  configured by this work.

## External work: public evidence versus owner reports

At this snapshot the branch heads below have public commits, but no corresponding
open service PR was observed. Commit messages or file presence are not runtime,
coverage, authorization or integration evidence. No external implementation has
been merged or independently executed here.

| Service | Issue / owner evidence | Public observation | Handoff |
|---|---|---|---|
| SQS | [48](https://github.com/susumutomita/TenkaCloudSimulator/issues/48); personal owner unknown | `feat/sqs-basic-operations` at `9d3e174`, based on main6ff; service/store/tests and shared integration hunks exist | [Composition](https://github.com/susumutomita/TenkaCloudSimulator/issues/48#issuecomment-6623866640), [current limits](https://github.com/susumutomita/TenkaCloudSimulator/issues/48#issuecomment-6625188406); needs Draft PR/review/formal applicability/actual tests |
| SNS | [49](https://github.com/susumutomita/TenkaCloudSimulator/issues/49); Grok Bot is user-reported, GitHub assignee empty | `feat/sns-basics-20261104` at `f303278`, based on bdff; inspected commit changes README only, no SNS service implementation observed | Latest user report was survey-only/9% usage/reset11/5 13:29JST, contradicting earlier start language. [Composition](https://github.com/susumutomita/TenkaCloudSimulator/issues/49#issuecomment-6623912371), [confirmation semantics](https://github.com/susumutomita/TenkaCloudSimulator/issues/49#issuecomment-6625191525); actual implementation start UNKNOWN |
| DynamoDB | [47](https://github.com/susumutomita/TenkaCloudSimulator/issues/47); Muse is user-reported, GitHub assignee empty | `feat/dynamodb-basics-20261104` at `ed877ec`, based on bdff; service/store/tests and shared integration hunks exist | [Composition](https://github.com/susumutomita/TenkaCloudSimulator/issues/47#issuecomment-6623865340), [size contract](https://github.com/susumutomita/TenkaCloudSimulator/issues/47#issuecomment-6625190075); needs current-main integration, Draft PR/review/formal applicability/actual tests |

Service-owned files/tests stay on dedicated branches. Root is the final writer of
shared Gateway/dispatch/server/OpenAPI/navigation/assets/CI. Propose minimal
additive hunks; preserve existing providers, IAM and SQLite state. SNS-to-SQS
composition must agree atomicity/retry behavior without nested transactions.
Inspect the service backlog and roster before claiming another service.
