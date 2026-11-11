# Secret description workflow abstraction

`SecretDescriptionWorkflow` covers the description editor's mutation,
acknowledgement, metadata reconciliation and context invalidation. The existing
`SecretsMutationRefresh` abstraction covers Create/Put acknowledgement and
read-only refresh, but not context epochs or uncertain description writes.
`SecretMetadataViewEpoch` covers read-only version metadata, not writes that can
commit after their browser request is aborted. Their contracts remain separate.

## State and transitions

An explicit invocation captures a context and epoch and synchronously acquires
its submission latch before asynchronous work. The request is either
`UpdateSecret` or `DescribeSecret`. A valid write acknowledgement starts a
metadata read within the same invocation, controller and epoch; it does not
submit another mutation.

The server finish and UI callback are separate transitions. An already
dispatched update can commit before or after context invalidation, including
unmount. Aborting the browser request does not undo that commit or prove that
it failed. Server completion can also be followed by an invalid/lost response,
leaving the UI uncertain. The abstract server commit set is distinct from the
UI's knowledge; the model makes no rollback promise.

A current, identity-valid acknowledgement sets the saved state. A failed
follow-up read sets refresh-failed while retaining acknowledged success. A
write error or invalid acknowledgement instead sets uncertain. Failed read
reconciliation must preserve uncertainty and its save fence; only a successful
valid read clears uncertainty for that unchanged intent. Separately, an explicit
input edit clears the fence and requires a fresh confirmed intent before another
write. This is not an unconditional requirement to reconcile forever.

A context change or explicit input edit advances the epoch, aborts the current
controller and clears displayed state and latch. The model keeps old callbacks
schedulable. Their success, error and finally paths must not publish stale
acknowledgements, metadata or errors, initiate a stale follow-up read, or release
a newer invocation's latch.

## Properties and controls

The fourteen invariants cover state types, current-context emissions,
acknowledgement and metadata identity validation, the synchronous submission
latch, retention of the live latch, uncertainty fencing, acknowledged status,
read-only retries, stale follow-up prevention, permitted operations, durable
server commits and hidden display after unmount.

Eighteen exact negative controls independently bypass the latch, uncertainty
fence or epoch; publish stale write/read results; accept malformed ACK/metadata;
release a new latch from an old finally callback; clear uncertainty on failed
reconciliation; lose acknowledged success on failed refresh; resubmit mutations
from refresh; retrieve values; start a stale follow-up read; suppress or undo a
server commit after abort; or retain display after unmount. Their target
invariants use independent captured truth rather than the mutated freshness
predicate. Controls are sensitivity checks, not proof of implementation equivalence.

## Bounds and assumptions

The configuration allows nine nonstuttering transitions, three explicit
invocations and one scope or explicit-input invalidation. Automatic post-ACK
metadata reading stays within the same invocation. These bounds include an
uncertain write followed by a failed reconciliation and a subsequent successful
read. They do not cover arbitrary-length retry histories or all possible
interleavings.

`validACK` abstracts exact ARN/name and allowed-field validation; `validMetadata`
abstracts identity and description validation. The context represents captured
region, authenticated request callback, selected physical ARN and active state;
a separate epoch also models an edit without a scope change. Confirmation and
input validation are assumed to have passed before a modeled write. Inactive
views and unmounted views are both unable to issue requests in this abstraction.

Atomic state transitions abstract JavaScript execution between asynchronous
boundaries. Render/layout-effect timing, React scheduling, DOM behavior,
keyboard interaction, accessibility, actual AbortController delivery, Unicode
and ARN parsing, callback authentication, IAM, HTTP, SQLite and crash recovery
remain independent test obligations. Locale preservation belongs to component
checks and existing view models. Finite checking does not prove Go/React/browser
refinement, real network cancellation, backend transaction correctness or AWS
console equivalence. No secret-value retrieval is part of this editor contract.
