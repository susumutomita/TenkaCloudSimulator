------------------------ MODULE KMSAliasesViewEpoch ------------------------
EXTENDS Naturals, FiniteSets, TLC
CONSTANTS Contexts, Keys, AliasName, MaxRequests
VARIABLES current, active, aborted, ui, server, pendingList, pendingTarget,
          listIssued, targetIssued, opened, write, writes, retryDone,
          retryCounts, locale, localeBefore
vars == <<current,active,aborted,ui,server,pendingList,pendingTarget,
          listIssued,targetIssued,opened,write,writes,retryDone,
          retryCounts,locale,localeBefore>>

None == "none"
Forms == {"Create","Update"}
ScopeKey(c) == IF c = "A" THEN "K1" ELSE "K2"
EmptyTarget == [present |-> FALSE,scope |-> None,form |-> 0,rev |-> 0,key |-> None]
TargetInfo(r) == [present |-> TRUE,scope |-> r.scope,form |-> r.form,
                  rev |-> r.rev,key |-> r.key]
EmptyList == [present |-> FALSE,scope |-> None,rev |-> 0,outcome |-> None]
EmptyWrite == [present |-> FALSE,pending |-> FALSE,scope |-> None,kind |-> None,
               target |-> None,info |-> EmptyTarget,confirmation |-> "",intent |-> 0]
FreshUI == [mode |-> "List",form |-> 0,targetRev |-> 0,target |-> None,
            targetInfo |-> EmptyTarget,draft |-> FALSE,typed |-> "",
            selectedAlias |-> FALSE,listRev |-> 0,status |-> "Idle",
            listInfo |-> EmptyList,rows |-> FALSE,ack |-> FALSE,
            intent |-> 0,ackIntent |-> 0,pickerError |-> FALSE,writeError |-> FALSE]
LifeCurrent(c) == c = current /\ c \in active /\ c \notin aborted
VisibleRow(s,c) == s[c].exists /\ s[c].target = ScopeKey(c)
ListRequest(c,r,s) == [scope |-> c,rev |-> r,rows |-> VisibleRow(s,c)]
TargetRequest(c,f,r,k) == [scope |-> c,form |-> f,rev |-> r,key |-> k]
VerifiedTarget == /\ ui.targetInfo.present /\ ui.target \in Keys
                  /\ ui.targetInfo.scope = current /\ ui.targetInfo.form = ui.form
                  /\ ui.targetInfo.rev = ui.targetRev /\ ui.targetInfo.key = ui.target
Frame == <<current,active,aborted,ui,server,pendingList,pendingTarget,
           listIssued,targetIssued,opened,write,writes,retryDone,retryCounts>>

(* Reachable seed: the real component's first successful ListAliases has
   finished. Two subsequent list requests and two picker reads are in this bound.
   Server outcomes are an abstract native input, not an IAM/SQL implementation. *)
Init == /\ current = "A" /\ active = {"A"} /\ aborted = {}
        /\ server \in [Contexts -> [exists : BOOLEAN,target : Keys]]
        /\ ui = [FreshUI EXCEPT !.status = "Ready",
                  !.rows = VisibleRow(server,"A"),
                  !.listInfo = [present |-> TRUE,scope |-> "A",rev |-> 0,outcome |-> "Success"]]
        /\ pendingList = {} /\ pendingTarget = {}
        /\ listIssued = 0 /\ targetIssued = 0 /\ opened = {}
        /\ write = EmptyWrite /\ writes = 0
        /\ retryDone = FALSE /\ retryCounts = [before |-> 0,after |-> 0]
        /\ locale = "en" /\ localeBefore = <<>>

StartList ==
  LET revision == ui.listRev + 1 IN
  /\ LifeCurrent(current) /\ ui.mode = "List" /\ ~write.pending
  /\ listIssued < MaxRequests
  /\ ui' = [ui EXCEPT !.listRev = revision,!.status = "Loading",
             !.selectedAlias = FALSE,
             !.listInfo = IF ui.listInfo.outcome = "Failure" THEN EmptyList ELSE @]
  /\ pendingList' = pendingList \cup {ListRequest(current,revision,server)}
  /\ listIssued' = listIssued + 1 /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,server,pendingTarget,targetIssued,
                 opened,write,writes,retryDone,retryCounts,locale>>

