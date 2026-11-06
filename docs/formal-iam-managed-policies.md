# Managed-policy two-request scenario contract

This is a **registered source scope with local focused checks** for Issue53, not a
successful optimization or proof of the earlier four-request exploratory model.
The original candidate timed out three times at the unchanged60-second deadline.
Its full source/config/document, all failed receipts and8d8dda9 source history
are retained. The existing29 registered models, gates, deadlines and coverage floors
remain unchanged; this scope adds one model and eleven required controls.

The current candidate deliberately checks **at most two further arbitrary API
requests from four API-reachable initial states**. It does not exhaust arbitrary
three/four-request suffixes. Registration must name these actual bounds rather
than relabel the old four-request attempt as passed.

## State and admission contract

`db.views` is actual stored per-role Allow/Deny count state. `ExpectedView` specifies
its union of inline and attached-default fragments independently of the mutable
builder. A default flip (including CreateVersion with SetAsDefault) must publish
the pointer and all affected stored role views atomically. Admission checks every
resulting role aggregate before committing. Refusal or storage failure preserves
all durable versions, counters, edges, pointers and views.

Policy and role incarnations are retained across deletion. Attachment edges bind
both incarnations; version IDs advance within a policy identity, including after
a deletion gap. A fresh policy identity may restart at v1. Policy deletion requires
no attachments and only the default version; default-version deletion refuses;
role deletion refuses inline or managed dependencies. Duplicate valid-parent
Attach and absent Detach are no-ops; missing parents refuse. Service trust does
not change through attachment.

The twelve proposed Query operations are Create/Get/List/DeletePolicy;
Create/Get/List/DeletePolicyVersion and SetDefaultPolicyVersion;
AttachRolePolicy, DetachRolePolicy and ListAttachedRolePolicies. Read/list, XML,
pagination, authentication and request errors remain native/SDK responsibilities;
the model does not fabricate their coverage through stuttering.

## Actual finite bounds and seed provenance

There are two policies/two roles, two incarnations per name, numeric versions1–3,
two retained versions, documents Allow1/Deny1/Mixed2 for one action/resource,
absent or one inline Allow/Deny, aggregate budget3, and MaxRequests2. The concrete
planned implementation instead has five retained versions,20 attachments,
10 inline policies,100 statements and32KiB aggregate: those numeric/byte contracts
are not established here. Exhausting an identity carrier stops allocation rather
than inventing an API error. No fairness or liveness is asserted.

Each seed has a constructive accepted-API prelude, outside the two-request suffix:

| Seed | API prelude | Stored role views |
|---|---|---|
| Empty | Create native R,S; PutRolePolicy R=Allow | R Allow1; S empty |
| Shared | Empty; Create P(v1 Allow), P v2 Deny nondefault; Create Q(v1 Allow); Attach P to R,S | R Allow2; S Allow1 |
| Peer | Same policies/versions as Shared; attach P only to R,Q only to S | R Allow2; S Allow1 |
| Budget | Empty; Create P(v1 Allow) and Q(v1 Allow); Attach P to R,S and Q to R | R Allow3; S Allow1 |

Budget has only P v1, next2, default1. It is not an invalid/over-budget starting
state. One CreateVersion P Mixed2 SetAsDefault is within retained-version budget,
but its candidate would make R total4 while S total2. The entire operation must
refuse with no version/counter/pointer/view changes. The prelude is a logical
source-reachability witness, not an actual cloud/SDK execution claim.

## Important two-request scenarios and exact controls

All13 invariants and11 literal behavior mutation controls are retained. The table
lists planned source witnesses. Actual shortest counterexample traces can select
a different reachable seed/request and are preserved in the scoped receipt:

