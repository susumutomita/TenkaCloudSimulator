-------------------- MODULE DurableGuestLease --------------------
EXTENDS Naturals, FiniteSets
CONSTANT Ops
ASSUME Ops = {1, 2, 3}
VARIABLES s, last
vars == <<s, last>>
Faults == {"none", "owner", "region", "instance", "deployment", "profile",
           "record", "operation", "lease", "unknown", "alias", "ack",
           "obs-owner", "obs-fence", "obs-operation", "obs-action",
           "obs-requestlease", "obs-lease", "obs-state"}
Owner == [account |-> 1, region |-> 1, instance |-> 1,
          deployment |-> 1, profile |-> 1]
Request(op) == [owner |-> Owner, fence |-> 1, op |-> op, action |-> op,
                lease |-> IF op=1 THEN 0 ELSE 1, pending |-> TRUE]
Prior(op) == [Request(op) EXCEPT !.op=op-1, !.action=op-1, !.pending=FALSE]
BadOwner(f) == [Owner EXCEPT
 !.account=IF f="owner" THEN 2 ELSE @,
 !.region=IF f="region" THEN 2 ELSE @,
 !.instance=IF f="instance" THEN 2 ELSE @,
 !.deployment=IF f="deployment" THEN 2 ELSE @,
 !.profile=IF f="profile" THEN 2 ELSE @]
Expected(op,f) == [Request(op) EXCEPT !.owner=BadOwner(f),
 !.fence=IF f="record" THEN 2 ELSE @,
 !.op=IF f="operation" THEN 0 ELSE @,
 !.lease=IF f="lease" THEN 2 ELSE @]
Intent(r) == [fence |-> r.fence, op |-> r.op, action |-> r.action, lease |-> r.lease]
Envelope(op,f,state,mapping) ==
 [intent |-> [Intent(Request(op)) EXCEPT
    !.fence=IF f \in {"obs-owner","obs-fence"} THEN 2 ELSE @,
    !.op=IF f="obs-operation" THEN 0 ELSE @,
    !.action=IF f="obs-action" THEN 0 ELSE @,
    !.lease=IF f="obs-requestlease" THEN 2 ELSE @],
  lease |-> IF f="alias" THEN 2
            ELSE IF f="obs-lease" THEN (IF op=1 THEN 0 ELSE 2) ELSE mapping,
  state |-> IF f="obs-state" THEN 0 ELSE state]
Retained(records) == {k \in 1..3 : records[k]#0}
InitialRecords(op) == [k \in 1..3 |-> IF k=2 \/ (k=1 /\ op#1) THEN 1 ELSE 0]
Save(e) == last'=[before |-> s, event |-> e]
Init == \E op \in Ops, f \in Faults:
 /\ s=[op |-> op, fault |-> f, db |-> Prior(op), work |-> Request(op),
        expected |-> Expected(op,f), tx |-> TRUE, durable |-> FALSE,
        committed |-> FALSE, dispatches |-> 0, queue |-> 0,
        backend |-> op-1, high |-> op-1, effects |-> 0,
        payload |-> 0, mapping |-> IF op=1 THEN 0 ELSE 1,
        snapshot |-> Request(op), observed |-> 0, observing |-> FALSE,
        envelope |-> Envelope(op,"none",0,0),
        ackWork |-> Prior(op), ackTx |-> FALSE, ackDone |-> FALSE,
        advanced |-> FALSE, crashed |-> FALSE, reopened |-> FALSE,
        profileNow |-> 1, peer |-> 2, readDone |-> FALSE, readOK |-> FALSE,
        failed |-> FALSE, records |-> InitialRecords(op),
        otherTx |-> FALSE, otherDone |-> FALSE, otherTarget |-> 0,
        otherSnapshot |-> InitialRecords(op), otherAccepted |-> FALSE]
 /\ last=[before |-> s, event |-> "init"]

Commit == /\ s.tx /\ ~s.crashed
 /\ LET admitted == s.op#1 \/ (s.records[1]=0 /\ Cardinality(Retained(s.records))<2)
    IN s'=[s EXCEPT !.db=IF admitted THEN s.work ELSE @, !.tx=FALSE,
         !.durable=admitted, !.committed=admitted,
         !.records=IF admitted THEN [@ EXCEPT ![1]=1] ELSE @]
 /\ Save("commit")
Rollback == /\ s.tx /\ ~s.crashed
 /\ s'=[s EXCEPT !.tx=FALSE] /\ Save("rollback")
Dispatch == /\ s.durable /\ ~s.tx /\ ~s.crashed /\ s.dispatches=0
 /\ s'=[s EXCEPT !.dispatches=@+1, !.queue=2, !.payload=s.op,
                  !.high=s.op]
 /\ Save("dispatch")
\* Two delayed identical deliveries are not two registry dispatches.
Effect == /\ s.queue>0
 /\ LET fresh == s.op >= s.high
        duplicate == s.effects>0
    IN s'=[s EXCEPT !.queue=@-1,
      !.backend=IF fresh THEN s.op ELSE @,
      !.effects=IF fresh /\ ~duplicate THEN @+1 ELSE @,
      !.mapping=IF fresh /\ ~duplicate THEN 1 ELSE @]
 /\ Save("effect")
