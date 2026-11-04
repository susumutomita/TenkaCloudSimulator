# SQS console epoch and receipt-custody model (source candidate)

`SQSConsoleEpoch` is newly registered source, not a TLC result. It adds one finite
baseline/four exact expected-invariant controls to recorded mainb61's unchanged
ten baselines/thirty-six controls: eleven/forty total. All forty IDs are literal
minimum requirements in `scripts/formal_model_gate.py`; the prepared policy unit
removes each individual control. No old model, property, CFG or mutation is
removed or weakened.

## Abstraction and bounds

Two context labels abstract a queue or an enclosing region/principal/navigation
context. Epochs are0..1, monotone without wrap; the terminal unmount transition
caps the abstract epoch. One response identity exists per context/epoch. Pending
native work may complete after cancellation, so abort itself is not modeled as
an authority guarantee. Delivery must pass the mounted/epoch view guard.

Shown rows, private receipt custody and a separate hypothetical raw-render sink
are finite sets. Accepted history captures the original mounted flag, context
and epoch at delivery; future transitions cannot relabel a past acceptance as
valid or invalid. This is safety exploration, with stuttering and no liveness
claim. Receipt identities abstract response custody, not actual random receipt
freshness, message visibility or the cryptographic/account identity of callers.
Native `SQSLifecycle` and `SQSStorageBudget` retain their own separate scopes.

| Property | Source correspondence | Prepared concrete check |
| --- | --- | --- |
| OnlyCurrentReplies | `valid(epoch)` before accepting list/detail/poll results; queue/cancel invalidation; only SQS keyed remount in main.jsx | Hold an actual native poll response, change region, deliver/abort it and reject old body/receipt in new context |
| NoUnmountedAcceptance | alive flag plus revision advance and abort in effect cleanup | Region/navigation remount during a real pending reply; model history captures whether acceptance happened while mounted |
| ReceiptCustodyBound | clearMessages; private Map filled only after valid native poll; release/delete/context switch clears or removes receipts | Native visibility0→redelivery/newreceipt→stale receipt refusal; UI selected message only; region switch clears received rows |
| RawReceiptsPrivate | only MessageId/body/selected metadata enter row state; receipt stays in ref and is sent only to native delete/visibility | Actual browser response receipts absent from DOM, URL, storage, console output and reports; no raw receipt values in test output |

The four controls deliberately omit the epoch guard, omit **both** redundant
unmount defenses (alive and cleanup epoch advance), retain handles across a
context switch, or copy the raw receipt into rendered row state. Each expects its
specific named invariant; parser errors, timeout, TypeOK failures or arbitrary
nonzero exits do not count as a detected intended fault. The paired unmount
control is explicit rather than pretending either redundant source defense must
individually be necessary.

## What this does not establish

TLC does not execute JavaScript/React, DOM rendering, promises/AbortController,
threads, authentication, IAM, native SQS/Go/SQL, receipt freshness/visibility,
cryptography or OS isolation. It assumes source correspondence that needs review
and actual browser/native SDK tests. Forms, value parsing, layout, error text and
rendering are not verified by this abstract model. Mapping main.jsx covers only
the added SQS navigation/remount seam; it is not a proof of the full console or
its existing BrowserSessions authentication implementation. Actual token
binding/403/401 and scoped persistence are separate native acceptance tests.

The helper `test/e2e/sqs_console.py` is mandatory in Conformance source and is
prepared for two real authenticated CLI processes/databases, current native
state, cross-token401, participant-denied send with no mutation, same-name
regional state, delayed native reply, restart/relogin and cleanup. Native/server/
core files remain byte-identical to mainb61. All new runtime/browser/model/policy
execution remains **unrun** at this source checkpoint; no previous head's proof
is used as new success. The official AWS screenshots returned403; public text
references do not establish authenticated AWS screen equivalence.
