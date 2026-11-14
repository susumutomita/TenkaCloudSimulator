-------------------- MODULE MacProbeLifecycle --------------------
EXTENDS Naturals
\* Finite control-flow projection. Predicate inputs stand for the concrete
\* validators/OS calls, NOT proofs of their implementations. launchd's PID
\* absence is deliberately not a wait/reap witness.
VARIABLE s
Init == \E mode \in {"probe", "builder", "launchd"}:
 s = [mode |-> mode, phase |-> "new", attempted |-> FALSE,
      validated |-> FALSE, marker |-> FALSE, launched |-> FALSE,
      waited |-> FALSE, observed |-> FALSE, failed |-> FALSE,
      uncertain |-> FALSE, group |-> FALSE, absent |-> FALSE,
      noRestart |-> FALSE, serviceAbsent |-> FALSE,
      logSaved |-> FALSE, receipt |-> FALSE, passed |-> FALSE,
      reapedClaim |-> FALSE, battleReady |-> FALSE]
Prepare(valid) == /\ s.phase="new" /\ s.mode#"launchd"
 /\ s'=[s EXCEPT !.validated=valid,
        !.phase=IF valid THEN "prepared" ELSE "done", !.failed=~valid]
Mark(ok) == /\ s.mode="builder" /\ s.phase="prepared"
 /\ s'=[s EXCEPT !.marker=ok,
        !.phase=IF ok THEN "marked" ELSE "done", !.failed=~ok]
\* Builder marker precedes every subprocess; ISO errors are a failed attempt.
BuildISO(ok) == /\ s.mode="builder" /\ s.phase="marked"
 /\ s'=[s EXCEPT !.phase=IF ok THEN "spawn" ELSE "log", !.failed=~ok]
\* Validation and exclusive marker creation gate entry by control-flow phase;
\* Popen does not re-check their Boolean evidence a second time.
Spawn(ok) == /\ s.mode#"launchd"
 /\ (s.phase="spawn" \/ (s.mode="probe" /\ s.phase="prepared"))
 /\ s'=[s EXCEPT !.launched=ok,
       !.phase=IF ok THEN "running" ELSE IF s.mode="builder" THEN "log" ELSE "done",
       !.failed=~ok]
\* Successful communicate/wait returns a wait witness; EOF alone does not.
Wait(ok) == /\ s.phase="running" /\ s.mode#"launchd"
 /\ s'=[s EXCEPT !.waited=ok, !.failed=~ok,
        !.phase=IF ok THEN "observe" ELSE "stop"]
Observe(ok) == /\ s.phase="observe"
 /\ s'=[s EXCEPT !.observed=ok, !.failed=~ok,
        !.phase=IF ok THEN "close" ELSE "stop"]
\* Error path kill+wait can itself fail. No later receipt certifies uncertainty.
Stop(ok) == /\ s.phase="stop" /\ s.launched
 /\ s'=[s EXCEPT !.waited=@ \/ ok, !.uncertain=~ok, !.phase="close"]
Close(ok) == /\ s.phase="close"
 /\ s'=[s EXCEPT !.failed=@ \/ ~ok,
        !.phase=IF s.mode="builder" THEN "log" ELSE "publish"]
SaveLog(ok) == /\ s.mode="builder" /\ s.phase="log"
 /\ s'=[s EXCEPT !.logSaved=ok, !.failed=@ \/ ~ok, !.phase="publish"]
PublishOwned(ok) == /\ s.mode#"launchd" /\ s.phase="publish"
 /\ LET accepted == ~s.failed /\ ~s.uncertain /\ s.waited /\ s.observed
                 /\ (s.mode#"builder" \/ s.logSaved) /\ ok
    IN s'=[s EXCEPT !.receipt=accepted, !.passed=accepted,
           !.reapedClaim=accepted, !.phase="done"]
\* Any bootstrap failure may still have installed the fresh exact label;
\* cleanup is inspected after attempted registration even on failure.
Register(ok) == /\ s.mode="launchd" /\ s.phase="new"
 /\ s'=[s EXCEPT !.attempted=TRUE, !.failed=~ok,
        !.phase=IF ok THEN "kick" ELSE "cleanup"]
Kick(ok) == /\ s.phase="kick"
 /\ s'=[s EXCEPT !.launched=ok, !.failed=~ok,
        !.phase=IF ok THEN "group" ELSE "cleanup"]
Group(ok) == /\ s.phase="group"
 /\ s'=[s EXCEPT !.group=ok, !.failed=~ok,
        !.phase=IF ok THEN "kill" ELSE "cleanup"]
Kill(ok) == /\ s.phase="kill"
 /\ s'=[s EXCEPT !.failed=~ok,
        !.phase=IF ok THEN "absence" ELSE "cleanup"]
Absent(ok) == /\ s.phase="absence"
 /\ s'=[s EXCEPT !.absent=ok, !.failed=~ok,
        !.phase=IF ok THEN "restart" ELSE "cleanup"]
RestartCheck(ok) == /\ s.phase="restart"
 /\ s'=[s EXCEPT !.noRestart=ok, !.failed=~ok, !.phase="cleanup"]
\* ok means successful exact-label print, optional bootout, then recognized
\* absence. Generic nonzero exit, timeout, and failed bootout are not absence.
Cleanup(ok) == /\ s.phase="cleanup" /\ s.attempted
 /\ s'=[s EXCEPT !.serviceAbsent=ok, !.failed=@ \/ ~ok, !.phase="record"]
Record(ok) == /\ s.phase="record"
 /\ LET accepted == ~s.failed /\ s.serviceAbsent
    IN s'=[s EXCEPT !.receipt=ok, !.passed=ok /\ accepted, !.phase="done"]
Next == \E ok \in BOOLEAN:
 Prepare(ok) \/ Mark(ok) \/ BuildISO(ok) \/ Spawn(ok) \/ Wait(ok)
 \/ Observe(ok) \/ Stop(ok) \/ Close(ok) \/ SaveLog(ok) \/ PublishOwned(ok)
 \/ Register(ok) \/ Kick(ok) \/ Group(ok) \/ Kill(ok) \/ Absent(ok)
 \/ RestartCheck(ok) \/ Cleanup(ok) \/ Record(ok)
TypeOK == /\ s.mode \in {"probe","builder","launchd"}
 /\ s.phase \in {"new","prepared","marked","spawn","running","observe",
 "stop","close","log","publish","done","kick","group","kill","absence",
 "restart","cleanup","record"}
 /\ \A k \in DOMAIN s \ {"mode","phase"}: s[k] \in BOOLEAN
BuilderAttemptBeforeLaunch == s.mode="builder" /\ s.launched => s.marker
OwnedSuccessEvidence == s.mode#"launchd" /\ s.passed => s.launched /\ s.waited /\ s.observed
FailureCannotPass == s.failed \/ s.uncertain => ~s.passed
BuildReceiptAfterLog == s.mode="builder" /\ s.receipt => s.logSaved
LaunchdSuccessEvidence == s.mode="launchd" /\ s.passed =>
 s.attempted /\ s.group /\ s.absent /\ s.noRestart /\ s.serviceAbsent
AbsenceIsNotReap == s.mode="launchd" => ~s.waited /\ ~s.reapedClaim
ReapClaimEvidence == s.reapedClaim => s.launched /\ s.waited
NoBattleQualification == ~s.battleReady
=============================================================================