| Control / contract | Seed and suffix (at most two requests) | Intended invariant |
|---|---|---|
| PartialDefaultRoleRefresh | Shared: SetDefault P v2, then v1; both accepted, both roles must refresh | RoleViewsExact |
| AttachWithoutViewRefresh | Shared: Attach Q to R; accepted, R Allow2→Allow3 | RoleViewsExact |
| DetachWithoutViewRefresh | Shared: Detach P from S; accepted, S Allow1→empty | RoleViewsExact |
| DropManagedDeny | Shared: SetDefault P v2; both roles must observe explicit Deny | ExplicitDenyWins |
| DeleteDefaultVersion | Shared: DeleteVersion P v1 must refuse | DeleteDependencies |
| IgnorePolicyDeleteDependencies | Peer: Detach P from R, then Delete P must refuse its nondefault; Budget Delete P separately refuses attachments | DeleteDependencies |
| IgnoreRoleDeleteAttachments | Shared: DeleteRole S must refuse, despite no inline fragment | DeleteDependencies |
| ReuseDeletedVersionID | Shared: DeleteVersion P v2, CreateVersion P Allow nondefault; accepted version must be3, not2 | VersionIDsMonotonic |
| SkipVersionCap | Shared: third retained version must refuse | VersionCap |
| IgnoreAggregateLimit | Budget: CreateVersion P Mixed2 default; R overflows while S fits, whole refusal frame required | AggregateBudget |
| CommitFailedCandidate | Shared: storage-failed SetDefault P v2 must preserve pointer and both role views | FailureFrame |

Fresh incarnations also have real two-request accepted witnesses: Shared Delete Q
(unattached/default-only) then Create Q advances policy generation; Empty Delete S
(no dependencies) then Create S advances role generation. Denied SetDefault,
missing version, duplicate Attach and absent Detach exercise durable refusal/no-op
frames. Existing retained documents must remain unchanged on every explored
transition; generation-bound attachments and valid defaults remain invariant.

This scope does not include every longer cross-operation pipeline. For example,
creating a policy, deleting a now-empty role, then attaching to that missing role
as a unique error cause requires a longer suffix not covered by these seeds.
Native storage/HTTP tests remain necessary; finite scenarios do not establish
arbitrary operation interleavings or all unsupported inputs.

## Evaluation changes and diagnostic limits

The prior approved observer quotient remains: failed `last.req` is a fixed Rejected
record. Actual q still determines admission/candidate/commit. Durable db, real
before-frame, outcome and request count remain; accepted requests are exact. This
loses failed-request identity in diagnostic traces. Every req-dependent invariant
is guarded Accepted; Next does not read old last. That is an invariant-preserving
unlabeled quotient, not equality of labeled failed API traces.

Finite singleton value bindings for result/state/raw candidate preserve the same
transition values within this new scenario scope. They do not make the new scope
bisimilar to the old four-request model. The official TLC1.7.4 evaluation reference
is [Tool.java](https://github.com/tlaplus/tlaplus/blob/v1.7.4/tlatools/org.lamport.tlatools/src/tlc2/tool/impl/Tool.java#L3471)
and [ContextEnumerator.java](https://github.com/tlaplus/tlaplus/blob/v1.7.4/tlatools/org.lamport.tlatools/src/tlc2/tool/impl/ContextEnumerator.java#L39).
No Java implementation is adopted by the simulator.

## Acceptance and unmodeled behavior

Local scoped checks completed: SANY exit0; baseline exit0 in3.551s with138,714
generated/1,750 distinct states; all11 original controls exit12 at their exact
intended semantic invariant with real behavior traces. Every case retained the
original60s/CPU1/512MiB condition. These results apply only to the two-request,
four-seed source/configuration, not the failed four-request model. No parse/type
error, timeout, deadlock or unexecuted case was counted as success.

The registry now declares the actual bounds and source correspondence. Integration
still requires immutable input binding and the complete registered gate at the
resulting HEAD. The local twelve TLC cases do not establish global
registered-model, native Go, SDK, browser or hosted-CI success.

The reviewed source bridge is `internal/aws/iam_managed.go` plus narrow aggregate,
constructor and DeleteRole hooks in `iam_roles.go`. It must be compared with actual
code, not inferred from model presence. Native tests must demonstrate real core
Allow/Deny decisions, second-role SQL failure/IGNORE rollback, generation/version
gaps, concrete quotas, restart and two independent DBs.

No Go/SQL/parser/HTTP refinement, wildcard matching, host authentication, trust,
crypto, arbitrary concurrency, unbounded history or liveness is proved. One fixed
account is assumed. Participant isolation comes from separate processes/token
sources, not model labels. AWS-managed policies/users/groups/boundaries/tags/CFN
enrollment are excluded; legacy CFN managed-ARN metadata grants no native authority.

Official references:

- [CreatePolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreatePolicyVersion.html)
- [DeletePolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeletePolicyVersion.html)
- [DeletePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeletePolicy.html)
- [Managed policy versioning](https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_managed-versioning.html)
