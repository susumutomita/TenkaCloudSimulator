# Registered finite models

[`models.json`](models.json) is the executable inventory: **13 baselines and50
required negative controls**. Local immutable runs, including `c97bdde`, completed
all63 cases. Each source change still needs its own fixed-HEAD evidence; a prior
run is not silently treated as a new-head pass. Hosted CI/repository protection
is separate from the local receipts.

| Model | Negative controls | Bounded contract |
| --- | ---: | --- |
| [ObjectStore](ObjectStore.tla) |1 | Two bucket names/keys; parent ownership, no orphan objects, abstract allow/deny precedence |
| [StackOwnership](StackOwnership.tla) |1 | Two owners/names; retained drift claims and exclusive cleanup/adoption |
| [S3Versions](S3Versions.tla) |1 | Enabled version history/delete markers and history-aware bucket deletion |
| [NativeIAMOwnership](NativeIAMOwnership.tla) |1 | Two case-folded names; native active/retired claims versus legacy identities |
| [RESTDeploymentSnapshot](RESTDeploymentSnapshot.tla) |1 | One API, two immutable deployments/handler configurations and one stage |
| [SSMParameterState](SSMParameterState.tla) |5 | Two regions×two names, latest-row versions1–2, overwrite/refusal/read frames; version2 is an exploration cutoff, not an API limit/history feature |
| [StartupTokenVisibility](StartupTokenVisibility.tla) |3 | One configured file/two tokens, three file states, auth/show/TTY/CI inputs and at most two emissions |
| [SQSLifecycle](SQSLifecycle.tla) |11 | Two queue scopes, one message each, at most two receives; clock/delay/visibility/retention, current receipts and lookup/delete interleaving |
| [SQSStorageBudget](SQSStorageBudget.tla) |7 | Two scopes, four candidate rows; MaxRows2/MaxBytes3,729 exact initial assignments including legacy over-capacity states |
| [BrowserSessions](BrowserSessions.tla) |5 | Two clients, three fresh browser tokens, operator/participant credentials, clock0–3 and bounded parent/browser expiry |
| [SecretsVersionLifecycle](SecretsVersionLifecycle.tla) |6 | Two teams, two physical incarnations/team, two immutable versions and dummy values |
| [SecretsRevealEpoch](SecretsRevealEpoch.tla) |3 | Two resources/two request-navigation epochs and one component lifetime |
| [SecretsDisclosureChannels](SecretsDisclosureChannels.tla) |5 | Four operation contexts/sinks, Boolean native authority and at most two emissions |

Each `.cfg` lists the checked invariants and exact finite assignments. Registry
`source_scope`, properties and limits document the reviewed correspondence to
implementation/test caller paths. They do not execute those Go/React paths.
New runtime transitions are not automatically proved by an unrelated model.

## Run and interpret the gate

From the repository root, use the official TLA+ release1.7.4 jar matching
[`tla2tools.sha256`](tla2tools.sha256):

```sh
JAVA_TOOL_OPTIONS='-XX:ActiveProcessorCount=1 -Xmx512m' \
TLA_JAR=/path/to/tla2tools.jar \
python3 scripts/check-formal.py --merge-gate

python3 scripts/test-formal-gate.py
python3 scripts/test-formal-applicability.py
python3 scripts/check-formal-applicability.py check --base <full-immutable-base-sha>
```

`--merge-gate` requires a clean immutable HEAD and exact policy/registry/model/
source inputs before and after execution. It runs one TLC worker, the original
finite bounds and default60-second limit per case, writing per-case logs and
machine-readable evidence even on failure. Java/TLC is development/CI tooling,
not an application runtime dependency.

Baselines must finish successfully. Every registered negative control must
produce exit12 and its specified named invariant violation. Recorded runs have
48 behavior traces; `BrowserSessions_RawOperatorCookie` and
`SQSStorageBudget_BlockLegacyMaintenance` fail at an explicit initial-state
valuation. Those are real counterexamples, not invented multi-step traces.
Parser/type/runtime errors, empty mutation anchors, wrong invariants, unexpected
success and timeouts are failures, not sensitivity evidence.

All13 required models/invariant sets and all50 reviewed control identities are
protected. Unit fixtures remove each control individually and reject missing
models/configs/properties, stale/dirty inputs and incorrect process results.
The applicability gate binds each changed Git blob to a documented model or an
explicit permitted nonformal category. Prose alone does not exempt unknown
runtime code. Go module metadata exceptions are narrow; dependencies and sums
remain exact HEAD inputs. See [the validation policy](../docs/formal-validation.md)
for event bases, classification and independent-review limitations.

## Secrets correspondence and actual implementation evidence

SecretsVersionLifecycle covers abstract immutable values, unique current/
previous ownership, retained physical identity and authorization. SecretsRevealEpoch
covers explicit retrieval, reply ARN/context, Hide/navigation/unmount and late
response visibility. SecretsDisclosureChannels separates authorized Get from
metadata/error/log emissions. `AcquireRaw` is a call-site obligation, not an
access-control check inside the plaintext accessor.

These models do not prove SQL transaction failures, cursor signatures, quotas,
formatting, cryptography, memory erasure, browser layout/accessibility or complete
IAM/HTTP semantics. Native tests independently check real ABORT/IGNORE/deferred-
commit rollback, repaired retries, private-table frames, staging failures after
raw reads, physical budgets, metadata redaction and two-team persistence.
Browser/SDK tests execute the same actual HTTP state and reject stale reveals.

Recorded implementation results at `0888924` are full Go/race/build, one-profile
statement94.78% and unfiltered source90.12%,24 E2E/actual start checks, and five
CGO-disabled cross-compilation targets. At `c97bdde`,19 gate units,21 applicability
units,42 exact changed blobs and the complete13/50 TLC suite passed;50 baseline
screens and five separate Secrets screens passed automated axe checks. These
named receipts distinguish executed local evidence from later commits and
hosted CI. Source-equivalent documentation changes still receive fresh immutable
applicability/TLC checks.

The models are finite safety abstractions, not unbounded theorems, liveness,
Go/SQLite/React refinement, real-AWS equivalence or security/accessibility
certification. [Secrets scope](../docs/secrets-manager.md),
[SSM correspondence](../docs/formal-ssm.md), [SQS state/budgets](../docs/sqs.md),
[browser sessions](../docs/console-sessions.md) and [startup](../docs/start.md)
state the concrete limitations.