FinishList(r,outcome) ==
  LET eligible == /\ LifeCurrent(r.scope) (* ListLifeGuard *)
                   /\ r.rev = ui.listRev (* ListRevisionGuard *)
  IN /\ r \in pendingList
     /\ pendingList' = pendingList \ {r}
     /\ ui' = IF eligible THEN
          [ui EXCEPT !.status = IF outcome = "Success" THEN "Ready" ELSE "Failed",
           !.listInfo = [present |-> TRUE,scope |-> r.scope,rev |-> r.rev,outcome |-> outcome],
           !.rows = IF outcome = "Success" THEN r.rows ELSE FALSE, (* ClearFailedRows *)
           !.selectedAlias = FALSE]
        ELSE ui
     /\ localeBefore' = <<>>
     /\ UNCHANGED <<current,active,aborted,server,pendingTarget,listIssued,
                    targetIssued,opened,write,writes,retryDone,retryCounts,locale>>

SelectAlias == /\ LifeCurrent(current) /\ ui.mode = "List" /\ ui.status = "Ready"
               /\ ui.rows /\ ~ui.selectedAlias /\ ~write.pending
               /\ ui' = [ui EXCEPT !.selectedAlias = TRUE]
               /\ localeBefore' = <<>>
               /\ UNCHANGED <<current,active,aborted,server,pendingList,pendingTarget,
                              listIssued,targetIssued,opened,write,writes,retryDone,retryCounts,locale>>

(* Open abstracts the native key-list/initial DescribeKey completing: source
   keeps Select disabled while that initial lookup is loading. The two later
   target requests model user-selected/retried reads, which may overlap. *)
Open(kind,outcome) ==
  /\ LifeCurrent(current) /\ current \notin opened /\ ui.mode = "List"
  /\ ui.status = "Ready" /\ ~write.pending
  /\ (kind = "Create" \/ ui.selectedAlias)
  /\ (kind \in Forms \/ outcome = "Success")
  /\ ui' = [ui EXCEPT !.mode = kind,!.form = IF kind \in Forms THEN @ + 1 ELSE @,
             !.targetRev = 0,
             !.target = IF kind \in Forms THEN ScopeKey(current) ELSE None,
             !.targetInfo = IF kind \in Forms /\ outcome = "Success" THEN TargetInfo(TargetRequest(current,ui.form+1,0,ScopeKey(current))) ELSE EmptyTarget,
             !.draft = kind \in Forms,!.typed = "",
             !.intent = 1,!.ack = FALSE,!.ackIntent = 0,
             !.pickerError = kind \in Forms /\ outcome = "Failure",!.writeError = FALSE]
  /\ UNCHANGED <<pendingTarget,targetIssued>>
  /\ opened' = opened \cup {current} /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,server,pendingList,listIssued,
                 write,writes,retryDone,retryCounts,locale>>

ChooseTarget(k) ==
  LET revision == ui.targetRev + 1 IN
  /\ LifeCurrent(current) /\ ui.mode \in Forms /\ ~write.pending
  /\ targetIssued < MaxRequests
  /\ ui' = [ui EXCEPT !.target = k,!.targetRev = revision,
             !.targetInfo = EmptyTarget,!.pickerError = FALSE]
  /\ pendingTarget' = pendingTarget \cup {TargetRequest(current,ui.form,revision,k)}
  /\ targetIssued' = targetIssued + 1 /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,server,pendingList,listIssued,
                 opened,write,writes,retryDone,retryCounts,locale>>

