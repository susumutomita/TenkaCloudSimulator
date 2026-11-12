# HTTP observations on a persistent private NIC

`NewProbeHTTPObserver` binds one request to a trusted, constructor-approved
service port and either `/` or `/healthz` on an existing PersistentTransport.
Construction allocates no probe and changes no NIC lifetime. Observe has no URL,
address, command, credentials, request-header, body or score input.

An admitted observer is single-use. It opens a fresh child tuple, sends the fixed
HTTP/1 GET, and reads a bounded final response header through the same parser as
the short HTTPObserver. Header bytes, fields and informational responses retain
the existing limits. Redirect and failure statuses are returned as received;
there is no redirect following, DNS, ambient proxy, body read or retry.

The result contains only the actual status and timing, the exact immutable TCP
route used by that child, and the constructor-fixed path. It is not a score,
authorization token or promise that the service remains healthy. A received
header does not prove the response body completed.

## Cleanup and final acceptance

Before accepting an observation, the child Engine, frame pumps and public Dial
work must have joined. Normal child close leaves siblings, the framed stream and
IMDS service alive. Failure or ambiguous cleanup returns a zero observation.
The NIC, original caller and eight-second deadline remain checked independently
of the session and Engine contexts intentionally canceled by successful cleanup.

The same immutable FrameCommitter supplied to the NIC must implement
ProbeHTTPObservationCommitter. Under its actual physical-owner/policy fence, it
must validate the current incarnation and exact route/path, reject revoked or
expired work and accept the observation atomically. Copying these fields or
matching an earlier database snapshot does not implement that authority. There
is no production implementation or scorer endpoint in this component.

## Timeout is not settlement

The original caller deadline, original NIC deadline and eight seconds bound the
caller's wait. The earlier deadline receives an independent timer, so delayed
parent notification cannot extend it. Eight observation slots are shared with the
short HTTPObserver. A blocked child close or final owner call retains its slot
until that work actually settles, even after Observe has returned an error.

WaitContext observes that actual worker settlement, with an independently bounded
caller/five-second wait. It returns ErrDrainPending only while work remains, and
nil after the child, publication call and observation hooks have joined and the
slot is released. Nil means settlement, not a successful HTTP observation. Keep
the original Observe error; do not turn a completed wait into a success or retry.
This wait does not certify the separately owned whole NIC's physical cleanup.

A future TenkaCloud SimulatorUptimeTransport adapter must keep its underlying
promise unsettled until this work ends, including after the Host's own timeout.
Treating Observe's bounded return as backend settlement would release the Host's
pending-call protection too early. Such an adapter must also verify authenticated
event/team/job/instance/lease ownership and map its fixed service slot to the
captured route/path. Those provider and Host bindings remain separate; the
simulator must not calculate points or reinterpret Challenge scoring metadata.

## Compatibility and limits

The old HTTPObserver still owns and closes its whole short Transport. It cannot
be constructed from PersistentTransport. Both observers use the same response
parser and observation capacity. The old observer also honors the original
shorter caller deadline with an independent timer and rejects an already expired
caller before consuming its transport.

These interfaces do not qualify physical ownership, attach a VM NIC, provide
participant sessions or enable Battle. A production owner, durable containment,
current-policy delivery fence and Host integration are still required.
