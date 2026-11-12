# TCP route classifier: bounded source correspondence

Only internal/workload/guestnet/tcp_routes.go is proposed as scope. This is not
packet.go, frame.go or arp.go coverage. Configuration and classification are
two successive model transitions; all finite configurations start in Init,
without prebuilt valid seed states. The graph is acyclic and fully explored
when TLC reports completion.

## Concrete operations

NewTCPRoutes copies a trusted bounded list, rejects empty/oversized input,
validates each route, and rejects overlap for identical local/peer IP/MAC and
local port when either peer port is wildcard or both fixed peer ports coincide.
The model samples six route profiles, including both fixed peer ports, a
wildcard, another local port, a zero local port and identical local/peer MACs.
The two-entry bound represents (but does not prove scaling to) the actual eight.

Classify rejects unavailable configuration, invalid direction and parser
failure. The parsed contract includes nonzero TCP ports. It normalizes source
and destination based on direction, compares all five fixed tuple fields and
fixed-or-wildcard peer port, and returns the constructor-list index. Its output
does not dispatch, mutate an endpoint, or grant permission. An independent
directional predicate checks results without reusing implementation Normalize
or Matches expressions. Constructor overlap prevents ambiguous successful
selection. Reject output remains -1.

Every packet IP/MAC/port field independently ranges over 0,1,2 (729 tuples).
All three directions and both parser outcomes are explored, including malformed
packets and cross-field spoofs. Address/MAC values are symbolic equivalence
classes, not actual bytes. Constructor route profile sampling is explicit;
this is exhaustive over that finite pool, not every possible IPv4 configuration.

## Native test correspondence

- TestTCPRoutesSeparateSharedNICServices: selected index, direction, fixed and
  wildcard peer ports, local service separation.
- TestTCPRoutesRejectAmbiguousConfiguration: overlap in either list order.
- TestTCPRoutesConfigurationBoundsAndInvalidValues: concrete eight-route bound,
  IPv4/global/link-local classes, zero/group/equal MAC and port constraints.
- TestTCPRoutesRejectSpoofedAndMalformedFrames: full tuple and parser rejection.
- TestTCPRoutesCopiedConfigurationAndConcurrentReads: actual slice copying and
  concurrent reads; model value immutability does not prove Go ownership.

## Remaining obligations

Parser success is deliberately an external predicate. No claim is made about
IPv4 total/header length, fragmentation/reserved bits, checksum arithmetic,
TCP option scanning or byte offset safety. These belong to packet.go and its
native negative/vector tests and separate source-specific reasoning.
Frame codec partial read/write and sticky poison belong to frame.go.
ARP trusted reply encoding and padding belong to arp.go.
Physical lifetime, permission, expected-route enforcement at actual dispatch,
packet budgets and TCP engine state remain the caller's obligations.
