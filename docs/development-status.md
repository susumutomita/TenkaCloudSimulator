# Development roster

**Snapshot: 2026-11-04 12:35 UTC. Integration owner: `root`.** This is a reviewed
assignment snapshot, not a live process count or completion percentage. Follow
[parallel development rules](contributing/parallel-development.md),
[shared contracts](design/0005-parallel-service-contracts.md), and the
[public service verification matrix](service-verification.md). The
[service backlog](https://github.com/susumutomita/TenkaCloudSimulator/issues/52)
is a plan, not an implemented-service count.

## Public main and supported boundary

Verified public main is **`2d8a41a0cb576bd42561a7162d35129f44979c08`**.
[PR310](https://github.com/susumutomita/TenkaCloudSimulator/pull/310) merged as
`55a111953eb3eb0dfd90aaa473ac3fe39cb10b66`; the additive annotation classification
[PR314](https://github.com/susumutomita/TenkaCloudSimulator/pull/314) merged as
`2d8a41a0cb576bd42561a7162d35129f44979c08`. Exact-main
[ci41828639809](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41828639809),
[Formal41828639605](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41828639605)
and [Push on main41828638968](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41828638968)
succeeded. These runs are evidence for that main commit, not later candidates.

Main has bounded AWS S3, Standard SSM Parameter Store, native IAM roles, Python
Lambda with its limited S3/SSM SDK bridge, Regional REST API Gateway, and the
documented CloudFormation subset. Only **five finite models** are registered:
ObjectStore, S3Versions, StackOwnership, NativeIAMOwnership and
RESTDeploymentSnapshot. Azure/GCP native services are not implemented in main.
Only **hello-world** has completed host import→deploy→participant operation→score→cleanup;
service code or API200 does not enable another catalog problem. Authenticated
AWS console equivalence remains unverified.

## Public PR acceptance snapshot

| Public change | Exact published source / base | Observed evidence and remaining acceptance |
|---|---|---|
| [PR308 authenticated make start](https://github.com/susumutomita/TenkaCloudSimulator/pull/308) | Draft `ac52f4b71abf37e5fa8f0441bdaea07562adac8f`, base `6ffbd188` | Historical [ci41804716967](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41804716967) succeeded. Integration with the current formal policy/base and fresh exact-HEAD checks remain pending. Current main does not yet include this command. |
| [PR309 DynamoDB](https://github.com/susumutomita/TenkaCloudSimulator/pull/309) | Draft `ed877ecc56c0fd4eef7a3a5a3b0b23d3ca435cfe`, base `6ffbd188` | [ci41797379476](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41797379476) failed; separate code-scanning success is not a passing whole CI. State/validation/model and current-base integration still require acceptance. Not main support. |
| [PR311 API reference](https://github.com/susumutomita/TenkaCloudSimulator/pull/311) | Draft `dada2ad5aa2850efd7b6e21defd9d63d93c86558`, base `55a11195` | [ci41823917760](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41823917760) and [Formal41823917931](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41823917931) succeeded on that source/base. Current-main integration, preserved Scalar request flows and fresh CI/review remain pending. |
| [PR312 browser logout](https://github.com/susumutomita/TenkaCloudSimulator/pull/312) | Draft `ebbcdec654b34e44182521132e51ba944b01250f`, base `55a11195` | [ci41825440653](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41825440653) and [Formal41825441004](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41825441004) succeeded on that source/base. Current-base integration must preserve account menu, session separation, actual logout/replay and keyboard/browser checks. |
| [PR313 SSM model](https://github.com/susumutomita/TenkaCloudSimulator/pull/313) | Draft `e5e5546510c866f97cfe7bf5e36e40f48e665ff3`, base `2d8a41a0` | [Formal41829572922](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41829572922) succeeded; [ci41829572864](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41829572864) was running at this snapshot. Earlier `ecc`/base55a results are historical. Do not infer current complete CI or merge. |
| [PR315 SQS](https://github.com/susumutomita/TenkaCloudSimulator/pull/315) | Draft `95300b90ca74c5984ecf6627edfc010b5fc2496b`, base `55a11195` | [ci41824302719](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41824302719) and [Formal41824302713](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41824302713) failed. A later local correction cannot substitute for checks of the published exact HEAD. Not main support. |
| [PR50 WAF](https://github.com/susumutomita/TenkaCloudSimulator/pull/50) | Open Draft; held/unmerged | Held status only; no new verification or main-support claim. |

These are public observations, not permission to force updates. A successful run
on an older base does not validate a new integration commit. Recheck current
HEAD, all required checks, unresolved reviews and merge state immediately before
an authorized merge; verify main CI afterward. Repository-settings changes are
on hold; workflow execution does not establish configured branch protections.

## Current assignments and dependencies

Seven assigned roles include the integration owner. This table does not claim
seven concurrent tests or hundreds of running agents. Root schedules heavy
Go/WASI/browser/TLC runs separately; source work can proceed independently.
Local, unpublished implementation/test receipts are kept outside this public
roster and do not change main support.

| Owner | Public task / current responsibility | Dependency and acceptance boundary |
|---|---|---|
| `root` | Shared integration, review, formal registry/CI, assets and publication; PR310/314 merged | Own final shared-file composition and exact current-HEAD review/CI. Settings work remains on hold. Preserve distinct candidate histories; no force updates or inferred releases. |
| `cfn_adapters` | [SQS48](https://github.com/susumutomita/TenkaCloudSimulator/issues/48) / PR315 correction and verification | Resolve public CI/formal failures; freeze explicit queue/receipt state contracts, safe cleanup and meaningful finite models. SDK, persistence, two-team isolation and both global90% floors remain required. |
| `cfn_callback` | [Secrets Manager266](https://github.com/susumutomita/TenkaCloudSimulator/issues/266) bounded API/UI/model preparation | Define versioned JSON/API, saved-state and disclosure/authorization boundaries. Registry applicability, actual SDK/browser and exact whole gates must precede support claims; do not log secret values. |
| `waf_service` | [SSM54](https://github.com/susumutomita/TenkaCloudSimulator/issues/54) / PR313 integration; [startup304](https://github.com/susumutomita/TenkaCloudSimulator/issues/304) / PR308 follow-up | SSM depends on the merged policy and fresh e5 CI; startup must account for current module-metadata rules and preserve TTY/auth/persistent state behavior. Other-cloud catalog plans are not running service implementations. |
| `team_host` | [DynamoDB47](https://github.com/susumutomita/TenkaCloudSimulator/issues/47) / PR309 state and validation follow-up; dependency-lock verification | Preserve generation/ownership and document the accepted table/item subset. Failed public CI and unverified lock generation remain unresolved until reproducible evidence and fresh exact-HEAD checks exist. |
| `waf_console` | [API reference305](https://github.com/susumutomita/TenkaCloudSimulator/issues/305) accessibility and request-flow follow-up | Preserve actual Scalar Send, original pinned dependencies/notices, full response access and PC/mobile keyboard checks. Preview styling is not complete accessibility acceptance; unverified dependency provenance must remain unverified. |
| `grader_http` | Public management/verification docs and [Azure catalog316](https://github.com/susumutomita/TenkaCloudSimulator/issues/316) planning record | Keep the public matrix tied to real public bases/PRs and bounded support; maintain the dated product-source inventory without calling it implemented services or an official universal count. Service child links remain pending. |

The Azure index is a dated official-products normalization, not an implementation
count. Catalog categories, aliases, tools/suites, resource-provider namespaces,
preview, renamed and retired offerings require separate applicability decisions.
Azure and Google Cloud retain their own authority/resource/UI contracts; an AWS
implementation does not supply their semantics.

## Integration contract

For each increment, record the exact public source/base and operation/field
subset, official references, mandatory/optional/unconfirmed dependencies, a
single writer for shared state/API files, and named acceptance tests. Shared
service references must be validated and mutated in one provider transaction;
do not create a second backend or hold a database transaction across a guest
that needs that database. Root owns provider registration, common navigation,
OpenAPI, generated assets and combined registry requirements.

Before support changes, require actual initialization→operation→state observation→cleanup,
denied/wrong-scope and two-team tests, restart/pagination/rollback, UI/API shared
state and applicable finite models with intended mutation counterexamples.
Retain race, statement and unfiltered whole-source90%, mandatory E2E/axe,
license/security, cross-build and exact-HEAD review/CI gates. Profiles are not
combined to manufacture coverage; source presence or model registration alone
is not acceptance. Cross-compilation does not prove foreign-platform execution.

Main remains a local single Go binary with embedded UI/SQLite/WASI. Each team
needs a separate process, database and token authority; loopback is not
authorization. No real cloud resources, sign-in, deployment, release, network
forwarding or extra catalog grading is implied by this roster. CFN-created IAM
roles remain legacy aggregates, not native IAM role-picker/trust integration.
