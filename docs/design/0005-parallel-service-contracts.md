# Parallel service contracts and integration ownership

This is the dependency contract for Issues37–44, not a claim of completed WAF/custom resources or full WP. The execution environment supports at most7 concurrent agents including one integration owner;200 concurrent agents are not available here. Work uses separate cloud worktrees/branches. No real AWS sign-in/resource creation, deployment, public release, forced push or duplicate recovery PR is authorized by this plan.

| Issue | Owner role / boundary | Dependencies |
|---|---|---|
| #37 native API Gateway/Lambda proxy | Integration owner: existing tested native implementation, HTTP entrypoint, shared registry/server/OpenAPI/navigation/assets/CI | Native IAM main bdff8b7 |
| #38 WAFv2 | Backend worker: new wafv2_* files/tests, SQLi library and coordinated notices | #37 stage contract; matching/store can start independently |
| #39 CFN service adapters | Single CFN owner: new adapter files and coordinated compiler/transaction edits | #37, then #38 for WAF adapters |
| #40 cfnresponse primitive | Callback worker: new callback store/capability/module/tests only | Host-owned invocation scope; no CFN state-machine edit |
| #41 grader HTTP | HTTP-capability worker: new resolver/adapter/trusted-module/tests only | #37 execution port; shared runtime changes require integration review |
| #42 WAF console | UI worker: new wafv2.jsx/browser helper/reference notes only | #38 wire schema; integration owner registers/builds |
| #43 original WP lifecycle | Test worker: new wp-lifecycle/oracle helpers and evidence only | #38–42, #44; original-source preparation can start now |
| #44 custom-resource orchestration | SAME CFN owner as #39 | #39+#40; no second writer of cloudformation.go |

## Native state and execution port

All AWS state remains in provider-owned SQLite tables, one process/database/token authority per team. No second in-memory emulator or problem-specific logic enters core/runtime. Native service and CFN provisioning share validation, quotas, transaction-aware primitives, ownership and drift guards. Do not nest a native store's BeginTx inside an active CFN transaction; introduce coordinated transaction-aware helpers rather than duplicating a backend.

The integration owner supplies the common REST execution path: trusted region/API/stage/method/actual path/raw query/headers/body -> resolve immutable stage snapshot -> WAF decision -> bounded real saved-role Lambda -> validated proxy response. No worker independently edits Gateway/server dispatch or the current API handler. The path must retain URI template versus actual path and repeated query values. Host connection authentication precedes it. All cookies, simulator authority and AWS session/signature credentials are excluded from the guest; explicit application Bearer data is allowed. External browser responses remain non-executing attachments until a separately reviewed application-origin design exists.

WAF port: NewWAFStore(db), ServeHTTP(w,r,region,authorize), Evaluate(ctx,region,resourceARN,uriPath,body) -> WAFDecision with ALLOW/BLOCK, CountedRules and terminating-rule metadata. The trusted adapter supplies the actual API stage ARN and original request path/body; guest input never chooses association/authority. Count continues; Block stops before Lambda. API Gateway default body inspection16KiB and CONTINUE prefix behavior must be independently verified. Wire SearchString is base64; CFN supplies plain text and the adapter encodes it. Unknown statements/transforms/actions fail explicitly.

## Separate capabilities

The callback primitive is a one-shot host-owned response sink bound to stack/request/logical/physical IDs, nonce, deadline and invocation. It does not perform a network PUT to arbitrary ResponseURL. The CFN owner persists/reconciles pending states and releases SQLite transactions before waiting for a guest whose SDK needs the same database. Duplicate/FAILED/expired/cancelled/restarted callbacks and reverse cleanup must be tested. Original EvidenceSeed needs S3 PutObject/DeleteObject; do not invent unrelated guest SSM writes. Retained versions/markers cannot be silently destroyed or ignored.

The grader HTTP capability is different: a host-derived scope and injected executor allow only known same-environment API stage URLs, in-process, with bounded methods/body/response/calls/aggregate/deadline/recursion. No DNS/general networking, metadata endpoints, console/control routes, cross-team targets or arbitrary redirect is allowed. Preserve the original urllib caller and error/status behavior. Two outer graders must not occupy both VM slots and cyclically wait for nested app calls; agree reservation/fail-fast/retryable behavior before central runtime integration. A stub executor test alone does not complete this capability: integrate the real API and saved-role effects.

## Workflow and acceptance

Each worker commits only its owned files in its own branch and records focused tests, limits, decisions and blockers in its Issue. Shared dependency/lock/legal changes have one coordinated owner. Generated assets, capability/catalog enablement and CI registration belong to integration. No service is advertised from code existence or a200 alone. Full original WP remains unavailable until the pinned original template/handlers/grader and real host ledger pass two-team initialize→operate→all seven flags (six live CHECKS plus evidence flag)→cleanup. Root reviews each patch, reruns integration/race/both90% coverage/E2E/axe/formal/security/license checks, and uses exact-HEAD CI/review before merge, then main CI.

A newly created AWS account is not permission to sign in or create resources. Use public primary material now; reference requests should name the needed read-only screen and method, never request secrets. Relevant future references: S3 bucket list/create/details/versions/settings; IAM role create/trust/inline permissions; Lambda create/Code/Test/Configuration-Permissions; API Gateway resource/method/deploy/stages; WAF regional ACL/rules/Count-to-Block/association. Authenticated screens remain unobserved until separately authorized evidence is supplied.