FinishTarget(r,outcome) ==
  LET eligible == /\ LifeCurrent(r.scope) /\ r.form = ui.form
                   /\ r.rev = ui.targetRev (* TargetRevisionGuard *)
  IN /\ r \in pendingTarget
     /\ pendingTarget' = pendingTarget \ {r}
     /\ ui' = IF eligible THEN [ui EXCEPT
            !.targetInfo = IF outcome = "Success" THEN TargetInfo(r) ELSE EmptyTarget,
            !.pickerError = outcome = "Failure"] ELSE ui
     /\ localeBefore' = <<>>
     /\ UNCHANGED <<current,active,aborted,server,pendingList,listIssued,
                    targetIssued,opened,write,writes,retryDone,retryCounts,locale>>

Cancel ==
  /\ LifeCurrent(current) /\ ui.mode # "List" /\ ~write.pending
  /\ ui' = [ui EXCEPT !.mode = "List",
             !.form = IF ui.mode \in Forms THEN @ + 1 ELSE @,
             !.targetRev = IF ui.mode \in Forms THEN @ + 1 ELSE @,
             !.target = None,!.targetInfo = EmptyTarget, (* CancelTargetClear *)
             !.draft = FALSE,!.typed = "",!.pickerError = FALSE,!.writeError = FALSE]
  /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,server,pendingList,pendingTarget,
                 listIssued,targetIssued,opened,write,writes,retryDone,retryCounts,locale>>

TypeConfirmation(value) ==
  /\ LifeCurrent(current) /\ ui.mode = "Delete" /\ ~write.pending
  /\ ui.typed # value
  /\ ui' = [ui EXCEPT !.typed = value] /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,server,pendingList,pendingTarget,
                 listIssued,targetIssued,opened,write,writes,retryDone,retryCounts,locale>>

SubmitForm ==
  /\ LifeCurrent(current) /\ ui.mode \in Forms /\ ui.draft /\ ~write.pending
  /\ writes = 0 /\ listIssued < MaxRequests
  /\ VerifiedTarget (* SubmitTargetVerified *)
  /\ write' = [present |-> TRUE,pending |-> TRUE,scope |-> current,kind |-> ui.mode,
               target |-> ui.target,info |-> ui.targetInfo,confirmation |-> "",intent |-> ui.intent]
  /\ writes' = writes + 1 /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,ui,server,pendingList,pendingTarget,
                 listIssued,targetIssued,opened,retryDone,retryCounts,locale>>

SubmitDelete ==
  /\ LifeCurrent(current) /\ ui.mode = "Delete" /\ ~write.pending
  /\ writes = 0 /\ listIssued < MaxRequests
  /\ ui.typed = AliasName (* ExactDeleteConfirmation *)
  /\ write' = [present |-> TRUE,pending |-> TRUE,scope |-> current,kind |-> "Delete",
               target |-> None,info |-> EmptyTarget,confirmation |-> ui.typed,intent |-> ui.intent]
  /\ writes' = writes + 1 /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,ui,server,pendingList,pendingTarget,
                 listIssued,targetIssued,opened,retryDone,retryCounts,locale>>

(* ACK and load's synchronous setup have no intervening JS yield: clear the
   intent before starting the independent fetch, including its new revision. *)
WriteACK ==
  LET exists == write.kind # "Delete"
      target == IF write.kind = "Delete" THEN server[write.scope].target ELSE write.target
      nextServer == [server EXCEPT ![write.scope] = [exists |-> exists,target |-> target]]
      live == LifeCurrent(write.scope)
      revision == ui.listRev + 1
  IN /\ write.pending
     /\ (IF write.kind = "Create" THEN ~server[write.scope].exists ELSE server[write.scope].exists)
     /\ (~live \/ listIssued < MaxRequests)
     /\ server' = nextServer /\ write' = [write EXCEPT !.pending = FALSE]
     /\ ui' = IF live THEN [ui EXCEPT
          !.mode = "List", (* AckFormExit *)
          !.draft = FALSE, (* AckDraftClear *)
          !.form = @ + 1,!.targetRev = @ + 1,!.target = None,!.targetInfo = EmptyTarget,
          !.typed = "",!.selectedAlias = FALSE,!.rows = FALSE,!.listInfo = EmptyList,
          !.ack = TRUE,!.ackIntent = write.intent,!.listRev = revision,!.status = "Loading",
          !.pickerError = FALSE,!.writeError = FALSE] ELSE ui
     /\ pendingList' = IF live THEN pendingList \cup {ListRequest(current,revision,nextServer)} ELSE pendingList
     /\ listIssued' = IF live THEN listIssued + 1 ELSE listIssued
     /\ localeBefore' = <<>>
     /\ UNCHANGED <<current,active,aborted,pendingTarget,targetIssued,
                    opened,writes,retryDone,retryCounts,locale>>

