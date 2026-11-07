# TenkaCloudSimulator working contract

This repository owns an independent Go single-binary multi-cloud simulator. AWS is the first provider, with S3 basics/version history/bucket settings, Standard SSM parameters, bounded Python Lambda execution/inline CloudFormation provisioning and a deliberately limited CloudFormation subset. Static console, local Scalar API reference, SQLite and the fixed CPython/WASI interpreter are embedded; runtime must not require host Python, Node, Docker, an external DB, or real cloud access.

- `internal/core/` owns provider-neutral relational object storage and the currently supported identity-policy matching primitive. Do not introduce cloud-specific protocol dispatch or problem IDs here.
- `internal/provider/` registers separate cloud protocol handlers. Preserve the ability to run several providers at once. New providers keep their own authorization semantics, resource model, namespace, and UI rather than inheriting AWS behavior.
- `internal/aws/` owns S3 REST/XML, SSM JSON 1.1, Lambda REST/JSON, CloudFormation Query/XML and its provider-specific tables (including atomic S3 bucket provisioning/ownership, tags and caller action preflight), and maps requests to documented IAM actions/ARNs. Native IAM role management is bounded Query/XML with service trust and inline-policy aggregation. Legacy identities retain a separate JSON control API; native/legacy ownership and mutation must be atomic and reject cross-adoption.
- `internal/server/` composes providers, local token access control and official Cloudscape/Scalar embedded assets. The console and API reference must use the same actual HTTP APIs and SQLite state as SDK clients.
- TenkaCloudChallenge owns problems, scenarios, grading, and required capabilities; TenkaCloud owns platform compatibility and integration. Do not add problem-specific logic here.
- internal/workload/ owns provider-neutral bounded language execution. Never grant guest code host filesystem/environment/process/network access or add problem IDs/scoring. Generic bounded capability callbacks and readonly module files are supplied only by trusted providers; guests cannot select their authority. AWS owns function state, ARN/IAM and the restricted execution-role SDK bridge. Preserve pinned runtime rights/provenance and all containment negative tests.

Read README and docs/design/0001-go-standalone.md before changing public contracts. Search for related capabilities, handlers, and tests. Preserve Git history and unrelated/uncommitted work. Never read/write `.env`, credentials, or secret directories. Do not perform release, deployment, cloud provisioning, push, or PR without the relevant authorization.

For authorized parallel work, follow [parallel development rules](docs/contributing/parallel-development.md) and check/update the single [development roster](docs/development-status.md) before claiming a task or starting another agent.

Do not silently simulate success for unsupported operations. Unsupported policy fields must be rejected. IAM principal selection is exercise context, not authentication or tenant isolation. The CLI must require local access tokens by default; hosts allocate separate processes/SQLite/token authorities per team. Participant tokens bind an identity and cannot select admin. Keep default numeric loopback binding, no outbound cloud calls, payload limits, and SQLite transaction/foreign-key invariants.

## API reference and execution acceptance

An implemented API is not documentation-complete with static guides, schemas,
examples, screenshots or a developer-only test. Each supported service/operation
must have a user-reachable execution control that calls the actual authenticated
local API and its SQLite-backed state. Shared path/method operations must select
the correct protocol and routing tuple; never invent alternate endpoints to hide
OpenAPI's one-operation-per-path/method limit. Unsupported operations stay explicit.

For API or reference changes, all of the following are required:

1. Reconcile the runtime-supported operation inventory with executable reference
   contracts. Missing operations, templates or routing bindings are failures, not
   a smaller silently filtered success set. Selection must rebuild method, path,
   Content-Type, service/target headers and body without stale values.
2. Distinguish static request/response examples from actual HTTP results. Show the
   real status, content type and response body safely, including failures; never
   substitute an example, fixture or generated response for a native execution.
3. Run registered browser acceptance through the real embedded UI, real local
   HTTP API and persisted state. Include success, refusal, authentication and
   permission denial, service switching and team separation. SQS uses actual
   QueueUrl/ReceiptHandle from create/send/receive/delete; Logs uses actual
   group/stream/events. A mock-only or direct-SDK test does not prove this UI flow.
4. Verify English and Japanese controls, explanations, validation and result
   states, including persisted/reloaded console language. Never translate wire
   identifiers, operation names, request keys, native data or user content.
5. Record the exact immutable source/HEAD, commands and result evidence. Keep
   planned, unrun, failed, skipped and passed distinct. Resource or dependency
   blockers preserve an unfinished change; they never justify fake success,
   weaker assertions, removed cases or bypassed acquisition/security controls.

Retain `make before-commit`, `make e2e`, applicable formal checks, independent
review and exact-HEAD CI. New APIs extend the execution inventory and registered
regressions; static documentation and these rules alone are not runtime evidence.

Before completing a change run `make before-commit`; for protocol, persistence, console, or documentation-UI changes also run `make e2e`. For model changes run `TLA_JAR=... make formal`. Cross-platform execution is not implied by successful cross-compilation. Record acceptance criteria, verification, compatibility impact, remaining risks, and unverified behavior. Do not overstate bounded TLA+ checking as a proof of implementation.

Optional skills under `.claude/skills/` are inherited workflow references, not mandatory gates. Use the current Go Makefile gates; do not reintroduce removed TypeScript harness commands. Do not weaken regression tests to get green checks; adjust stale tooling only with evidence and matching detector coverage.
