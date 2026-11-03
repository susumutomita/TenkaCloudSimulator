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
5. Real binary + SDK + offline console/Scalar E2E, seven-state accessibility audit,
   finite TLC model checking and detection of the deliberate deletion-guard mutant.

Coverage generation and threshold checks are ordinary required steps. An absent,
empty, malformed or below-floor report exits nonzero. Regression tests exercise
both floors independently, repeated statement blocks and missing/invalid reports.
No source files or packages are removed from the coverage reports: `cmd/simulator`
and all internal packages are included. Foreign builds are compile checks, not
foreign-platform runtime tests. TLC is bounded checking, not an implementation proof.

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
