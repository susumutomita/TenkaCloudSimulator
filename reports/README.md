# Verification evidence

Generated in the cloud workspace. No Mac files, real cloud credentials, resources, pushes, PRs, or deployment were used.

- quality-gates.txt: Makefile gates, race tests and static binary build.
- e2e.txt: actual binary + boto3 + Chromium; state in both directions, IAM and deletion errors, file persistence/restart, fresh process isolation, standalone startup/cleanup, offline Scalar request client.
- console-objects-desktop.png, console-mobile.png: original console with actual API-backed resource state.
- api-reference.png: locally embedded Scalar executing an S3 request against console-created state.
- sdk-model.json: exact SDK/model versions and model digest used for conformance.
- formal-model-check.txt: finite TLC run, 125 reachable states.
- formal-mutation-check.txt: nonempty-delete guard removed, NoOrphans counterexample detected.

These are scoped checks, not exhaustive AWS conformance or a proof of the whole implementation. Cross-platform outputs are compile checks only. Cloudscape is not imported; the console is an original interface. The reference SimAWS page was readable through web tools, but direct Chromium access to its domain failed with ERR_TUNNEL_CONNECTION_FAILED, so no claim of a live visual comparison is made.

- cross-build.txt: final code cross-compilation for five OS/architecture targets.
- govulncheck.txt: vulnerability database access was blocked (Forbidden at vuln.go.dev); no clean vulnerability-scan result is claimed.

Attribution status: one MIT-declared Scalar dependency is missing its original upstream notice; review remains open before distribution. These are local validation results. The GitHub workflow was authored but not run on GitHub because no push/PR was authorized.
