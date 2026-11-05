---------------------- MODULE SecretMetadataViewEpoch ----------------------
EXTENDS Naturals, FiniteSets, Sequences
CONSTANTS MaxSteps, MaxReads, MaxEpoch
VARIABLES mode, step, mounted, alive, renderScope, committedScope, layoutPending,
          arn, include, epoch, nextID, requests, pending, aborted, latest,
          result, problem, loading, locale, receipts, lastLocale, parentReveal
vars == <<mode,step,mounted,alive,renderScope,committedScope,layoutPending,
          arn,include,epoch,nextID,requests,pending,aborted,latest,
          result,problem,loading,locale,receipts,lastLocale,parentReveal>>
IDs == 1..MaxReads
ARNs == 0..2
InitialScope == [region |-> 1,principal |-> 1,selected |-> 0,active |-> TRUE,revision |-> 0]
NoCursor == [present |-> FALSE,scope |-> InitialScope,arn |-> 0,include |-> FALSE,pos |-> 0]
EmptyResult == [present |-> FALSE,scope |-> InitialScope,arn |-> 0,include |-> FALSE,
                page |-> 0,rows |-> <<>>,cursor |-> NoCursor,valid |-> TRUE]
EmptyProblem == [present |-> FALSE,scope |-> InitialScope,arn |-> 0,include |-> FALSE]
EmptyRequest == [ticket |-> 0,scope |-> InitialScope,arn |-> 0,include |-> FALSE,
                 page |-> 0,cursor |-> NoCursor,operation |-> "ListSecretVersionIds"]
RequestedOperation == "ListSecretVersionIds"
Issued == 1..(nextID-1)
Current(id) == alive /\ requests[id].ticket=epoch /\ requests[id].scope=committedScope
               /\ id \notin aborted
LatestPending == IF latest=0 THEN FALSE ELSE latest \in pending /\ Current(latest)
ResultVisible == mounted /\ result.present /\ result.scope=renderScope
                  /\ result.arn=arn /\ result.include=include
ShownRows == IF ResultVisible THEN result.rows ELSE <<>>
ShownProblem == mounted /\ problem.present /\ problem.scope=renderScope
ViewFrame == <<mounted,alive,renderScope,committedScope,layoutPending,arn,include,
               epoch,nextID,requests,pending,aborted,latest,result,problem,loading,parentReveal>>
Init == /\ mode \in {"Overlap","Scope","Paging","Locale","Tab"} /\ step=0
        /\ mounted=TRUE /\ alive=TRUE /\ renderScope=(IF mode="Tab" THEN [InitialScope EXCEPT !.active=FALSE] ELSE InitialScope)
        /\ committedScope=renderScope /\ layoutPending=FALSE
        /\ arn=(IF mode="Tab" THEN 0 ELSE 1) /\ include=FALSE /\ epoch=0 /\ nextID=1
        /\ requests=[id \in IDs |-> EmptyRequest] /\ pending={} /\ aborted={}
        /\ latest=0 /\ result=EmptyResult /\ problem=EmptyProblem /\ loading=FALSE
        /\ locale=0 /\ receipts={} /\ lastLocale=[seen |-> FALSE] /\ parentReveal=(mode="Tab")
CanInteract == mounted /\ alive /\ ~layoutPending /\ committedScope.active

(* Page size is abstracted to at most one metadata row and two pages. Cursor
   scope fields are provenance annotations, not browser parsing of opaque HMAC.
   First/Next are user actions; absence of hidden network effects needs code and
   actual browser inspection, not just this finite action system. *)
Issue(cursor,page) ==
    /\ step<MaxSteps /\ CanInteract /\ arn#0 /\ nextID<=MaxReads /\ epoch<MaxEpoch
    /\ requests'=[requests EXCEPT ![nextID]=[ticket |-> epoch+1,scope |-> committedScope,
          arn |-> arn,include |-> include,page |-> page,cursor |-> cursor,
          operation |-> RequestedOperation]]
    /\ pending'=pending \cup {nextID}
    /\ aborted'=aborted \cup (IF latest=0 THEN {} ELSE {latest})
    /\ latest'=nextID /\ nextID'=nextID+1 /\ epoch'=epoch+1
    /\ result'=EmptyResult /\ problem'=EmptyProblem /\ loading'=TRUE
    /\ step'=step+1
    /\ UNCHANGED <<mode,mounted,alive,renderScope,committedScope,layoutPending,
                     arn,include,locale,receipts,lastLocale,parentReveal>>
FirstRead == Issue(NoCursor,1)
NextRead == /\ mode="Paging" /\ ResultVisible /\ result.cursor.present /\ result.page<2
            /\ Issue(result.cursor,result.page+1)

ReplyRows(req,stamp,empty) == IF empty THEN <<>> ELSE
    <<[scope |-> req.scope,arn |-> req.arn,include |-> req.include,
       page |-> req.page,version |-> stamp]>>
