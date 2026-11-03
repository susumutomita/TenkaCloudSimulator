## Result and acceptance criteria

Describe the observable behavior and the supported scope.

## Compatibility

Explain protocol, capability, schema, or cross-repository impact. State unsupported behavior and migration/recovery steps.

## Validation

- [ ] `make before-commit`
- [ ] `make e2e` for HTTP/SQLite/console/API-reference changes
- [ ] `TLA_JAR=... make formal` for modeled transition changes
- [ ] `make cross-check` for runtime/platform changes
- [ ] UI loading, empty, error, success, and responsive states checked where relevant

Record executed commands, evidence, and unverified conditions. A compile check is not runtime validation; bounded model checking is not a proof of the entire implementation.

## Remaining risks

List concrete limitations, attribution/license issues, security checks that could not run, and follow-up work. Do not describe unimplemented providers/APIs as supported.
