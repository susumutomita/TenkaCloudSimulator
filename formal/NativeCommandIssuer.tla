---------------- MODULE NativeCommandIssuer ----------------
EXTENDS Naturals, FiniteSets
VARIABLES s, last
vars == <<s,last>>
Save(e) == last'=[event |-> e, before |-> s]
Init == \E m \in {"backend","admission","page"}:
 /\ s=[mode |-> m, phase |-> "initial", parentIntent |-> FALSE,
    reserved |-> FALSE, readback |-> FALSE, worker |-> FALSE, effects |-> 0,
    live |-> TRUE, restarted |-> FALSE, sealed |-> FALSE, lease |-> 1,
    reply |-> "none", observed |-> "Unknown", joined |-> FALSE,
    revision |-> 1, checkedRevision |-> 0, doc |-> FALSE, target |-> FALSE,
    accepted |-> FALSE, page |-> FALSE, frozen |-> {1,2}, items |-> {1,2},
    pageRole |-> 1, pageRegion |-> 1, pageFilter |-> 1, pageSize |-> 1,
    role |-> 1, region |-> 1, filter |-> 1, size |-> 1, expired |-> FALSE,
    unchangedRequest |-> TRUE, returned |-> {}, read |-> FALSE]
 /\ last=[event |-> "init",before |-> s]
ParentCommit == /\ s.phase="initial" /\ s.live
 /\ s'=[s EXCEPT !.parentIntent=TRUE, !.phase="reserve"] /\ Save("parent")
Reserve(ok) == /\ s.phase="reserve" /\ s.live
 /\ s'=[s EXCEPT !.reserved=ok, !.readback=ok, !.phase=IF ok THEN "worker" ELSE "stopped"] /\ Save("reserve")
StartWorker == /\ s.phase="worker" /\ s.live /\ ~s.restarted
 /\ s.reserved /\ s.readback /\ ~s.worker
 /\ s'=[s EXCEPT !.worker=TRUE, !.phase="submit"] /\ Save("worker")
Duplicate == /\ s.reserved /\ s.live /\ s.phase="submit"
 /\ s'=[s EXCEPT !.phase="submit"] /\ Save("duplicate")
Submit == /\ s.phase="submit" /\ s.live /\ ~s.restarted
 /\ s.parentIntent /\ s.reserved /\ s.readback /\ s.worker
 /\ ~s.sealed /\ s.lease=1 /\ s.effects=0
 /\ s'=[s EXCEPT !.effects=@+1, !.phase="reply"] /\ Save("effect")
Seal == /\ ~s.sealed /\ s'=[s EXCEPT !.sealed=TRUE] /\ Save("seal")
ReplaceLease == /\ s.lease=1 /\ s'=[s EXCEPT !.lease=2] /\ Save("lease")
Crash == /\ s.live /\ ~s.restarted
 /\ s'=[s EXCEPT !.live=FALSE, !.worker=FALSE] /\ Save("crash")
Restart == /\ ~s.live /\ ~s.restarted
 /\ s'=[s EXCEPT !.live=TRUE, !.restarted=TRUE, !.phase="worker"] /\ Save("restart")
Receive(r) == /\ s.phase="reply" /\ s.live /\ ~s.restarted
 /\ s'=[s EXCEPT !.reply=r, !.phase="observe"] /\ Save("reply")
Publish == /\ s.phase="observe" /\ s.live /\ ~s.restarted
 /\ s'=[s EXCEPT !.observed=IF s.reply="exit0" THEN "Success" ELSE "Unknown", !.phase="join"] /\ Save("publish")
Join == /\ s.phase="join" /\ s.live
 /\ s'=[s EXCEPT !.joined=TRUE, !.phase="done"] /\ Save("join")
(* Two revisions each permit only one resource. A third permits both.
 A final single immutable snapshot must be used, not accumulated checks. *)
Precheck == /\ s.phase="initial"
 /\ s'=[s EXCEPT !.doc=TRUE, !.phase="resolve"] /\ Save("precheck")