Accepted(req,rows,hasNext,valid) ==
    [present |-> TRUE,scope |-> req.scope,arn |-> req.arn,include |-> req.include,
     page |-> req.page,rows |-> rows,valid |-> valid,
     cursor |-> [present |-> hasNext,scope |-> req.scope,arn |-> req.arn,
                  include |-> req.include,pos |-> req.page]]
(* Abort does not remove a pending callback from this abstraction. Old responses
   and old finally blocks may still run. Parent resolution/IAM/SQL and response
   validation are external contracts, not a React or backend refinement proof. *)
Settle(id,kind,valid,hasNext,stamp,empty) ==
    /\ step<MaxSteps /\ id \in pending
    /\ LET req == requests[id]
           accept == Current(id)
           success == kind="Success" /\ valid
           rows == ReplyRows(req,stamp,empty)
       IN /\ result'=IF accept /\ success THEN Accepted(req,rows,hasNext,valid) ELSE result
          /\ problem'=IF accept THEN
                 IF success THEN EmptyProblem ELSE
                   [present |-> TRUE,scope |-> req.scope,arn |-> req.arn,include |-> req.include]
                 ELSE problem
          /\ loading'=IF accept THEN FALSE ELSE loading
          /\ latest'=IF accept THEN 0 ELSE latest
          /\ receipts'=IF accept THEN receipts \cup {
                 [request |-> req,atEpoch |-> epoch,atScope |-> committedScope,
                  atARN |-> arn,atInclude |-> include,wasMounted |-> mounted,
                  wasAlive |-> alive,wasAborted |-> id \in aborted]}
                 ELSE receipts
    /\ pending'=pending \ {id} /\ step'=step+1
    /\ UNCHANGED <<mode,mounted,alive,renderScope,committedScope,layoutPending,
                     arn,include,epoch,nextID,requests,aborted,locale,lastLocale,parentReveal>>

Clear == /\ epoch'=epoch+1 /\ aborted'=aborted \cup (IF latest=0 THEN {} ELSE {latest})
         /\ latest'=0 /\ result'=EmptyResult /\ problem'=EmptyProblem /\ loading'=FALSE
Cancel == /\ step<MaxSteps /\ CanInteract /\ epoch<MaxEpoch /\ Clear
          /\ step'=step+1
          /\ UNCHANGED <<mode,mounted,alive,renderScope,committedScope,layoutPending,
                           arn,include,nextID,requests,pending,locale,receipts,lastLocale,parentReveal>>
EditARN(a) == /\ step<MaxSteps /\ CanInteract /\ epoch<MaxEpoch /\ a \in ARNs /\ a#arn
              /\ Clear /\ arn'=a /\ step'=step+1
              /\ UNCHANGED <<mode,mounted,alive,renderScope,committedScope,layoutPending,
                               include,nextID,requests,pending,locale,receipts,lastLocale,parentReveal>>
FilterEdit == /\ step<MaxSteps /\ CanInteract /\ epoch<MaxEpoch /\ Clear
              /\ include'=~include /\ step'=step+1
              /\ UNCHANGED <<mode,mounted,alive,renderScope,committedScope,layoutPending,
                               arn,nextID,requests,pending,locale,receipts,lastLocale,parentReveal>>
NewScope(axis) == CASE axis="Region" -> [renderScope EXCEPT !.region=3-@,!.revision=@+1]
                   [] axis="Principal" -> [renderScope EXCEPT !.principal=3-@,!.revision=@+1]
                   [] axis="ARN" -> [renderScope EXCEPT !.selected=IF @=1 THEN 2 ELSE 1,!.revision=@+1]
                   [] axis="Active" -> [renderScope EXCEPT !.active=~@,!.revision=@+1]
(* A split render/commit overapproximation exercises the immediate render guard.
   scopeNow ref is updated by layout effect, not during an abandoned render.
   ShownRows denotes child render output, not a proof of browser paint timing. *)
RenderScope(axis) == /\ step<MaxSteps /\ mode="Scope" /\ mounted /\ ~layoutPending
                    /\ renderScope.revision<2 /\ renderScope'=NewScope(axis)
                    /\ layoutPending'=TRUE /\ step'=step+1
                    /\ UNCHANGED <<mode,mounted,alive,committedScope,arn,include,epoch,
                                     nextID,requests,pending,aborted,latest,result,problem,
                                     loading,locale,receipts,lastLocale,parentReveal>>
CommitScope == /\ step<MaxSteps /\ mounted /\ layoutPending /\ epoch<MaxEpoch
               /\ committedScope'=renderScope /\ layoutPending'=FALSE
               /\ arn'=renderScope.selected /\ include'=FALSE /\ Clear
               /\ step'=step+1
               /\ UNCHANGED <<mode,mounted,alive,renderScope,nextID,requests,pending,
                                locale,receipts,lastLocale,parentReveal>>
