# Secrets mutation ACK and metadata refresh

`SecretsMutationRefresh` models the view contract after one CreateSecret or
PutSecretValue submission. It does not modify or replace SecretsRevealEpoch or
SecretsDisclosureChannels, which cover different reveal/context boundaries.
It is registered additively in the repository gate with five required properties
and four literal-protected sensitivity controls. The combined registry has
27 models and 153 controls: 180 planned executions. Registration is not a
successful full integrated TLC run.

## Source correspondence and defect

Baseline source `d9a4f1b` in `tools/console/src/secrets-manager.jsx`, `mutate`,
awaits the native write, rejects stale/unmounted ACKs, clears its draft and request
token, then awaits `browse`. The old source leaves the form mode open until a
successful metadata response. When that read fails after an acknowledged write,
the existing form remains open with cleared draft/token. Root's targeted fix
adds form exit immediately after the live write ACK and before the independent
metadata refresh. The model specifies that corrected contract, not the buggy
baseline as a passing implementation. The integrated fix closes the form after the live ACK. The actual browser/native
regression retains the failed old-source case and separately checks the fixed
embedded binary; see the current validation status in [Secrets Manager](secrets-manager.md).

`Submit` represents one native Create/Put request, including its original T1.
`WriteACK` closes the form and clears draft/token. `StartMetadataRead` is a separate
transition, so the closed/cleared state is visible before reading. Metadata read
failure retains that state. `RetryMetadataRead` is read-only and snapshots the
write-request count before/after; it cannot recreate or put another version.
`FreshOpen` represents a new user form intent with T2, corresponding to `open`'s
fresh draft and `crypto.randomUUID` request token. Acknowledged history belongs
to generation 0; its closed/cleared requirements do not prohibit a new form.

## Explicit finite bounds

There are two initial operation kinds, one initial request, one acknowledged
write, success/failure of the first metadata read, at most one read retry, and
one new form generation. No second committed write is represented. Request counts
0..2 deliberately permit the replay control to produce an ordinary safety
violation rather than a TypeOK failure. Token None is a valid modeled value, so
the missing-fresh-token control fails its named property rather than typing.
Terminal self-observation marks the finite scenario's end; it does not introduce
fairness, eventual read success or any other liveness claim.

The model assumes a live/current ACK has already passed the epoch/alive guard.
It excludes unacknowledged network ambiguity, retries of failed writes, server
idempotency semantics, Delete/rollback/staging-label writes, further form edits,
multiple versions and arbitrary repeated new intents. It does not prove token
entropy, authentication/IAM, SQL transactions, request bytes, React scheduling,
Go/JS refinement, secret disclosure or real AWS behavior. Existing independent
native/SDK/epoch/disclosure tests remain required.

## Properties and exact sensitivity controls

| ID | Literal old → new | Named invariant |
| --- | --- | --- |
| OmitAckFormExit | `formOpen' = FALSE (* AckFormExit *)` → `formOpen' = formOpen (* AckFormExit *)` | AckClosesForm |
| ReplayWriteOnReadRetry | `requests' = requests (* RetryReadOnly *)` → `requests' = requests + 1 (* RetryReadOnly *)` | NoWriteOnReadRetry |
| RetainDraftOnAck | `draftPresent' = FALSE (* AckDraftClear *)` → `draftPresent' = draftPresent (* AckDraftClear *)` | AckClearsDraft |
| OmitFreshIntentToken | `token' = "T2" (* FreshIntentToken *)` → `token' = "None" (* FreshIntentToken *)` | FreshFormHasToken |

Each control must produce exit 12, the named invariant and a real counterexample.
Syntax/runtime/type failures and timeouts are failures of validation, not accepted
controls. Execution receipts belong outside the source tree and identify exact
model/config/tool hashes. No old PR or other model success establishes this
model's result. All26 main models and149 protected controls are retained
unchanged. The full immutable integrated run remains a separate required gate.
