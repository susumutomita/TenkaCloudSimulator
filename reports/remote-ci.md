# Remote CI status

[Run 41631944882](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41631944882), head 6b4270632ab226f868354f0efe8f460e39d4fd48, passed asset rebuild/provenance, full-module race tests and both coverage floors, five-target compilation, canonical online govulncheck, real SDK/browser/standalone tests, seven-state accessibility audit, and bounded TLC plus deliberate mutation detection.

The final coverage-upload step failed with HTTP 404 Not Found from GitHub Code Quality. It uses the pinned official actions/upload-code-coverage action and code-quality:write permission. The required ci check remains failed. No continue-on-error, rule disablement, fake status, or merge bypass is used. A 404 alone does not establish whether the cause is entitlement, enablement, or API access; repository administration must check the service configuration. See [official prerequisites](https://docs.github.com/en/code-security/how-tos/maintain-quality-code/set-up-code-coverage).

Subsequent review fixes add literal-dot-segment browser mutation tests and reject unsupported ACL/options on bucket creation. All final-head required checks must still pass before merge.

CI migration follow-up: [docs/ci.md](../docs/ci.md) records the diagnosed failure and the user-requested replacement of the external upload with mandatory full-module coverage gates, native workflow summaries and retained Go/XML/HTML artifacts. The historical failed run above is preserved as evidence; no rules or thresholds are disabled.
