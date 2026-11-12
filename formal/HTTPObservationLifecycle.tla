---------------- MODULE HTTPObservationLifecycle ----------------
EXTENDS Naturals
VARIABLE s
Min(a,b)==IF a<b THEN a ELSE b
Init == s=[phase |-> "new", path |-> "", started |-> FALSE, slot |-> FALSE,
 now |-> 0, deadline |-> 2, originalDeadline |-> 2, canceled |-> FALSE,
 bytes |-> 0, fields |-> 0, infos |-> 0, attemptedBytes |-> 0, attemptedFields |-> 0, informationalSeen |-> 0, status |-> 0,
 received |-> 0, rawOK |-> FALSE, rawLine |-> "", badHeader |-> FALSE,
 closeRequested |-> FALSE, cleanup |-> "none", cause |-> "none", owner |-> "none",
 callerDone |-> FALSE, timedOut |-> FALSE, published |-> 0, workerDone |-> FALSE,
 bodyReads |-> 0, attempts |-> 0, invalidRequest |-> FALSE,
 badPublication |-> FALSE, acceptedStatus |-> 0]
Construct(path)== /\ s.phase="new"
 /\ s'=[s EXCEPT !.path=path, !.phase=IF path \in {"/","/healthz"} THEN "ready" ELSE "rejected"]
Admit(caller,transport,available)== /\ s.phase="ready" /\ ~s.started /\ ~s.canceled
 /\ s'=[s EXCEPT !.phase=IF available THEN "request" ELSE "rejected",
 !.started=available, !.slot=available,
 !.deadline=Min(2,Min(caller,transport)), !.originalDeadline=Min(2,Min(caller,transport))]
Request(full)== /\ s.phase="request"
 /\ s'=[s EXCEPT !.phase=IF full THEN "raw" ELSE "failure",
 !.attempts=@+1, !.invalidRequest=@ \/ (s.path # "/" /\ s.path # "/healthz")]
Raw(fields,bytes,line)== /\ s.phase="raw"
 /\ LET ok==s.bytes+bytes<=3 /\ s.fields+fields<=2 /\ line="crlf"
    IN s'=[s EXCEPT !.bytes=@+bytes, !.fields=@+fields, !.attemptedBytes=@+bytes, !.attemptedFields=@+fields,
 !.rawOK=ok, !.rawLine=line, !.phase=IF ok THEN "parse" ELSE "failure"]
ParseError== /\ s.phase="parse" /\ s'=[s EXCEPT !.phase="failure"]
Parsed(version,status)== /\ s.phase="parse"
 /\ LET accepted==version \in {10,11} /\ status>=100 /\ status<=599 /\ status # 101
        info==status<200
        overflow==s.infos+1>1
    IN s'=[s EXCEPT
 !.phase=IF ~accepted THEN "failure" ELSE IF info THEN IF overflow THEN "failure" ELSE "raw" ELSE "parsed",
 !.infos=IF accepted /\ info THEN @+1 ELSE @, !.informationalSeen=IF accepted /\ info THEN @+1 ELSE @,
 !.status=IF accepted /\ ~info THEN status ELSE @,
 !.received=IF accepted /\ ~info THEN s.now ELSE @,
 !.acceptedStatus=IF accepted /\ ~info THEN status ELSE @,
 !.badHeader=@ \/ (accepted /\ (~s.rawOK \/ s.rawLine # "crlf" \/ s.fields>2 \/ s.bytes>3 \/ (version # 10 /\ version # 11) \/ status<100 \/ status>599 \/ status=101))]
CheckBeforeClose== /\ s.phase="parsed"
 /\ s'=[s EXCEPT !.phase=IF ~s.canceled /\ s.now<s.deadline THEN "closing" ELSE "failure", !.closeRequested=@ \/ (~s.canceled /\ s.now<s.deadline)]
FailureClose== /\ s.phase="failure" /\ s'=[s EXCEPT !.phase="failedclose", !.closeRequested=TRUE]
CloseReturn(ok,cause)== /\ s.phase \in {"closing","failedclose"}
 /\ s'=[s EXCEPT !.cleanup=IF ok THEN "joined" ELSE "uncertain", !.cause=cause,
 !.phase=IF s.phase="closing" /\ ok /\ cause="exactCanceled" /\ ~s.canceled /\ s.now<s.deadline THEN "owner" ELSE "failed"]
OwnerReturn(ok)== /\ s.phase="owner"
 /\ s'=[s EXCEPT !.owner=IF ok THEN "accepted" ELSE "rejected",
 !.phase=IF ok /\ ~s.canceled /\ s.now<s.deadline THEN "result" ELSE "failed"]
