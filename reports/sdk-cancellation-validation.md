# SDK cancellation hardening — local candidate

This follows public SDK branch HEAD f3dadb9eb5f1a6819d22aede2dc5a71ef62316a3 on base main 2b625ddf2a7fa3b8f8c044d0e5fdd3914794e473. It is a separate local branch, local/sdk-cancellation-hardening-20261103; no push or PR has been performed for this fix.

## Reproduced failure

A deterministic test holds SQLite's sole database connection, starts each of the five permitted SDK operations, waits for database/sql to report the queued connection request, then cancels the invocation before releasing the connection. On the public SDK baseline all five operations remained blocked until release; queued PutObject/DeleteObject could then mutate the sentinel object. The failing baseline is recorded in /workspace/simulator-python-build/sdk-cancel-db-before.log. This is a real cancellation regression in the unmerged candidate, not a claim about full WP support or a calibrated vulnerability severity.

## Change

The saved execution-role policy read, SSM reads and native S3 object transactions now receive the existing request context through database/sql's context-aware acquisition and query methods. S3 helpers accept the same narrow transactional query interface for native request-bound transactions and existing CloudFormation transactions. Existing background-context store methods remain compatible. Quotas, version ownership, conditional writes, authorization and transaction boundaries are unchanged.

The queued-operation regression requires cancellation to return before the held connection is released and verifies the sentinel survives both mutations. A second regression invokes every underlying store operation with an already canceled context while the connection remains held. Neither test uses a longer function deadline or permits a late successful write. Targeted race tests pass.

## Completed local acceptance

- make before-commit passes formatting, vet, embedded-asset integrity, Go module verification, all-package race tests and CGO-free static build. Statements3262/3425=95.24%; unfiltered Cobertura source4235/4650=91.08%. Both90% floors and four coverage-gate negative tests pass. Log: sdk-cancel-full-gates.log.
- All12 actual-binary/SDK/browser E2E cases and strict responsive-detector positive/negative test pass. Log: sdk-cancel-e2e.log. Twenty-three actual console states pass scoped axe/keyboard/reflow checks; this is not full accessibility certification. Log: sdk-cancel-audit.log.
- Pinned real TenkaCloud host and participant browsers score100/95 across two separate processes, award duplicates0, reject cross-team401/private outputs403 and reach DELETED. Browser external requests0/page errors0/mobile overflowfalse. Logs: sdk-cancel-platform.log and sdk-cancel-platform-browser.log.
- The original unmodified WP handler SHA2568d559b2b8ad72f7a281d3ef5cd3ba804d76cb01d6f2d45049baf56ee4c8a4f03 still executes actual Python/SQLite through the explicitly incomplete resource projection: original SQL leak, SSM changes producing400/401 and DELETED. Log: sdk-cancel-wp-subset.log. This does not implement the missing full problem.
- Linux amd64/arm64, Darwin amd64/arm64 and Windows amd64 compile; foreign execution is unverified. Three finite formal models hold and all three required mutants produce counterexamples. Logs: sdk-cancel-cross.log and sdk-cancel-formal.log. These models do not prove cancellation implementation correctness.
- Go source vulnerability check passes against the Go team's fixed official OSV snapshot at /workspace/simulator-python-build/osv-v1. Log: sdk-cancel-govuln.log. This is not a fresh canonical-service scan; canonical exact-head CI/CodeQL and independent PR reviews remain outstanding. No dependencies or frontend assets changed from the validated SDK baseline.

Logs named above are under /workspace/simulator-python-build. Local testing used Go1.26.10 with GOFLAGS=-buildvcs=false only for this worktree build environment; CI remains unchanged.

## Publication state

The public SDK baseline passed push CI 41728456359, but has no PR, CodeQL or PR reviews. Draft PR creation for feat/lambda-sdk-bridge-20261103 was rejected by automatic approval review, stating that external PR publication was not explicitly authorized. A scoped remote query confirms no PR exists. The denied action has not been retried or routed through another tool. The public baseline must not be merged while this local regression fix and the required exact-head reviews are outstanding.

Only hello-world is selectable. API Gateway, WAF, original custom resources and the seven original WP grading checks remain unsupported.