\* A conflicting payload at the same durable request key is rejected.
Conflict == /\ s.payload=s.op /\ s.queue>0
 /\ s'=s /\ Save("conflict")
Crash == /\ ~s.crashed /\ ~s.reopened
 /\ s'=[s EXCEPT !.crashed=TRUE, !.tx=FALSE, !.ackTx=FALSE,
                  !.observing=FALSE, !.otherTx=FALSE] /\ Save("crash")
Reopen == /\ s.crashed
 /\ s'=[s EXCEPT !.crashed=FALSE, !.reopened=TRUE] /\ Save("reopen")
ProfileRecreate == /\ s.profileNow=1 /\ ~s.tx
 /\ s'=[s EXCEPT !.profileNow=2] /\ Save("profile")
Read == /\ ~s.readDone /\ s.committed
 /\ s'=[s EXCEPT !.readDone=TRUE,
       !.readOK=(s.expected.owner=s.db.owner /\ s.expected.fence=s.db.fence
                  /\ s.expected.op=s.db.op /\ s.expected.lease=s.db.lease
                  /\ s.expected.pending=s.db.pending /\ s.expected.action=s.db.action)]
 /\ Save("read")
Observe == /\ s.committed /\ ~s.crashed /\ ~s.tx /\ ~s.observing
 /\ ~s.ackDone /\ ~s.advanced /\ s.expected=s.db
 /\ s'=[s EXCEPT !.observing=TRUE, !.snapshot=s.expected,
     !.observed=IF s.fault="unknown" THEN 0 ELSE s.backend,
     !.envelope=Envelope(s.op,s.fault,IF s.fault="unknown" THEN 0 ELSE s.backend,s.mapping)]
 /\ Save("observe")
PrepareACK == /\ s.observing /\ ~s.ackTx /\ ~s.ackDone /\ ~s.crashed
  /\ s.envelope.intent.fence=s.snapshot.fence
 /\ s.envelope.intent.op=s.snapshot.op
 /\ s.envelope.intent.action=s.snapshot.action
 /\ s.envelope.intent.lease=s.snapshot.lease
 /\ s.envelope.state=s.snapshot.op
 /\ s.envelope.lease#0
 /\ (s.snapshot.op=1 \/ s.envelope.lease=s.snapshot.lease)
 /\ s'=[s EXCEPT !.ackTx=TRUE,
      !.ackWork=[s.snapshot EXCEPT !.pending=FALSE,
         !.lease=s.envelope.lease]]
 /\ Save("prepare-ack")
\* Another reconciliation snapshot wins before this continuation's CAS.
\* Its following Advance commits a newer intent, then dispatches outside the Tx.
ConcurrentAdvance == /\ s.ackTx /\ ~s.advanced /\ s.op<3
 /\ s'=[s EXCEPT !.db=[s.snapshot EXCEPT !.op=@+1, !.action=@+1, !.lease=1],
        !.advanced=TRUE, !.high=s.op+1, !.backend=s.op+1]
 /\ Save("concurrent-advance")
ACK == /\ s.ackTx /\ ~s.crashed
 /\ LET exact == s.db=s.snapshot
        unique == s.ackWork.lease#s.peer
        final == s.fault#"ack"
    IN s'=[s EXCEPT !.ackTx=FALSE, !.ackDone=TRUE,
       !.failed=~(exact /\ unique /\ final),
       !.db=IF exact /\ unique /\ final THEN s.ackWork ELSE @]
 /\ Save("ack")
