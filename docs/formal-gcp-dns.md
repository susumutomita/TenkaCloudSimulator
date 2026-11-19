# Cloud DNS atomic change model

[CloudDNSAtomicChange](../formal/CloudDNSAtomicChange.tla) and its
[configuration](../formal/CloudDNSAtomicChange.cfg) describe a bounded local
Cloud DNS API candidate for
[#343](https://github.com/susumutomita/TenkaCloudSimulator/issues/343). The
specification is unchanged from its isolated source
`e28de2eb336eb3cb3496e8ee63a17a2dab950e30`. The local integration candidate
registers the model. It has passed static registry and applicability checks,
but neither the baseline nor its sensitivity controls have run in TLC.

## Correspondence to the local transaction

| Model operation or property | Native path |
| --- | --- |
| Owner/project admission | [Engine.ServeHTTP](../internal/gcp/dns/engine.go#L147) checks the configured owner and request project before the transaction. The model assumes a fixed owner map rather than checking SQL bootstrap or a full authorization system. |
| Stage and Write | [Engine.applyChange](../internal/gcp/dns/operations.go#L151) checks exact deletions (including TTL and values), apex protection and the complete intended graph before generating any SQL mutation. The write and receipt insert use that same transaction. |
| Verify | [verifyChangeState](../internal/gcp/dns/store.go#L198) reads the persisted zone, complete RRset collection and expected ordered receipt documents, refusing drift before commit. Model disturbance is a finite abstraction of storage drift, not a model of SQLite triggers. |
| Commit and Abort | [Engine.ServeHTTP](../internal/gcp/dns/engine.go#L199) returns successful data only after the transaction commits; errors use the rollback path. The model's `DeniedStateUnchanged` invariant specifically describes its Abort transition, not every rejected HTTP route or admission check. `PendingInvisible` concerns tentative state in the model. |
| Child page scope | [Engine.execute](../internal/gcp/dns/operations.go#L86) includes the zone ID in change and RRset page scope. [Engine.page](../internal/gcp/dns/pagination.go#L35) checks project, collection and filter scope. Model zone-generation replay is about these child collections, not the managedZones list, which uses project-wide scope. HMAC integrity and stable traversal under concurrent changes are not modeled. |

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

The registry lists two sensitivity controls for rejecting stale paired data
and a missing receipt. They describe intended invariant failures, not observed
results. The applicability record is bound to the integration base
`e1c83bc1ffe8062b6c15f2a1b941f49d1ec81433`; older work records describe their
own historical bases. Neither the static checks nor the scope review
establishes a TLC result, formal gate pass, AWS behavior, or Terraform
compatibility.