WriteFailure ==
  /\ write.pending
  /\ write' = [write EXCEPT !.pending = FALSE]
  /\ ui' = IF LifeCurrent(write.scope) THEN [ui EXCEPT !.writeError = TRUE] ELSE ui
  /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,server,pendingList,pendingTarget,
                 listIssued,targetIssued,opened,writes,retryDone,retryCounts,locale>>

RetryRead ==
  LET revision == ui.listRev + 1 IN
  /\ LifeCurrent(current) /\ ui.mode = "List" /\ ui.status = "Failed" /\ ui.ack
  /\ ~retryDone /\ ~write.pending /\ listIssued < MaxRequests
  /\ ui' = [ui EXCEPT !.listRev = revision,!.status = "Loading",!.listInfo = EmptyList,!.selectedAlias = FALSE]
  /\ pendingList' = pendingList \cup {ListRequest(current,revision,server)}
  /\ listIssued' = listIssued + 1 /\ retryDone' = TRUE
  /\ writes' = writes (* RetryNoWriteRequest *)
  /\ write' = write (* RetryNoWritePending *)
  /\ retryCounts' = [before |-> writes,after |-> writes']
  /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,active,aborted,server,pendingTarget,targetIssued,opened,locale>>

SwitchContext ==
  /\ current = "A" /\ listIssued < MaxRequests
  /\ current' = "B" /\ active' = {"B"} /\ aborted' = aborted \cup {"A"}
  /\ ui' = [FreshUI EXCEPT !.listRev = 1,!.status = "Loading"]
  /\ pendingList' = pendingList \cup {ListRequest("B",1,server)}
  /\ listIssued' = listIssued + 1 /\ localeBefore' = <<>>
  /\ UNCHANGED <<server,pendingTarget,targetIssued,opened,write,writes,retryDone,retryCounts,locale>>

Unmount ==
  /\ current \in active
  /\ active' = active \ {current} /\ aborted' = aborted \cup {current}
  /\ ui' = FreshUI /\ localeBefore' = <<>>
  /\ UNCHANGED <<current,server,pendingList,pendingTarget,listIssued,targetIssued,
                 opened,write,writes,retryDone,retryCounts,locale>>

ChangeLocale ==
  /\ current \in active
  /\ locale' = IF locale = "en" THEN "ja" ELSE "en"
  /\ localeBefore' = Frame
  /\ UNCHANGED <<current,active,aborted,ui,server,pendingList,pendingTarget,
                 listIssued,targetIssued,opened,write,writes,retryDone,retryCounts>> (* LocaleIntentFrame *)

(* Finite scenario completion, not a UI/backend liveness assertion. Pending
   transport replies remain enabled even after abort and must be consumed. *)
Terminal == /\ current \notin active /\ pendingList = {} /\ pendingTarget = {} /\ ~write.pending
            /\ UNCHANGED vars
Next == \/ StartList
        \/ SelectAlias
        \/ \E kind \in {"Create","Update","Delete"},outcome \in {"Success","Failure"} : Open(kind,outcome)
        \/ \E k \in Keys : ChooseTarget(k)
        \/ \E r \in pendingList,outcome \in {"Success","Failure"} : FinishList(r,outcome)
        \/ \E r \in pendingTarget,outcome \in {"Success","Failure"} : FinishTarget(r,outcome)
        \/ Cancel
        \/ \E value \in {AliasName,"wrong"} : TypeConfirmation(value)
        \/ SubmitForm
        \/ SubmitDelete
        \/ WriteACK
        \/ WriteFailure
        \/ RetryRead
        \/ SwitchContext
        \/ Unmount
        \/ ChangeLocale
        \/ Terminal