\* Competing inserts have independent snapshots but commit against current rows.
\* This atomic predicate represents serialized SQL writes, not a SQL proof.
OtherBegin(k) == /\ ~s.otherTx /\ ~s.otherDone /\ ~s.crashed
 /\ s'=[s EXCEPT !.otherTx=TRUE, !.otherTarget=k, !.otherSnapshot=s.records]
 /\ Save("other-begin")
OtherCommit == /\ s.otherTx /\ ~s.crashed
 /\ LET unused == s.records[s.otherTarget]=0
        room == Cardinality(Retained(s.records))<2
    IN s'=[s EXCEPT !.otherTx=FALSE, !.otherDone=TRUE,
      !.otherAccepted=unused /\ room,
      !.records=IF unused /\ room THEN [@ EXCEPT ![s.otherTarget]=2] ELSE @]
 /\ Save("other-commit")
Next == (\E k \in 1..3 : OtherBegin(k)) \/ OtherCommit \/ Commit \/ Rollback \/ Dispatch \/ Effect \/ Conflict \/ Crash \/ Reopen
        \/ ProfileRecreate \/ Read \/ Observe \/ PrepareACK \/ ConcurrentAdvance \/ ACK
        \/ UNCHANGED vars

TypeOK == /\ s.op \in Ops /\ s.backend \in 0..3 /\ s.high \in 0..3
          /\ s.db.op \in 0..3 /\ s.queue \in 0..2
IntentBeforeDispatch == last.event="dispatch"
 => /\ last.before.db=last.before.work
    /\ last.before.db.pending
    /\ last.before.records[1]=1
ExternalOutsideTransaction == last.event \in {"dispatch","observe"} => ~last.before.tx
NoRedispatch == s.dispatches<=1
PendingInvisible == last.event="rollback" => s.db=last.before.db
CrashDurability == last.event \in {"crash","reopen"}
 => s.db=last.before.db /\ s.records=last.before.records
\* Read evidence must refer to its capture, not a later mutable DB record.
ReadFence == last.event="read" /\ s.readOK => s.expected=last.before.db
CASFence == last.event="ack" /\ last.before.db#last.before.snapshot
            => s.db=last.before.db
FailedACKRetainsPending == last.event="ack" /\ last.before.fault="ack"
                           => s.db=last.before.db
RetainedLeaseUnique == s.committed => s.db.lease#s.peer
UnknownNeverRetires == last.event="prepare-ack" => last.before.observed#0
ObservationEvidence == last.event="ack" /\ ~s.failed
                       => s.db.op=last.before.observed
NoFalseStopped == s.committed /\ ~s.db.pending /\ s.db.op>=2
                   => s.backend>=s.db.op
BackendDuplicateSuppression == s.effects<=1
BackendMonotone == last.event="effect" => s.backend>=last.before.backend
BackendImmutableMapping == s.mapping \in {0,1}
BackendPayloadIdentity == last.event="conflict" => s.payload=last.before.payload
AuthoritativeObservation == last.event="observe"
 => s.observed=0 \/ s.observed=last.before.backend
RetainedBudget == Cardinality(Retained(s.records))<=2
NoReplacement == \A k \in 1..3 : last.before.records[k]#0
                  => s.records[k]=last.before.records[k]
TombstoneRetained == s.records[2]=1
AdmissionCurrentRows == last.event="other-commit" /\ s.otherAccepted
 => last.before.records[s.otherTarget]=0 /\ Cardinality(Retained(last.before.records))<2
ObservationEnvelopeExact == (last.event="prepare-ack" \/ (last.event="ack" /\ ~s.failed))
 => /\ last.before.envelope.intent=Intent(last.before.snapshot)
    /\ last.before.envelope.state=last.before.snapshot.op
    /\ last.before.envelope.lease#0
    /\ (last.before.snapshot.op=1 \/ last.before.envelope.lease=last.before.snapshot.lease)
    /\ s.db.owner=last.before.snapshot.owner
    /\ s.db.fence=last.before.snapshot.fence
CleanupSurvivesProfile == last.event="ack" /\ last.before.profileNow=2
 /\ last.before.db=last.before.snapshot /\ last.before.fault="none"
 => ~s.db.pending
=============================================================================
