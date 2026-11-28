------------------------ MODULE ServiceQuotaDiscovery ------------------------
(* A finite projection oracle, not a proof of JSON, IAM evaluation, SQL or Go. *)
(* Supported represents the three literal endpoint regions checked natively. *)
(* Counts are prior observations; Create/Delete abstract the existing catalog. *)
EXTENDS Integers, FiniteSets
CONSTANTS TeamA, TeamB, Supported, Tokyo, None, MaxSteps
Teams == {TeamA, TeamB}
Regions == {Supported, Tokyo}
Ops == {"Services", "DefaultGet", "DefaultList", "AppliedGet", "AppliedList"}
ListOps == {"Services", "DefaultList", "AppliedList"}
Grants == {"Wildcard", "QuotaOnly", "Deny", "OtherAction"}
Other(t) == IF t = TeamA THEN TeamB ELSE TeamA
Req(op, t, region, grant, service, quota, level, token) ==
  [op |-> op, team |-> t, region |-> region, grant |-> grant,
   knownService |-> service, knownQuota |-> quota, level |-> level, token |-> token]
Normal == {Req(o,t,r,g,TRUE,TRUE,"ACCOUNT",FALSE):
            o \in Ops, t \in Teams, r \in Regions, g \in Grants}
UnknownService == {Req(o,t,Supported,"Wildcard",FALSE,TRUE,"ACCOUNT",FALSE):
                    o \in Ops \ {"Services"}, t \in Teams}
UnknownQuota == {Req(o,t,Supported,"Wildcard",TRUE,FALSE,"ACCOUNT",FALSE):
                  o \in {"DefaultGet","AppliedGet","AppliedList"}, t \in Teams}
Levels == {Req("AppliedList",t,Supported,"Wildcard",TRUE,TRUE,l,FALSE):
            t \in Teams, l \in {"ALL","RESOURCE"}}
Tokens == {Req(o,t,Supported,"Wildcard",TRUE,TRUE,"ACCOUNT",TRUE):
            o \in ListOps, t \in Teams}
Requests == Normal \cup UnknownService \cup UnknownQuota \cup Levels \cup Tokens
Response(error, rows, value, arn, cursor) ==
  [error |-> error, rows |-> rows, value |-> value, arn |-> arn, cursor |-> cursor]
Empty == Response("None",0,0,FALSE,FALSE)
Configurations == {[t \in Teams |-> IF t=TeamA THEN a ELSE 3]: a \in {None,2}}

(* Independent contract oracle: a wildcard grant also allows a concrete ARN. *)
Expected(r, caps) ==
  LET authorized == r.grant = "Wildcard" \/
                    (r.op = "AppliedGet" /\ r.grant = "QuotaOnly")
      list == r.op \in ListOps
      defaults == r.op \in {"DefaultGet","DefaultList"}
      missing == r.region = Tokyo \/ ~r.knownService \/
                 (r.op \in {"DefaultGet","AppliedGet"} /\ ~r.knownQuota)
      empty == (r.op="AppliedList" /\ (~r.knownQuota \/ r.level="RESOURCE")) \/
               (~defaults /\ caps[r.team]=None)
  IN IF ~authorized THEN Response("Denied",0,0,FALSE,FALSE)
     ELSE IF list /\ r.token THEN Response("Token",0,0,FALSE,FALSE)
     ELSE IF r.op="Services" THEN
       Response("None",IF r.region=Supported THEN 1 ELSE 0,0,FALSE,FALSE)
     ELSE IF missing THEN Response("Missing",0,0,FALSE,FALSE)
     ELSE IF empty THEN
       Response(IF r.op="AppliedGet" THEN "Missing" ELSE "None",0,0,FALSE,FALSE)
     ELSE Response("None",1,IF defaults THEN 5 ELSE caps[r.team],~defaults,FALSE)

