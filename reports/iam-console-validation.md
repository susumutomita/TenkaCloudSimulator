# Local IAM-to-Lambda deployment increment

Base main2b625ddf2a7fa3b8f8c044d0e5fdd3914794e473, branch fix/iam-lambda-console-20261104. No push, PR or merge was performed for this increment. Existing SDK-publication denial remains separate and has not been bypassed.

## Main diagnosis and usable workaround

Main has no IAM service view and requires an existing execution-role ARN in Lambda creation. Its Identity policies editor does already create a local identity accepted by Lambda. A real browser on the separately running exact main opened Identity policies, created main-lambda-executor with an explicit Deny-all policy, entered arn:aws:iam::000000000000:role/main-lambda-executor in Lambda, created a Python function and invoked it successfully. No real AWS role or resource was created. Log: /workspace/simulator-python-build/iam-main-workaround.log; screenshot: /workspace/main-preview-20261104-data/main-lambda-workaround.png. Only an operator can perform that setup.

## Local change

Services→IAM→Roles now lists global local execution-role identities and supports name/policy creation, policy readback/edit and a Lambda creation link. It uses the existing local JSON identity control API/SQLite. Lambda's separate Permissions section includes Existing role selection, refresh, ARN entry and an IAM link for an empty list. The reserved simulator-admin identity never appears as an execution role. Participants without list permissions can still enter a supplied ARN; native CreateFunction/PassRole checks remain authoritative.

Conditional PUT with If-None-Match:* calls atomic identity Create instead of upsert; an existing name returns412 without mutation. Existing explicit edits still upsert. Eight concurrent create attempts produce one success and seven precondition failures. Invalid conditional values are rejected and participant management is denied. A generation guard discards an earlier role-inspection result after a new creation form opens, preserving its intended permissions.

This is a deployment-unblocking local subset. Users/roles share the current identity namespace. Native IAM Query API, trust/STS evaluation, managed policies, default Lambda role creation and CloudWatch logging permissions are NOT implemented. No role model equivalence to AWS is claimed. The default role policy explicitly denies service actions; replace its document to grant limited permissions rather than adding Allow below Deny.

## Validation

- make before-commit: formatting/vet/assets/modules, all-package race, four coverage-gate controls and static binary PASS. Statements3101/3260=95.12%; unfiltered source4008/4408=90.93%, both unchanged90% floors. Final asset/vet checks pass after generated UI changes.
- Final full12 actual-binary/SDK/browser E2E cases and strict responsive-detector control PASS. The IAM case creates its role entirely in the UI, selects it, creates a function, uploads a real Python ZIP and observes deployed42. It verifies PC/390px, no external requests/page errors, a separate team's empty local-role list and participant list/write403.
- The strengthened IAM case additionally edits/reads the actual stored policy, delays a role detail response while opening a new creation form to detect stale policy overwrite, and checks that duplicate creation displays412 while preserving the previous policy. Targeted final run is recorded in iam-browser-final-target.log.
- Twenty-eight actual console states pass scoped axe/keyboard/reflow checks. An initially detected unlabeled success-alert dismiss button was fixed with an explicit accessible name. No audit rule was suppressed. This is not full accessibility certification.
- Five OS/arch targets compile; foreign execution is unverified. Three finite storage/ownership models and all three mandatory mutants behave as expected. These models do not prove the new role UI or conditional-create implementation.
- Pinned real TenkaCloud host/participant browser still score100/95 for two teams, duplicate awards0, cross-team401/private outputs403, DELETED; browser external requests0/page errors0/mobile overflowfalse.
- Console npm audit reports0 vulnerabilities; runtime dependencies are unchanged. Go source govulncheck passes against the official fixed OSV snapshot. Canonical exact-head CI, CodeQL and independent PR reviews are unrun for this unpublished candidate; no managed security scan/certification is claimed.

Logs are under /workspace/simulator-python-build: iam-console-full-gates.log, iam-console-e2e-final.log, iam-browser-final-target.log, iam-console-audit.log, iam-console-cross.log, iam-formal.log, iam-platform.log, iam-platform-browser.log, iam-console-govuln.log, iam-console-npm-audit.log. Actual dummy-data images: reports/iam-role-desktop.png, iam-role-mobile.png and iam-lambda-deployed.png.

## Fidelity and service scope

Authenticated current AWS screens were unavailable. Public primary documentation was researched; [console-fidelity.md](../docs/console-fidelity.md) lists missing S3 creation settings, the IAM trust/permission wizard and Lambda console features. Passing local operations/Cloudscape usage is not a claim of matching actual AWS screens. Additional Product Design references and its dedicated Browser integration were unavailable; local screenshots were produced with actual cloud Chromium, not a completed Product Design audit workflow.

[services-roadmap.md](../docs/services-roadmap.md) and the source inventory record83Kumo registered packages and48LocalStack public provider factories for35API identifiers in the inspected file. Neither upstream runtime was exercised.110services is unconfirmed and no new service is considered supported from source registration alone. Only hello-world remains selectable; API Gateway/WAF/custom resources/all seven original WP grading checks remain incomplete.
