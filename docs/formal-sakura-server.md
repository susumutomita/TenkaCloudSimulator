# Sakura Server metadata model proposal (#423)

Source fc81380a50b9d04bdd14d3f85d809622036f17de, with test-only SDK child
975f1a9f5f5683a5498499f1f34028eb7a272aca. Scope: internal/sakura/iaas/server_http.go
and internal/sakura/iaas/server_store.go, not Disk's models or shared provider.
The candidate based on current main, 8d3e88ce6cc4cc9377138b40c34e4aeb53a61c39,
contains the same two source blobs: server_http.go 73112a8df5442b5fc8feb42091e0f400e5b31113
and server_store.go 4700767031f608d25d732ba848b91e47bce946e2.
The Server model is registered in this candidate. The unmodified model-only
baseline completed a standalone run with the pinned TLC 1.7.4 and observed
5,776 distinct states. This was not a registered shard or aggregate run. Both
registered sensitivity controls and the full gate are still pending.

ServerHandler.ServeHTTP checks trusted subject and all three realm components
(environment/project/zone) before any lookup, plus its local marker. This
model uses opaque realm equality and already-mounted stores. Reopen separately
compares the trusted configured owner and realm against the durable binding and
does not read the marker. The synthetic reopen marker is fixed true only to
avoid duplicate abstract requests; it is not a store prerequisite. ReopenBinding
checks both matching acceptance and mismatching rejection. NewServerStore
and check retain the persisted binding instead of adopting another config.
ServerStore.Create atomically commits the new row and increments next_id; Get
observes that ID; UpdateNameDesc alters only selected metadata; Delete removes
exactly that ID without advancing or rewinding the counter. A failed commit
is abstract, not actual SQL fault injection. A patch of both fields is not
represented by the separate name/description actions. Collection list and query
are also out of scope. CPU/RAM and a symbolic
stable generation are preserved; the model does not claim generation randomness,
a wire precondition, or timestamp correctness. No HTTP/parser/authentication,
SQL/locking/crash/Go, SDK or VM proof.

Bounds: two realms/owners with independently overlapping IDs, up to two live rows
per realm, three committed IDs per realm, and two symbolic values per patch field.
All state domains are finite, with no step-count cutoff and with stutter. The
former step counter was ghost data; dropping it projects every previous
bounded path and also permits longer paths over the same finite states. The
initial event still uses the initial marker to suppress non-initial checks.
Bounds are not product quotas.

Admission inputs are factored by their purpose. Each eligible metadata
operation and argument is present along the owner path, and all four trusted
configured owner/realm combinations remain on the reopen path. For HTTP,
each of the seven combinations with at least one mismatch (subject, realm or
marker) is represented per target realm using Get ID 1. Every probe still
uses the same Authorized predicate and transition; none is denied by
construction. In the reference model, authorization cannot depend on method,
ID or values; denied operations leave both stores and response observation
unchanged. The denied event's other request fields are not used by any
applicable invariant. Thus collapsing those input shapes preserves the modeled
refusal and frame obligations. This is an abstraction of the common gate, not
an HTTP proof that every operation reaches it. In the altered realm-guard
control, the one changed class (matching subject and marker, wrong realm)
becomes admitted through that same predicate; create A1 then probe it, so the
named counterexample remains meaningful without preserving other behaviors of
the deliberately altered model. The allocated-ID control stays on the original
owner path.

## Exact observation quotient

Keep the complete stores unchanged. Instead of retaining the raw event tuple,
store one boolean per event-based predicate. The observer substitutes the exact
old event and store arguments into the v3 formulas: q, unprimed stores as the
prior snapshot, the actual post store map (before assigning it to stores prime),
result, observation, and allocated ID. The CFG checks the same invariant names.
TypeOK carries the old allocation-range check in its own event boolean, and
FrozenScope and StoredIDsAndQuota still check the full live stores. OnlyOwnerRealmLookup
uses ExpectedAuth, independent of mutable Authorized. FreshCommittedID still
compares the actual ID against the BEFORE next counter and used set; its
post-used value comes from the actual next map. Init projects the original
initial event and stutter preserves its observation.

No modeled action or action guard reads the past event; all use the complete
unmodified stores and incoming request. Thus transitions correspond in both
directions on reachable states of the primary and the two changed models:
project the initialization; any full transition maps to the same next stores
and these booleans; for each reduced transition choose the same request and
outcome in a full representative, which is enabled regardless of its past
event. Every checked state invariant is preserved under this mapping, including
the immediate violating successor in each sensitivity case. No past-event
sequence property is asserted. This is a state quotient of these models,
not a SQL implementation proof, trace/fairness/liveness proof, or measured
state-count or deadline claim.

Reasoned witness only: A creates ID 1; B creates ID 1; wrong realm with A
subject is denied on A's record with no observation; A changes Name and reads
it; A reopens same binding and sees unchanged snapshot; mismatched reopen denied;
A deletes 1 and subsequent Get reports missing; A re-creates at ID 2 while B's
ID 1 is untouched. Separate trace: create 1 and 2, attempt a third at quota,
delete 1, then allocate 3. An abstract commit failure preserves the full snapshot.
The model-only baseline state count above is not a witness-trace report.

Two registered model-only sensitivity controls (not yet executed):
- In Authorized only, replace the exact tuple guard with
  q.sub = stores[q.r].binding[1]. Keep ExpectedAuth unchanged. A Get on A's existing
  row with A subject and B realm reaches OnlyOwnerRealmLookup.
- Replace FreshSlot only with the minimum unused current row in Ids
  (CHOOSE i in Ids with row absent and no smaller absent row). No guard, policy,
  allocator-counter or invariant changes. Create A1, delete A1, create again;
  FreshCommittedID detects the reused committed ID. Independently confirm actual
  TLC counterexample and first violation before official registry qualification.

If exploration is too large, separate access/admission from persistence,
explicitly recording the trusted admission precondition and retaining both
store frames, reopen and allocator. Review each witness and control again,
rather than weakening deadline or omitting an obligation.
