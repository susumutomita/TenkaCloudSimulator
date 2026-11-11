---------------- MODULE EngineLifecycle ----------------
EXTENDS Naturals
\* timer tracks the requested deadline, not actual dependency timer state.
\* connClosed/timerReset/Destroy denote returned calls; ignored errors are not success.
VARIABLE s
Init == s = [phase |-> "fresh", slot |-> FALSE, workers |-> 0,
 built |-> FALSE, ready |-> FALSE, closed |-> FALSE, canceled |-> FALSE,
 now |-> 0, started |-> FALSE, dials |-> 0, dial |-> "none",
 conn |-> "none", registered |-> FALSE, hasConnection |-> FALSE, dialCompleted |-> FALSE, endpointPending |-> FALSE, mutation |-> FALSE,
 mutationOnce |-> FALSE, timer |-> 0, requested |-> 0,
 captured |-> FALSE, capturedEver |-> FALSE, timerReset |-> FALSE, connClosed |-> FALSE,
 destroyed |-> FALSE, released |-> FALSE, badAdmission |-> FALSE]
Validate(ok) == /\ s.phase="fresh"
 /\ s'=[s EXCEPT !.phase=IF ok THEN "validated" ELSE "rejected"]
Slot(ok) == /\ s.phase="validated"
 /\ s'=[s EXCEPT !.phase=IF ok THEN "slot" ELSE "rejected", !.slot=ok]
Workers(n) == /\ s.phase="slot"
 /\ s'=[s EXCEPT !.phase=IF n<=2 THEN "reserved" ELSE "rejected",
 !.workers=IF n<=2 THEN n ELSE 0, !.slot=IF n<=2 THEN TRUE ELSE FALSE]
Build(ok) == /\ s.phase="reserved"
 /\ s'=[s EXCEPT !.built=TRUE, !.phase=IF ok THEN "live" ELSE "failed", !.ready=ok]
DialStart == /\ s.ready /\ ~s.closed /\ ~s.canceled /\ ~s.started
 /\ s.dial="none" /\ s.dials<2
 /\ s'=[s EXCEPT !.started=TRUE, !.dials=@+1, !.dial="pending", !.endpointPending=TRUE,
 !.badAdmission=@ \/ s.closed \/ s.canceled]
DialResult(ok) == /\ s.dial="pending" /\ ~s.mutation
 /\ s'=[s EXCEPT !.dial=IF ~ok THEN "error" ELSE IF s.closed \/ s.canceled THEN "lateclose" ELSE "registered",
 !.conn=IF ~ok THEN "none" ELSE IF s.closed \/ s.canceled THEN "late" ELSE "owned",
 !.registered=ok /\ ~s.closed /\ ~s.canceled, !.hasConnection=ok, !.endpointPending=FALSE]
LateConnectionClosed == /\ s.dial="lateclose"
 /\ s'=[s EXCEPT !.dial="error", !.conn="closed", !.connClosed=TRUE]
DialDone == /\ s.dial \in {"registered","error"}
 /\ s'=[s EXCEPT !.dial="none", !.dialCompleted=TRUE, !.canceled=@ \/ s.dial="error"]
MutationStart(t) == /\ s.conn="owned" /\ s.dialCompleted /\ ~s.closed /\ ~s.canceled
 /\ s.now<2 /\ ~s.mutationOnce
 /\ s'=[s EXCEPT !.mutation=TRUE, !.mutationOnce=TRUE, !.requested=t,
 !.badAdmission=@ \/ s.closed \/ s.canceled \/ (s.now>=2)]
MutationReturn == /\ s.mutation
 /\ s'=[s EXCEPT !.mutation=FALSE, !.timer=IF s.requested=0 \/ s.requested>2 THEN 2 ELSE s.requested]
Tick == /\ s.built /\ s.now<2 /\ s'=[s EXCEPT !.now=@+1]
Cancel == /\ s.built /\ ~s.canceled /\ s'=[s EXCEPT !.canceled=TRUE]
TimerDelivery == /\ s.built /\ s.now=2 /\ ~s.canceled /\ s'=[s EXCEPT !.canceled=TRUE]
CloseBegin == /\ s.built /\ ~s.closed /\ ~s.mutation /\ s.dial # "lateclose"
 /\ s'=[s EXCEPT !.closed=TRUE, !.canceled=TRUE, !.captured=(s.conn="owned"), !.capturedEver=@ \/ (s.conn="owned"),
 !.conn=IF s.conn="owned" THEN "closing" ELSE s.conn]
ResetTimer == /\ s.closed /\ s.captured /\ ~s.timerReset
 /\ s'=[s EXCEPT !.timerReset=TRUE, !.timer=0]
CloseConnection == /\ s.closed /\ s.captured /\ s.timerReset /\ s.conn="closing"
 /\ s'=[s EXCEPT !.conn="closed", !.connClosed=TRUE]
Destroy == /\ s.closed /\ s.dial="none" /\ ~s.destroyed
 /\ (~s.captured \/ s.connClosed)
 /\ s'=[s EXCEPT !.destroyed=TRUE]
ReleaseWorkers == /\ s.destroyed /\ s.workers # 0
 /\ s'=[s EXCEPT !.workers=0]
ReleaseSlot == /\ s.destroyed /\ s.workers=0 /\ s.slot
 /\ s'=[s EXCEPT !.slot=FALSE, !.released=TRUE]
Next == (\E ok \in BOOLEAN: Validate(ok) \/ Slot(ok) \/ Build(ok) \/ DialResult(ok))
 \/ (\E n \in {1,3}: Workers(n)) \/ DialStart \/ DialDone \/ LateConnectionClosed
 \/ (\E t \in 0..3: MutationStart(t)) \/ MutationReturn \/ Tick \/ TimerDelivery
 \/ Cancel \/ CloseBegin \/ ResetTimer \/ CloseConnection \/ Destroy \/ ReleaseWorkers \/ ReleaseSlot
TypeOK == /\ s.workers \in 0..3 /\ s.now \in 0..2 /\ s.dials \in 0..2
 /\ s.timer \in 0..3 /\ s.dial \in {"none","pending","lateclose","registered","error"}
WorkerBound == s.workers<=2
WorkerDebt == (s.built /\ ~s.destroyed) => s.workers>0
WorkerReservation == s.workers>0 => s.slot /\ ~s.released
RejectedRollback == s.phase="rejected" => ~s.slot /\ s.workers=0
SingleDial == s.dials<=1
NoInvalidAdmission == ~s.badAdmission
EndpointJoin == s.dialCompleted => ~s.endpointPending
ConnectionOwnership == s.registered => s.conn \in {"owned","closing","closed"}
RegistrationBeforeDone == (s.dialCompleted /\ s.hasConnection) => (s.registered \/ s.connClosed)
CloseWaitsMutation == s.closed => ~s.mutation
DeadlineBound == s.timer<=2
NoTimerAfterReset == s.timerReset => s.timer=0
DialJoin == s.destroyed => s.dial="none"
CaptureRetained == s.capturedEver => s.captured
ConnectionJoin == s.destroyed => (~s.capturedEver \/ s.connClosed)
TimerJoin == s.conn="closed" /\ s.captured => s.timerReset
ReleaseAfterDestroy == s.released => s.destroyed /\ s.workers=0 /\ ~s.slot
LateConnectionJoin == s.dial="none" => s.conn # "late"
=============================================================================
