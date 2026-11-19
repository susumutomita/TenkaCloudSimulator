# SimpleDB first acceptance registration

This is additive factory registration, not a service implementation or a passed product acceptance. The immutable TestWriter snapshot is `7253c5b5dd9b0ba4574fbfe8bb2209228f99ba6d`, based on main `a53131cd61030c8d238823478ec96917b19e95ef`; its nine files remain byte-for-byte unchanged.

## Integration boundary

Read-only comparison found main still at `a53131c`. Azure Queue #502 has its separate clean implementation checkout at `92028d29ca4eb803f1d4a19a4cf177a8e1ad5b3b`; GCP DNS #343 acceptance is clean at `aaaecd2764540ac1dd306f110b89362fb60536c6`, while its separate leaf worktree contains uncommitted implementation work. None is main integration evidence.

Only the two shared registration scripts were adapted from the committed GCP acceptance snapshot (which already includes Azure registration):

- `scripts/factory_acceptance.py`: upstream snapshot Git blob `f7f22b4c3f77350106c5f4e2c90a23950a29930a`.
- `scripts/test-factory-acceptance.py`: upstream snapshot Git blob `8e459d3d71a026659e6c91625fd8e97d96cf7bf4`.

SQS/Azure/GCP suite identities, commands, operation lists and provider-specific path rules are retained. Operation eligibility now also selects the path authority, preventing mixed AWS suite lists from authorizing SQS paths for a SimpleDB operation or the reverse. No other provider oracle, runtime or leaf test is imported. Existing legacy source-pin semantics are preserved; this change does not certify Azure/GCP oracle provenance or product acceptance.

`DEFAULT_SUITES` explicitly retains main's SQS lifecycle. Azure/GCP implementations are absent on this base, and SimpleDB is unimplemented. Registration is not default activation: these suites run only when selected with `--suite`. Nothing is reported as skipped or passed for an unselected suite. Activating a service in the default gate requires a separate reviewed integration with its actual implementation and independent acceptance. This avoids making current main's normal gate depend on unintegrated leaf packages or RED oracles.

## SimpleDB registration

- `simpledb-first-oracle`: immutable TestWriter fixture/hash consistency only.
- `simpledb-first-native`: all six named native tests and all twelve required validation subtests; first five APIs, real HTTP handler and SQLite, no SDK/UI.
- `simpledb-first-sdk-wire`: fixed SDK entrypoint, existing required boto3/botocore versions, same-clean-HEAD CLI build, four required wire cases. Missing dependencies, product RED, preflight-only, timeout and cleanup failure cannot pass.
- `simpledb-first-sdk-harness-controls`: all three named pure detector controls, not SDK execution.

A SimpleDB finding's scope validation requires all four suites, plus one of exactly CreateDomain/ListDomains/DeleteDomain/PutAttributes/GetAttributes and explicitly listed leaf Go paths. Pure checks cannot authorize a product finding. Select, conditional writes, DomainMetadata, DeleteAttributes, gateway/server/auth and fixture edits are outside this registration's implementation path authority.

`test/factory/simpledb-registration.json` pins all nine original TestWriter files plus the fixed SDK entrypoint. The suite descriptors, exact commands and leaf path list are also included in the SimpleDB source binding. Receipts bind clean HEAD, source binding, suite set, command/toolchain, process exit, named tests/cases and log hash. A timeout is failed evidence and allows graceful owned-CLI cleanup before forced termination. Reports remain local, never automatically published.

The SDK entrypoint checks the existing official pins before attempting a build. Python invocation paths retain venv symlinks so the reviewed environment is not replaced by its base interpreter; receipts bind that invocation path. It installs nothing and accepts no arbitrary command, binary, report or endpoint from a finding. The CLI is built into an owned temporary directory with the configured single Go executable and the existing offline environment. The immutable SDK harness uses authentication-enabled numeric loopback with dummy credentials/local token; no new SigV2 credential authority is added. SIGTERM/SIGINT unwind its cleanup; primary product and cleanup failures remain separate. Actual SDK wire execution remains unverified on this environment if pinned dependencies are unavailable.

## Required evidence before a service claim

Registration alone does not make #300 ready: native tests currently stop at CreateDomain HTTP501 from the CloudFormation fallback. A failed receipt must stay failed; later assertions are prepared but not observed. Actual SDK wire cannot be replaced by its pure controls. The registered leaf scope alone cannot implement a passing composed server/CLI: a separately owned SimpleDB adapter/Gateway/server integration is still absent. It needs its own explicit integration-scope acceptance/ownership handoff before any implementation claim can be considered executable. Do not broaden the leaf allowlist to grant shared integration paths. After separate authorization and that integration, the owner must run all registered surfaces on the exact clean implementation candidate, with all required AGENTS gates and independent review. Existing claim/attempt/denial history is not reset.

Run `python3 scripts/test-factory-acceptance.py` for the combined SQS/Azure/GCP/SimpleDB registration detectors and `python3 scripts/test-software-factory.py` for ledger regression. Synthetic logs in detector tests are controls, not product acceptance receipts. No runtime implementation, claim, push, PR, merge or release is part of this registration change.
