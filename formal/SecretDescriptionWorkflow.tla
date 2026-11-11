---------------------- MODULE SecretDescriptionWorkflow ----------------------
EXTENDS Naturals, FiniteSets
CONSTANTS MaxSteps, MaxRequests
VARIABLES step, context, epoch, invalidations, alive, locked, status, acked, uncertain, metadata,
          nextID, requests, serverCommits, durableCommits, emissions, writes, calls
vars == <<step,context,epoch,invalidations,alive,locked,status,acked,uncertain,metadata,nextID,
          requests,serverCommits,durableCommits,emissions,writes,calls>>
IDs == 1..MaxRequests
Pending == {"WritePending", "ReadPending"}
Empty == [context |-> 0, ticket |-> 0, phase |-> "Idle", serverDone |-> FALSE]
Current(id) == alive /\ requests[id].context=context /\ requests[id].ticket=epoch
LivePending == {id \in IDs : alive /\ requests[id].context=context
  /\ requests[id].ticket=epoch /\ requests[id].phase \in Pending}
Receipt(id,kind,valid) == [kind |-> kind, requestContext |-> requests[id].context,
  currentContext |-> context, ticket |-> requests[id].ticket, currentEpoch |-> epoch,
  alive |-> alive, valid |-> valid]
Dispatch(operation,intent,current) == [operation |-> operation,intent |-> intent,current |-> current,priorLocked |-> locked]
Init == /\ step=0 /\ context=0 /\ epoch=0 /\ invalidations=0 /\ alive=TRUE /\ locked=FALSE
        /\ status="None" /\ acked=FALSE /\ uncertain=FALSE /\ metadata=FALSE /\ nextID=1
        /\ requests=[id \in IDs |-> Empty] /\ serverCommits={} /\ durableCommits={}
        /\ emissions={} /\ writes={} /\ calls={}

(* Synchronous latch precedes the first await. Confirmation, text validity and
   authentication are assumed admitted; they are native/component test duties. *)
Issue(write) ==
 /\ step<MaxSteps /\ nextID<=MaxRequests /\ alive
 /\ ~locked
 /\ (~write \/ status#"Uncertain")
 /\ requests'=[requests EXCEPT ![nextID]=[context |-> context,ticket |-> epoch+1,
                  phase |-> IF write THEN "WritePending" ELSE "ReadPending",serverDone |-> FALSE]]
 /\ locked'=TRUE /\ epoch'=epoch+1 /\ nextID'=nextID+1 /\ metadata'=FALSE
 /\ writes'=IF write THEN writes \cup {[context |-> context,uncertain |-> uncertain]} ELSE writes
 /\ calls'=calls \cup {Dispatch(IF write THEN "UpdateSecret" ELSE "DescribeSecret",IF write THEN "Write" ELSE "Read",TRUE)}
 /\ acked'=IF write THEN FALSE ELSE acked
 /\ status'=IF write THEN "None" ELSE status
 /\ step'=step+1
 /\ UNCHANGED <<context,invalidations,alive,uncertain,serverCommits,durableCommits,emissions>>

(* Abort/context invalidation is not server rollback. The server may finish an
   already dispatched write after unmount or replacement with a new context. *)
ServerFinish(id,commit) ==
 /\ step<MaxSteps /\ requests[id].phase="WritePending" /\ ~requests[id].serverDone
 /\ requests'=[requests EXCEPT ![id].serverDone=TRUE]
 /\ serverCommits'=IF commit THEN serverCommits \cup {id} ELSE serverCommits
 /\ durableCommits'=IF commit THEN durableCommits \cup {id} ELSE durableCommits
 /\ step'=step+1
 /\ UNCHANGED <<context,epoch,invalidations,alive,locked,status,acked,uncertain,metadata,nextID,emissions,writes,calls>>

WriteReply(id,validACK) ==
 /\ step<MaxSteps /\ requests[id].phase="WritePending" /\ requests[id].serverDone
 /\ (validACK => id \in serverCommits)
 /\ LET current == Current(id)
        accepted == current /\ validACK
    IN /\ requests'=[requests EXCEPT ![id].phase=IF accepted THEN "ReadPending" ELSE "Done"]
       /\ status'=IF current THEN IF accepted THEN "Saved" ELSE "Uncertain" ELSE status
       /\ acked'=IF current THEN accepted ELSE acked
       /\ uncertain'=IF current THEN ~accepted ELSE uncertain
       /\ locked'=IF current THEN accepted ELSE locked
       /\ emissions'=IF current THEN emissions \cup {Receipt(id,IF accepted THEN "ACK" ELSE "Error",validACK)} ELSE emissions
       /\ calls'=IF accepted THEN calls \cup {Dispatch("DescribeSecret","AfterACK",current)} ELSE calls
 /\ step'=step+1
 /\ UNCHANGED <<context,epoch,invalidations,alive,metadata,nextID,serverCommits,durableCommits,writes>>