CallerReceive== /\ s.phase \in {"result","failed"} /\ ~s.callerDone
 /\ LET accept==s.phase="result" /\ ~s.canceled /\ s.now<s.deadline
    IN s'=[s EXCEPT !.callerDone=TRUE, !.published=IF accept THEN s.status ELSE 0,
 !.badPublication=@ \/ (accept /\ (s.cleanup # "joined" \/ s.cause # "exactCanceled" \/ s.owner # "accepted" \/ s.now>=s.originalDeadline \/ s.canceled))]
CallerTimeout== /\ s.started /\ ~s.callerDone /\ (s.canceled \/ s.now>=s.deadline)
 /\ s'=[s EXCEPT !.callerDone=TRUE, !.timedOut=TRUE, !.published=0, !.closeRequested=TRUE]
WorkerReturn== /\ s.phase \in {"result","failed"} /\ ~s.workerDone
 /\ s'=[s EXCEPT !.workerDone=TRUE, !.slot=FALSE]
Tick== /\ s.started /\ s.now<2 /\ s'=[s EXCEPT !.now=@+1]
Cancel== /\ ~s.canceled /\ s'=[s EXCEPT !.canceled=TRUE]
Next== (\E p \in {"/","/healthz","/bad"}: Construct(p))
 \/ (\E a,b \in {0,1,2}, ok \in BOOLEAN: Admit(a,b,ok))
 \/ (\E full \in BOOLEAN: Request(full))
 \/ (s.phase="raw" /\ \E f \in 0..2, b \in 1..3, line \in {"crlf","bareLF","folded"}: Raw(f,b,line))
 \/ ParseError \/ (s.phase="parse" /\ \E v \in {10,11,12,20}, c \in {99,100,101,199,200,302,500,599,600}: Parsed(v,c))
 \/ CheckBeforeClose \/ FailureClose
 \/ (\E ok \in BOOLEAN, cause \in {"exactCanceled","joinedCanceledError","other"}: CloseReturn(ok,cause))
 \/ (\E ok \in BOOLEAN: OwnerReturn(ok)) \/ CallerReceive \/ CallerTimeout \/ WorkerReturn \/ Tick \/ Cancel
TypeOK== /\ s.now \in 0..2 /\ s.deadline \in 0..2 /\ s.status \in 0..600
RequestBound== s.attempts<=1 /\ ~s.invalidRequest
DeadlineImmutable== s.deadline=s.originalDeadline
RawAccounting== s.bytes=s.attemptedBytes /\ s.fields=s.attemptedFields
InfoAccounting== s.infos=s.informationalSeen
RawHeaderBound== ~s.badHeader
InformationalBound== s.infos<=2 /\ (s.phase \in {"parsed","closing","owner","result"} => s.infos<=1)
NoBodyConsumption== s.bodyReads=0
RetainedWorkerDebt== (s.started /\ ~s.workerDone) => s.slot
PublicationEvidence== ~s.badPublication
ReceivedAtObserved== s.status>0 => s.received<=s.now
StatusPreserved== s.published # 0 => s.published=s.acceptedStatus
TimeoutRequestsClose== s.timedOut => s.closeRequested
CleanupRequested== s.cleanup # "none" => s.closeRequested
NoPostTimeoutPublication== s.timedOut => s.published=0
=============================================================================
