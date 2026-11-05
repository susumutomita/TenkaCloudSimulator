# Formal validation and applicability

This gate runs finite TLA+ specifications and checks an exact-file applicability
record. It does **not** prove that Go refines those specifications, establish full
AWS/IAM correctness, authenticate a human reviewer, or prove unbounded liveness.
The native race, SDK, browser, containment and coverage gates remain independent.

## Model inventory and sensitivity

`formal/models.json` is the executable inventory. Every `.tla` and `.cfg` in
`formal/` must be registered; required existing models cannot be removed.
Each entry names exact implementation paths, finite bounds, limitations, checked
invariants and mutations. The registry is a reviewable correspondence claim, not
machine-discovered semantic coverage. A newly added implementation file is not
covered merely because an existing model passes: its exact path must have an
appropriate reviewed scope mapping and property argument.

The current registry has 33 baselines/197 required named mutation controls
(230 required executions). The previous30/175 entries and controls are preserved;
RESTRouteMatching, RESTProxyEventBoundary and NativeWireProjection add three
bounded contracts and22 semantic defect controls.
The new IAM scope is four API-reachable seeds and at most two subsequent requests;
the broader four-request exploratory candidate timed out and is not claimed.
Registration and Python guards are not a successful immutable TLC execution.

| Model | Finite contract | Important exclusions |
|---|---|---|
| ObjectStore | two buckets/two keys; parent integrity, abstract allow/deny | concrete IAM, HTTP, SQL, crashes |
| StackOwnership | two owners/two names; retained drift reservations | authentication, callback recovery, full CFN |
| S3Versions | one bucket/two keys; at most three history entries per key | suspended versions, byte quotas, SQL |
| NativeIAMOwnership | two case-folded names; native/legacy exclusivity | trust parsing, full IAM, process isolation |
| [IAMManagedPolicies](formal-iam-managed-policies.md) | four reachable seeds, two-request suffixes; stored aggregates, default/version/deletion/edge invariants | unsuccessful four-request exploration, Go/SQL refinement, concrete auth race, wire parsing/UI, actual five-version/twenty-attachment quotas |
| RESTDeploymentSnapshot | one API/two deployments/two handlers/one stage | path matching, Lambda execution/permissions |
| RESTRouteMatching | finite template/carrier choices; arity, captures and literal specificity | concrete byte scanner, general paths, method selection, Go refinement |
| RESTProxyEventBoundary | fixed input samples, projected application data, excluded transport authority and abstract event budget | concrete HTTP authentication, UTF-8/JSON parser, case-fold implementation, real memory bounds |
| NativeWireProjection | three ordered fields, six value kinds and provider-selected omission; complete output or error | JSON escaping/parser, XML, exact AWS schemas, Go refinement |

