-------------------- MODULE SSMDurableCommands --------------------
EXTENDS Naturals, FiniteSets
\* Conditional on DurableBackend's durable token deduplication and atomic
\* lease-lifecycle fencing. No actual backend, SQL, wire protocol or liveness.
VARIABLES s, last
vars == <<s, last>>
Targets == {1,2}
Faults == {"none", "authority", "lease", "request", "token", "started",
           "cleanup", "truncated", "exit"}
Identity == [authority |-> 1, lease |-> 1, request |-> 1, token |-> 1]
Envelope(f) == [Identity EXCEPT
 !.authority=IF f="authority" THEN 2 ELSE @,
 !.lease=IF f="lease" THEN 2 ELSE @,
 !.request=IF f="request" THEN 2 ELSE @,
 !.token=IF f="token" THEN 2 ELSE @]
Save(e) == last'=[before |-> s, event |-> e]
Init == \E mode \in {"dispatch","reconcile","page"}, f \in Faults:
 /\ (mode="reconcile" \/ f="none")
 /\ s=[mode |-> mode, fault |-> f, committed |-> mode="reconcile", live |-> TRUE, restarted |-> FALSE,
   dispatch |-> [t \in Targets |-> 0], queue |-> [t \in Targets |-> 0],
   effects |-> [t \in Targets |-> 0], held |-> TRUE,
   status |-> [t \in Targets |-> IF mode="reconcile" /\ t=2 THEN 2 ELSE 0], version |-> 0,
   observing |-> FALSE, snapshot |-> 0, observedTarget |-> 1,
   terminalSeen |-> FALSE, done |-> FALSE, success |-> FALSE,
   identity |-> Identity, page |-> FALSE, items |-> {1,2},
   frozen |-> {}, returned |-> {}, pageRead |-> FALSE,
   pageScope |-> 1, pageFilter |-> 1, pageSize |-> 1,
   suppliedScope |-> 1, suppliedFilter |-> 1, suppliedSize |-> 1,
   expired |-> FALSE, admitted |-> FALSE]
 /\ last=[before |-> s, event |-> "init"]
Commit == /\ ~s.committed /\ s.live /\ ~s.restarted
 /\ s'=[s EXCEPT !.committed=TRUE] /\ Save("commit")
Dispatch(t) == /\ s.committed /\ s.live /\ ~s.restarted
 /\ s.dispatch[t]=0
 /\ s'=[s EXCEPT !.dispatch[t]=@+1, !.queue[t]=2]
 /\ Save("dispatch")
\* Two transport deliveries may arrive across crash/release boundaries.
Effect(t) == /\ s.queue[t]>0
 /\ LET permitted == s.held /\ s.effects[t]=0
    IN s'=[s EXCEPT !.queue[t]=@-1,
        !.effects[t]=IF permitted THEN @+1 ELSE @]
 /\ Save("effect")
Release == /\ s.held /\ s'=[s EXCEPT !.held=FALSE] /\ Save("release")
Crash == /\ s.live /\ ~s.restarted
 /\ s'=[s EXCEPT !.live=FALSE, !.observing=FALSE] /\ Save("crash")
Restart == /\ ~s.live /\ ~s.restarted
 /\ s'=[s EXCEPT !.live=TRUE, !.restarted=TRUE] /\ Save("restart")
\* Read a whole-record version before a bounded backend observation.
Observe(t) == /\ s.committed /\ s.live /\ ~s.observing /\ ~s.done
 /\ s.status[t]#2
 /\ s'=[s EXCEPT !.observing=TRUE, !.snapshot=s.version,
                  !.observedTarget=t] /\ Save("observe")
\* Another reconcile can commit first, including an authoritative terminal.
Peer == /\ s.observing /\ s.version=s.snapshot /\ s.version<3
 /\ s'=[s EXCEPT !.version=@+1, !.status[s.observedTarget]=2,
                  !.terminalSeen=TRUE] /\ Save("peer")
