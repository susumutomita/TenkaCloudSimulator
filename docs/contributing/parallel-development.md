# Starting and integrating parallel work

These rules apply to authorized parallel development in this repository. They
coordinate work; they do not grant new permission to publish, deploy, contact
others, access AWS or start agents. Follow the user's current instructions and
existing authorization without repeatedly requesting approval already given.
Use the current [working contract](../../AGENTS.md) and
[service integration contracts](../design/0005-parallel-service-contracts.md).
Maintain current assignments and evidence in the single
[development roster](../development-status.md); the service roadmap is a plan,
not a second live roster. Update the snapshot at claims, handoffs and merges.

## Before starting an agent

1. Read current main, the working-tree status, relevant code/tests and the open
   **and closed** Issues and PRs. Check active local branches/worktrees and the
   integration owner's task list. A merged historical implementation, current
   local candidate and open PR are different evidence; do not create duplicate
   work because a service is absent from main alone.
2. Claim one concrete Issue or agree its scope with the integration owner before
   editing. Record the owner, base commit, dedicated branch/worktree, exact files,
   dependencies, acceptance tests and shared contracts. Identify unverified or
   unsupported behavior. A short Issue note is enough; do not invent a separate
   approval ceremony for routine work already authorized.
3. Check actual available agent slots, CPU/memory and heavy-test reservations.
   Count the integration owner against local slots; track independently hosted
   external workers separately. Start only independent tasks that fit the available capacity. A plan for many tasks is not evidence
   that those agents exist, have accepted them or are running.

Use explicit status: **planned** means proposed, **reserved** means awaiting an
owner's acknowledgement, **claimed** means an acknowledged owner/branch/scope,
**in progress** means work has started, **ready for integration** means a specific
commit and focused evidence exist, and **merged** requires verified remote merge.
For example, SQS/SNS/DynamoDB reservations with another team must be checked with
that team before assignment; do not report them as active or completed merely
because they appear in a roadmap. External owners retain their agreed files.

A useful claim is one paragraph:

> Issue, owner, status; base SHA and branch/worktree; owned files and excluded
> shared files; dependencies and agreed interface; acceptance checks; next handoff.

## Keep concurrent edits independent

Each writer uses a dedicated branch/worktree. Preserve unrelated work, existing
PRs and history; do not reset, overwrite or force-push another owner's branch.
One file has one active writer. If two tasks need the same state machine, assign
one owner or serialize the edits rather than racing to resolve conflicts later.

The integration owner coordinates Gateway/provider registration, server dispatch,
runtime authority, common database/CFN contracts, OpenAPI, global navigation,
generated assets, dependency/lock/legal changes, shared CI and catalog enablement.
Workers deliver independent modules/tests/components and request a concrete
handoff for shared-file edits. A handoff names the file, receiving owner, base
commit, proposed interface/change and compatibility effect. Update dependent
owners when that interface changes; do not silently change another worker's API.

The [Issues37–44 ownership table](../design/0005-parallel-service-contracts.md)
is an example of this division: one CFN owner handles both service adapters and
custom-resource orchestration, while callback, grader HTTP, WAF and UI workers
own separate files. Issue37's native API Gateway work already has PR45; a worker
must inspect that PR's actual current state before proposing overlapping work.
Issue/PR state changes over time, so recheck it rather than treating this example
as a live status report.

Share native SQLite state, validation, quotas, IAM and ownership rules. Do not
create a second emulator backend or hold a database transaction while waiting
for a guest that needs the same database. Keep callback response authority and
grader HTTP authority separate. Changes across providers must preserve the
provider-neutral core and independent provider semantics.

## Make progress and evidence reviewable

Keep concise decisions, dependencies, tested commits/commands/results and blockers
with the Issue. Distinguish a planned test, a stub/unit check, a real service test,
a browser operation and a complete original-problem lifecycle. Report the exact
source hashes used; preserve original problem/grader bytes and license notices.
Do not include tokens, flags, credentials, private outputs or sensitive logs.

If public logging or another action is denied, preserve a safe local worklog and
tell the integration owner the exact action/target/status and stated reason.
Stop that action, not unrelated authorized work. Do not evade a denial through
another API, Issue body, PR, account or agent, or by publishing the blocked payload
inside documentation. Retry only under the applicable approval rules with new
relevant authorization and a confirmed unexecuted outcome; check uncertain results
before retrying. Do not claim a pending or denied action succeeded.

Code presence, service registration, an API200, an emulator's source inventory
or a projected template is not completion. Fail explicitly for unsupported
operations. Keep unsupported problems unavailable until their actual original
initialize→operate→grade→cleanup path works, including team isolation and failure
recovery. Real Cloudscape components and a screenshot alone do not establish AWS
console fidelity; record the reference and the actual operation/remaining gaps.

## Integrate a concrete commit

Workers commit only their owned files and hand off the SHA, scope, dependency
versions, focused tests, remaining risks and unexecuted checks. Under existing
publication authorization, use one dedicated Draft PR for the change; first check
for an existing PR/branch and update that work instead of opening a duplicate.
Where publication is not authorized or is denied, keep the local commit ready and
report its state. Do not ask again merely because an authorized step is routine.

The integration owner reviews each patch and composes shared files. Run the gates
required by [AGENTS.md](../../AGENTS.md), including race/coverage, relevant native
SDK and UI/API E2E, license/security checks and formal models for changed model
behavior. Coordinate expensive Go/WASI/browser runs so concurrent jobs do not
invalidate timing or exhaust capacity. Do not lower thresholds, ignore failures
or substitute compilation for foreign-platform execution.

Before merge, review the **current exact PR HEAD**, its required CI, unresolved
review findings and dependencies. A successful older run is not current evidence.
Merge only when authorized and important blockers are resolved; confirm the remote
merge result and then main CI. A new commit requires checks for that commit.
Report service scope and remaining gaps after each increment without calling a
partial simulator finished. Keep release/deployment/AWS changes within their own
explicit authorization.
