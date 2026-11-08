------------------------ MODULE CFNPhysicalResourceACK ------------------------
EXTENDS Naturals
CONSTANTS Faults
VARIABLES fault, phase, data, work, earlyOK, outcome
vars == <<fault,phase,data,work,earlyOK,outcome>>
InitialFrame(f) == [a |-> f#"MissingA", b |-> TRUE, peer |-> TRUE, stack |-> TRUE, claims |-> TRUE]
Init == /\ fault \in Faults /\ phase="DeleteA"
        /\ data=InitialFrame(fault) /\ work=data /\ earlyOK=FALSE /\ outcome="Pending"
DeleteA == /\ phase="DeleteA"
           /\ work'=[work EXCEPT !.a=(fault \in {"IgnoreA","ReinsertA"})] (* SelectedDeleteA *)
           /\ phase'="DeleteB"
           /\ UNCHANGED <<fault,data,earlyOK,outcome>>
DeleteB == /\ phase="DeleteB"
           /\ fault#"AbortB" (* SQLExecSucceeded *)
           /\ LET delivered == [work EXCEPT !.b=(fault="IgnoreB"), !.a=(@ \/ fault="CrossReinsertA")]
              IN /\ work'=delivered
                 /\ earlyOK'=(~delivered.a /\ ~delivered.b)
           /\ phase'="DeleteClaims"
           /\ UNCHANGED <<fault,data,outcome>>
AbortB == /\ phase="DeleteB" /\ fault="AbortB"
          /\ data'=data (* RollbackAfterExecFailure *)
          /\ outcome'="Refused" /\ phase'="Done"
          /\ UNCHANGED <<fault,work,earlyOK>>
DeleteClaims == /\ phase="DeleteClaims"
                /\ work'=[work EXCEPT !.claims=FALSE, !.a=(@ \/ fault="LateClaimA")]
                /\ phase'="DeleteStack"
                /\ UNCHANGED <<fault,data,earlyOK,outcome>>
DeleteStack == /\ phase="DeleteStack"
               /\ work'=[work EXCEPT !.stack=FALSE, !.a=(@ \/ fault="LateStackA")]
               /\ phase'="FinalRead"
               /\ UNCHANGED <<fault,data,earlyOK,outcome>>
PhysicalAccepted == ~work.a /\ ~work.b (* FinalPhysicalAbsence *)
Commit == /\ phase="FinalRead" /\ PhysicalAccepted
          /\ data'=work /\ outcome'="Success" /\ phase'="Done"
          /\ UNCHANGED <<fault,work,earlyOK>>
Refuse == /\ phase="FinalRead" /\ ~PhysicalAccepted
          /\ data'=data (* RollbackAfterPostcondition *)
          /\ outcome'="Refused" /\ phase'="Done"
          /\ UNCHANGED <<fault,work,earlyOK>>
Next == DeleteA \/ DeleteB \/ AbortB \/ DeleteClaims \/ DeleteStack \/ Commit \/ Refuse
TypeOK == /\ fault \in Faults /\ phase \in {"DeleteA","DeleteB","DeleteClaims","DeleteStack","FinalRead","Done"}
          /\ data \in [{"a","b","peer","stack","claims"} -> BOOLEAN]
          /\ work \in [{"a","b","peer","stack","claims"} -> BOOLEAN]
          /\ earlyOK \in BOOLEAN /\ outcome \in {"Pending","Success","Refused"}
SuccessPhysicalAbsent == outcome="Success" => ~data.a /\ ~data.b
FailureAtomic == outcome="Refused" => data=InitialFrame(fault)
PeerFramePreserved == data.peer=InitialFrame(fault).peer
PendingFrameUnchanged == outcome="Pending" => data=InitialFrame(fault)
Spec == Init /\ [][Next]_vars
=============================================================================
