----------------------- MODULE SNSConsoleResponseAdmission -----------------------
EXTENDS Naturals, FiniteSets

\* SNS console only; source tree 0344b741d87c940b110a74dc8f3bcd0bca6c7e80.
\* "instance" names the React state owner (not a JS counter); "topicRev"
\* is an observation label for a valid loadTopic/refetch. Neither is a
\* runtime callback guard. Single flight and React owner isolation matter.
\* Observe view-local data/errors, not the shared 401 navigation side effect.
\* The dedicated Scalar page is not this identity or region selection.
CONSTANTS East, West, Reader, OtherReader, TopicA, TopicB, Absent, MoveLimit, RequestLimit
Regions == {East, West}
Principals == {Reader, OtherReader}
Topics == {TopicA, TopicB}
Required == {"ListTopics", "ListSubscriptionsByTopic", "Subscribe", "Unsubscribe"}
Actions == Required \cup {"GetSubscriptionAttributes"}
Handoffs == {"none", "parameter", "instance"}
CapInputs == [ready : BOOLEAN, console : BOOLEAN, array : BOOLEAN,
              handoff : Handoffs, operations : SUBSET Actions]
Contexts == [region : Regions, principal : Principals, navigation : 0..MoveLimit]
Tickets == 1..RequestLimit
Calls == [ticket : Tickets, owner : 0..MoveLimit, scope : Contexts,
          topic : Topics \cup {Absent}, rev : 0..RequestLimit, kind : Required]
Phases == {"loading", "masked", "sns", "left"}

VARIABLES cap, v
vars == <<cap, v>>

SNSAvailable(c) ==
  /\ c.ready /\ c.handoff = "none"
  /\ c.console /\ c.array /\ Required \subseteq c.operations

\* main.jsx key={region+'|'+principal+'|'+navigationVersion}.
Key(s) == <<s.region, s.principal, s.navigation>>
ScopesAfter(s) ==
  { [s EXCEPT !.region = r] : r \in Regions \ {s.region} } \cup
  { [s EXCEPT !.principal = p] : p \in Principals \ {s.principal} } \cup
  (IF s.navigation < MoveLimit THEN {[s EXCEPT !.navigation = @ + 1]} ELSE {})

Init == /\ cap \in CapInputs
        /\ v = [phase |-> "loading",
                 scope |-> [region |-> East, principal |-> Reader, navigation |-> 0],
                 instance |-> 0, birth |-> Absent, moves |-> 0,
                 topic |-> Absent, topicRev |-> 0,
                 issued |-> 0, active |-> Absent, waiting |-> {}, aborted |-> {},
                 rows |-> Absent, topics |-> Absent, settled |-> Absent]

Enter == /\ v.phase = "loading"
         /\ v' = IF SNSAvailable(cap)
                    THEN [v EXCEPT !.phase = "sns", !.birth = v.scope]
                    ELSE [v EXCEPT !.phase = "masked"]
         /\ UNCHANGED cap

\* New instance means different React hook state even when a key recurs
\* after returning to a region/principal. Old promises can remain pending
\* until passive effect cleanup; their setters still belong to the old instance.
MountAt(scope) ==
  [v EXCEPT !.phase = "sns", !.scope = scope, !.birth = scope,
     !.instance = @ + 1, !.moves = @ + 1, !.active = Absent,
     !.topic = Absent, !.topicRev = 0, !.rows = Absent, !.topics = Absent]

Switch(scope) ==
  /\ v.phase = "sns" /\ v.moves < MoveLimit /\ scope \in ScopesAfter(v.scope)
  /\ v' = IF Key(scope) # Key(v.scope) THEN MountAt(scope)
              ELSE [v EXCEPT !.scope = scope, !.moves = @ + 1]
  /\ UNCHANGED cap

Away == /\ v.phase = "sns" /\ v.moves < MoveLimit
        /\ v' = [v EXCEPT !.phase = "left", !.birth = Absent, !.moves = @ + 1,
                   !.scope.navigation = @ + 1, !.active = Absent,
                   !.topic = Absent, !.topicRev = 0, !.rows = Absent, !.topics = Absent]
        /\ UNCHANGED cap

Return == /\ v.phase = "left" /\ v.moves < MoveLimit
          /\ SNSAvailable(cap)
          /\ v' = MountAt([v.scope EXCEPT !.navigation = @ + 1])
          /\ UNCHANGED cap

\* SNSView run rejects when there is an active.current, and assigns the new
\* operation synchronously. loadTopic selects an already well-formed ARN and
\* clears its rows; refresh can select that same topic again. Mutating kinds
\* keep the same operation through their read-back; native state is abstract.
MayStart == v.phase = "sns" /\ v.active = Absent /\ v.issued < RequestLimit
Issue(kind, topicArg) ==
  /\ MayStart
  /\ LET pick == kind = "ListSubscriptionsByTopic"
         inTopic == kind # "ListTopics"
         ticket == v.issued + 1
         newTopic == IF pick THEN topicArg ELSE v.topic
         generation == IF pick THEN v.topicRev + 1 ELSE v.topicRev
         op == [ticket |-> ticket, owner |-> v.instance, scope |-> v.scope,
                topic |-> IF inTopic THEN newTopic ELSE Absent,
                rev |-> IF inTopic THEN generation ELSE 0, kind |-> kind]
     IN /\ (inTopic => newTopic \in Topics)
        /\ v' = [v EXCEPT !.issued = ticket, !.active = ticket,
                 !.waiting = @ \cup {op}, !.topic = newTopic, !.topicRev = generation,
                 !.rows = IF inTopic THEN Absent ELSE v.rows]
  /\ UNCHANGED cap