CAS(result) == /\ s.observing /\ s.live /\ ~s.done
 /\ LET good == Envelope(s.fault)=s.identity
                   /\ s.fault#"started"
                   /\ (result#2 \/ s.fault \notin {"cleanup","truncated","exit"})
        accepted == good /\ s.version=s.snapshot
    IN s'=[s EXCEPT !.done=TRUE, !.observing=FALSE,
          !.status[s.observedTarget]=IF accepted THEN result ELSE @,
          !.version=IF accepted THEN @+1 ELSE @,
          !.admitted=accepted]
 /\ Save("cas")
Complete == /\ s.done /\ ~s.success
 /\ (\A t \in Targets: s.status[t]=2)
 /\ s'=[s EXCEPT !.success=TRUE] /\ Save("complete")
Snapshot == /\ ~s.page
 /\ s'=[s EXCEPT !.page=TRUE, !.frozen=s.items] /\ Save("snapshot")
Append == /\ s.page /\ 3 \notin s.items
 /\ s'=[s EXCEPT !.items=@ \cup {3}] /\ Save("append")
Expire == /\ s.page /\ ~s.expired
 /\ s'=[s EXCEPT !.expired=TRUE] /\ Save("expire")
ReadPage(a,f,z) == /\ s.page /\ ~s.pageRead
 /\ LET allowed == a=s.pageScope /\ f=s.pageFilter /\ z=s.pageSize
                    /\ ~s.expired
    IN s'=[s EXCEPT !.pageRead=TRUE, !.suppliedScope=a,
      !.suppliedFilter=f, !.suppliedSize=z,
      !.returned=IF allowed THEN s.frozen ELSE {}]
 /\ Save("page-read")
Next == (s.mode="dispatch" /\ (Commit \/ Release \/ Crash \/ Restart
             \/ (\E t \in Targets: Dispatch(t) \/ Effect(t))))
 \/ (s.mode="reconcile" /\ (Crash \/ Restart \/ Peer \/ Complete
             \/ (\E t \in Targets: Observe(t))
             \/ (\E result \in {0,1,2}: CAS(result))))
 \/ (s.mode="page" /\ (Snapshot \/ Append \/ Expire \/ Crash \/ Restart
             \/ (\E a,f,z \in {1,2}: ReadPage(a,f,z))))
TypeOK == /\ s.dispatch \in [Targets -> 0..2]
 /\ s.queue \in [Targets -> 0..2] /\ s.effects \in [Targets -> 0..2]
 /\ s.status \in [Targets -> 0..2] /\ s.version \in 0..4
 /\ s.items \subseteq {1,2,3} /\ s.frozen \subseteq {1,2,3}
IntentBeforeDispatch == (\E t \in Targets: s.dispatch[t]>0) => s.committed
NoRedispatch == \A t \in Targets: s.dispatch[t]<=1
RestartNoDispatch == last.event="dispatch" => ~last.before.restarted
CrashDurability == last.event \in {"crash","restart"} =>
 /\ s.committed=last.before.committed /\ s.status=last.before.status
 /\ s.identity=last.before.identity /\ s.frozen=last.before.frozen
BackendDeduplication == \A t \in Targets: s.effects[t]<=1
BackendLeaseFence == last.event="effect" /\ ~last.before.held =>
 s.effects=last.before.effects
ImmutableIdentity == s.identity=Identity
ExactObservation == last.event="cas" /\ s.admitted =>
 Envelope(s.fault)=s.identity /\ s.fault#"started"
CompleteEvidence == last.event="cas" /\ s.admitted
 /\ s.status[s.observedTarget]=2 =>
 s.fault \notin {"cleanup","truncated","exit"}
CASFence == last.event="cas" /\ s.admitted =>
 last.before.version=last.before.snapshot
TerminalImmutable == last.event="cas" /\ last.before.terminalSeen =>
 s.status=last.before.status
AllTargets == s.success => \A t \in Targets: s.status[t]=2
SnapshotImmutable == /\ (s.page => s.frozen={1,2})
 /\ (last.event="page-read" /\ s.returned#{} => s.returned=s.frozen)
ScopedPagination == last.event="page-read" /\ s.returned#{} =>
 /\ s.suppliedScope=s.pageScope /\ s.suppliedFilter=s.pageFilter
 /\ s.suppliedSize=s.pageSize /\ ~last.before.expired
=============================================================================
