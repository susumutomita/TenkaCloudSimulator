---------------- MODULE CodeStarBranchNotificationDelivery ----------------
EXTENDS Naturals, FiniteSets

\* One tracked branch, a preexisting source object for CreateBranch, two
\* target topics with two separate queue subscriptions each. An execution
\* covers one Git request and any number of eligible delivery attempts.
Targets == {"a", "b"}
GitModes == {"ok", "writerNoQueue", "iamDeny", "wrongCAS", "commitFault", "deadline",
             "foreignRegion", "foreignRepo", "disabled", "filtered", "oneTarget",
             "noTargets", "noRoom", "oneSlot", "oneSlotOneTarget", "fullWithoutMatch", "staleQueueBinding", "repairedSubscription"}
WorkerModes == {"ok", "topicDeny", "ownerDenyFirst", "ownerDenySecond", "writerOnly", "splitRevisions",
                "ruleChanged", "ruleDisabled", "repoRecreated", "topicRecreated",
                "ownerRecreated", "queue1Recreated", "queue2Recreated", "subRecreated",
                "noRecipients", "readFault", "afterFirstQueue", "receiptFailure", "commitFailure", "deadline"}
VARIABLES phase, old, ref, wroteObjects, envelope, pending, delayed, receipts,
          sns, first, second, last
vars == <<phase, old, ref, wroteObjects, envelope, pending, delayed, receipts,
          sns, first, second, last>>
Zero == [t \in Targets |-> 0]
BaselineLife(t) ==
 [repo |-> 1, rule |-> 1, owner |-> 1, topic |-> t, topicGen |-> 1,
  sub1 |-> <<t,1>>, sub2 |-> <<t,2>>, queue1 |-> 1, queue2 |-> 1]
CaptureRow(t,m) == IF m = "repairedSubscription"
                   THEN [BaselineLife(t) EXCEPT !.sub1 = <<t,3>>, !.queue1 = 2]
                   ELSE BaselineLife(t)
CapturedLife(m) == [t \in Targets |-> CaptureRow(t,m)]
Current(t,m) == [envelope.life[t] EXCEPT
 !.repo = IF m = "repoRecreated" THEN @+1 ELSE @,
 !.rule = IF m = "ruleChanged" THEN @+1 ELSE @,
 !.owner = IF m = "ownerRecreated" THEN @+1 ELSE @,
 !.topicGen = IF m = "topicRecreated" THEN @+1 ELSE @,
 !.sub1 = IF m = "subRecreated" THEN <<t,4>> ELSE @,
 !.queue1 = IF m = "queue1Recreated" THEN @+1 ELSE @,
 !.queue2 = IF m = "queue2Recreated" THEN @+1 ELSE @]
OriginalQueue(m) == IF m = "repairedSubscription" THEN 2 ELSE 1
QueueAtCapture(m) == IF m \in {"staleQueueBinding", "repairedSubscription"} THEN 2 ELSE 1
ValidBinding(m) == OriginalQueue(m) = QueueAtCapture(m)
Empty == [kind |-> "none", previous |-> 0, commit |-> 0, validTopology |-> FALSE, life |-> CapturedLife("ok")]
Pub == [pending |-> pending, delay |-> delayed, receipt |-> receipts,
        audit |-> sns, q1 |-> first, q2 |-> second]
Durable == [ref |-> ref, objects |-> wroteObjects, event |-> envelope, publication |-> Pub]
Notice(op, mode, target, result) ==
 [op |-> op, mode |-> mode, target |-> target, result |-> result, before |-> Durable]
Init ==
 /\ old \in {0, 1} /\ ref = old /\ wroteObjects = FALSE /\ envelope = Empty
 /\ phase = "git" /\ pending = {} /\ delayed = {} /\ receipts = {}
 /\ sns = Zero /\ first = Zero /\ second = Zero /\ last = Notice("init", "ok", "", "none")

Origin(op) == op \in {"PutFile", "CreateBranch"}
GitAllowed(m) == m \notin {"iamDeny", "wrongCAS", "commitFault", "deadline"}
CAS(op) == op # "CreateBranch" \/ old = 0
Source(m) == [region |-> IF m = "foreignRegion" THEN "west" ELSE "east",
              arn |-> "same-name", uuid |-> IF m = "foreignRepo" THEN 2 ELSE 1]
CurrentRule(m) == /\ Source(m).region = "east" /\ Source(m).uuid = 1
                  /\ m \notin {"disabled", "filtered", "noTargets", "fullWithoutMatch"}
