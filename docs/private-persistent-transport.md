# Persistent private framed transport

`guesttcp.PersistentTransport` is an explicit composition of one supplied private
framed stream, one finite passive service, fixed gateway ARP and short active
probe exchanges. It does not open a socket, launch a VM or supply physical or
provider-policy authority. Construction requires the same trusted
`FrameCommitter` contract as the existing exchange-scoped transport.

## Lifetime and ownership

`NewPersistentTransportUntil` captures one original absolute boot deadline, at
most `MaxPassiveLifetime` (24 hours). The earlier parent or passive-service deadline wins. The service
must expose one finite immutable admission context, captured once. No traffic,
probe creation or metadata token TTL extends the attachment.

Only successful construction transfers stream and service ownership. Rejection
leaves both with the caller; internal registry reservations are revoked and
released separately. Failure or expiry initiates independent stream and service
closure before joining work, so a blocked dependency does not prevent the other
from receiving its close request. Unfinished children, callbacks, scheduler and
close calls retain transport capacity. A failed external Close retains permanent
uncertain-cleanup debt, using the existing transport contract.

`OpenProbe` accepts only a constructor-approved service port. Each exchange owns
its Engine and two pumps, and uses a fresh tuple from the
[probe-session registry](private-tcp-probe-sessions.md). Child construction and
unfinished teardown count against eight live reservations. Engine/Listener stack
and worker limits also apply, so eight probes plus a passive listener are not
guaranteed to fit simultaneously.

Normal probe Close stops new output, discards queued requests that have not been
selected and waits for selected output to settle. Only then does it revoke its
session and close its Engine. It leaves the passive service and framed stream
alive. It establishes local cleanup, not a peer's TCP shutdown or physical drain.
The returned connection's Close has this same bounded behavior. A timeout returns
pending cleanup without releasing the reservation or restarting the exchange.

External deadline/cancellation during an entered write is different from normal
retirement: the write may already have effects, so the registry revokes shared
admission and the entire attachment closes. Late success is rejected and never
permits replay. A registry failure is watched directly, including while another
pump is blocked.

## Routing and output scheduling

One bounded FIFO scheduler serializes output from probe engines, the passive
service and ARP before entering a registry transmit callback. It admits at most
16 queued requests; each producer has at most one outstanding request. Every
request retains its source and fixed context until completion. The writer seals
enqueue before its final drain and settles each accepted request once. Cancellation
does not abandon queued or entered work.

Passive routes must match the configured guest IP/MAC and local Ethernet address.
They cannot overlap any of the 1024 future probe source ports. Ingress is
classified before injection; endpoints are never tried in sequence. Valid retired
probe frames are discarded. Active frames first enter a bounded copied queue,
then pass the physical owner's callback immediately around actual Engine injection.
The queue itself conveys no delivery authority.

The owner callback checks the attachment, passive service and relevant probe
lifetime before and after actual injection or encoder output. A frame waiting for
owner admission cannot acquire a newer probe's lifetime. The owner must still
serialize physical/policy revocation with the entire effect; context checks do
not eliminate a check-to-write race or recall an entered write.

All incoming and outgoing traffic, including ARP, invalid and retired frames,
shares an attachment budget of 65536 frames and 64 MiB per fixed one-minute
window. Exceeding a window's budget ends the attachment; the next window starts
fresh, so a long attachment is not limited by its total traffic. The registry,
Engine and passive service retain their independent budgets. No counter resets
when a probe closes. An IMDS service retains its existing eight lifetime TCP
attempts and token namespace; this multiplexer does not replenish either.

## Compatibility and remaining integration

Existing Transport constructors keep their short exchange ownership: closing an
observation still closes its stream and service. PersistentTransport is a distinct
wrapper and does not satisfy HTTPObserver construction. There is no no-op Close
adapter, new scorer endpoint or synthetic HTTP observation.

A production physical owner, private descriptor attachment, crash-safe whole-domain
containment, authoritative policy fence and final host observation/scoring bridge
are still required. This constructor enables no runtime capability by itself.
