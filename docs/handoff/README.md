# Review handoff and preservation

The working repository is `/workspace/TenkaCloudSimulator`, branch `codex/go-single-binary-console`, based on upstream main commit `256dc8384a724e2a8a684986a97bb05f7944ad67`. All implementation and evidence changes are committed locally. Nothing was pushed, published as a PR, deployed, or provisioned in a real cloud. The original macOS work was not accessed or moved.

The handoff package is a **private review snapshot, not a cleared public release**. Its top-level manifest identifies the exact source commit, binary digest, included verification, and remaining limitations. It contains source, a Linux amd64 static executable, notices, and text evidence. Browser screenshots stay in the cloud working repository; they are not copied to Library without a request for screenshots as saved deliverables. Re-run the audit in a cloud workspace to regenerate them.

The existing workspace should not be treated as an indefinite storage guarantee. The persistent Library copy is confirmed only when a successful save result and link are returned. A separate full Git bundle, retained in `/workspace/deliverables`, preserves commits/history locally; it is not a substitute for remote storage while GitHub push remains unauthorized.

Restore the private source snapshot into a cloud workspace, or import the verified local Git bundle into a fresh cloud checkout. Do not use macOS disk as a build or runtime workspace for this request. The Linux binary can run from an empty cloud directory with no Node, Docker, external SQLite, Java, or Python installed:

```sh
chmod +x tenkacloud-simulator-linux-amd64
./tenkacloud-simulator-linux-amd64 --listen 127.0.0.1:4566 --state ./practice.db
```

Console: `/_simulator/console/`; local interactive API reference: `/_simulator/docs/`; S3 endpoint: the root URL or `/_simulator/providers/aws/`. Default numeric loopback binding is intentional. Use only dummy simulator credentials. The process is not a multi-tenant authentication boundary.

Verification evidence:

- `reports/quality-gates.txt`: vet, module/asset integrity, race tests, static binary build.
- `reports/e2e.txt`: standalone startup/stop, real boto3 and Chromium interactions, shared API/UI state, persistent restart and isolation, offline Scalar request client.
- `reports/audit/README.md` and `reports/audit-run.txt`: seven captured/visually inspected states, contrast fixes, keyboard validation/focus, mobile reflow, scoped automated accessibility results.
- `reports/formal-model-check.txt`: the unchanged valid model has 125 reachable states with no reported invariant violation.
- `reports/formal-mutation-check.txt`: the intentionally broken temporary deletion guard is detected; no such defect is left in the runtime or normal model.
- `reports/vulnerability-review.md`: official-source snapshot scan and x/sys update; canonical-service access remains blocked. Linux/Windows/Darwin source configurations report no vulnerabilities against the identified snapshot. This does not mean a canonical online scan passed.
- `reports/cross-build.txt`: final five-target compilation; foreign binaries were not runtime-tested.

Distribution status: Scalar's `@replit/codemirror-css-color-picker@6.3.0` declares MIT and author Faris Masad at the exact published commit. No original LICENSE/NOTICE/copyright text is supplied by its tarball, source tree, or 64 fetched history commits. Verified metadata, file inventory, attribution, and standard MIT terms are retained. Do not fabricate an upstream notice or call original-notice verification complete. `third_party/color-picker-evidence.json` and `COLOR-PICKER-NOTICE.txt` contain the precise evidence.

Scope remains a basic S3 and limited identity-policy increment. Full IAM, other cloud providers, deep links, advanced S3 administration, infrastructure execution, full AWS console fidelity, full accessibility compliance, full cloud API conformance, and a proof of the Go implementation are not claimed.