ReadReply(id,validMetadata) ==
 /\ step<MaxSteps /\ requests[id].phase="ReadPending"
 /\ LET current == Current(id)
        valid == validMetadata
    IN /\ requests'=[requests EXCEPT ![id].phase="Done"]
       /\ status'=IF current THEN
             IF valid THEN IF status \in {"Saved","Refresh"} THEN "Saved" ELSE "Loaded"
             ELSE IF status="Uncertain" THEN "Uncertain"
             ELSE IF status \in {"Saved","Refresh"} THEN "Refresh" ELSE "ReadError"
           ELSE status
       /\ uncertain'=IF current /\ valid THEN FALSE ELSE uncertain
       /\ metadata'=IF current THEN valid ELSE metadata
       /\ locked'=IF current THEN FALSE ELSE locked
       /\ emissions'=IF current THEN emissions \cup {Receipt(id,IF valid THEN "Metadata" ELSE "Error",validMetadata)} ELSE emissions
 /\ step'=step+1
 /\ UNCHANGED <<context,epoch,invalidations,alive,acked,nextID,serverCommits,durableCommits,writes,calls>>

(* Context abstracts one ARN edit, selected ARN/region/principal callback change,
   inactive tab or unmount. Old callbacks remain schedulable despite abort. *)
Invalidate(changeScope,remainAlive) ==
 /\ step<MaxSteps /\ invalidations=0 /\ (changeScope \/ remainAlive)
 /\ context'=IF changeScope THEN 1 ELSE context
 /\ invalidations'=1 /\ epoch'=epoch+1 /\ alive'=remainAlive
 /\ locked'=FALSE /\ status'="None" /\ acked'=FALSE /\ uncertain'=FALSE /\ metadata'=FALSE
 /\ step'=step+1
 /\ UNCHANGED <<nextID,requests,serverCommits,durableCommits,emissions,writes,calls>>
Next == (\E write \in BOOLEAN : Issue(write))
        \/ (\E id \in IDs, result \in BOOLEAN : ServerFinish(id,result) \/ WriteReply(id,result) \/ ReadReply(id,result))
        \/ (\E changeScope,remainAlive \in BOOLEAN : Invalidate(changeScope,remainAlive))
        \/ UNCHANGED vars
TypeOK == /\ step \in 0..MaxSteps /\ context \in 0..1 /\ invalidations \in 0..1 /\ epoch \in 0..(MaxRequests+1)
          /\ alive \in BOOLEAN /\ acked \in BOOLEAN /\ locked \in BOOLEAN /\ uncertain \in BOOLEAN /\ metadata \in BOOLEAN
          /\ nextID \in 1..(MaxRequests+1)
          /\ status \in {"None","Saved","Uncertain","Loaded","Refresh","ReadError"}
          /\ serverCommits \subseteq IDs /\ durableCommits \subseteq IDs
FreshEmissions == \A e \in emissions : e.alive /\ e.requestContext=e.currentContext /\ e.ticket=e.currentEpoch
AcknowledgmentIdentity == \A e \in emissions : e.kind="ACK" => e.valid
MetadataIdentity == \A e \in emissions : e.kind="Metadata" => e.valid
OnePendingSubmission == \A d \in calls : d.intent#"AfterACK" => ~d.priorLocked
LiveWorkKeepsLatch == alive /\ LivePending#{} => locked
UncertaintyFence == uncertain => status="Uncertain"
AcknowledgedStatusPreserved == acked => status \in {"Saved","Refresh"}
NoWriteWhileUncertain == \A w \in writes : ~w.uncertain
ReadRetriesStayReadOnly == \A d \in calls : d.intent#"Write" => d.operation="DescribeSecret"
NoStaleFollowupRead == \A d \in calls : d.intent="AfterACK" => d.current
NoValueRetrieval == \A d \in calls : d.operation \in {"UpdateSecret","DescribeSecret"}
ServerCommitPreserved == durableCommits=serverCommits
UnmountHidesDisplay == ~alive => status="None" /\ ~metadata
=============================================================================
