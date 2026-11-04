# CI for the standalone Go simulator

The required `ci` job verifies the actual Go module, embedded console/API reference,
and SQLite runtime. Node/npm, Python/Chromium/SDK and Java are development tools;
the resulting statically linked executable does not require them at runtime.

Every push, pull request and manual run checks out the exact tested head and runs:

1. Integrity-locked Scalar rebuild, actual bundle-input exclusion and original notices.
2. Go format/vet, module and embedded-asset integrity, full-module race tests and
   statement coverage, static binary build, and five OS/architecture compile checks.
3. Unfiltered Cobertura conversion and Go HTML coverage view. Both full-module
   statement coverage and source-line coverage must be at least 90%.
4. Locked npm dependency audit (high/critical fail) and canonical Go govulncheck.
5. Eleven real binary + SDK + offline console/Scalar E2E cases, 23-state accessibility audit,
   three finite TLC models and three required mutation counterexamples.

Coverage generation and threshold checks are ordinary required steps. An absent,
empty, malformed or below-floor report exits nonzero. Regression tests exercise
both floors independently, repeated statement blocks and missing/invalid reports.
No source files or packages are removed from the coverage reports: `cmd/simulator`
and all internal packages are included. Foreign builds are compile checks, not
foreign-platform runtime tests. TLC is bounded checking, not an implementation proof.

The embedded CPython/WASI/stdlib and original rights inventory are verified offline
by tools/python/check.py. Race/SDK/browser tests execute actual Python with SQL,
check guest containment and cancellation, exercise atomic Lambda persistence and
compare independent team processes. The fixed JIT compiler may clean up asynchronously;
a deterministic compiler barrier checks that shutdown/client cancellation return
without waiting and that no runtime is published after Close. This is not a claim
that the finite object/stack models prove the Python interpreter or full AWS behavior.

The coverage step writes a table and PASS/FAIL result to `GITHUB_STEP_SUMMARY`.
A pinned `actions/upload-artifact` stores the raw Go profile, unfiltered XML, HTML
source view and Markdown summary for 14 days. Evidence is retained even after a
failed threshold check; uploading an artifact does not change the failed result.
Missing evidence cannot make generation or validation succeed.

## Why Code Quality upload was replaced

At head 7dfc3c8000be9abe047ce07156e0f8d929795fb6, both the PR and push runs
completed the actual tests and coverage checks, then failed with HTTP 404 from
`PUT /repos/susumutomita/TenkaCloudSimulator/code-coverage/report`. The report was
124,916 bytes; full-module statement coverage was 94.6% and source line coverage
was 1018/1124 (90.57%). The runtime token explicitly had CodeQuality write access.
The workflow used the current pinned official upload action and correct PR/head.

GitHub's [Code Quality coverage prerequisites](https://docs.github.com/en/code-security/how-tos/maintain-quality-code/set-up-code-coverage)
require repository enablement; its [enablement documentation](https://docs.github.com/en/code-security/how-tos/maintain-quality-code/enable-code-quality)
also specifies feature availability. A [maintainer response about the same 404](https://github.com/actions/upload-code-coverage/issues/16)
asks users to verify enablement and investigate continued failures. The available
connection cannot inspect that administration setting, so disabled service versus
entitlement/API access is not established by the 404 alone.

The Go test/coverage gates do not need this service. Following the user's CI
migration request, only the unavailable Code Quality upload and its extra token
permission were removed. This is not `continue-on-error`, a lower threshold, a
fake status or a ruleset bypass. Repository protections and external CodeQL/
GitGuardian checks are unchanged and remain authoritative for merge eligibility.

GitHub documents [workflow artifacts](https://docs.github.com/en/actions/tutorials/store-and-share-data)
as a way to retain test/coverage output, and [job summaries](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-commands#adding-a-job-summary)
as the native way to display Markdown in a workflow run. Those mechanisms expose
and preserve the same required local coverage evidence without calling Code Quality.

## Cloudscape / local catalog increment

The console has its own locked, development-only npm build in tools/console.
CI rebuilds the actual official component graph and original notices, compares
embedded assets/digests/inventories, and audits both console and Scalar dependencies.
Python remains only for active SDK/browser, coverage, build, formal and license
verification; it is not part of the runtime. SSM/CloudFormation/auth transaction
and isolation tests belong to the full-module race/coverage gates. StackOwnership
adds drift-reservation invariants and a required negative control to TLC.

The real-platform catalog runner and participant browser run in CI: see test/platform/README.md. CI fetches immutable reviewed TenkaCloud/Challenge commits and installs only tools/platform/package-lock.json development dependencies; setup refuses to overwrite existing module directories. No catalog scripts or real AWS are run. A failed acquisition or lifecycle fails CI.
Remote checks remain authoritative only for their exact tested commit. Historical
reports below predate this increment and do not certify the changed working tree.

S3 versioning increment adds the enabled-history S3Versions model and a mandatory mutant that incorrectly guards bucket deletion using only current objects. TLC must reject that mutant with NoHistoryOrphans. This finite abstraction does not prove the implementation or model suspended versions/quota; native race/SQLite/SDK/browser tests cover those contracts. The browser audit now captures 23 states, including version settings, public-access confirmation, version table, bucket tag editor and mobile reflow. Existing 90% statement and unfiltered source-line floors remain unchanged.

The full-module Go race command runs packages with `-p 1`: several test binaries independently JIT-compile the fixed embedded CPython/SQLite WASM. Cross-package CPU competition caused non-performance containment tests to hit their existing execution deadlines on the cloud worker. Packages run sequentially; goroutine concurrency, race instrumentation, cancellation/deadline negative assertions and both 90% floors remain enforced. The positive four-invocation state-isolation test uses a supported15-second budget for two queued waves of fresh interpreter startup under race instrumentation; every payload must still be1. It is not a startup performance benchmark. No failed test is skipped or retried by CI.

## Responsive layout synchronization

The main f790403 run41706632955 passed Go/coverage/security but caught an SSM mobile width assertion immediately after viewport change/Cancel. Cloudscape processes resize asynchronously; E2E now waits up to3s for the exact document.scrollWidth<=innerWidth geometry to stay stable100ms. A real Chromium regression first observes temporary overflow, then waits for a delayed responsive update, and requires permanent1000px overflow at390px to fail. No width tolerance, ignored failure or coverage change is introduced. All viewport geometry assertions use this gate; persistent overflow reports geometry only, without text/input/token data.

IAM console increment: twelve actual-binary/SDK/browser E2E cases and28scoped axe/keyboard/reflow states now include local role creation/read/edit, existing-role selection and ZIP deployment. Conditional identity creation races must yield exactly one success without overwriting prior permissions; a delayed detail read must not replace a new role form. That legacy control flow remains as a regression. Native IAM is tested separately through real Query/SDK/Cloudscape, including service trust; STS remains unsupported.

Native IAM reference examples execute all nine Query operations. Native/legacy simultaneous name creation, retired ownership and policy rollback have Go race tests; the original legacy-browser flow is preserved separately. NativeIAMOwnership adds a two-name finite model and a mutation that removes the legacy ownership guard; it supplements, rather than proves, concrete transactional behavior.