All registered models run even for documentation-only changes. The supported CFG
grammar is deliberately restricted to named invariants, init/next/specification,
Boolean deadlock setting and finite literal constant assignments. Views,
constraints, substitutions and unknown directives fail instead of masquerading
as checked properties or silently pruning the explored behavior. Missing/empty
model, cfg, checksum, Java or jar; a wrong jar hash; unsuccessful TLC; unexpected
invariant names; an unchanged/stale mutation anchor; or a failed sensitivity
control fails the command. Mutation success means TLC exit **12** (a safety
violation) and the **specified invariant** counterexample, not a parser failure,
timeout, crash or any arbitrary nonzero exit. The exit class comes from the pinned
[TLC 1.7.4 source](https://github.com/tlaplus/tlaplus/blob/v1.7.4/tlatools/org.lamport.tlatools/src/tlc2/output/EC.java).

The downloaded tool is the official TLC1.7.4 release verified against
`formal/tla2tools.sha256`. `TLA_JAR=/path/to/tla2tools.jar make formal` runs the
suite without downloading or substituting a tool. This default working-tree mode
is explicitly **not merge evidence**. The CI command adds `--merge-gate`, rejects
dirty/staged/untracked policy/model inputs, verifies HEAD blob hashes, checks
inputs again after execution, and runs copies of the verified HEAD specifications
and verified jar. Reports under `reports/formal/`
include actual process logs, input/config hashes, bounds, limitations and a JSON
success/failure result. Controlled-process Python unit tests validate failure
handling; they are not TLC/model evidence.

No Poteto configuration/toolchain was found in the tracked baseline instructions
or files. No Poteto, proof-assistant, implementation-refinement or complete
simulator proof is claimed.

## Exact-change applicability record

Before committing, fetch/rebase onto the intended main baseline, then stage all
implementation, model, test and documentation changes:

```sh
export PYTHONPYCACHEPREFIX="${TMPDIR:-/tmp}/tenkacloud-formal-cache"
base=$(git rev-parse origin/main)
git add <changed-files>
python3 scripts/check-formal-applicability.py init --base "$base"
# Edit formal/applicability.json: explain every disposition and model binding.
git add formal/applicability.json
git commit
python3 scripts/check-formal-applicability.py check --base "$base"
python3 scripts/test-formal-gate.py
python3 scripts/test-formal-applicability.py
TLA_JAR=/path/to/tla2tools.jar make formal # developer specification iteration
PYTHONPYCACHEPREFIX=/tmp/tcsim-formal-cache TLA_JAR=/path/to/tla2tools.jar \
  python3 scripts/check-formal.py --merge-gate # clean committed HEAD only
```

`init` reads the staged index and intentionally writes **unresolved** entries.
It does not infer that a model is semantically applicable. Do not run it before
staging new files; untracked files are not Git change evidence.

The committed record binds the immutable base SHA and SHA256 of every added or
modified blob. Validation rejects dirty index/worktree inputs and untracked
policy/model replacements, including ignored replacements. Keep Python bytecode
caches outside the checkout (`PYTHONPYCACHEPREFIX`); merge evidence must not read
uncommitted policy bytecode. Deletions use `null` and retain their prior model-scope obligation.
`check` compares the committed HEAD tree against the trusted event base. Missing,
extra, duplicate, stale or unresolved entries fail. The record excludes only
itself to avoid a recursive hash; no source or model is excluded. A later source
change or rebase requires regenerating and reviewing the complete record.

A `modeled` entry names registered models, at least one checked safety invariant
beyond TypeOK, an explicit applicability argument and what remains unmodeled.
The model must declare that exact source/artifact path. Unknown implementation
paths cannot be exempted with prose or an unrelated model. Add an appropriate
specification/scope mapping and native tests, or hold the change.

Known documentation, presentation, test support, validation/build metadata and
formal infrastructure have explicit nonformal dispositions with reasons. These
are declared classifications, **not** proof that a diff is stateless. For example,
auth changes hidden inside a UI diff require reviewer correction and the relevant
model; path classification cannot establish program semantics. A registered state
path cannot be relabeled as documentation. No `reviewed_by` checkbox/name is
accepted as evidence of an authenticated review.

This single-record design deliberately fails when a merge-group's aggregate diff
is not completely described; multi-PR merge-queue batching is not certified.
PRs must be up to date and each tested record must match its actual event base.

## Required check and remaining repository configuration

The intended unique GitHub Actions job/check name is **`Formal validation`**.
It runs on every PR and main push, merge-group and manual replay without path
filters, skip conditions or `continue-on-error`; feature-branch push is omitted
to avoid two same-name checks with different PR/base interpretations. It runs independently
of the long Go/browser job. It uses an unprivileged `pull_request` workflow,
read-only contents permission, pinned checkout/artifact actions and the checked
TLC jar. `pull_request_target` and write credentials are unnecessary. Logs and
applicability evidence must be uploaded even when a check fails.

A workflow file alone does not make this a merge requirement or make a PR's edits
to its own checker unforgeable. GitHub documents [required status checks and
unique check names](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches).
An authorized maintainer must separately inspect/configure main protection:

- require the exact `Formal validation` check, selecting GitHub Actions as its
  expected source where supported, alongside all existing required checks;
- require the branch to be up to date before merging and prevent bypass of those
  requirements, including applicable administrator/bypass privileges;
- require appropriate independent review/CODEOWNERS review for `formal/**`, the
  checker scripts, workflow and applicability classifications; dismiss stale
  approvals or require approval of the latest reviewable push;
- if a merge queue is used, require this check on `merge_group` too and verify its
  full aggregate-record behavior before relying on batching.

Current `CODEOWNERS` assigns `* @susumutomita`; that does not establish independent
approval of that same account's own PR. Review/APPROVED operations and repository
settings are not performed by this gate. Actual protection inspection through the
available GitHub integration returned HTTP403 `Resource not accessible by
integration`, so required-check/ruleset enforcement is **unknown**, not claimed
configured. No security-sensitive repository settings were changed.


## Secrets Manager registration and executed evidence

Historical Secrets registration at `c97bdde` retained ten prior models/thirty-six
controls and added SecretsVersionLifecycle (six), SecretsRevealEpoch (three),
and SecretsDisclosureChannels (five): thirteen baselines and fifty controls.
All required models/invariants and each reviewed control identity are protected;
unit fixtures reject every individual control removal.

At immutable `c97bdde`, the complete63 TLC cases,19 gate units,21 applicability
units and42 exact changed blobs passed. Forty-eight controls have behavior
traces; BrowserSessions_RawOperatorCookie and
SQSStorageBudget_BlockLegacyMaintenance contain explicit violating initial-state
valuations. Runtime/parser/type errors and timeouts are not counterexamples.
Named local receipts remain distinct from subsequent commits and hosted CI.
Documentation-only changes still require fresh immutable-head applicability/TLC
checks; the declaration itself never grants a new-head execution result.

The bindings cover named finite state/channel properties, not Go/SQL refinement,
cryptography or full AWS equivalence. Separate concrete results at `0888924`
cover full Go/race/build, one-profile94.78% statement/90.12% unfiltered source,
24 E2E and actual start checks; baseline50 and separate Secrets5 automated screen
checks also passed. SQL rollback/ignored-write faults, policy matching, storage
budgets, persistence, raw routing and browser behavior are exercised independently.
Hosted exact-HEAD CI and publication review remain separate from local results.

## Current Secrets ACK / metadata-read union

Historical ACK integration retained26 prior models/149 literal controls.
SecretsMutationRefresh adds five properties/four named controls:27/153,180
required finite executions. It covers only live-ACK form exit/draft clearing/read-only
retry/fresh form intent. It excludes ambiguous unacknowledged writes, React/Go/SQL
refinement and auth. Earlier eac3/f5d134 success is a historical input, not current
later-revision verification. [Mapping](formal-secrets-mutation-refresh.md).


## Azure properties lifecycle candidate

Historical registration at `8e656eb` preserved 27 prior models/153 controls
and added AzurePropertiesEpoch (nine invariants/five controls), producing
28 baselines + 158 controls = 186 executions. The merged `ce622018` subsequently
passed the full 186-case gate and its main CI. Later Container Properties adds
AzurePropertiesTarget without changing those TLA/CFG files, bounds, properties
or controls. Use the current inventory above and exact-revision CI; older
receipts do not certify later source changes.

The model abstracts component lifecycle/request epochs, successful HEAD publication,
close/open clearing and locale-stable state; it does not prove JavaScript, native
HTTP parsing, Go/SQLite authorization, SDK compatibility or full Azure parity.
New immutable formal/application checks and hosted gates remain required.


## Azure Properties typed targets

The Container Properties extension preserves every maince622 model/config/control
and adds `AzurePropertiesTarget`: complete kind/parent/name target identity,
read-only HEAD route carriers and exact container/blob displayed field sets.
All seven invariants and six literal controls are mandatory. A pinned scoped run
passed the baseline (85 generated / 20 distinct states) and all six expected
semantic counterexamples. Registration plus this scoped result is not a full
193-execution or implementation/refinement proof.

[Source correspondence and limits](formal-azure-properties-target.md) explain
ghost capture provenance, the separately checked lifecycle/epoch assumptions and
concrete native/browser obligations.