Matched(m) == IF ~CurrentRule(m) THEN {} ELSE IF m \in {"oneTarget", "oneSlotOneTarget"} THEN {"a"} ELSE Targets
Slots(m) == CASE m \in {"noRoom", "fullWithoutMatch"} -> 0 [] m \in {"oneSlot", "oneSlotOneTarget"} -> 1 [] OTHER -> 2
Kind == IF old = 0 THEN "referenceCreated" ELSE "referenceUpdated"
Event(m) == [kind |-> Kind, previous |-> old, commit |-> 2, validTopology |-> ValidBinding(m), life |-> CapturedLife(m)]
Git(op, m) ==
 /\ phase = "git"
 /\ LET targets == Matched(m)
        ok == Origin(op) /\ CAS(op) /\ GitAllowed(m) /\ Cardinality(targets) <= Slots(m)
    IN /\ ref' = IF ok THEN 2 ELSE ref
       /\ wroteObjects' = (ok /\ op = "PutFile")
       /\ pending' = IF ok THEN targets ELSE pending
       /\ envelope' = IF ok /\ targets # {} THEN Event(m) ELSE envelope
       /\ last' = Notice(op, m, "", IF ok THEN "committed" ELSE "refused")
 /\ phase' = "worker"
 /\ UNCHANGED <<old, delayed, receipts, sns, first, second>>

\* A changed durable identity or topology is terminal for the captured intent.
\* A policy deny within the same generation is temporary.
RuleCurrent(t,m) == /\ m # "ruleDisabled"
                    /\ envelope.life[t].rule = Current(t,m).rule
                    /\ envelope.life[t].repo = Current(t,m).repo
TopicCurrent(t,m) == envelope.life[t].topicGen = Current(t,m).topicGen
OwnerCurrent(t,m) == envelope.life[t].owner = Current(t,m).owner
TopologyCurrent(t,m) ==
 /\ envelope.validTopology /\ m # "noRecipients"
 /\ envelope.life[t].sub1 = Current(t,m).sub1 /\ envelope.life[t].sub2 = Current(t,m).sub2
 /\ envelope.life[t].queue1 = Current(t,m).queue1 /\ envelope.life[t].queue2 = Current(t,m).queue2
ServiceAllows(m) == m # "topicDeny"
OwnerSnapshot(m) ==
 CASE m \in {"ownerDenyFirst", "splitRevisions"} -> {2}
 [] m = "ownerDenySecond" -> {1} [] m = "writerOnly" -> {} [] OTHER -> {1,2}
OwnerAllows(m) == {1,2} \subseteq OwnerSnapshot(m)
CommitWorker(m) == m \notin {"readFault", "receiptFailure", "commitFailure", "deadline"}
Verdict(t,m) ==
 CASE ~RuleCurrent(t,m) \/ ~TopicCurrent(t,m) \/ ~OwnerCurrent(t,m) \/ ~TopologyCurrent(t,m) -> "cancel"
 [] ~ServiceAllows(m) \/ ~OwnerAllows(m) \/ m = "afterFirstQueue" -> "defer"
 [] ~CommitWorker(m) -> "rollback"
 [] OTHER -> "deliver"

Bump(count, t, yes) == IF yes THEN [count EXCEPT ![t] = @ + 1] ELSE count
Attempt(t, m) ==
 /\ phase = "worker" /\ t \in pending \ delayed
 /\ LET result == Verdict(t,m)
        deliver == result = "deliver"
        remove == deliver \/ result = "cancel"
    IN /\ pending' = IF remove THEN pending \ {t} ELSE pending
       /\ delayed' = IF result = "defer" THEN delayed \cup {t} ELSE delayed
       /\ receipts' = IF deliver THEN receipts \cup {t} ELSE receipts
       /\ sns' = Bump(sns, t, deliver)
       /\ first' = Bump(first, t, deliver)
       /\ second' = Bump(second, t, deliver)
       /\ last' = Notice("attempt", m, t, result)
 /\ UNCHANGED <<phase, old, ref, wroteObjects, envelope>>

Tick(t) ==
 /\ t \in delayed /\ delayed' = delayed \ {t}
 /\ last' = Notice("tick", "ok", t, "ready")
 /\ UNCHANGED <<phase, old, ref, wroteObjects, envelope, pending, receipts, sns, first, second>>
Restart ==
 /\ phase = "worker"
 /\ pending' = pending /\ receipts' = receipts /\ delayed' = delayed
 /\ last' = Notice("restart", "ok", "", "reopened")
 /\ UNCHANGED <<phase, old, ref, wroteObjects, envelope, sns, first, second>>
\* Rule changes remove this rule's unpublished intents in their own transaction.
\* A prior native publication remains observable even after rule deletion.
ChangeRule(mode) ==
 /\ phase = "worker"
 /\ pending' = IF mode = "changeFailed" THEN pending ELSE {}
 /\ delayed' = IF mode = "changeFailed" THEN delayed ELSE {}
 /\ last' = Notice("changeRule", mode, "", IF mode = "changeFailed" THEN "refused" ELSE "changed")
 /\ UNCHANGED <<phase, old, ref, wroteObjects, envelope, receipts, sns, first, second>>