TypeOK == /\ current \in Contexts /\ active \subseteq Contexts /\ aborted \subseteq Contexts
          /\ server \in [Contexts -> [exists : BOOLEAN,target : Keys]]
          /\ ui.mode \in {"List","Create","Update","Delete"}
          /\ ui.form \in 0..2 /\ ui.targetRev \in 0..3 /\ ui.target \in Keys \cup {None}
          /\ ui.targetInfo \in [present : BOOLEAN,scope : Contexts \cup {None},form : 0..2,rev : 0..3,key : Keys \cup {None}]
          /\ ui.draft \in BOOLEAN /\ ui.typed \in {"",AliasName,"wrong"} /\ ui.selectedAlias \in BOOLEAN
          /\ ui.listRev \in 0..2 /\ ui.status \in {"Idle","Loading","Ready","Failed"}
          /\ ui.listInfo \in [present : BOOLEAN,scope : Contexts \cup {None},rev : 0..2,outcome : {None,"Success","Failure"}]
          /\ ui.rows \in BOOLEAN /\ ui.ack \in BOOLEAN /\ ui.intent \in 0..1 /\ ui.ackIntent \in 0..1
          /\ ui.pickerError \in BOOLEAN /\ ui.writeError \in BOOLEAN
          /\ pendingList \subseteq [scope : Contexts,rev : 1..2,rows : BOOLEAN]
          /\ pendingTarget \subseteq [scope : Contexts,form : 1..1,rev : 1..2,key : Keys]
          /\ listIssued \in 0..MaxRequests /\ targetIssued \in 0..MaxRequests
          /\ opened \subseteq Contexts /\ writes \in 0..2 /\ retryDone \in BOOLEAN
          /\ write.present \in BOOLEAN /\ write.pending \in BOOLEAN
          /\ write.scope \in Contexts \cup {None} /\ write.kind \in {None,"Create","Update","Delete"}
          /\ write.target \in Keys \cup {None} /\ write.confirmation \in {"",AliasName,"wrong"}
          /\ write.info \in [present : BOOLEAN,scope : Contexts \cup {None},form : 0..2,rev : 0..3,key : Keys \cup {None}]
          /\ write.intent \in 0..1
          /\ retryCounts \in [before : 0..2,after : 0..2] /\ locale \in {"en","ja"}
CurrentListOutcome == ui.status \in {"Ready","Failed"} =>
  /\ ui.listInfo.present /\ ui.listInfo.scope = current /\ ui.listInfo.rev = ui.listRev
  /\ current \in active /\ current \notin aborted
NoCachedRowsOnFailure == ui.status = "Failed" => ~ui.rows
CurrentTargetMetadata == (ui.mode \in Forms /\ ui.targetInfo.present) =>
  /\ ui.targetInfo.scope = current /\ ui.targetInfo.form = ui.form
  /\ ui.targetInfo.rev = ui.targetRev /\ ui.targetInfo.key = ui.target
  /\ current \in active /\ current \notin aborted
ClosedFormHasNoTarget == ui.mode \in {"List","Delete"} => (~ui.targetInfo.present /\ ui.target = None)
AckClosesForm == (ui.ack /\ ui.intent = ui.ackIntent) => ui.mode = "List"
AckClearsDraft == (ui.ack /\ ui.intent = ui.ackIntent) => (~ui.draft /\ ui.typed = "" /\ ui.target = None)
ReadRetryNeverWrites == retryCounts.before = retryCounts.after
OnlyVerifiedTargetSubmitted == (write.present /\ write.kind \in Forms) =>
  /\ write.target \in Keys /\ write.info.present /\ write.info.key = write.target
  /\ write.info.scope = write.scope /\ write.info.form = 1 /\ write.info.rev \in 0..2
DeleteRequiresExactConfirmation == (write.present /\ write.kind = "Delete") => write.confirmation = AliasName
LocalePreservesIntent == localeBefore = <<>> \/ localeBefore = Frame
Spec == Init /\ [][Next]_vars
=============================================================================
