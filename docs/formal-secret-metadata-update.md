# Description-update finite abstraction

## Finite abstraction

`formal/SecretMetadataUpdate.tla` models one request against two abstract secret
keys. It enumerates three descriptions (`old`, `new`, empty), four Boolean
verdicts (input validity, target existence, captured authorization, commit
acknowledgement), and three write outcomes (exact, ignored, metadata drift).
There are 288 initial request configurations. A request stages private pending
state, then either commits after authorization/acknowledgement/exact-state checks
or preserves the original durable state. The completed state can stutter.

Thirteen invariants cover types, authorization, admission, acknowledgement,
visibility before commit, refusal rollback, exact requested metadata, the peer
secret, physical identity, creation time, version ID, value and stage. Fourteen
literal-source negative controls separately remove each admission/commit guard,
expose a pending write, retain a refused write, corrupt returned metadata or
alter each protected part of the durable state.

The key denotes an already resolved physical identity in its storage scope.
The authorization verdict is assumed captured and correct. Actual name/ARN
resolution, policy matching and policy snapshot acquisition are not proved.
Private pending state and atomic commit are an abstraction of a SQLite
transaction, not a verification of SQLite, concurrency or crash recovery.
Values are opaque finite symbols, not real secrets. The model does not parse
JSON/Unicode/tokens, model clocks beyond 0/1, execute Go, prove an implementation
refinement, or establish SDK/browser/AWS parity. Native tests separately check
actual transactions, faults, restart and independent databases.

## Required verification

`SecretMetadataUpdate` is registered in `formal/models.json`. Its thirteen
invariants, fourteen exact negative controls and runtime source bindings are
protected by `scripts/formal_model_gate.py` and removal regressions. The normal
formal gate validates the pinned TLC tool, baseline and each required named
counterexample. Source applicability binds the exact changed files; it is not
semantic coverage or a proof of refinement.

The runtime binding covers only the metadata-only update helper and its HTTP
admission/dispatch wiring. Other Secrets Manager operations retain their existing
models and independent tests. Native tests remain necessary for database faults,
reopening, strict input validation, IAM and process/database isolation.
