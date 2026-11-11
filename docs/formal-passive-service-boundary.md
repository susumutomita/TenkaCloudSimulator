# Passive service boundary obligations

Partial source scope is passive_lifetime.go, transport_passive.go and
transport_service.go. This model does not prove a full transport lifecycle.
Independent initial cases each take one boundary decision. No scenario links
constructor success to physical ownership or an actual packet delivery.

Deadline uses now=2, maximum window=3, requested0..7 and parent0..6 (zero means
absent deadline). Parent presence and cancellation vary. Original requested
expiry must be nonzero, future and within the maximum BEFORE parent clamping;
a shorter parent cannot launder an overlong request. The effective deadline is
then checked again for expiry. DeadlineExact uses independent accepted ranges;
ExpiryExact uses a relational oracle, not the Effective function. Custom context
behavior, time sampling, error-type distinction and real timer delivery are not
proved. The public short/default wrapper supplies MaxLifetime, whereas Until
supplies the requested bound and requires lifetime; those argument choices are
source inspection obligations, not modeled as a second clock.

Constructor cases use 13 symbolic route profiles, optional service and passive
versus mixed mode. ValidRoute reflects finite address/MAC/port conditions, not
netip implementation. Standalone passive requires AnyPeerPort and route validity.
Mixed service additionally binds PeerIP/PeerMAC/LocalMAC to the active config and
rejects overlapping active tuples. It intentionally need not use the same local
IP: a separate service local IP is accepted by actual source. ConstructorExact
lists independently accepted profiles. Constructor output is only a call to
newTransport; ownership has NOT yet transferred. Stream/owner checks, ARP
matching, captured service lifetime and all actual success/cleanup/Close work
belong to newTransport in transport.go, outside this proof. No Boolean valid
input is used to declare those obligations satisfied.

Receive cases use already classified -1/0/1 indices. Engine route zero takes
priority, then only the exact configured service route delegates to commit.
Classification is not authorization. Send cases with routes configured account
all output, including rejected output, before comparing classification against
expected route. A valid budget and matching route delegate to persistent queue
or owner commit according to mode. Accounted means the accounting call occurred,
not a proof of its internal counters or overflow safety. The routes=nil fallback
is unmodeled and remains a transport.go/deliver obligation. No callback is modeled
as entered or delivered by this delegation.

Error cases distinguish exact ErrClosed from wrapped or distinct failure.
For exact sentinel, service revocation/expiry takes precedence, then shared
context cancellation, then original sentinel. Wrapped/distinct failures retain
their identity. Lifetime error is an explicit three-value result interface, not
proof of serviceLifetimeError or immutable context capture. Concurrent causes
and timing are not ordered by this model. The successful ReadFrame loop is
represented by Send; repetition and actual service implementation are excluded.

Native witnesses should separately cover passive deadline range and clamping,
invalid mixed route binding/overlap, outbound cross-route rejection, lifetime
revocation while admission waits, and wrapped-error preservation. The model does not replace native tests or establish operational Battle.
Dependency provenance and physical ownership remain independent obligations.

Concrete native test references (not rerun):
- TestPassiveBootDeadlineValidationAndFailureOwnership and
  TestPassiveBootDeadlineOriginalBoundAndIndependentParentExpiry: original bound,
  expired parent and error ownership.
- TestTransportServiceConstructorFailureLeavesOwnership and
  TestPassiveTransportConstructorFailuresPreserveOwnership: wrapper delegation.
- TestTransportServiceIngressSelectsBeforeInjection and
  TestTransportServiceRejectsOtherProducerOutput: directional dispatch boundary.
- TestTransportServiceSharesAggregateBudgets and
  TestPassiveTransportOwnerDenialAndAggregateBudget: accounting/owner boundaries.
- TestTransportServiceCancellationKeepsDistinctFailureCauses: sentinel versus
  wrapped/distinct errors. Actual callback/Close tests are stronger independent
  native evidence, not inferred from this model.

Historical control correction: removing only the original future-deadline check
is equivalent under final effective>now because effective<=requested. The final
AcceptExpiredRequest control removes BOTH future checks to represent admission
of an expired request; baseline guards, corpus and invariants remain unchanged.
Initial TLA parsing/LET-parenthesis evaluation failures are retained and are not
source counterexamples or successful validation.

The mixed constructor's active Config is a fixed valid symbolic binding with
local/peer IP1/2, local/peer MAC1/2 and source/peer port1. Config normalization
and independently invalid active IP/MAC/port profiles are not covered by the
13 passive-route profiles; they remain delegated/source/native obligations.
