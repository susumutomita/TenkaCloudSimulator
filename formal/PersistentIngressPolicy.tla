---------------- MODULE PersistentIngressPolicy ----------------
\* One guest frame on a persistent VM NIC. Abstract frame facts mirror the
\* checks in persistentNIC.unhandled/receive; byte parsing is not modeled.
EXTENDS Naturals
ARP == 2054
IPv4 == 2048
IPv6 == 34525
Frames == [eth: {ARP, IPv4, IPv6, 35020}, arpTrusted: BOOLEAN, tcpParse: BOOLEAN,
 passiveRoute: BOOLEAN, dstLocal: BOOLEAN, portInRange: BOOLEAN, peerOK: BOOLEAN]
VARIABLES frame, phase, outcome, charged, accepted
vars == <<frame, phase, outcome, charged, accepted>>
TypeOK == /\ frame \in Frames /\ phase \in {"new","done"}
 /\ outcome \in {"none","drop","handle","revoke"} /\ charged \in BOOLEAN /\ accepted \in BOOLEAN
Init == /\ frame \in Frames /\ phase="new" /\ outcome="none" /\ charged=FALSE /\ accepted=FALSE
\* Implementation predicate (persistentNIC.unhandled).
Unhandled == \/ frame.eth \notin {ARP, IPv4}
 \/ (frame.eth=ARP /\ ~frame.arpTrusted)
 \/ (frame.eth=IPv4 /\ (~frame.tcpParse \/ (~frame.passiveRoute /\ (~frame.dstLocal \/ ~frame.portInRange))))
Receive == /\ phase="new" /\ phase'="done" /\ UNCHANGED frame
           /\ IF Unhandled
              THEN outcome'="drop" /\ charged'=FALSE /\ accepted'=FALSE
              ELSE /\ charged'=TRUE
                   /\ IF frame.eth=ARP \/ frame.passiveRoute \/ frame.peerOK
                      THEN outcome'="handle" /\ accepted'=TRUE
                      ELSE outcome'="revoke" /\ accepted'=FALSE
Next == Receive
\* Independent oracle: the only frames an owned route may consume.
OwnedARP == frame.eth=ARP /\ frame.arpTrusted
OwnedPassive == frame.eth=IPv4 /\ frame.tcpParse /\ frame.passiveRoute
OwnedProbe == frame.eth=IPv4 /\ frame.tcpParse /\ ~frame.passiveRoute /\ frame.dstLocal /\ frame.portInRange
Owned == OwnedARP \/ OwnedPassive \/ OwnedProbe
UnownedNeverRevokes == phase="done" /\ ~Owned => outcome="drop"
DroppedNotChargedOrAccepted == outcome="drop" => ~charged /\ ~accepted
OwnedNeverDropped == phase="done" /\ Owned => outcome # "drop" /\ charged
OwnedProbeAbuseRevokes == phase="done" /\ OwnedProbe /\ ~frame.peerOK => outcome="revoke" /\ ~accepted
AcceptOnlyOwnedValid == accepted => (OwnedARP \/ OwnedPassive \/ (OwnedProbe /\ frame.peerOK))
Spec == Init /\ [][Next]_vars
=============================================================================
