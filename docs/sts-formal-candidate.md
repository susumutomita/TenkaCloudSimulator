# STS lifecycle model candidate

Status: **registered model; integrated exact-HEAD gate pending**.
`formal/STSSessionLifecycle.tla` and its configuration are registered in
`formal/models.json`, with eight required source-edit sensitivity controls.
The earlier standalone baseline/eight CFG variants passed independently. That
receipt does not establish the current registered full gate, final applicability
hashes or a refinement proof. Exact-source validation remains a separate gate.

The model explores two session tokens, two successive generations of one role,
three clock ticks, maximum session lifetime two ticks, and a chaining limit of
one tick. It covers issuance, use, deletion, recreation, expiry and process
restart. Role trust, ExternalId equality, caller policy, signature verification,
tenant authority, dispatch selectors and storage validity are independent finite
booleans. It does not model cryptography, HTTP parsing, Go scheduling, SQL,
network isolation, account creation, or actual elapsed seconds. Symbolic token
slots may be reused after restart; actual credential randomness and uniqueness
are not proved by this abstraction.

The invariants require every issued session to have passed trust, ExternalId,
caller permission and chaining guards, and every accepted use to have passed
signature, tenant, dispatch, storage, current role generation, expiry and current
permission checks. Historical acceptance is compared with its historical oracle,
not with later state after expiry or deletion.

The use transition assumes a coherent generation-and-policy authorization
boundary. The Go path reads role generation and policy in one SQL statement and
passes the resulting immutable permission snapshot to native authorizers. Tests
cover deletion/recreation and slow-body expiry; clocks are checked again after
bounded body and state reads. These correspondence arguments and regressions are
not a refinement proof of SQL, concurrency or the Go implementation.

The optional standalone diagnostic runs the finite baseline and eight CFG-guard
variants. The registered gate instead applies eight equivalent source edits,
with its normal exact-HEAD and pinned-tool requirements:

```
TLA_JAR=/path/to/existing/tla2tools.jar python scripts/check-sts-formal-candidate.py
```

The checker requires the baseline to complete successfully and each variant to
produce the expected invariant counterexample. Variants separately omit the
role-generation, expiry, signature, trust, ExternalId, caller-permission,
tenant-authority or role-chaining guard. A parser/runtime failure is not accepted
as sensitivity evidence. This bounded check is neither a mathematical proof of
all executions nor a refinement proof of the Go implementation.