Unmount == /\ step<MaxSteps /\ mounted /\ epoch<MaxEpoch
           /\ mounted'=FALSE /\ alive'=FALSE /\ epoch'=epoch+1
           /\ aborted'=aborted \cup (IF latest=0 THEN {} ELSE {latest}) /\ latest'=0
           /\ step'=step+1
           /\ UNCHANGED <<mode,renderScope,committedScope,layoutPending,arn,include,
                            nextID,requests,pending,result,problem,loading,locale,receipts,lastLocale,parentReveal>>
LocaleFlip == /\ step<MaxSteps /\ mode="Locale" /\ mounted /\ ~layoutPending
              /\ locale'=1-locale /\ arn'=arn /\ include'=include
              /\ UNCHANGED <<mode,mounted,alive,renderScope,committedScope,layoutPending,
                               epoch,nextID,requests,pending,aborted,latest,result,problem,
                               loading,receipts,parentReveal>>
              /\ lastLocale'=[seen |-> TRUE,before |-> ViewFrame,after |-> ViewFrame']
              /\ step'=step+1
(* Tab seed starts after an explicit prior parent reveal, outside this child's
   request log. Parent onChange invalidates that state before entering metadata.
   This Boolean models the React state protocol, not memory zeroization or Get. *)
ParentTabMetadata == /\ step<MaxSteps /\ mode="Tab" /\ mounted /\ ~layoutPending
                     /\ ~renderScope.active /\ renderScope.revision<2
                     /\ renderScope'=[renderScope EXCEPT !.active=TRUE,!.revision=@+1]
                     /\ layoutPending'=TRUE /\ parentReveal'=FALSE /\ step'=step+1
                     /\ UNCHANGED <<mode,mounted,alive,committedScope,arn,include,epoch,
                                      nextID,requests,pending,aborted,latest,result,problem,
                                      loading,locale,receipts,lastLocale>>
Next == FirstRead \/ NextRead \/ Cancel \/ FilterEdit \/ Unmount
        \/ (\E a \in ARNs : EditARN(a))
        \/ (\E id \in IDs,kind \in {"Success","NativeError"},valid,hasNext,empty \in BOOLEAN,
                stamp \in 1..2 : Settle(id,kind,valid,hasNext,stamp,empty))
        \/ (\E axis \in {"Region","Principal","ARN","Active"} : RenderScope(axis))
        \/ CommitScope \/ LocaleFlip \/ ParentTabMetadata \/ UNCHANGED vars
TypeOK == /\ mode \in {"Overlap","Scope","Paging","Locale","Tab"} /\ step \in 0..MaxSteps
          /\ mounted \in BOOLEAN /\ alive \in BOOLEAN /\ arn \in ARNs /\ include \in BOOLEAN
          /\ epoch \in 0..MaxEpoch /\ nextID \in 1..(MaxReads+1)
          /\ pending \subseteq Issued /\ aborted \subseteq Issued /\ latest \in IDs \cup {0}
          /\ layoutPending \in BOOLEAN /\ locale \in 0..1
          /\ renderScope.region \in 1..2 /\ renderScope.principal \in 1..2
          /\ renderScope.selected \in ARNs /\ renderScope.active \in BOOLEAN
          /\ renderScope.revision \in 0..2 /\ committedScope.revision \in 0..2
          /\ parentReveal \in BOOLEAN /\ Len(result.rows)<=2
MetadataRequestsOnly == \A id \in Issued : requests[id].operation="ListSecretVersionIds"
CursorRequestBound == \A id \in Issued :
    LET req == requests[id] IN req.cursor.present =>
       req.cursor.scope=req.scope /\ req.cursor.arn=req.arn /\ req.cursor.include=req.include
       /\ req.cursor.pos=req.page-1
NoStaleCallbackAcceptance == \A r \in receipts :
    r.wasMounted /\ r.wasAlive /\ ~r.wasAborted /\ r.request.ticket=r.atEpoch
    /\ r.request.scope=r.atScope /\ r.request.arn=r.atARN /\ r.request.include=r.atInclude
NoStaleRenderedMetadata == \A i \in 1..Len(ShownRows) :
    ShownRows[i].scope=renderScope /\ ShownRows[i].arn=arn /\ ShownRows[i].include=include
NoStaleRenderedError == ShownProblem =>
    problem.scope=renderScope /\ problem.arn=arn /\ problem.include=include
LoadingTracksLatest == mounted => (loading=LatestPending)
PendingClearsMetadata == mounted /\ loading => ~result.present /\ ~problem.present
NoRowsOnError == ShownProblem => ShownRows = <<>>
ValidatedMetadataOnly == result.present => result.valid
CurrentPageOnly == result.present =>
    \A i \in 1..Len(result.rows) : result.rows[i].page=result.page
LocalePreservesQuery == IF lastLocale.seen THEN lastLocale.before=lastLocale.after ELSE TRUE
ParentRevealClearedForMetadata == renderScope.active => ~parentReveal
=============================================================================
