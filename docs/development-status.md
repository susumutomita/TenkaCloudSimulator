# Development roster

## S3 multipart and GUI main integration, 2026-11-08

Claimed by the PR440 integration worker on `integrate/pr440-gui-20261108`
in `/workspace/scratch/5da5816d240c/simulator-pr440-merge`: normal merge of
published PR440 `31df6fc66ff4a742334dc6cc8d4ceaafcc439a9f` with GUI main
`d968be38dd2ed933e1e756b219ef2b0d26f665b0`. Owns conflict resolution,
console bundle regeneration, registry union and exact applicability only.
Preserves GUI handoff/auth/SSM behavior and the multipart cache-header fix.
The combined inventory is 60 models and 514 controls (574 executions).
Full native/race, coverage, assets, formal and automatic browser gates are
pending for this integration; prior receipts below remain historical.
No workflows, dependency locks, releases or unrelated service work are changed.
Heavy checks are serialized in the integration worker's reserved CPU lane;
local browser execution is unavailable and must not be claimed as verified.

## S3 multipart PR440 integration, 2026-11-08

Status: in progress on `integrate/pr440-docs-20261108`, integrating the
published source `0937c5c958334cea6696c32c7b021d43838cf566` with documentation-only
main `74985ee93a1ab38c9b1101ffdd3a2c8c3430e01b`. Runtime, embedded console,
models, dependency locks and workflows are unchanged by this integration.
The integration owner owns this roster and the exact applicability record;
GUI console-handoff and WAF work remain separate and merges are serialized.

The published source includes the browser multipart cache-header fix. Its
identical local source tree passed full `make before-commit`, including all
race packages, assets, dependency verification, build and 95.61% full-module
statement coverage. Exact applicability and 27 applicability regressions passed.
Published-source Formal validation, Japanese console acceptance, CodeQL and
GitGuardian passed; aggregate CI was still running when this entry was written.
These results apply to the named source, not this new documentation integration.
The registered inventory is 59 finite models and 494 controls; these bounded
models are not proofs of concrete implementation or complete cloud compatibility.

## Service evidence documentation integration, 2026-11-08

