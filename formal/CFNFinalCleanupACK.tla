------------------------- MODULE CFNFinalCleanupACK -------------------------
EXTENDS Naturals
CONSTANTS Faults
VARIABLES fault, phase, data, work, affected, outcome
vars == <<fault,phase,data,work,affected,outcome>>
InitialFrame == [stack |-> TRUE, claim |-> TRUE, resource |-> TRUE, peer |-> TRUE]
FinalRecordsAbsent(frame) == ~frame.stack /\ ~frame.claim
\* Existing resource handlers are ASSUMED successful in DeleteResource.
\* This model does not prove their physical DELETE acknowledgement.
Init == /\ fault \in Faults
        /\ phase="Ready" /\ data=InitialFrame /\ work=InitialFrame
        /\ affected=0 /\ outcome="Pending"
DeleteResource == /\ phase="Ready"
                  /\ work'=[work EXCEPT !.resource=FALSE] (* DeleteOwnedResource *)
                  /\ phase'="ResourceDeleted"
                  /\ UNCHANGED <<fault,data,affected,outcome>>
DeleteClaims == /\ phase="ResourceDeleted"
                /\ work'=[work EXCEPT !.claim=(fault="IgnoreClaims")]
                /\ phase'="ClaimsDeleted"
                /\ UNCHANGED <<fault,data,affected,outcome>>
DeleteStack == /\ phase="ClaimsDeleted"
               /\ work'=[work EXCEPT !.stack=(work.claim \/ fault \in {"IgnoreStack","ReinsertStack"})]
               /\ affected'=IF work.claim \/ fault="IgnoreStack" THEN 0 ELSE 1
               /\ phase'="Deleted"
               /\ UNCHANGED <<fault,data,outcome>>
\* FK/Exec refusal is independent of the mutable final acknowledgement checks.
ExecSucceeded == ~work.claim
CleanupAccepted == affected=1 /\ ~work.stack /\ ~work.claim (* FinalCleanupACK *)
Commit == /\ phase="Deleted" /\ ExecSucceeded /\ CleanupAccepted
          /\ data'=work /\ outcome'="Success" /\ phase'="Done"
          /\ UNCHANGED <<fault,work,affected>>
Refuse == /\ phase="Deleted" /\ (~ExecSucceeded \/ ~CleanupAccepted)
          /\ data'=data (* RollbackFrame *)
          /\ outcome'="Refused" /\ phase'="Done"
          /\ UNCHANGED <<fault,work,affected>>
Next == DeleteResource \/ DeleteClaims \/ DeleteStack \/ Commit \/ Refuse
TypeOK == /\ fault \in Faults /\ phase \in {"Ready","ResourceDeleted","ClaimsDeleted","Deleted","Done"}
          /\ data \in [ {"stack","claim","resource","peer"} -> BOOLEAN]
          /\ work \in [ {"stack","claim","resource","peer"} -> BOOLEAN]
          /\ affected \in 0..1 /\ outcome \in {"Pending","Success","Refused"}
NoFalseFinalRecordACK == outcome="Success" => FinalRecordsAbsent(data)
FailureAtomic == outcome="Refused" => data=InitialFrame
PeerFramePreserved == data.peer=InitialFrame.peer
PendingFrameUnchanged == outcome="Pending" => data=InitialFrame
Spec == Init /\ [][Next]_vars
=============================================================================
