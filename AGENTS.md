# TenkaCloudSimulator working contract

This repository owns an independent Go single-binary multi-cloud simulator. AWS S3 is the first implemented provider. Static console, local Scalar API reference, and SQLite are embedded; runtime must not require Node, Docker, an external DB, or real cloud access.

- `internal/core/` owns provider-neutral relational object storage and the currently supported identity-policy matching primitive. Do not introduce cloud-specific protocol dispatch or problem IDs here.
- `internal/provider/` registers separate cloud protocol handlers. Preserve the ability to run several providers at once. New providers keep their own authorization semantics, resource model, namespace, and UI rather than inheriting AWS behavior.
- `internal/aws/` owns S3 wire behavior and maps requests to documented IAM actions/ARNs. Simulator IAM management is explicitly a separate JSON control API.
- `internal/server/` composes providers and serves the embedded assets. The console and API reference must use the same actual HTTP APIs and SQLite state as SDK clients.
- TenkaCloudChallenge owns problems, scenarios, grading, and required capabilities; TenkaCloud owns platform compatibility and integration. Do not add problem-specific logic here.

Read README and docs/design/0001-go-standalone.md before changing public contracts. Search for related capabilities, handlers, and tests. Preserve Git history and unrelated/uncommitted work. Never read/write `.env`, credentials, or secret directories. Do not perform release, deployment, cloud provisioning, push, or PR without the relevant authorization.

Do not silently simulate success for unsupported operations. Unsupported policy fields must be rejected. IAM principal selection is exercise context, not authentication or tenant isolation. Keep default numeric loopback binding, no outbound cloud calls, payload limits, and SQLite transaction/foreign-key invariants.

Before completing a change run `make before-commit`; for protocol, persistence, console, or documentation-UI changes also run `make e2e`. For model changes run `TLA_JAR=... make formal`. Cross-platform execution is not implied by successful cross-compilation. Record acceptance criteria, verification, compatibility impact, remaining risks, and unverified behavior. Do not overstate bounded TLA+ checking as a proof of implementation.

Optional skills under `.claude/skills/` are inherited workflow references, not mandatory gates. Their old Bun/TypeScript harness commands are superseded by the current Makefile. Do not weaken regression tests to get green checks; adjust stale tooling only with evidence and matching detector coverage.
