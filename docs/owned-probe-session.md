# Owned TCP probe lifecycle coordinator

This opt-in coordinator owns one transferred stream. It is not a production NIC
issuer, VM attachment, authenticated endpoint, provider registry or Battle enablement.
The existing production runtime remains unchanged.

The trusted provider must supply an already-issued `IssuedProbeResource` whose
`Transact` holds actual current lease/policy authority across the entire callback.
Matching IDs, a database row, raw stream or an interface implementation alone cannot
establish that authority. No concrete production issuer is supplied here.

`OwnedProbeSession` implements both frame and observation committers on the same
object. The direct `NewOwnedProbeSession` constructor permits only probe TCP;
ARP, passive IMDS and general NIC traffic remain unsupported on that path.
Use `arpConfig=nil` there. The separate [single-use attachment](owned-probe-attachment.md)
constructor adds explicitly configured passive TCP and canonical ARP handling.
The stable stream facade is for the trusted transport, not an untrusted caller:
Read/Write accounting does not replace frame authorization.

Validation failures do not close or consume the supplied stream. Success transfers
exclusive ownership. If subsequent transport construction fails, revoke and wait;
never reuse the transferred resource. The raw stream must not be accessed elsewhere.

Revocation seals admission, cancels lifetime and starts Close once before joining
an I/O-held fence. A callback already executing may have partial effects; errors
never justify replay. Failed, blocked or unjoined cleanup retains one of eight
process-wide owner slots. Wait timeout does not end cleanup or return a slot.
There is no rebind, renewal or tombstone-pruning operation.

A narrow completed local inbound-injection exception keeps the shared owner live
when the original probe context cancels after synchronous local injection succeeds.
It requires fresh private per-invocation provenance, a clean producer outcome,
exact callback/provider error propagation and an independently live owner. The
packet callback still returns an error; this never publishes an observation or
releases cleanup debt. Generic failed writes, ambiguous effects, provider wrapping,
replayed tokens, observation callbacks and owner expiry remain fail-closed.

The provider transaction encloses observation acceptance. Evidence is bounded,
copied local data that keeps the newest `EvidenceLimit` observations (older ones
are dropped, so a long-lived session keeps accepting probes), hidden until the transaction returns successfully and context
checks pass. A provider error, duplicate callback or panic retracts tentative
records; late callbacks fail closed. This is a local acceptance point, not an
atomic transaction with Host scoring or external guest lifecycle. Physical alias
freedom, issuer correctness and synchronization of external revocation remain
provider obligations. Memory tests cannot qualify those obligations.

No production transport callsite or real resource issuer is connected by this
slice. The direct TCP-only constructor cannot supply the passive policy required
by `NewPersistentTransportUntil`; use the separate single-use attachment only
with an explicitly issued descriptor and a real bounded passive service. Its
memory integration tests do not qualify a production issuer or VM attachment.
Already accepted evidence can remain readable after later revocation; late
callbacks cannot make a new acceptance but cannot erase previously observed data.
