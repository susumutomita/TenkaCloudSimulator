# Review handoff and preservation

The working repository is `/workspace/TenkaCloudSimulator`, branch `codex/go-single-binary-console`, based on upstream main commit `256dc8384a724e2a8a684986a97bb05f7944ad67`. All implementation and evidence changes are committed locally. Nothing was pushed, published as a PR, deployed, or provisioned in a real cloud. The original macOS work was not accessed or moved.

The handoff package is a **private review snapshot, not a cleared public release**. Its top-level manifest identifies the exact source commit, binary digest, included verification, and remaining limitations. It contains source, a Linux amd64 static executable, notices, and text evidence. The final requested handoff includes the synthetic-resource browser screenshots as deliverables. They can also be regenerated in a cloud workspace with make audit.

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

Distribution status: the formerly unresolved optional color-picker dependency is excluded from the rebuilt Scalar JS/CSS input graph. Only CSS color swatches are omitted. Original notices for the included package/style entries are retained and checked; `third_party/color-picker-removal.json` records the removal. The earlier historical evidence is not a list of shipped dependencies. This removes that particular missing-notice blocker without inventing attribution. Canonical online vulnerability-service access and real GitHub CI execution remain unverified, so public release/deployment clearance is not claimed.

Scope remains a basic S3 and limited identity-policy increment. Full IAM, other cloud providers, deep links, advanced S3 administration, infrastructure execution, full AWS console fidelity, full accessibility compliance, full cloud API conformance, and a proof of the Go implementation are not claimed.