PR [#327](https://github.com/susumutomita/TenkaCloudSimulator/pull/327) is being
integrated with public main `835853a2eddf95c85a9a8bbb9ad3a6d9ba0b7152`.
The integration owner owns only this roster, the
[service verification matrix](service-verification.md), and their exact
applicability record. Runtime, console assets, dependency locks, model registry,
workflows and existing service assignments remain unchanged from that base.
The matrix separates the current bounded multi-cloud contracts and 58 registered
finite models from its explicitly historical November 4 evidence. Historical
PR states, five-model totals and AWS-only statements are not current support claims.

The dated records below are preserved rather than replaced by the older PR.
Their assignments, pending checks and completion statements apply to their named
source revisions; this documentation integration does not reassign service work.
PR #27, WAF #50 and ongoing console work are outside its editing scope.
Fresh exact-HEAD CI and Formal validation are required before merge; earlier
successful runs do not validate this integration. No release or deployment is
performed by this change.

## CloudFormation local SSM parameter values, 2026-11-08

Issue #54: `feat/battle-backlog-20261108`, base `0d0abe6eff9a219f5503ea7cc3c3ba612df4ae24`.
The change covers CloudFormation parameter resolution, native/SDK tests,
documentation and the finite SSM snapshot/admission model. S3 #440, EC2/Route53,
UI, dispatch and dependencies remain excluded.

The proposed subset resolves String and EC2 Image ID carriers only from local
Standard String values, with caller GetParameters permission and preserved input
keys/resolved values. No public AWS lookup, AMI existence, EC2 execution or full
Battle success follows. Implementation and focused native/race checks are complete. Independent review
found and repaired replay-name validation and exact-ARN preflight regressions.
The scoped finite model passed its baseline and 15 controls.
[PR #445](https://github.com/susumutomita/TenkaCloudSimulator/pull/445) merged as
`835853a2eddf95c85a9a8bbb9ad3a6d9ba0b7152` after all eleven checks on source
`be291bc02712dbf9d02fe5e167e4bc33b0c4d565` succeeded, including whole CI and
Formal validation. Post-merge main checks are separate evidence and are not
claimed by those pre-merge results.

## Standalone release pipeline, 2026-11-08

[Draft PR #444](https://github.com/susumutomita/TenkaCloudSimulator/pull/444)
owns release workflows/build and verification scripts, regression hooks, release
documentation and exact applicability. It now normally integrates main
`4ab99d4902571c75862aa642052cd94a1e4f5b61`; runtime service/UI bytes are unchanged
from that main. The integrated source requires new exact-HEAD CI before merge. BUILDINFO now
declares launcher/token-control `host_protocol: 1` for host-local release locks;
this is not a database downgrade guarantee and does not pin host source hashes.

Pre-integration `be3bfc7` passed 15 release regressions, 27 applicability tests,
76 formal-policy controls, both hosted coverage gates, full CI/browser/platform
acceptance and named Formal validation. The actual packaged Linux/macOS
amd64/arm64 binaries each passed native startup/auth/assets/persistence/restart/
shutdown on its respective GitHub runner. These are historical results, not
substitutes for the new integrated-source gates. Linux also passed local smoke
with an empty executable PATH; all four candidate checksums were verified.

Acceptance preserves full CI and formal gates, immutable-source build, complete
SHA256SUMS/BUILDINFO, four native smoke runners, candidate-only manual runs,
and draft-first publication without replacing existing tags/assets. Ambient Go
configuration and observed tag drift are refused. Windows remains compile-only.
No tag or public release has been created. Signing/notarization, Gatekeeper and
repository tag protections remain outside the verified scope.
## Participant console handoff, 2026-11-08

In progress: dedicated branch `feat/participant-console-handoff`, integrated
with public main `74985ee93a1ab38c9b1101ffdd3a2c8c3430e01b`. The integration
owner retains STS browser binding, the exact signed handoff endpoint, fragment
redemption, capability discovery, tests and the separate ConsoleHandoff finite
model. Other service PRs and release workflows remain outside this change.

Historical native checks and the isolated macOS host GUI-to-100-point rehearsal
are recorded in [console sessions](console-sessions.md). Independent review
repaired a leading-slash parameter alias gap and unsupported capability metadata.
The new 59th finite model preserves the prior 58 registry entries; its baseline,
twenty required controls and all 78 formal-policy tests pass. The final integrated
native source passed `make before-commit` with race coverage of 95.60%, vet,
assets, module verification and build. Exact-commit browser/formal gates,
publication and merge remain pending. No release or current-head GUI pass is
implied by historical evidence.

The PR #446 audit follow-up owns only `test/e2e/console_handoff.py`, this roster
and their exact applicability entries, starting from
`3395abbf2a22c80b2128a36d2f4a3e0c507e7532` in the same dedicated branch/worktree.
The hosted browser reached the bound parameter, then correctly rejected the
test's inline script tag under the Console CSP. The fix uses the existing
Playwright audit evaluation pattern without changing the site's CSP, runtime
or assets. Both 1440/390 accessibility audits and all subsequent scope, write,
replay and revocation assertions remain required. Native `make before-commit`
and fresh exact-head browser/formal checks are required for this follow-up;
the previous source's successful native/formal checks are historical evidence.
The next hosted run passed both accessibility audits but found the denied-write
text in both the visible form error and Cloudscape's hidden live-region copy.
The same owner scopes error/success assertions to their respective Alerts;
the visible denial, successful write and later authorization checks stay intact.


## CloudFormation Battle intrinsic prerequisite: 2026-11-08

[PR #443](https://github.com/susumutomita/TenkaCloudSimulator/pull/443) merged as
`4ab99d4902571c75862aa642052cd94a1e4f5b61` after exact-head full CI, native SDK/
browser/platform/audit and Formal validation passed. The resulting main tree
matches the independently reviewed/tested source. Post-merge full CI did not
start because GitHub's generated squash body inherited an intermediate skip-CI
marker; that is not claimed as a new-main pass. The release integration's explicit
merge message will avoid that marker and requires new-main checks.

The bounded scope is Select/GetAZs/Base64 and escaped scalar Sub, admission of
all branches, native state/event/permission rollback and cleanup, unchanged
original Battle fixture, and documentation. GetAZs is one synthetic local zone
per supported region. EC2, InstanceProfile, SSM-backed AMIs, Linux/AL2023/nginx/
systemd and Session Manager remain unsupported; the original full Battle stack
must fail without leaving state. The finite admission abstraction does not prove
concrete value parsing or full Battle execution.

## Current integration snapshot: catalog backlog, 2026-11-07

Root integrates the open backlog on branch `claude/adoring-noether-7fvwju`
(PR #439, 112 AWS services). The user authorized implementing and merging it.
Parallel local branches, merged by root only:

| Scope | Branch | Status |
|---|---|---|
| AWS catalog round 3 (59 services) | `catalog-r3-integration` | Integration on top of PR #439 in progress |
| AWS Query/EC2/REST-XML protocols and five services | `catalog-query` | Done locally; merged after round 3 |
| AWS Query/EC2 services (RDS family, ElastiCache, Redshift, VPC, EBS, VPN, PrivateLink) | `catalog-qdb`, `catalog-qnet` | In progress |
| AWS services needing engine features (Network Firewall, SWF, Application Auto Scaling, Organizations, Keyspaces and others) | `catalog-gaps` | In progress |
| Google Cloud discovery catalog (about 47 APIs) | `gcp-wip` | Integration in progress |
| Azure Resource Manager catalog (#316) | `azure-arm` | In progress |
| Sakura Server, Switch/NIC and Object Storage (#423) | `sakura-iaas` | In progress |

Native work owned elsewhere is not touched: S3 multipart (#51), the unpublished
Route 53 (#241) and EC2 network interface (#57) implementations, and Firestore
documents (#338). Products that cannot be served are recorded in the
[coverage assessment](coverage-assessment.md).


## Current integration snapshot: login and published catalog, 2026-11-06

PR [#437](https://github.com/susumutomita/TenkaCloudSimulator/pull/437) is being
integrated with published main `b81496f73e31be6150fc237f4641d6369446cc38`
(catalog PR #438). The catalog's Go code, model data, tests and notices are kept;
the login input-capture fix and its original browser assertions are also kept.
The combined inventory is **53 models / 429 controls / 482 executions**.

| Owner / handoff | Exclusive scope and observed status |
|---|---|
| root | Merge coordination, applicability record, final commits and serialized execution; combined validation is pending |
| lp_quality | Registry and formal-gate/test union resolved; source handoff complete |
| formal_review | Independent model/policy union review complete; no combined runtime acceptance claimed |
| docs_merge_b8 | Four documentation conflicts recovered and verified; source handoff complete |
| assets_merge_b8 | Merged-source console rebuilt in local commit `5219eb1`; 1353 inputs and unchanged original package notices verified; combined CI remains pending |
| catalog_compat_b8 | Read-only routing/authentication intersection review complete; Go and catalog fixtures kept byte-identical to incoming main |
| ci_watch_b8 | Exact-SHA monitoring for prior login `8149cac` and incoming main; these results do not validate the combined tree |

These are ownership and handoff entries, not a claim that seven workers are
currently running. The ceiling remains seven including root. Completed reviewers
can take the next independent task; heavy checks use one explicit local runtime
lane. The prior login's 102 focused checks and all eleven hosted checks passed,
but its base was `03060d4`. Actual Dia/1Password behavior remains unverified.
The new combined HEAD must pass its own quality, browser and formal gates.

The following catalog and IAM sections retain their original dated evidence.

## Current integration snapshot: AWS service catalog, 2026-11-06

Root integrates the model-driven AWS catalog on branch
`claude/adoring-noether-7fvwju` from main `03060d44f2c7c6e52aaf8799f4bacf21a22cd6bc`.
The user authorized implementing and merging the open backlog. This slice adds
the [catalog engine and four services](aws-catalog.md) (#297, #83, #111, #147);
later slices add further backlog services to the same engine. No other agent
writes these files; existing open PRs and their owners are untouched.

| Scope | Status and evidence boundary |
|---|---|
| `internal/aws/catalog` engine, specifications and embedded official subsets | Focused package tests, real-binary boto3 two-team flow and console/reference browser flow pass locally |
| Formal `CatalogResourceLifecycle` | Baseline and all 12 controls produce their expected counterexamples locally |
| Whole gates | `make before-commit`, `make e2e`, `make formal` and hosted CI are required before merge |

## Current integration snapshot: IAM53, 2026-11-05

Root integrates `feat/iam-managed-policy-53-20261105` from main
`4a649814bee73b06ee82aecc6ca952dfe6bc8137`. The public claim and contract decisions
are recorded in [Issue53](https://github.com/susumutomita/TenkaCloudSimulator/issues/53).
This is local integration in progress, not a merged feature or a claim that all
cloud services work. S3 multipart51 belongs to another owner and is excluded.

| IAM handoff | Actual status and evidence boundary |
|---|---|
| grader_http — store handoff | Native customer-policy/version/attachment implementation, transaction/aggregate/generation and corruption/quota tests; focused normal/race/vet passed |
| team_host — Query/SDK handoff | Twelve new Query operations; actual two-process SDK permission/default/restart/isolation/cleanup passed; existing six role operations also receive the authorized-generation guard |
| waf_console — console handoff | Cloudscape customer policy/version/role attachment flows; actual1440/390 browser, fourteen axe/reflow states and92 SDK round trips passed; full regression remains separate |
| cfn_adapters/waf_service — model handoff | New two-request/four-seed scope:13 invariants/11 sensitivity controls locally passed. Earlier four-request exploration timed out and remains unverified |
| cfn_callback — role guard/admission handoff | Existing six role paths generation-bound, strict cursor/identity/error/refusal tests; focused normal/race/vet passed |
| Root | Shared capabilities,21 served IAM templates, required test registration,30-model/175-control registry, English and Japanese entry points, immutable applicability and final gates |

The configured ceiling is seven local agents including root, with a four-CPU
cgroup quota. A slot or completed handoff is not an active worker. Heavy tests
are scheduled explicitly; current exact-HEAD full validation/CI/review and
publication remain integration gates. Do not infer final success from these
focused receipts. Existing main420 passed its own seven hosted checks.

API navigation has a separate [Issue421](https://github.com/susumutomita/TenkaCloudSimulator/issues/421).
Its source groups existing operations by cloud and labels Sakura unavailable;
strict browser validation identified a contrast problem that is being repaired.
The attempted Issue progress comment was denied and remains unposted. Sakura
catalog research adds no native service support; its new Issue creation was
denied and remains unexecuted. English documentation is integrated locally;
repository description update returned Forbidden and is unchanged. These held
publication actions must not be retried through another route.

The sections below are historical snapshots. Their “current”, agent counts,
branches, open PR states and test totals describe those recorded revisions,
not this integration or present assignments.

---


## Backlog integration: 2026-11-05

The user authorized backlog implementation and merging. Integration owner: `root`.
Base: `f5dba05` (PR412); branch `integrate/backlog-services-20261105`.
Existing service PR heads have successful hosted checks against their previous
base, but require integration with the merged provider/language console changes
and fresh complete verification before this integration can merge.

| Owner | Claimed scope and editing boundary | Acceptance / handoff |
|---|---|---|
| root | Integrate PR410 SQS console, PR407 Logs, PR408 Secrets and PR309 DynamoDB; shared routing/navigation, OpenAPI, generated assets, registry, applicability and publication | Preserve all main/service tests; current-HEAD quality, both coverage floors, E2E/audit and formal gates; merge only after required checks succeed |
| backlog_triage | Official Python GCP/Azure SDK acceptance: isolated test, pinned development dependencies and acceptance documentation | Two real local CLIs, persistence/isolation/CAS/checksums and denied access; propose minimal CI integration to root; no cloud calls |
| service_review | Independent Logs and DynamoDB source/contract review in a dedicated worktree; service leaf fixes only after handoff | Preserve existing API scope, state limits and all negative tests; report concrete blockers with tests |
| secrets_review | Independent Secrets Manager source/contract review in a dedicated worktree; service leaf fixes only after handoff | Preserve local practice-only scope and redaction; report concrete blockers with tests |

Four local agent slots including root are available in this session. Root owns
the heavy runtime/test lane; other agents coordinate execution rather than
running deadline-sensitive Go/WASI/browser/TLC checks concurrently. Original PR
branches are preserved. WAF50 and unrelated historical candidates are excluded.
No real cloud resource, release or repository protection change is authorized by
this implementation record.

PR408 and PR410 advanced during validation. This integration includes their
reviewed `ffdf1e29` and `39fca989` revisions respectively; newer locale, alias,
login and test deltas remain in those open PRs. Only unchanged PR309 and PR407
can be reconciled as superseded after the integration actually merges.

### Integrated source and remaining acceptance

All four service candidates are composed with PR412's provider/language shell.
The service leaf reviews added strict DynamoDB admission checks, canonical KMS
operation IDs in Japanese, and Japanese SQS/Logs/Secrets presentation. Native
API/SDK lifecycle and served-example checks have passed on the composed source;
the pinned official Google/Azure Python SDK check also passed with two independent
authenticated local CLIs. These focused results do not certify the final HEAD.

The acceptance inventory now contains 42 mandatory E2E cases and 26 finite models
with 149 sensitivity controls. Current-HEAD race/coverage, exact-file applicability,
all 175 finite baseline/control runs and hosted checks remain required. Local
Chrome is denied at process initialization, so actual browser operation and the
six Japanese desktop/mobile screenshots must pass in hosted CI before merge.
See [the integration scope and limits](acceptance/backlog-services.md).

---

## Historical SQS/login-main source refresh

Mainfcf added the merged native-clipboard login/error handling to main26d.
That isolated SQS refresh planned 28 E2E cases and 19 models / 98 controls.
Its acceptance requirements and restricted publication delegation applied to
that previous task; the current integration above has a new explicit user
authorization, shared source composition and a larger verification inventory.

## Previous console integration snapshot

Current integration snapshot: 2026-11-05. Integration owner: `root`.

Verified public main: `fcf8e702b45953cdeeba49883a238e638af725e8` (PR411 merged).
All six main checks / three workflows succeeded, including CI41941140492 and
Formal41941140471. This does not establish Dia-specific compatibility.

## Current console assignments and integration

All five source scopes are privately integrated on
`integrate/cloud-console-contract-20261105`; no new UI PR is published yet.
Each author used a dedicated worktree from the shared `c334f4f` baseline.

| Owner | Delivered scope | Verified / remaining |
|---|---|---|
| root | shared locale, Login extraction, integration/assets, focus and final gates | Canonical 22-package embed/build, actual clipboard and file login, language and provider focused tests PASS; whole gates/CI pending |
| cfn_adapters | provider catalog/search, AWS-only Cloudscape shell, S3/SSM/CFN translation | Five catalog unit checks PASS; root fixed keyboard focus recovery and provider-owned control styling |
| team_host | GCP shell, staged create, live generation-conditional object metadata, docs | Syntax/11 presentation checks and root actual metadata412→200/browser lifecycle PASS; authenticated Google pixels unverified |
| waf_service | Azure resource menu/table/action pane, pane locale, docs | Root actual lifecycle/properties/native frames PASS; three public official images inspected, authenticated Portal unverified |
| grader_http | masked explicit token-file fallback and bounded read revision tests | Actual embedded chooser/login/logout/clipboard/races PASS; Mac/Dia real device remains unverified |
| cfn_callback | seven AWS child modules and success notices; independent integration review | 435 presentation translations; default-English erasure exactly matches seven base modules. Found menu-focus issue; root fixed and browser checked it |
| waf_console | provider/language acceptance and latest PR CI observation | Existing HTTP/data/auth/peer/download oracles retained; 22 provider and12 language audit states PASS, no external requests/page errors |

Focused checks do not certify the full integration HEAD. Full Go/race, both
coverage floors, 28-case E2E, audit, immutable formal and hosted CI are the
remaining gates. GCP/Azure official SDK execution and authenticated cloud visual
parity are unverified. See [common contract](design/0006-console-provider-contract.md).
There are seven local slots including root, not 200 executing agents. Heavy
checks are explicitly reserved within four available CPUs. Library screenshot
saving failed at the helper's tools-list network request; candidate images are
preserved locally and no alternate write was attempted.

## Other work boundaries

Other open PRs retain their existing public descriptions and review state.
Their metadata and Ready transitions are outside this console change. The
previously denied PR408 Ready transition and WAF50 security reproduction remain
stopped. No Composio, real cloud mutation or protection-setting change is part
of this work.

---

# Historical roster: 2026-11-04 integration snapshot

# Development roster

Current integration snapshot: 2026-11-04. Integration owner: `root`.
This section is the single current assignment roster. The previous snapshot is
retained below as history. Follow the [parallel rules](contributing/parallel-development.md)
and [shared contracts](design/0005-parallel-service-contracts.md). Backlog issue
creation, source implementation, executable verification and public merge are separate states.

## Public baseline and current integration

The last independently checked public baseline is
`b61d0299a2139661f01ef29ae5826e4c070cc7bf`: PR313 (SSM model), PR308
(make start), PR311 (API reference), PR315 (SQS), and PR312 (logout/README)
were individually verified and merged. All six main checks succeeded, including
[CI41855423133](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41855423133)
and [Formal41855423028](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41855423028).
This is a recorded baseline, not a claim that no other remote work exists.

The multi-cloud storage integration is tracked in [Draft PR406](https://github.com/susumutomita/TenkaCloudSimulator/pull/406). It is published for review and exact-HEAD hosted CI, and remains unmerged. New merges are on hold under the latest owner instruction; no blanket merge is attempted. Kumo
is an architectural reference, not the base engine for these independently written
providers. Existing third-party notices are preserved. Composio is not used.

## Current ownership and verified boundaries

| Owner / editing boundary | Actual state | Remaining dependency / acceptance |
|---|---|---|
| `root`: server composition, dispatch, OpenAPI, navigation, generated assets, README and final gates | Integrated authenticated GCP/Azure routes; two-server native normal/race/vet and binary build pass. Existing locked console dependencies reproduced unchanged main assets before the new build | Fresh whole Go/race/build and both floors94.95%/90.46% now pass; full22-case E2E/start also pass; immutable16-baseline/82-counterexample and83-blob applicability checks also pass on code input73de6a7. Current hosted CI/review is authoritative in Draft PR406; merge remains held |
| `cfn_callback`: common Actor context/access hook and its model | Full Go race/build/quality gate and one-profile94.83% statement/90.28% unfiltered source pass. Interrupted E2E is retained as not passed; full21-case rerun and immutable11/42 formal checks passed at private2b193 | Actor13-file input is preserved at2b193; separate CloudWatch Logs main-b61 source integration is in progress with focused native/SDK proof. Final shared-file composition stays with root |
| `waf_service`: GCP Storage leaf, project-owner factory, generation download and ProjectAuthority model |27 native tests normal/race/vet passed;90.76% package source coverage, finite leaf baseline+8 controls and owner-authority baseline+6 controls passed | Root native/browser/immutable gates now pass; versioned official SDK execution remains unverified. No Google IAM/OAuth or official SDK success claim |
| `cfn_adapters`: Azure Blob leaf/factory/range and independent model review |31 native tests normal/race/vet passed;91.60% package source coverage; original finite baseline+11 and BlobRange baseline+8 controls passed; typedXML288 serializer byte-goldens preserved | Official Azure SDK and final root gates; owner-only policy is not Azure RBAC. HTTP Range has a finite normalized-selection model; concrete header/SQL/SDK behavior is native-tested |
| `waf_console`: GCP view and two-provider actual browser acceptance |2 CLI/2 DB,1440/390px actual lifecycle/download/state isolation, reader403, logout/relogin and Scalar Send pass;18 axe states0, strict reflow, no external requests/page errors. CPU released | The helper is mandatory and full22-case regression passes. Authenticated real cloud visual parity remains unverified |
| `team_host`: Azure view, escaped-path seam and formal/source reviews | Azure view included in actual browser proof; root source and narrow CFG parser reviews completed | Portable same-lock rebuild is byte-identical and narrow source/applicability review is recorded; hosted CI and future scopes remain; no shared-file rewrites or runtime jobs without reservation |
| `grader_http`: official SDK acceptance source | Real official-import two-CLI acceptance source prepared; syntax/preflight checked. Missing SDK packages produce explicit failure before network/process startup | Approved/verified development dependency availability and actual official SDK execution; never count native HTTP as SDK proof |

The environment has seven slots including root; that is a capacity, not a claim
that every slot or every service is executing. Heavy Go/WASI/browser/TLC jobs are
explicitly serialized to protect existing deadline assertions. A completed author
may perform independent source review while another owns the runtime lane.

[Storage boundaries](multicloud-storage.md), [browser acceptance](acceptance/crossprovider-console.md)
and [official SDK acceptance](acceptance/crossprovider-official-storage.md) record
verified and unverified behavior. Models are finite abstractions, not Go refinement
or complete real-cloud compatibility proofs. Two in-process HTTP servers and two
actual CLI processes are distinct evidence and are recorded separately.

---

# Historical roster: 2026-11-04 09:04 UTC

**Snapshot: 2026-11-04 09:04 UTC. Update owner: the integration owner (`root`).**
This historical snapshot is retained for decision provenance, not current assignment or completion status. Update it at claims, handoffs and
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

## Historical internal assignments

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

### PR407 source refresh

CloudWatch Logs basic7 only, normal merge of main5c97 into publiccca ancestry. Source integration pending currentHEAD validation; retention/CFN/Lambda delivery remain unsupported. Separate private CFN candidate is excluded.
