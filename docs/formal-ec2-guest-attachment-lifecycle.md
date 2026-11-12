# EC2 guest attachment lifecycle model

`EC2GuestAttachmentLifecycle` checks one exact held lease, one retained attachment reservation, one owner and transport construction, one seal, one profile mutation, one held-to-contain advance and one restart. All finite schedules start with an empty attachment journal. There are no seeded states, depth cutoffs, fairness or liveness claims. Fixed symbolic attachment and original lifetime deadlines represent immutable capture, not clock arithmetic.

## Source correspondence

`ec2_guest_attachment.go` captures the same issued object's association and descriptor. `Reserve` records the single retained entry; `Prepare` is the short database transaction checking the exact held lease and current profile incarnation before inserting the preparing row. It distinguishes acknowledged, uncommitted and committed-but-unacknowledged outcomes. Unknown preparation never admits owner construction.

`BeginOwner` is the post-prepare admission decision, not a physical resource-method call. `RegisterOwner` installs the retained pointer before transport attachment. Registration and revocation are separate: `SealStart` captures the pointer under the entry mutex, and `RevokeCaptured` acts outside it. Registration may occur after a seal captured no pointer; `DeferredConstructorRevoke` then closes that owner before finishing the sealed branch. Seal acknowledgement does not imply every newly registered pointer was already revoked. It does imply the captured owner was revoked and local admission was sealed.

`StartTransport` denotes the existing owner's serialized attachment reservation, not the time of every constructor step or packet effect. `FinishTransport` can occur after sealing and separately records whether transport/service ownership transferred. A pre-transfer failure needs only the owner join. `constructorDone` corresponds to `finishConstruction`, not merely a nonnull pointer. `CommitAttached` and `Publish` keep database acknowledgement distinct from publication under the entry lock. Published scope is retained historical lookup after revocation, not renewed authority.

`OwnerJoins`, `TransportJoins` and `Drain` abstract successful cleanup observations. The sealed `ObserveCleanup` path uses the attachment's idempotent `CloseContext`, which combines transport shutdown/join and owner join; an owner-only receipt uses `owner.Wait`. Errors remain pending. The model assumes these underlying positive completion contracts rather than proving their Go implementation. It never derives backend containment from local drain; `BackendObserve` is independent.

`Advance` covers the new guard at `ec2_guest_lease.go`'s held-to-contain transition. An ordinary or aliased store cannot pass an unresolved journal row. A coordinated call needs the exact association and acknowledged local seal. Backend dispatch follows the durable lease transition. Later release transitions and the existing backend lifecycle remain separate obligations.

`ProfileMutation` covers only the new conflict guard in the two shared instance-profile transaction helpers in `iam_instance_profiles.go`. Matching deletion invalidates the profile incarnation; membership changes preserve it, so attachment preparation after a prior membership change remains possible. Both matching mutation kinds are refused while an unresolved row exists. Unrelated profiles and absence of an obligation are not blocked. Database transaction serialization is assumed; no role-trust, inline-policy or managed-policy protection is inferred.

## Properties and sensitivity

The 14 properties check exact preparation, durable-before-consumption, registered admission before transport, publication conditions, local and durable seal acknowledgement, separate backend evidence, independent cleanup joins, guarded advancement, profile association conflict, immutable capture, retained scope and restart without reconstructed receipt. The 22 semantic controls remove guards or corrupt prerequisite transitions; coordinated changes are used where a single removal would be redundant.

## Limits

One exclusive coordinator actor is assumed before restart. Restart loses its receipt but does not prove physical revocation of another surviving process. Canonical record equality, short SQL transactions and provider authority are abstract prerequisites, not JSON/SQLite/Go memory-model refinements. No physical issuer, current-policy fencing implementation, native termination API, production registry or host clock qualification is supplied. The single retained slot does not prove full quota contention. Constructor failure without a positive owner remains quarantined; no tombstone pruning, reattachment or final backend retirement is modeled.
