# Durable local EC2 guest attachment obligations

The opt-in EC2GuestAttachmentCoordinator correlates an already-issued persistent
probe resource with an exact held EC2GuestLeaseStore record. The same trusted
resource supplies EC2Issuance and PersistentDescriptor. Metadata equality is not
physical authority. This coordinator correlates an already-issued resource; the
issuer, VM attachment and credential provider are separate dependencies, not
created by the coordinator. See [explicit Linux Battle runtime](start.md#explicit-linux-battle-runtime)
for the current opt-in composition and its separate qualification requirements.

Attach records canonical bounded policy and incarnation data before constructing
an owner. It registers that retained owner before starting persistent transport.
The same entry serializes publication and seal decisions. Resource constructors,
provider callbacks, Close and Wait never run inside SQL transactions. Constructor
ambiguity retains a receipt, not permission to retry. Failed post-transfer work
retains both owner and transport cleanup obligations.

Seal closes local admission before acknowledging a durable seal intent. It does
not claim callback drainage or backend containment. ObserveCleanup requires the
actual owner/transport joins to succeed. A locally_drained journal row remains
separate from the backend's contained/released lease states. Errors or timeouts do
not erase debt. Scope discovery survives revocation once publication succeeded.

Ordinary lease-store Advance checks unresolved attachment rows even when that
store has no coordinator reference. Only the same actor's matching sealed entry
permits coordinated containment dispatch. Shared IAM instance-profile mutation
helpers, used by Query and CloudFormation, reject relevant changes until local
drain. This is conservative conflict protection, not automatic containment by
IAM. The optional table's absence is checked in the same mutation transaction.
No native TerminateInstances handler is added.

A restarted coordinator cannot reconstruct a live receipt or resource from rows.
Get reports unresolved foreign entries as recovery-required. No replay, adoption,
reopening, reuse or garbage collection exists. Exclusive ownership by a single
process-level coordinator actor is a deployment prerequisite, not something a
sql.DB pointer map can prove. An unresolved former supervisor requires externally
qualified fencing/containment before recovery can be extended safely.

Tests use only injected memory resources and local SQLite state. They do not
qualify physical NIC ownership, supervisor-loss fencing, clock domains or a real
Battle deployment. Provider Transact must still enclose the actual callback under
its authoritative external lease/policy fence.

Both the original owner lifetime and the requested attachment deadline are stored
unchanged; the latter cannot extend the former. RecoveryRequired is true for
unresolved foreign entries and local sealed, constructing or ownerless entries.
A repeated successful ObserveCleanup observes the same local drain, without
releasing capacity twice or claiming backend settlement.

Rows and actor entries remain retained even when locally_drained. The cumulative
1024-record quota, no same-owner reattachment and no retirement API are deliberate
bounds. This increment does not finalize both local and backend obligations into
a reusable deployment. It never deletes tombstones or reuses identities.

The IAM hook protects only instance-profile association deletion and role
addition/removal. It does not intercept role inline/trust-policy edits or all
network-policy changes. No IAM credential authority is granted by the network
policy descriptor. The qualified resource's Transact fence must independently
check current applicable physical lease and network policy at every callback.

Cleanup observation is allowed only after the entry is sealed and construction
has finished. It reasserts the attachment's existing one-shot CloseContext, whose
fixed shutdown deadline never renews and whose success requires both actual joins
and successful owned Close returns. WaitContext instead includes the terminal
operation error, so a normally canceled operation can remain a failed operation
while its cleanup bookkeeping becomes locally_drained. No cancellation error is
filtered or interpreted as evidence of successful cleanup; failed Close remains
pending and is never retried.

The bounded ordering model and its exclusions are described in
[EC2 attachment lifecycle model](formal-ec2-guest-attachment-lifecycle.md).
