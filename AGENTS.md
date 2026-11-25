# TenkaCloudSimulator agent contract

Normative rules for every agent, subagent, and reviewer. Read this file at the working base and any applicable nested AGENTS.md before edits. Keys below are acceptance criteria, not executable configuration.

```yaml
start:
  reconcile: [current main, code, tests, open/closed issues and PRs, live ownership]
  handoff_transport: agent-to-agent only; no routine human handoff or review; ask the owner only for missing scope or authorization
  handoff_required: [issue, base_sha, scoped_operations, owned_paths, acceptance_checks]
  public_contract_change_requires: [README.md, docs/design/0001-go-standalone.md]
  parallel_rules: docs/contributing/parallel-development.md
  parallel: independent worktrees; one active writer per shared file; fill available authorized capacity without duplicates or resource overcommit
  continue: consume results and start the next executable stage immediately; block only the affected work, record cause and resume condition, then advance other work
  progress_excludes: [polling, assignment alone, status reports]
  integrity: preserve history and unrelated changes; inspect uncertain outcomes before safe retry; never bypass access restrictions or read/write .env, credentials, or secret files/directories
  authorization: PR, push, merge, release, deploy, and cloud operations and external agent access or transfer each require existing scoped approval
architecture:
  artifact: standalone Go binary; embedded console, Scalar, SQLite, and pinned CPython/WASI; no host Python/Node/Docker, external DB, or real cloud runtime dependency
  core: provider-neutral storage and policy primitives; no provider dispatch or exercise IDs
  providers: concurrent, distinct protocols, models, namespaces, authorization, and UI; do not inherit AWS semantics
  aws: own IAM actions/ARNs and protocol/state; native and legacy ownership/mutation remain atomic; reject cross-adoption; own function state and restricted execution-role SDK bridge
  state: shared actual HTTP API and SQLite state across console/Scalar/SDK; preserve atomicity, foreign keys, and input bounds
  unsupported: reject unsupported operations and policy fields; no fake success or runtime claims for catalog metadata, stubs, mocks, or placeholder UI
  perimeter: numeric loopback and local token auth by default; no outbound cloud calls; distinct authenticated and selected exercise identities; participants cannot select admin
  tenant: one process, SQLite database, and token authority per team
  guest: deny host FS/env/process/network; maintain pinned runtime rights/provenance and negative containment tests; only trusted providers supply bounded capabilities and read-only modules; guests never select authority
  inference: only opt-in numeric-loopback Ollama or explicitly selected absolute-path Apple helper; no backend/cloud/synthetic fallback or prompt/response storage or logging
  bedrock: AWS owns wire shapes, bedrock:InvokeModel evaluation, and operator model mapping
  boundary: TenkaCloudChallenge owns questions/scenarios/scoring/required capabilities; TenkaCloud owns platform integration; no problem-specific simulator logic
done:
  define_before_edit: official spec + issue -> operations, inputs, observable state, errors, cleanup, applicable entry points; evidence in existing issue, PR, or tests
  environment: actual simulator built from one candidate SHA; approved isolated hosted runner; no owner's Mac or live cloud
  console: real browser + provider-equivalent entry/sequence/inputs -> actual API, persisted state/read-back, errors, cleanup
  api_reference: actual Scalar Send -> same simulator API, response and state
  terraform: pinned official provider -> apply, read-back, destroy, verify removal
  surface_gate: every applicable operation and entry point must pass; no substitution; justify not applicable
  checks:
    always: make before-commit
    protocol_persistence_console_docs_ui: make e2e
    model: TLA_JAR=... make formal
    finder_fixer: docs/contributing/software-factory.md; register independent acceptance before claim; run make test on clean exact HEAD and include acceptance report in independent review
    invalid_evidence: [required check failed or not run, required test skipped or missing, failed cleanup, stale SHA, altered log, weakened regression]
  claims: cross-build != cross-platform execution; bounded model checking != implementation proof; optional skills/role prompts != gates
  worker_receipt: [candidate_sha, operations, compatibility, commands_and_evidence, passed_failed_not_run_not_applicable_with_reason, blockers, next_step]
  merge: integration owner verifies independent agent review of exact SHA + required CI, then normal authorized merge per service or agreed scope
  after_merge: verify remote merge and main; repeat impacted checks if tested source changed; only then report done; no service-wide claims from partial coverage
```
