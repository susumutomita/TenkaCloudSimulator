# Managed-policy state contract

`formal/IAMManagedPolicies.tla` is a finite design model for the native IAM
managed-policy storage increment (Issue53). It complements
`NativeIAMOwnership`; it does not replace that model or migrate legacy/CFN
roles. This source candidate has not yet been parsed or model checked. Proposed
mutation controls are outside the repository pending independent review and
scheduled Java execution. It is not registered in the shared formal gate yet.

The storage contract has twelve new Query operations: Create/Get/List/Delete
Policy; Create/Get/List/Delete PolicyVersion and SetDefaultPolicyVersion; and
AttachRolePolicy, DetachRolePolicy, ListAttachedRolePolicies. Read/list protocol,
XML, pagination and authentication behavior are not represented by state-machine
stuttering here; they require native/SDK tests.

## Durable state and atomicity

`db.views` is an explicit stored per-role Allow/Deny statement-count pair. It is
not a derived alias. `ExpectedView` independently specifies the union of the
role's inline fragment and the default document of every attached managed
policy. Repeated statements contribute counts; set deduplication cannot hide
an aggregate limit or remove a Deny. The native representation remains a policy
document in `identity_policies`, not these abstract counters.

Policies contain a live flag, incarnation, default pointer, next-version counter
and retained immutable documents. Edges retain both role and policy
incarnations. Deleted policy/role metadata retains its last incarnation; a new
creation advances it. Deleting a nondefault version leaves the monotonic
counter unchanged. A new policy incarnation starts at version1; this is not
version-ID reuse inside the old policy identity.

A default change, including CreatePolicyVersion with SetAsDefault, publishes
the pointer and **all** affected stored role views together. Admission first
checks every resulting aggregate. Any denied, missing, conflicting, over-limit
or storage-failed transaction preserves the complete prior `db`, including all
versions, counters, edges and views. `last.before` only observes that durable
frame; it is not an authority source. Controls must mutate actual durable
effects, not just this observer.

Valid-parent duplicate Attach and absent Detach are local no-ops. Missing
parents refuse. DeletePolicy requires zero attachments and only its default
version; DeletePolicyVersion refuses the default; DeleteRole refuses both inline
and managed policies. No managed attachment changes service trust.

## Bounds and initial-state provenance

The configuration fixes two policy names, two role names, two incarnations per
name, version numbers1–3, **two retained versions**, one action/resource and at
most four requests after a valid seed. Documents are Allow1, Deny1 or Mixed2;
inline state is absent or one Allow/Deny statement. Stored aggregate counts
admit at most three statements in this abstraction.

These are deliberately smaller than the proposed implementation's five retained
versions, twenty managed attachments per role, separate ten inline policies,
100 aggregate statements and32KiB normalized aggregate. The model does not
prove those numerical limits or byte accounting. Finite identity carrier
exhaustion disables further allocation rather than inventing a production API
error. MaxRequests is an explicit exploration bound, not a liveness property.

Every initial state is source-reachable by ordinary accepted API operations:

| Seed | Constructive API sequence before the checked suffix |
|---|---|
| Empty | Create native roles R,S; PutRolePolicy R=Allow |
| Shared | Empty; CreatePolicy P(v1 Allow); CreatePolicyVersion P(v2 Deny, nondefault); CreatePolicy Q(v1 Allow); Attach P to R and S |
| Peer | Same roles, inline and policy versions; attach P only to R and Q only to S |

All stored views are initialized consistently with those valid graphs. Four
additional requests suffice for a shared default flip, deletion gap followed by
fresh version3, policy deletion/recreation, or aggregate-overflow rejection.
For example, from Shared attach Q to R, delete P's nondefault version, then try
creating Mixed2 as P's new default: S would fit but R would exceed the abstract
budget. The entire version/pointer/view frame must remain unchanged.

## Safety properties and negative controls

The configuration checks typed state; failed-operation frame; deletion
preconditions; monotonic version allocation; fresh incarnations; retained
version cap; a live default; generation-bound live attachments; explicit Deny;
aggregate budget; stored-view equality; unrelated-resource frame; and immutable
retained version content.

Proposed controls change actual behavior: update only one role on a default
flip; omit Attach/Detach refresh; drop managed Deny statements; permit default
version/policy/attached-role deletion; reuse a deleted version slot; bypass
retained-version or aggregate admission; and commit a failed transaction's
candidate. Exact literal edits and intended first invariant are in the private
handoff. They are **not executed sensitivity evidence** yet. Allow-only refresh
mutants isolate stale-count errors from the earlier Deny invariant; the Deny
control deliberately targets that earlier invariant. No parse/type/runtime
error, deadlock, timeout or observer-only mutation counts as a counterexample.

## Implementation correspondence and limits

The implementation candidate is `internal/aws/iam_managed.go`, with narrow
constructor, aggregate synchronization and DeleteRole hooks in `iam_roles.go`.
A read-only comparison confirmed that default changes and every affected
`iamSyncPolicy` call share one transaction and that version allocation advances
the persisted counter. This is source correspondence, not implementation proof.
Native tests
must prove two-role default changes through actual core authorization, real
SQLite second-role failure/IGNORE rollback, generation mismatches, version
deletion gaps, five-version and aggregate limits, restart and two-DB isolation.

No Go/SQL refinement, parser correctness, wildcard matching, HTTP/XML, host
authentication, service trust, encryption, arbitrary concurrency, unbounded
history or liveness is proved. One fixed account is assumed. Separate processes
and token authorities establish participant isolation; model labels do not.
AWS-managed policies, users/groups, boundaries, tagging and CFN enrollment are
outside this increment. Existing CFN managed-ARN metadata grants no native
managed-policy authority.

Official references:

- [CreatePolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_CreatePolicyVersion.html): five retained versions and default effectiveness.
- [DeletePolicyVersion](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeletePolicyVersion.html): default deletion refusal.
- [DeletePolicy](https://docs.aws.amazon.com/IAM/latest/APIReference/API_DeletePolicy.html): detach/remove nondefaults before policy deletion.
- [Managed policy versioning](https://docs.aws.amazon.com/IAM/latest/UserGuide/access_policies_managed-versioning.html): version allocation and default behavior.
