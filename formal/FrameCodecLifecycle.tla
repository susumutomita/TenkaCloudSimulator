---------------- MODULE FrameCodecLifecycle ----------------
EXTENDS Naturals, FiniteSets
VARIABLES d, e, lastD, lastE
vars == <<d,e,lastD,lastE>>
D0 == [phase |-> "idle", calls |-> 0, headers |-> 0, bodies |-> 0,
       failed |-> FALSE, fault |-> FALSE, header |-> FALSE, size |-> FALSE,
       body |-> FALSE, result |-> "none"]
E0 == [phase |-> "idle", calls |-> 0, writes |-> 0, failed |-> FALSE,
       fault |-> FALSE, valid |-> FALSE, submitted |-> FALSE,
       exact |-> FALSE, noerror |-> FALSE,
       uncertain |-> FALSE, result |-> "none"]
Init == /\ d=D0 /\ e=E0 /\ lastD=d /\ lastE=e
DRecord == /\ lastD'=d /\ UNCHANGED <<e,lastE>>
ERecord == /\ lastE'=e /\ UNCHANGED <<d,lastD>>
DStart(source) ==
 /\ d.phase="idle" /\ d.calls<2
 /\ LET bad == d.failed \/ ~source IN
    d'=[d EXCEPT !.calls=@+1, !.header=FALSE, !.size=FALSE, !.body=FALSE,
       !.phase=IF bad THEN "idle" ELSE "header",
       !.failed=bad, !.fault=@ \/ bad,
       !.result=IF bad THEN "error" ELSE "none"]
 /\ DRecord
DHeader(full,valid) ==
 /\ d.phase="header"
 /\ LET bad == ~full \/ ~valid IN
    d'=[d EXCEPT !.headers=@+1, !.header=full, !.size=full /\ valid,
       !.phase=IF bad THEN "idle" ELSE "body",
       !.failed=bad, !.fault=@ \/ bad,
       !.result=IF bad THEN "error" ELSE "none"]
 /\ DRecord
DBody(full) ==
 /\ d.phase="body"
 /\ d'=[d EXCEPT !.bodies=@+1, !.body=full, !.phase="idle",
       !.failed=~full, !.fault=@ \/ ~full,
       !.result=IF full THEN "success" ELSE "error"]
 /\ DRecord
EStart(destination,valid) ==
 /\ e.phase="idle" /\ e.calls<2
 /\ LET bad == e.failed \/ ~destination IN
    e'=[e EXCEPT !.calls=@+1, !.valid=valid, !.submitted=FALSE, !.exact=FALSE, !.noerror=FALSE,
       !.phase=IF bad \/ ~valid THEN "idle" ELSE "write",
       !.failed=bad, !.fault=@ \/ bad,
       !.result=IF bad THEN "error" ELSE IF ~valid THEN "local-reject" ELSE "none"]
 /\ ERecord
EWrite(exact,noerror) ==
 /\ e.phase="write"
 /\ LET ok == exact /\ noerror IN
    e'=[e EXCEPT !.writes=@+1, !.submitted=TRUE, !.phase="idle",
       !.exact=exact, !.noerror=noerror,
       !.failed=~ok, !.fault=@ \/ ~ok, !.uncertain=@ \/ ~ok,
       !.result=IF ok THEN "success" ELSE "error"]
 /\ ERecord
Next == (\E a \in BOOLEAN: DStart(a) \/ DBody(a))
     \/ (\E a,b \in BOOLEAN: DHeader(a,b) \/ EStart(a,b) \/ EWrite(a,b))
TypeOK == /\ d.phase \in {"idle","header","body"} /\ e.phase \in {"idle","write"}
 /\ d.calls \in 0..2 /\ e.calls \in 0..2
 /\ d.headers \in 0..2 /\ d.bodies \in 0..2 /\ e.writes \in 0..2
 /\ d.result \in {"none","error","success"}
 /\ e.result \in {"none","error","success","local-reject"}
DecoderPoison == d.fault => d.failed
EncoderPoison == e.fault => e.failed
DecoderNoRetry == lastD.failed => d.headers=lastD.headers /\ d.bodies=lastD.bodies
EncoderNoRetry == lastE.failed => e.writes=lastE.writes
HeaderBeforeBody == d.phase="body" => d.header /\ d.size /\ ~d.failed
DecoderSuccess == d.result="success" => d.header /\ d.size /\ d.body /\ ~d.failed
DecoderSerialization == d.calls>lastD.calls => lastD.phase="idle"
EncoderSerialization == e.calls>lastE.calls => lastE.phase="idle"
BoundedReads == d.bodies<=d.headers /\ d.headers<=d.calls
OneWrite == e.writes<=e.calls
LocalRejectNoIO == e.result="local-reject" => ~e.failed /\ e.writes=lastE.writes /\ ~e.submitted
EncoderSuccess == e.result="success" => e.valid /\ e.submitted /\ ~e.failed /\ e.exact /\ e.noerror
UncertainSticky == lastE.uncertain => e.uncertain
NoBodyForInvalidHeader == d.bodies>lastD.bodies => lastD.header /\ lastD.size
============================================================
