# Azure live-properties callback model

`AzurePropertiesEpoch` is an independent finite model of the local blob Properties
view, registered in `formal/models.json` and required by the repository formal
gate. It neither proves
JavaScript refinement nor Azure RBAC, transport authenticity, SQL, cryptography,
header parsing, resource-name encoding or authenticated Azure portal parity.
Container properties remain deferred.

The source correspondence is `azure-blob.jsx` at `9fc385c` (unchanged at helper-only
`5063299`). `showProperties` clears properties before invoking authenticated HEAD;
`perform` captures the component lifecycle object and epoch; its callback checks
`active && epoch === capturedEpoch && !signal.aborted`. Its finally block separately
checks active/epoch before clearing busy. `closePane` invalidates epoch, aborts,
clears properties and closes. Cleanup invalidates/aborts the old lifecycle; root
keys destroy/remount the leaf on authority changes. There is no literal independent
scope-field equality check in the source.

## Bounds and abstraction

There are two **component incarnations** A/B, two fresh request epochs per
incarnation, two literal selected resources, and English/Japanese presentation.
B is a fresh lifecycle object and never reactivates A. Epoch tokens may coincide
across incarnations; callback eligibility belongs to its captured lifecycle.
Fresh bounded epoch tokens abstract source increments without claiming unbounded
wraparound or arbitrary request depth. Pending native replies may finish after
abort, Close or unmount. Each reply independently has success, forbidden or missing
status; this assumes an actual trusted native response and does not model how
credentials authorize it. No content body is represented or requested.

`Open` models the actual callable showProperties handler, including a second
programmatic invocation after a completed pane, rather than claiming a user can
click inert background controls through a native dialog. This makes the explicit
Open clearing contract testable independently of Close clearing. `Unmount` and
`MountNext` represent destruction/fresh empty component state, not an extra source
setProperties call during cleanup. Neither arbitrary scope switching nor resource
ownership is inferred from these labels. Locale changes preserve the lifecycle,
selected resource, pending replies and metadata; they do not model translations.
No liveness or complete UI reachability claim is made.

## Properties and candidate controls

The configuration checks types; successful-only and current-only publication;
clearing on Close; no previous metadata during loading or after failed HEAD;
metadata matching the active selected request; stale finalizers not clearing a
new request's busy state; and locale frame preservation.

The five literal controls below are required sensitivity checks. They do not claim
that every individual redundant defense is independently necessary. At source
revision `8e656eb`, the pinned TLC gate passed the baseline and observed the named
invariant counterexample for each control. The full registry at that revision
passed 28 baselines and 158 controls (186 executions), locally and in
[branch CI 41984532262](https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/41984532262).
These receipts describe that immutable revision; later revisions require their
own applicable checks.

| ID | Exact replacement | Expected invariant |
| --- | --- | --- |
| `BypassCurrentPublication` | `eligible == Eligible(r)` → `eligible == TRUE` | `AcceptedOnlyCurrent` |
| `AcceptFailedHEAD` | `publish == eligible /\ outcome = "Success"` → `publish == eligible` | `SuccessfulOnly` |
| `OpenKeepsPrevious` | Open-only `properties' = [properties EXCEPT ![current] = Empty]` → `UNCHANGED properties` | `LoadingClearsPrevious` |
| `CloseKeepsPrevious` | Close-only `properties' = [properties EXCEPT ![current] = Empty]` → `UNCHANGED properties` | `ClearedWhenClosed` |
| `StaleFinallyClearsBusy` | `finalize == FinalizeEligible(r)` → `finalize == TRUE` | `CurrentRequestLoading` |

The first control deliberately bypasses the combined active/epoch/abort predicate;
it does not pretend that deleting only the abort check defeats the independent
epoch or active checks. Error handling does **not** invent a nonexistent cached-list
fallback: failed native reads publish no properties. Native status checks occur
in both the root request helper and leaf wrapper; `AcceptFailedHEAD` represents
bypassing that abstract combined chain, not a single-line exploit in either layer.
Model controls must be applied to only the named operator/unique anchor, and must
fail with the named invariant and a real trace, not syntax/type/timeout errors.

## Implementation evidence boundaries

The helper `azure_live_properties.py` uses actual native HTTP, not an official
Azure SDK. It compares stale-list versus new HEAD fields, checks no body GET,
403/404 refusal without cached details, locale stability, same-component
Close/reopen with old and new native HEAD responses held independently, actual
delayed-response Close/unmount behavior and a synthetic existing-root-handler
principal transition. The same-component check keeps the new HEAD withheld while
releasing the old response and verifies that loading and empty properties persist.
Browser abort may reject the old fetch before that release; this test therefore
does not independently prove that an old JavaScript callback ran past abort.
This model is supplementary; it cannot replace actual browser/native tests,
canonical asset embedding, mandatory wrapper registration or full repository gates.
