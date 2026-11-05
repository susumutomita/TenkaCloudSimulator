# SQS console epoch and receipt-custody model

`SQSConsoleEpoch` adds one baseline and four expected-invariant negative
controls to the existing registry. The combined candidate requires 19 baselines
and 98 controls (117 actual executions). Every registered property, domain and
control is preserved; current immutable-HEAD execution remains a separate gate.
The policy test removes each required control independently to detect missing
coverage. This is finite model checking, not a proof of React/Go refinement.

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

| Property | Source correspondence | Concrete check and limit |
| --- | --- | --- |
| OnlyCurrentReplies | `valid(epoch)` before accepting list/detail/poll results; queue/cancel invalidation; only SQS keyed remount in main.jsx | Hold an actual native poll response, change region, deliver/abort it and reject old body/receipt in new context |
| NoUnmountedAcceptance | alive flag plus revision advance and abort in effect cleanup | Actual region remount during a real pending reply; service/principal late-reply variants are not separately exercised. Model history captures mounted state at acceptance |
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

The mandatory `test/e2e/sqs_console.py` checks two authenticated CLI processes,
separate databases, cross-token rejection, participant denial without mutation,
regional state, held replies, receipts, restart and cleanup against the actual
embedded console. `test/e2e/sqs_locale.py` additionally checks English/Japanese
switches with live drafts and receipt custody, unchanged native payloads and
PC/mobile reflow. `test/e2e/sqs_docs.py` executes the served static templates with
live identifiers. These are independent implementation checks: TLC does not run
their code. The updated candidate must pass all original main cases and these
three helpers, current coverage gates, model/applicability checks and hosted CI.
Results from earlier SQS branches are historical evidence only.
