# Single-use owned probe attachment

`NewOwnedPersistentProbeSession` captures one immutable descriptor from the same
issued resource that supplies its stream and authoritative transaction. The
binding, named services, fixed passive TCP route and optional gateway ARP binding
are copied, never selected from Attach request IDs. No real resource issuer or
production runtime registration is implemented.

`AttachPersistent` reserves one attempt. Its context bounds admission only; the
persistent NIC uses the owner's captured lifetime intersected with the passive
service and requested absolute deadline. Canceling the admission context after
successful return cannot silently terminate the NIC or renew its deadline.

The same owner and stable stream facade construct the real persistent transport.
Probe TCP and one explicitly configured passive route are classified separately.
Optional ARP accepts only existing strict requests and the exact canonical reply
built from trusted configuration. Other Ethernet traffic remains unsupported.
The original TCP-only owner constructor remains TCP-only and cannot Attach.

After reservation, errors return a cleanup handle. `ServiceTransferred` is false
until transport construction succeeds; pre-success errors leave passive service
ownership with the caller. After transfer, even scope-binding failure or expired
admission retains the transport and its service for cleanup. Scope is exposed only
after complete success and remains the same object for discovery after revocation.
A second attachment attempt is always rejected.

Attachment Close requests owner revocation and transport shutdown, then observes
both obligations. Owner Wait only joins raw stream Close, facade I/O and the
provider fence; it never waits on transport completion. Thus transport closing
its stream facade cannot create an owner/transport join cycle. Owner and transport
retain separate capacity slots for their respective uncertain cleanup. A caller
wait timeout neither retries Close nor proves settlement.

Memory tests compose actual userspace TCP, passive Listener, ARP, operation scope
and the owned coordinator without host sockets. The resource issuer in these tests
is explicitly synthetic and cannot establish VM ownership, external policy fences,
clock qualification, Host provider registration or production Battle readiness.
