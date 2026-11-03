# Verification evidence

Current catalog/Cloudscape increment:

- catalog-e2e.txt: actual single binary, SDK/console/Scalar, authenticated actual hello-world fixture, read-only denial and literal object-key regressions.
- catalog-audit.txt and audit/: seven real Cloudscape states, keyboard/validation/axe/reflow.
- catalog-cross-build.txt: five cross-compilation targets (not foreign runtime execution).
- catalog-formal.txt, StackOwnership.txt/StackOwnershipMutant.txt and formal-*.txt: normal finite models and intentionally broken negative controls.
- current-validation.md: measured coverage, real platform/participant flow and security limits.

Other existing reports and audit/before are historical evidence for the earlier S3/custom-UI increment. They do not certify current sources. Raw runtime tokens, participant flags, real credentials and private stack outputs must never be saved here. Remote CI is authoritative only for its exact tested HEAD.