CurrentOwner(op) == v.phase = "sns" /\ op.owner = v.instance
Aborted(op) == op.ticket \in v.aborted

Cleanup(op) ==
  /\ op \in v.waiting /\ ~CurrentOwner(op) /\ ~Aborted(op)
  /\ v' = [v EXCEPT !.aborted = @ \cup {op.ticket}]
  /\ UNCHANGED cap

\* The setters target their React state owner. Successful data/readback
\* sites explicitly check the signal; outer catch/finally also check the
\* instance's active operation. Do not assume a separate generation guard.
UnabortedCallback(op) == ~Aborted(op)
ActiveCallback(op) == UnabortedCallback(op) /\ op.ticket = v.active
TargetsVisibleView(op) == CurrentOwner(op)
Apply(op, failed) ==
  /\ op \in v.waiting
  /\ LET attempt == UnabortedCallback(op) /\ (~failed \/ ~CurrentOwner(op) \/ ActiveCallback(op))
         applies == attempt /\ TargetsVisibleView(op)
         releases == CurrentOwner(op) /\ ActiveCallback(op)
         frame == [op |-> op, attempted |-> attempt, applied |-> applies, aborted |-> Aborted(op),
                   current |-> CurrentOwner(op), failed |-> failed]
     IN v' = [v EXCEPT !.waiting = @ \ {op}, !.aborted = @ \ {op.ticket},
                     !.active = IF releases THEN Absent ELSE v.active,
                     !.topics = IF applies /\ op.kind = "ListTopics" THEN op ELSE v.topics,
                     !.rows = IF applies /\ op.kind # "ListTopics" THEN op ELSE v.rows,
                     !.settled = frame]
  /\ UNCHANGED cap

Next == \/ Enter \/ Away \/ Return \/ (\E s \in Contexts : Switch(s))
        \/ Issue("ListTopics", Absent) \/ (\E t \in Topics : Issue("ListSubscriptionsByTopic", t))
        \/ (\E k \in {"Subscribe", "Unsubscribe"} : Issue(k, Absent))
        \/ (\E op \in v.waiting : Cleanup(op))
        \/ (\E op \in v.waiting, failed \in BOOLEAN : Apply(op, failed))
        \/ UNCHANGED vars

TypeOK == /\ cap \in CapInputs /\ v.phase \in Phases /\ v.scope \in Contexts
          /\ v.birth \in Contexts \cup {Absent} /\ v.moves \in 0..MoveLimit
          /\ v.instance \in 0..MoveLimit /\ v.topic \in Topics \cup {Absent}
          /\ v.topicRev \in 0..RequestLimit /\ v.issued \in 0..RequestLimit
          /\ v.active \in Tickets \cup {Absent} /\ v.waiting \subseteq Calls
          /\ v.aborted \subseteq {op.ticket : op \in v.waiting}
          /\ v.rows \in Calls \cup {Absent} /\ v.topics \in Calls \cup {Absent}

MountRequiresCapabilityAndNoHandoff ==
  /\ (v.phase \in {"sns", "left"}) =>
     (cap.ready /\ cap.console /\ cap.array /\ cap.handoff = "none" /\
      \A op \in {"ListTopics", "ListSubscriptionsByTopic", "Subscribe", "Unsubscribe"} : op \in cap.operations)
  /\ (v.phase = "masked") =>
     ( ~cap.ready \/ ~cap.console \/ ~cap.array \/ cap.handoff # "none" \/
       \E op \in {"ListTopics", "ListSubscriptionsByTopic", "Subscribe", "Unsubscribe"} : op \notin cap.operations )
  /\ (v.waiting # {} => cap.ready /\ cap.handoff = "none")

KeyedOwnerMatchesScope == v.phase # "sns" \/ v.birth = v.scope

OnlyOneCurrentFlight ==
  LET owners == {op \in v.waiting : CurrentOwner(op)}
  IN /\ Cardinality(owners) <= 1
     /\ IF v.active = Absent THEN owners = {} ELSE {op.ticket : op \in owners} = {v.active}

OnlyCurrentTopicUpdates ==
  IF v.rows = Absent THEN TRUE
  ELSE /\ v.phase = "sns" /\ v.rows.owner = v.instance /\ v.rows.scope = v.scope
       /\ v.rows.topic = v.topic /\ v.rows.rev = v.topicRev

OnlyCurrentTopicChoices ==
  IF v.topics = Absent THEN TRUE
  ELSE /\ v.phase = "sns" /\ v.topics.owner = v.instance /\ v.topics.scope = v.scope

NoLateVisibleEffect ==
  IF v.settled = Absent THEN TRUE ELSE v.settled.applied => v.settled.current

AbortInhibitsCallback ==
  IF v.settled = Absent THEN TRUE ELSE v.settled.attempted => ~v.settled.aborted
==============================================================================
