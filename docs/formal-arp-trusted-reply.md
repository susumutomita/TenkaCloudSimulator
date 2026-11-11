# Trusted ARP binding and reply fields

Proposed scope: internal/workload/guestnet/arp.go only. Focused finite baseline and28semantic controls verified; not integrated or
full acceptance. The model begins with seven explicit caller configuration
profiles and23 requests, constructs a private binding, allows the caller's
configuration value to change to any profile, then evaluates Reply once.

Valid binding uses distinct global-unicast IPv4 and distinct nonzero unicast
MACs. Numeric identities1/2 represent valid endpoints;0 represents absent and
3/4 represent an unsupported address/group-MAC class. This is not netip or
Ethernet byte validation proof.

Eight accepted request profiles cross frame42/60, destination broadcast/gateway
and target MAC zero/gateway. Fifteen one-field invalid requests cover bounds,
EtherType,padding,source/destination,ARP htype/ptype/hlen/plen/op,senderMAC/IP and
targetMAC/IP. Admission uses exact comparisons against retained binding.

Successful reply construction uses only retained trusted values, not request or
post-construction caller values: fixed60bytes, zero padding, EthernetARP,
Ethernet destination guest/source gateway; ARP reply2, sender gatewayMAC/IP and
target guestMAC/IP. Physical output/delivery is not a modeled action.
Caller mutation models the value-copy contract, not actual Go slice alias/race
behavior. Padding is an abstract all-zero/nonzero class, not a byte-loop proof.

Native mapping: TestARPResponder, TestARPResponderConfig,
TestARPResponderRejectsRequests, TestARPResponderOwnsValues. Actual byte field
offsets/endianness, output allocation/zero-fill and mutation/concurrent-read
behavior remain native obligations. No sockets, VM, neighbor table or physical
authority are modeled or exercised.


Verification used unique attempt directories, the shared heavy-test lane, a
pinned TLC jar,256MiB heap,one worker and60seconds per case. First baseline
failed on an IF-expression precedence error (record interpreted as conjunct);
parentheses corrected the expression without changing domains or invariants.
That failed run is preserved and not counted. Final baseline and28controls
use the same final source; every final rendered control hash is checked against
its successful run and raw expected-invariant transition trace.
