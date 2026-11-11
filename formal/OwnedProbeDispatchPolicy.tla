---------------- MODULE OwnedProbeDispatchPolicy ----------------
EXTENDS Naturals, FiniteSets
Canonical == [length |-> 60, eth |-> 2054, padding |-> 0, dst |-> 1, src |-> 2,
 htype |-> 1, ptype |-> 2048, hlen |-> 6, plen |-> 4, op |-> 2,
 smac |-> 2, sip |-> 2, tmac |-> 1, tip |-> 1]
ReplyFields == DOMAIN Canonical
Replies == {Canonical} \cup {[Canonical EXCEPT ![f]=@+1]: f \in ReplyFields}
Tuple == [localIP |-> 2, peerIP |-> 1, localMAC |-> 2, peerMAC |-> 1,
 localPort |-> 40000, peerPort |-> 80]
PassiveTuple == [Tuple EXCEPT !.localIP=3, !.localPort=8080, !.peerPort=32000]
TupleFields == DOMAIN Tuple
ProbeTuples == {Tuple,[Tuple EXCEPT !.localPort=41023]} \cup
 {[Tuple EXCEPT ![f]=@+100]:f \in TupleFields} \cup
 {[Tuple EXCEPT !.localPort=39999],[Tuple EXCEPT !.localPort=41024]}
PassiveTuples == {PassiveTuple,[PassiveTuple EXCEPT !.peerPort=32001]} \cup
 {[PassiveTuple EXCEPT ![f]=@+100]:f \in TupleFields \ {"peerPort"}}
\* Finite scenario oracle is enumerated independently of implementation predicates.
GoodProbe == {Tuple,[Tuple EXCEPT !.localPort=41023],[Tuple EXCEPT !.localPort=40100]}
GoodPassive == {PassiveTuple,[PassiveTuple EXCEPT !.peerPort=32001]}
VARIABLES kind, packet, tuple, direction, arpEnabled, passiveEnabled, present, ready,
 original, caller, captured, phase, matched, accepted
vars == <<kind,packet,tuple,direction,arpEnabled,passiveEnabled,present,ready,
 original,caller,captured,phase,matched,accepted>>
Init == /\ kind \in {"reply","request","badrequest","tcp","malformed"}
 /\ packet \in (IF kind="reply" THEN Replies ELSE {Canonical})
 /\ tuple \in (IF kind="tcp" THEN ProbeTuples \cup PassiveTuples ELSE {Tuple})
 /\ direction \in {"to","from","invalid"}
 /\ arpEnabled \in BOOLEAN /\ passiveEnabled \in BOOLEAN
 /\ present \in BOOLEAN /\ ready \in BOOLEAN
 /\ original=Canonical /\ caller=Canonical /\ captured=Canonical
 /\ phase="new" /\ matched=FALSE /\ accepted=FALSE
Capture == /\ phase="new" /\ captured'=caller /\ phase'="captured"
 /\ UNCHANGED <<kind,packet,tuple,direction,arpEnabled,passiveEnabled,present,ready,original,caller,matched,accepted>>
MutateCaller == /\ phase="captured" /\ caller' \in {Canonical,[Canonical EXCEPT !.sip=9]}
 /\ phase'="evaluate"
 /\ UNCHANGED <<kind,packet,tuple,direction,arpEnabled,passiveEnabled,present,ready,original,captured,matched,accepted>>
MatchReply == present /\ ready /\ packet=captured
ProbeAllowed == tuple.localIP=2 /\ tuple.peerIP=1 /\ tuple.localMAC=2 /\ tuple.peerMAC=1
 /\ tuple.localPort>=40000 /\ tuple.localPort<=41023 /\ tuple.peerPort=80
PassiveAllowed == tuple.localIP=3 /\ tuple.peerIP=1 /\ tuple.localMAC=2 /\ tuple.peerMAC=1
 /\ tuple.localPort=8080
DirectionOK == direction \in {"to","from"}
Dispatch == IF arpEnabled /\ kind \in {"reply","request","badrequest"}
 THEN IF direction="from" THEN kind="request" /\ present /\ ready
      ELSE direction="to" /\ kind="reply" /\ MatchReply
 ELSE kind="tcp" /\ DirectionOK /\ ((passiveEnabled /\ PassiveAllowed) \/ ProbeAllowed)
Evaluate == /\ phase="evaluate" /\ matched'=MatchReply /\ accepted'=Dispatch /\ phase'="done"
 /\ UNCHANGED <<kind,packet,tuple,direction,arpEnabled,passiveEnabled,present,ready,original,caller,captured>>
Next == Capture \/ MutateCaller \/ Evaluate
ImmutableCanonical == captured=original
ExactReplyMatch == phase="done" => matched=(present /\ ready /\ packet=original)
Expected ==
 IF kind="reply" THEN arpEnabled /\ direction="to" /\ present /\ ready /\ packet=Canonical
 ELSE IF kind="request" THEN arpEnabled /\ direction="from" /\ present /\ ready
 ELSE IF kind="tcp" THEN direction \in {"to","from"} /\ (tuple \in GoodProbe \/ (passiveEnabled /\ tuple \in GoodPassive))
 ELSE FALSE
DispatchSound == phase="done" /\ accepted => Expected
DispatchComplete == phase="done" /\ Expected => accepted
Spec == Init /\ [][Next]_vars
=============================================================================
