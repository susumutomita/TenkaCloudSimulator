# Development roster

**Snapshot: 2026-11-04 19:04 UTC. Update owner: the integration owner (`root`).**
This is the single active-assignment roster. Update it at claims, handoffs and
integration milestones; linked PRs and exact-HEAD checks remain authoritative.
Follow the [parallel rules](contributing/parallel-development.md) and
[shared contracts](design/0005-parallel-service-contracts.md). The
[258-unit service backlog](https://github.com/susumutomita/TenkaCloudSimulator/issues/52)
is a plan, not an implementation count or a second active roster.

Recorded Simulator main: `b61d0299a2139661f01ef29ae5826e4c070cc7bf`. The prior five authorized PRs are merged. PR406 is a separate **public Draft**, head `0196e5bc9dfa0ccb1f684e8ef2eeab817ea1ffbc`, not merged. All nine checks succeeded on this exact HEAD, independently verified at19:01UTC; a future child commit requires its own checks. All new merges remain on HOLD.

There are seven internal slots including root and a verified4CPU/16GiB execution limit. The assignments below are status snapshots, not a claim that seven services or seven runtime jobs are executing. CloudWatch completed its exclusive global gate; KMS, Secrets and Route53 now have independent one-core focused native reservations. Full Go/WASI/browser gates remain coordinated separately. Source claims, historical focused proof, hosted CI and a verified remote merge are distinct statuses. No AWS resources, billing operation or real credential configuration is part of this work.

## Active internal assignments

| Owner / editing boundary | Issue / PR | Observed state | Dependency / acceptance |
|---|---|---|---|
| `root`: shared integration, public review and roster | Draft PR406, head `0196e5b` | Runtime/native/SDK/UI/local gates recorded separately; exact0196 hosted9/9successful; not merged | Own shared dispatch/server/OpenAPI/nav/assets/CI in root tree; all new merges HOLD |
| `cfn_callback`: CloudWatch Logs existing candidate | [58](https://github.com/susumutomita/TenkaCloudSimulator/issues/58), `integrate/cloudwatch-logs-main-b61-20261104` | Original c005842 untouched; normal mainb61 local integration; full race/vet/assets/build PASS, one profile94.78%statements/90.06%source, full23E2E/start PASS; SDK32/reference7 and embeddedPC390/axe/stale-response PASS; cross5/audit50 PASS; old false200/partialwrite reproduced and guarded | Own Logs worktree only; exact-one INSERT/nilDB and four real recovery/boundary regressions; immutable code inputcf231ef passed12baselines/43requiredmutants and34-blob applicability; hosted CI/current canonical security and public merge remain pending; root owns review publication |
| `cfn_adapters`: KMS existing candidate restack | [117](https://github.com/susumutomita/TenkaCloudSimulator/issues/117) | Original5862047 retained; leaf16 normal/race/vet passed, canonical integration/protocol model sources prepared; two real Gateway routing/error-classification regressions reproduced before correction | Owned source now includes regular server/nav/root-POST documentation and model registration; new correction/native/CLI/UI/global gates remain separate, no publication |
| `waf_service`: Secrets Manager existing candidate restack | [266](https://github.com/susumutomita/TenkaCloudSimulator/issues/266) | Source-only isolated `/workspace/secrets-manager-b61-integration`; original96259aa retained | Seven operations; original leaf16 native/race/vet passed; new protocol/composition normal checks passed while fresh race/CLI SDK gates remain in progress; no public PR or whole-gate claim |
| `waf_console`: Route53 existing candidate restack | [241](https://github.com/susumutomita/TenkaCloudSimulator/issues/241) | Source-only isolated `/workspace/route53-main-integration`, originalb8375d0 retained; public Issue/closed-PR status unknown because read action stopped | Leaf17 native/race/vet, real boto3 native fixture and finite baseline+7 controls passed; canonical CLI/UI wiring and two new finite models are prepared, fresh native/CLI/global checks remain; stopped public reads/publication stay stopped |
| `grader_http`: independent read-only review | PR406 / Logs evidence | Review of SDK claim boundaries and native evidence, no implementation mutation | Report concrete findings independently; evidence is not service-completeness proof |
| `team_host`: CI observer | PR406 exact HEAD | Read-only current-head observation; private fixed-scope-menu source improvement is separately prepared, unbuilt/unverified | Do not reuse prior head's successful runs or change repository protections |

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