ChangePolicy(r) == /\ s.mode="admission" /\ s.phase="resolve" /\ r \in {2,3}
 /\ s.revision=1 /\ s'=[s EXCEPT !.revision=r] /\ Save("policy")
FinalCheck == /\ s.phase="resolve"
 /\ s'=[s EXCEPT !.doc=s.revision \in {1,3}, !.target=s.revision \in {2,3},
    !.checkedRevision=s.revision, !.phase="authorize"] /\ Save("snapshot")
Admit == /\ s.phase="authorize"
 /\ s'=[s EXCEPT !.accepted=s.doc /\ s.target, !.phase="done"] /\ Save("admit")
Snapshot == /\ ~s.page /\ s'=[s EXCEPT !.page=TRUE] /\ Save("snapshot-page")
ChangePage(kind) == /\ s.page /\ ~s.read /\ s.phase="initial"
 /\ s'=[s EXCEPT !.phase="changed", !.items=IF kind="append" THEN {1,2,3} ELSE @,
    !.expired=kind="expiry", !.unchangedRequest=kind#"record",
    !.role=IF kind="role" THEN 2 ELSE @, !.region=IF kind="region" THEN 2 ELSE @,
    !.filter=IF kind="filter" THEN 2 ELSE @, !.size=IF kind="size" THEN 2 ELSE @] /\ Save("page-change")
PageRead == /\ s.page /\ ~s.read
 /\ LET ok == s.role=s.pageRole /\ s.region=s.pageRegion /\ s.filter=s.pageFilter /\ s.size=s.pageSize /\ ~s.expired /\ s.unchangedRequest
    IN s'=[s EXCEPT !.read=TRUE, !.returned=IF ok THEN s.frozen ELSE {}] /\ Save("read")
Next == (s.mode="backend" /\ (ParentCommit \/ (\E ok \in BOOLEAN: Reserve(ok)) \/ StartWorker \/ Duplicate \/ Submit \/ Seal \/ ReplaceLease \/ Crash \/ Restart \/ (\E r \in {"exit0","lost","nonterminal"}: Receive(r)) \/ Publish \/ Join))
 \/ (s.mode="admission" /\ (Precheck \/ (\E r \in {2,3}: ChangePolicy(r)) \/ FinalCheck \/ Admit))
 \/ (s.mode="page" /\ (Snapshot \/ (\E k \in {"append","expiry","record","role","region","filter","size"}: ChangePage(k)) \/ PageRead))
TypeOK == /\ s.mode \in {"backend","admission","page"} /\ s.effects \in 0..2
 /\ s.lease \in {1,2} /\ s.revision \in {1,2,3} /\ s.checkedRevision \in 0..3
 /\ s.observed \in {"Unknown","Success"} /\ s.returned \subseteq {1,2,3}
ReservationBeforeEffect == last.event="effect" => last.before.parentIntent /\ last.before.reserved /\ last.before.readback
AtMostOneEffect == s.effects<=1
CurrentOwnerFence == last.event="effect" => ~last.before.sealed /\ last.before.lease=1
RestartNoWorker == last.event="worker" => ~last.before.restarted
WorkerRequiresReservation == last.event="worker" => last.before.reserved /\ last.before.readback
NoReplayAfterRestart == last.event="effect" => ~last.before.restarted
KnownExitOnly == s.observed="Success" => s.reply="exit0" /\ s.effects=1
NoCleanupFromLostReply == s.reply="lost" => s.observed="Unknown"
JoinAfterPublication == last.event="join" => last.before.phase="join" /\ last.before.live /\ last.before.worker
SinglePolicySnapshot == s.accepted => s.checkedRevision=3
PageScope == s.returned#{} => s.role=1 /\ s.region=1 /\ s.filter=1 /\ s.size=1 /\ ~s.expired
PageRecordRevalidated == s.returned#{} => s.unchangedRequest
PageSnapshotImmutable == s.returned#{} => s.returned={1,2}
Spec == Init /\ [][Next]_vars
=============================================================================
