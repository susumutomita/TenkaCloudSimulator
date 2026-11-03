# Local catalog / Cloudscape validation — 2026-11-03

Base: main 639af7a9e7384dcc0d6fdf42d05024f03ab75349. These results describe the local increment; remote CI must test its exact final head before merge.

- make before-commit: Go fmt/vet, locked assets, module integrity, all-package race tests and static binary build pass. Full-module statement coverage 1677/1762 = 95.18%; unfiltered Cobertura source lines 2149/2354 = 91.29%. Both unchanged 90% floors pass; report failure/absence/below-floor negative tests pass.
- Five binary/SDK/browser E2E tests pass, including actual catalog template → auth-bound participant login → Systems Manager Overview → read-only write denied; S3/Scalar real request execution, exact bytes, literal dot keys and restart/process behavior.
- The real TenkaCloud host at f99b3d9a00076a2a5e97f1d4d9b0b20a7e02245c imports the immutable Challenge catalog at a749b142416770abee3f173673702c55df3856aa, deploys to two actual Go processes, retrieves values through native SDK HTTP, scores through its real authenticated submission/ledger API and tears down. Correct awards 100; other-team answer wrong then correct 95; eight concurrent correct submissions award once; private stack output requests 403; cross-process operator/participant tokens 401; same-name parameter probes retain distinct per-team values.
- Participant-driven rehearsal exists (test/platform/README.md). A second integration browser opens both real Cloudscape consoles, selects service/region, reads each parameter, submits via the real host practice portal, verifies 100/95, zero external requests/page errors, mobile width and DELETED cleanup. No flag/token is logged.
- Seven captured Cloudscape flow states pass scoped axe WCAG rules, keyboard/validation/focus recovery and 390/720px reflow. Original Cloudscape source has one documented build-time ARIA attribute correction; no rule is suppressed. Long identifiers wrap. Finite ObjectStore/StackOwnership models pass; removing deletion/ownership guards produces required counterexamples.
- Linux amd64/arm64, Darwin amd64/arm64, Windows amd64 compile. Foreign runtime execution is unverified.
- Fresh locked console install/rebuild reproduces embedded assets, actual official component inputs and original license/notice inventory. Console/Scalar/platform-tool npm audits report zero vulnerabilities. Removed Replit color-picker remains excluded from shipped Scalar inputs.

## Security and evidence limits

Canonical local govulncheck cannot reach vuln.go.dev (HTTP403). Source govulncheck passes against the Go team's official raw OSV mirror commit a2082f757c2551694285855180b2d17666d7bcd6 (4712 unchanged OSV documents, range/symbol scan). This does not claim current canonical HTTP publication metadata. CI retains the mandatory canonical govulncheck; no continue-on-error or threshold reduction.

Changed Go/provider/auth/UI/CI paths were manually reviewed against SECURITY.md and exercised by regression tests. The managed Codex Security diff workflow was unavailable (no scan tools/config-preflight resource in this host); no managed-scan report or independent certification is claimed. Remote CodeQL remains required. No unresolved significant issue is asserted away by those tool limits.

The simulator's native AWS subset is deliberately small. STS/federation/CloudShell/managed-policy permissions/trust evaluation and non-hello-world AWS workloads remain unsupported. The practice seam is explicit local routing; the unchanged catalog's real-AWS instructions do not become native federation. No normal TenkaCloud production adapter, public proxy or deployment is included.

Official AWS public workflow docs informed navigation/names/pages/tabs. Authenticated live AWS pixel matching is unverified; screenshot fetch/live demo were blocked. Current screenshots are actual Simulator dummy-data views, never reference AWS screenshots. Accessibility automation is not full certification. The old environment's unsaved changes may have been lost; no old ZIP contents/hash are claimed verified.

## Automated review regressions

Codex review on 690a56e identified three P2 issues. New deterministic regressions fail on that baseline and pass after repair: (1) a delete paused before BEGIN cannot delete another connection's replacement stack resources; stack/ownership read now occurs inside the immediate transaction, (2) a participant bound to wildcard exercise IAM still receives 403 for host-private DescribeStacks, including a spoofed access-role header, and (3) forward resource Ref/GetAtt/Sub dependencies resolve topologically with cycle rejection. No original review finding is dismissed merely because the hello-world fixture did not trigger it. Updated final-head CI and review are required.