(* Separate producer functions provide precise mutation sites. *)
Authorized(r) == r.grant="Wildcard" \/ (r.grant="QuotaOnly" /\ r.op="AppliedGet")
Available(r) == r.region=Supported
KnownService(r) == r.knownService
HasBinding(r,caps) == caps[r.team] # None
Filtered(r) == r.op="AppliedList" /\ (~r.knownQuota \/ r.level="RESOURCE")
ForeignToken(r) == r.op \in ListOps /\ r.token
QuotaValue(r,caps,used) == IF r.op \in {"DefaultGet","DefaultList"} THEN 5 ELSE caps[r.team]
Cursor(r,rows) == FALSE
Project(r,caps,used) ==
  LET defaults == r.op \in {"DefaultGet","DefaultList"}
      error == IF ~Authorized(r) THEN "Denied"
               ELSE IF ForeignToken(r) THEN "Token"
               ELSE IF r.op="Services" THEN "None"
               ELSE IF ~Available(r) \/ ~KnownService(r) \/
                       (r.op \in {"DefaultGet","AppliedGet"} /\ ~r.knownQuota) \/
                       (r.op="AppliedGet" /\ ~HasBinding(r,caps)) THEN "Missing"
               ELSE "None"
      rows == IF error # "None" THEN 0
              ELSE IF r.op="Services" THEN IF Available(r) THEN 1 ELSE 0
              ELSE IF Filtered(r) \/ (r.op="AppliedList" /\ ~HasBinding(r,caps)) THEN 0
              ELSE 1
      value == IF rows=0 \/ r.op="Services" THEN 0 ELSE QuotaValue(r,caps,used)
  IN Response(error,rows,value,rows=1 /\ ~defaults /\ r.op # "Services",Cursor(r,rows))

VARIABLES counts, limits, epoch, steps, last
vars == <<counts,limits,epoch,steps,last>>
Init == /\ counts=[t \in Teams |-> 0] /\ limits \in Configurations
        /\ epoch=0 /\ steps=0
        /\ last=[event |-> "Init",team |-> TeamA,request |-> CHOOSE r \in Requests: TRUE,
                 response |-> Empty,before |-> counts,caps |-> limits,priorEpoch |-> epoch]
Observe(r) ==
  /\ steps < MaxSteps
  /\ last'=[event |-> "Read",team |-> r.team,request |-> r,
            response |-> Project(r,limits,counts),before |-> counts,caps |-> limits,priorEpoch |-> epoch]
  /\ counts'=counts (* read publication never changes the selected or peer state *)
  /\ UNCHANGED <<limits,epoch>> /\ steps'=steps+1
Change(t,delta) ==
  /\ steps < MaxSteps /\ limits[t] # None
  /\ ((delta=1 /\ counts[t]<limits[t]) \/ (delta=-1 /\ counts[t]>0))
  /\ counts'=[counts EXCEPT ![t]=@+delta]
  /\ last'=[last EXCEPT !.event=IF delta=1 THEN "Create" ELSE "Delete",
                       !.team=t,!.before=counts,!.caps=limits,!.priorEpoch=epoch]
  /\ UNCHANGED <<limits,epoch>> /\ steps'=steps+1
Restart == /\ steps < MaxSteps /\ epoch=0 /\ epoch'=1
           /\ last'=[last EXCEPT !.event="Restart",!.before=counts,!.caps=limits,!.priorEpoch=epoch]
           /\ UNCHANGED <<counts,limits>> /\ steps'=steps+1
Next == (\E r \in Requests: Observe(r)) \/ (\E t \in Teams,d \in {-1,1}: Change(t,d)) \/ Restart
Spec == Init /\ [][Next]_vars
TypeOK == /\ counts \in [Teams -> 0..3] /\ limits \in Configurations
          /\ steps \in 0..MaxSteps /\ epoch \in 0..1
          /\ last.request \in Requests
          /\ last.response.rows \in 0..1 /\ last.response.value \in (0..5) \cup {None}
ReadPreservesFrame == last.event="Read" => counts=last.before /\ limits=last.caps /\ epoch=last.priorEpoch
ReadProjectionCorrect == last.event="Read" => last.response=Expected(last.request,last.caps)
PeerPreserved == last.event \in {"Create","Delete"} => counts[Other(last.team)]=last.before[Other(last.team)]
RestartPreservesFrame == last.event="Restart" => counts=last.before /\ limits=last.caps
=============================================================================
