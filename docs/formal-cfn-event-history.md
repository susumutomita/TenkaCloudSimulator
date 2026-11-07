# Bounded CloudFormation event history

`CFNEventHistory` is a finite correspondence model for the synchronous local
CloudFormation event/history implementation. It is not an AWS asynchronous
CloudFormation model or a proof that Go or SQLite refines the specification.

## Scope and bounds

The write side starts from two canonical, reachable history seeds. `Create`
adds a new current generation while retaining one deleted older generation and
a live peer. `Delete` starts with that current generation already created,
records its deletion and prunes the old deleted generation. Old and current
reuse the same stack name in one region; the live peer has the same name in a
different region. There is one resource per stack, four events per operation,
a deleted-generation retention cap of one, and at most twenty symbolic events.
These are reduced finite witnesses, not claims to cover all possible histories,
the native 100-generation retention cap or the native 100-row page size.

The read side uses the existing old/current/peer snapshot, name or exact-ID
lookup, either region, an independent authorization verdict, and absent, valid
or invalid symbolic event cursors. It checks two-row newest-first pages and the
last-returned-row cursor only when another page exists. The authorization and
all read stages share one snapshot by assumption. Symbolic sequence numbers
stand for unique opaque stored event IDs plus their ordered SQL sequence; this
does not establish random-ID strength, unguessability or numeric parser safety.

## Source correspondence

- `internal/aws/cloudformation.go`: `StackStore.Create` and `deleteChecked`
  perform the stack/resource/claim and event writes in one transaction. Event
  completion and retention occur before the final stack/claim and selected
  physical-resource absence checks. Only commit follows those final checks.
- `internal/aws/cloudformation_events.go`: `beginStackEvents`,
  `appendStackEvent` and `finishStackEvents` bind the event/history write and
  acknowledgement stages. The model stages the four events for one resource,
  checks ignored event/history writes, marks a deletion, performs retention and
  cascaded event removal, and either commits the complete frame or rolls back.
- The same file's `StackStore.Events` binds name/exact-generation resolution,
  generation-specific authorization, cursor lookup and ordered page selection.
  A deleted generation is addressable by exact ID, not by the reused live name.
  A cursor is scoped to that generation and does not confer permission.

The established `StackOwnership`, `CFNFinalCleanupACK` and
`CFNPhysicalResourceACK` models are retained unchanged. They cover their existing
reservation and selected final-acknowledgement contracts; they do not model the
new event tables, generation-scoped reads or retention. The new model does not
silently broaden their source or semantic scope.

## Properties and sensitivity controls

The specification checks pending-state isolation, complete rollback after a
failure, preservation of live peer history, event-parent integrity, the deleted
history bound and retention of the latest deleted generation. Successful writes
must publish the exact four-event lifecycle delta with matching generation and
resource/stack statuses. History sequence metadata must identify its latest
event. A delete cannot commit a resource resurrected by a later event/history
write.

Reads must leave persisted state unchanged, require the authorization verdict,
resolve the exact permitted generation and region, reject a foreign cursor,
and return exactly the bounded descending page and next cursor. Sensitivity
variants remove individual write acknowledgements, transaction boundaries,
retention/cascade safeguards, the final cleanup check or individual read guards
and page rules. Each is required to produce its named invariant counterexample;
syntax errors, type errors, timeouts and arbitrary nonzero exits do not count.

## Assumptions and separate implementation obligations

SQL statement/transaction isolation, schema initialization, event-ID uniqueness,
foreign-key enforcement, resource-handler effects, IAM decisions and the
coherent read snapshot are abstract inputs or assumptions. Concrete JSON/XML,
SQL, timestamp formatting, cryptography, concurrency, crash/restart persistence,
actual quotas, arbitrary numbers of resources, event payload privacy and full
Go/AWS behavior are not proved. No unbounded theorem or liveness claim is made.

Native tests in `cloudformation_events_test.go` cover lifecycle/restart/name
reuse, authorization/pagination, failed transaction publication, real retention
and pruning rollback, and ignored event writes. The cleanup-order tests inject
late event/history writes that reinsert selected SSM/S3 resources and require
rollback. The official SDK original-template lifecycle checks actual native
events and cleanup separately. Finite model success does not replace those
checks, final source review, browser acceptance or hosted CI.

This model is a local validation candidate. Initial syntax diagnostics were
corrected before baseline checking; no failed diagnostic is counted as a
counterexample. Exact-input baseline/control and complete registry results must
be taken from the final validation receipt, not inferred from registration.
