# Cloud DNS atomic change model

[CloudDNSAtomicChange](../formal/CloudDNSAtomicChange.tla) and its
[configuration](../formal/CloudDNSAtomicChange.cfg) describe a bounded local
Cloud DNS API candidate for
[#343](https://github.com/susumutomita/TenkaCloudSimulator/issues/343). The
specification is derived from its isolated source
`e28de2eb336eb3cb3496e8ee63a17a2dab950e30`. Denied stages retain the
unchanged-store witness; aborts retain their before/after witnesses without
copying the discarded proposal. Successful stages and commits retain their
full intent. This candidate is registered but has not run in TLC.

## Correspondence to the local transaction

| Model operation or property | Native path |
| --- | --- |
| Owner/project admission | [Engine.ServeHTTP](../internal/gcp/dns/engine.go#L147) checks the configured owner and request project before the transaction. The model assumes a fixed owner map rather than checking SQL bootstrap or a full authorization system. |
| Stage and Write | [Engine.applyChange](../internal/gcp/dns/operations.go#L151) checks exact deletions (including TTL and values), apex protection and the complete intended graph before generating any SQL mutation. The write and receipt insert use that same transaction. |
| Verify | [verifyChangeState](../internal/gcp/dns/store.go#L198) reads the persisted zone, complete RRset collection and expected ordered receipt documents, refusing drift before commit. Model disturbance is a finite abstraction of storage drift, not a model of SQLite triggers. |
| Commit and Abort | [Engine.ServeHTTP](../internal/gcp/dns/engine.go#L199) returns successful data only after the transaction commits; errors use the rollback path. The model's `DeniedStateUnchanged` invariant specifically describes its Abort transition, not every rejected HTTP route or admission check. `PendingInvisible` concerns tentative state in the model. |
| Child page scope | [Engine.execute](../internal/gcp/dns/operations.go#L86) includes the zone ID in change and RRset page scope. [Engine.page](../internal/gcp/dns/pagination.go#L35) checks project, collection and filter scope. Model zone-generation replay is about these child collections, not the managedZones list, which uses project-wide scope. HMAC integrity and stable traversal under concurrent changes are not modeled. |

## Projection and concrete witnesses

The original 15 invariant names are retained across the two configurations.
`TypeOK` checks both projections; `NoOldGenerationReuse` is checked in
[CloudDNSChildToken](../formal/CloudDNSChildToken.tla). The remaining 13 are
checked in CloudDNSAtomicChange. Both retain two projects, both subjects, all
four owner maps (including aliases) and two zone generations.

* Storage: token actions stutter on storage, owners and transaction state.
  Their removal changes no storage action or guard. Stage resets only the
  previous observation: no transition uses it, and guarded predicates observe
  the full new create/delete/commit witness or abort before/after at the step
  that produced it. Those witnesses, the seven drift choices and both Verify
  controls are retained. Consequently, dropping a spent observation does not
  hide an absent, inconsistent or unauthorized newly produced witness.
* Tokens: project ownership, zone presence/generation, genuine issued tokens
  and use observations project directly. Storage steps stutter except zone
  create/delete; dropping the transaction idle and storage-empty deletion
  guards only adds possible traces. Create, Issue, Use and their acceptance
  conditions are otherwise unchanged. No token result can constrain storage.

| Properties | Concrete model path |
| --- | --- |
| TypeOK, NoOrphanRecords, ApexPreserved, AtomicPair | Initial state; create an owned zone, commit a fresh paired put, commit its exact removal and delete the empty zone. Pair and protected apex are checked along the path. |
| PendingInvisible, DeniedStateUnchanged | On an owned zone, stage and write a put, observe a wrong-value drift and abort after refusal. A wrong owner can also stage a denied proposal and abort. Both preserve the original store. |
| OnlyOwnerCommits, ExactDeletionRequired, CommittedStateMatchesIntent, OtherProjectUnchanged | Create both zones; use their real owners; stage a put in A, then replace it using the exact old paired values. Observe no drift and verify each transaction. B stays unchanged; omitting the expected old pair does not yield an accepted stage. |
| HistoryPrefixPreserved, ReceiptSequenceExact, CleanupAdmissibleAtQuota, NonemptyDeletionRefused | In one zone, commit two puts so the paired records are nonempty and the normal history quota is full. Zone deletion is disabled. The exact remove is enabled, commits the third receipt with prefix and sequence preserved, and unblocks zone deletion. |
| NoOldGenerationReuse | Create A, issue its first token and accept its use by A's owner. Reject use by a wrong owner, then reject A's token against a present B with B's owner. Delete/recreate A at generation 2 and reject the generation 1 token despite correct project and owner; a freshly issued token is accepted. The generation-only control makes that stale-token case fail the invariant. |

## Bounds and remaining checks

The model uses two projects and subjects, one zone slot per project, at most two
generations, synthetic apex data and two paired user keys. The pair is a fixed
atomic-replacement fixture, not an invariant that all production records occur
in equal pairs. The small history and success cutoffs are model bounds, not
runtime quotas. Cleanup admissibility is not eventual deletion or liveness.

The [native transaction test](../internal/gcp/dns/factory_lifecycle_test.go#L176)
and [final-state drift test](../internal/gcp/dns/sql_faults_test.go#L121) are
separate evidence. Neither gives a Go or SQLite refinement proof. The model
does not cover byte accounting, parsing, DNS serving, sockets, SDK behavior or
Terraform compatibility.

The registry preserves the two storage sensitivity controls for stale paired
data and a missing receipt, and adds a generation-only token control. These
state intended invariant failures; this revision has no TLC results. The applicability record is bound to the integration base
`e1c83bc1ffe8062b6c15f2a1b941f49d1ec81433`; older work records describe their
own historical bases. Neither the static checks nor the scope review
establishes a TLC result, formal gate pass, AWS behavior, or Terraform
compatibility.