Next == (\E op \in {"PutFile", "CreateBranch", "untrusted"}, m \in GitModes : Git(op, m))
        \/ (\E t \in Targets, m \in WorkerModes : Attempt(t, m))
        \/ (\E t \in Targets : Tick(t)) \/ Restart
        \/ (\E m \in {"update", "disable", "delete", "changeFailed"} : ChangeRule(m))

TypeOK ==
 /\ old \in {0,1} /\ ref \in 0..2 /\ wroteObjects \in BOOLEAN
 /\ pending \subseteq Targets /\ delayed \subseteq pending /\ receipts \subseteq Targets
 /\ sns \in [Targets -> 0..2] /\ first \in [Targets -> 0..2] /\ second \in [Targets -> 0..2]
GitRequest == last.op \in {"PutFile", "CreateBranch", "untrusted"}
ExpectedTargets ==
 CASE last.mode \in {"foreignRegion", "foreignRepo", "disabled", "filtered", "noTargets", "fullWithoutMatch"} -> {}
 [] last.mode \in {"oneTarget", "oneSlotOneTarget"} -> {"a"} [] OTHER -> Targets
ExpectedGit ==
 /\ last.op \in {"PutFile", "CreateBranch"} /\ (last.op # "CreateBranch" \/ old = 0)
 /\ last.mode \notin {"iamDeny", "wrongCAS", "commitFault", "deadline"}
 /\ Cardinality(ExpectedTargets) <= (CASE last.mode \in {"oneSlot", "oneSlotOneTarget"} -> 1 [] last.mode \in {"noRoom", "fullWithoutMatch"} -> 0 [] OTHER -> 2)
GitAdmission == ~GitRequest \/ (last.result = "committed") = ExpectedGit
GitAtomicFailure == ~GitRequest \/ ExpectedGit \/ Durable = last.before
GitCommitExact == ~GitRequest \/ ~ExpectedGit \/
                  (ref = 2 /\ wroteObjects = (last.op = "PutFile") /\ pending = ExpectedTargets)
EventExact == ~GitRequest \/ ~ExpectedGit \/
 (IF ExpectedTargets = {} THEN envelope = Empty
  ELSE envelope = [kind |-> IF old = 0 THEN "referenceCreated" ELSE "referenceUpdated",
                   previous |-> old, commit |-> 2, validTopology |-> last.mode # "staleQueueBinding", life |-> CapturedLife(last.mode)])
ExpectedResult ==
 CASE ~envelope.validTopology \/ last.mode \in {"ruleChanged", "ruleDisabled", "repoRecreated", "topicRecreated",
      "ownerRecreated", "queue1Recreated", "queue2Recreated", "subRecreated", "noRecipients"} -> "cancel"
 [] last.mode \in {"topicDeny", "ownerDenyFirst", "ownerDenySecond", "writerOnly", "splitRevisions", "afterFirstQueue"} -> "defer"
 [] last.mode \in {"readFault", "receiptFailure", "commitFailure", "deadline"} -> "rollback"
 [] OTHER -> "deliver"
WorkerDecision == last.op # "attempt" \/ last.result = ExpectedResult
PublicationAtomic == \A t \in Targets :
 sns[t] = first[t] /\ first[t] = second[t] /\ (t \in receipts) = (sns[t] = 1)
FailureHasNoPublication == last.op # "attempt" \/ last.result = "deliver" \/
  (sns = last.before.publication.audit /\ first = last.before.publication.q1 /\
   second = last.before.publication.q2 /\ receipts = last.before.publication.receipt)
AtMostOnce == \A t \in Targets : sns[t] <= 1
TargetRetirement == last.op # "attempt" \/
 (CASE last.result = "deliver" -> last.target \notin pending /\ last.target \in receipts
  [] last.result = "cancel" -> last.target \notin pending /\ last.target \notin receipts
  [] last.result = "defer" -> last.target \in pending /\ last.target \in delayed
  [] OTHER -> Pub = last.before.publication)
PeerAndGitFrame == last.op # "attempt" \/
 /\ ref = last.before.ref /\ wroteObjects = last.before.objects /\ envelope = last.before.event
 /\ \A t \in Targets \ {last.target} :
    /\ (t \in pending) = (t \in last.before.publication.pending)
    /\ (t \in delayed) = (t \in last.before.publication.delay)
    /\ (t \in receipts) = (t \in last.before.publication.receipt)
    /\ sns[t] = last.before.publication.audit[t]
RetryEligibility == last.op # "attempt" \/
                    (last.target \in last.before.publication.pending /\ last.target \notin last.before.publication.delay)
RuleChangeAtomic == last.op # "changeRule" \/
  (IF last.mode = "changeFailed" THEN Durable = last.before
   ELSE /\ pending = {} /\ delayed = {}
        /\ Durable = [last.before EXCEPT !.publication.pending = {}, !.publication.delay = {}])
RestartExact == last.op # "restart" \/ Durable = last.before
=============================================================================
