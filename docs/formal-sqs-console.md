# SQS console epoch and receipt-custody model

> Integration status (2026-11-05): See the
> [service backlog acceptance record](acceptance/backlog-services.md) for the
> integrated scope and required gates. Branch-specific status, receipts,
> model/control counts and E2E totals below are historical records of their
> named inputs. They do not establish the current integration's final CI
> or merge status.

> Historical PR410 source snapshot: this branch normal-merges mainfcf's login paste/error repair into public73.
> Login is exact main, with SQS/KMS content mounts and fixed GCP/Azure scope
> utilities preserved. Main26 E2E method bodies plus two SQS helpers require28.
> The19-model/98-control inventory remains117 planned executions. Earlier73
> results stay bound to the old login input; fresh canonical/login/SQS browser,
> applicability and final whole28/dual90/117TLC/hosted checks are separate gates.


`SQSConsoleEpoch` has a bounded SANY/TLC result at immutable341dec2. It adds one finite
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

The mandatory `test/e2e/sqs_console.py` passed actual embedded browser/native
checks: two authenticated CLI processes/DBs, cross-token401, participant403
without mutation, same-name regional data, a held actual response rejected in a
new region, current receipts/visibility/deletion, restart/relogin and cleanup.
Details/Messages at1440/390pixels passed four pinned-axe states. Native/server/
core code for this SQS slice is unchanged from main26d; receipt values are not emitted.

At341dec2, SANY semantic processing and all11baselines/40exact expected
counterexamples passed with one Java worker/512MiB/default60seconds and the
original bounds. The new baseline explored86distinct states (252generated,
depth9). Policy19/applicability21 units passed, including every one of40controls
removed individually. These are finite checks, not the implementation proofs
excluded above. Test-only heading disambiguation afterward did not change
runtime/model/assets; final documentation-HEAD immutable receipts remain required.
At code inputebb3ba5 all23E2E/start and50screen audit passed. The final
documentationHEAD needs its own immutable51/applicability receipts, recorded
separately; this does not change bounds or turn the model into an implementation
proof. Current hostedCI/security remain separate. Official screenshot GET403
prevents image/authenticated-screen equivalence claims.
