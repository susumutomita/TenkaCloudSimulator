-------------------------- MODULE CFNEventHistory --------------------------
EXTENDS Naturals, Integers, Sequences, FiniteSets

\* Three canonical generations: Old and Current reuse one East-region name;
\* Peer is a live West-region stack. One resource, retention one, page size two.
\* Seeds are reachable successful create/delete histories, not all SQL states.
Gens == {"Old", "Current", "Peer"}
Region(g) == IF g = "Peer" THEN "West" ELSE "East"
StackName(g) == IF g = "Absent" THEN "missing" ELSE "reused"
Event(g, n, op, k) == [gen |-> g, seq |-> n, operation |-> op,
                       kind |-> IF k \in {1, 4} THEN "Stack" ELSE "Resource",
                       status |-> IF k <= 2 THEN "InProgress" ELSE "Complete"]
Batch(g, first, op) == {Event(g, first+k-1, op, k) : k \in 1..4}
Seed(op) ==
 [hist |-> IF op = "Create" THEN {"Old", "Peer"} ELSE Gens,
  deleted |-> {"Old"},
  live |-> IF op = "Create" THEN {"Peer"} ELSE {"Current", "Peer"},
  resources |-> IF op = "Create" THEN {"Peer"} ELSE {"Current", "Peer"},
  claims |-> IF op = "Create" THEN {"Peer"} ELSE {"Current", "Peer"},
  last |-> [g \in Gens |-> CASE g = "Old" -> 8 [] g = "Peer" -> 12
                          [] OTHER -> IF op = "Create" THEN 0 ELSE 16],
  events |-> Batch("Old", 1, "Create") \cup Batch("Old", 5, "Delete")
             \cup Batch("Peer", 9, "Create")
             \cup IF op = "Create" THEN {} ELSE Batch("Current", 13, "Create")]
Numbers(s, g) == {e.seq : e \in {x \in s.events : x.gen = g}}
Maximum(ns) == CHOOSE n \in ns : \A m \in ns : m <= n
Minimum(ns) == CHOOSE n \in ns : \A m \in ns : n <= m
RowsBefore(s, g, bound) == {n \in Numbers(s, g) : n < bound}
FirstTwo(ns) == IF ns = {} THEN <<>>
               ELSE LET a == Maximum(ns) IN
                    IF ns \ {a} = {} THEN <<a>>
                    ELSE <<a, Maximum(ns \ {a})>>
ExpectedID(s, kind, wanted, region) ==
 IF kind = "ID" THEN
   IF wanted \in s.hist /\ Region(wanted) = region THEN wanted ELSE "None"
 ELSE LET candidates == {g \in s.live : StackName(g) = StackName(wanted)
                                       /\ Region(g) = region}
      IN IF candidates = {} THEN "None" ELSE CHOOSE g \in candidates : TRUE

VARIABLES mode, op, fault, before, data, work, phase, step, outcome, earlyAck,
          requestKind, wanted, requestRegion, authorized, cursor, resolved,
          cursorBound, page, nextCursor
vars == <<mode, op, fault, before, data, work, phase, step, outcome, earlyAck,
          requestKind, wanted, requestRegion, authorized, cursor, resolved,
          cursorBound, page, nextCursor>>
FrozenRequest == <<mode, op, fault, before, requestKind, wanted, requestRegion,
                   authorized, cursor>>
ReadResult == <<resolved, cursorBound, page, nextCursor>>

\* Write faults are delivered SQL failures/ignored effects, not arbitrary SQL.
\* Coherent transaction snapshots, opaque unique event IDs and SQL FK behavior
\* are assumptions. Mutation controls deliberately remove individual safeguards.
Init ==
 /\ mode \in {"Write", "Read"}
 /\ op \in IF mode = "Write" THEN {"Create", "Delete"} ELSE {"Delete"}
 /\ fault \in IF mode = "Write" THEN
      {"None", "WriteError", "IgnoredEvent", "IgnoredHistory", "PruneError",
       "IgnoredPrune", "PruneNewest", "LateRestore"} ELSE {"None"}
 /\ before = Seed(op) /\ data = before /\ work = before
 /\ phase = IF mode = "Write" THEN "Begin" ELSE "Resolve"
 /\ step = 1 /\ outcome = "Pending" /\ earlyAck = FALSE
 /\ requestKind \in IF mode = "Read" THEN {"Name", "ID"} ELSE {"ID"}
 /\ wanted \in IF mode = "Read" THEN Gens \cup {"Absent"} ELSE {"Current"}
 /\ requestRegion \in IF mode = "Read" THEN {"East", "West"} ELSE {"East"}
 /\ authorized \in IF mode = "Read" THEN BOOLEAN ELSE {TRUE}
 /\ cursor \in IF mode = "Read" THEN 0..21 ELSE {0}
 /\ resolved = "None" /\ cursorBound = 21 /\ page = <<>> /\ nextCursor = 0

Begin ==
 /\ phase = "Begin"
 /\ work' = IF op = "Create" THEN
      [work EXCEPT !.hist = @ \cup {"Current"}, !.live = @ \cup {"Current"},
                   !.claims = @ \cup {"Current"}] ELSE work
 /\ phase' = "Append"
 /\ UNCHANGED <<FrozenRequest, data, step, outcome, earlyAck, ReadResult>>

AppendEvent ==
 /\ phase = "Append"
 /\ LET n == (IF op = "Create" THEN 12 ELSE 16) + step
        row == Event("Current", n, op, step)
        eventWritten == ~(fault = "IgnoredEvent" /\ step = 3)
        historyWritten == ~(fault = "IgnoredHistory" /\ step = 4)
        delivered == [work EXCEPT
          !.events = IF eventWritten THEN @ \cup {row} ELSE @,
          !.last["Current"] = IF historyWritten THEN n ELSE @,
          !.resources = IF step = 3 THEN
             IF op = "Create" THEN @ \cup {"Current"} ELSE @ \ {"Current"}
             ELSE @,
          !.live = IF op = "Delete" /\ step = 4 THEN @ \ {"Current"} ELSE @,
          !.claims = IF op = "Delete" /\ step = 4 THEN @ \ {"Current"} ELSE @]
        accepted == ~(fault = "WriteError" /\ step = 2)
                    /\ eventWritten (* EventWriteACK *)
                    /\ historyWritten (* HistoryWriteACK *)
    IN /\ work' = delivered
       /\ data' = data (* PendingPublicationBoundary *)
       /\ earlyAck' = (op = "Create" \/ "Current" \notin delivered.resources)
       /\ phase' = IF ~accepted THEN "Abort"
                    ELSE IF step = 4 THEN "Finish" ELSE "Append"
       /\ step' = IF accepted /\ step < 4 THEN step + 1 ELSE step
 /\ UNCHANGED <<FrozenRequest, outcome, ReadResult>>

Finish ==
 /\ phase = "Finish"
 /\ LET marked == IF op = "Delete" THEN
                       [work EXCEPT !.deleted = @ \cup {"Current"}] ELSE work
        oldest == IF op = "Delete" THEN {"Old"} ELSE {}
        prune == IF fault = "IgnoredPrune" THEN {}
                 ELSE IF fault = "PruneNewest" THEN {"Current"}
                 ELSE oldest (* DeletedOnlyRetention *)
        removed == IF op = "Delete" THEN prune ELSE {}
        retainedEvents == {e \in marked.events : e.gen \notin removed} (* CascadeEvents *)
        delivered == [marked EXCEPT !.hist = @ \ removed, !.deleted = @ \ removed,
                     !.events = retainedEvents,
                     !.resources = IF op = "Delete" /\ fault = "LateRestore"
                                   THEN @ \cup {"Current"} ELSE @]
    IN /\ work' = delivered
       /\ phase' = IF op = "Delete" /\ fault = "PruneError" THEN "Abort" ELSE "Check"
 /\ UNCHANGED <<FrozenRequest, data, step, outcome, earlyAck, ReadResult>>

Check ==
 /\ phase = "Check"
 /\ LET retentionOK == op = "Create" \/
                  (Cardinality(work.deleted) <= 1 (* DeletedCountACK *)
                   /\ "Current" \in work.deleted) (* SelectedHistoryACK *)
        cleanupOK == op = "Create" \/
                  ("Current" \notin work.live /\ "Current" \notin work.claims
                   /\ "Current" \notin work.resources) (* FinalAfterAllWritesACK *)
    IN /\ phase' = IF retentionOK /\ cleanupOK THEN "Commit" ELSE "Abort"
 /\ UNCHANGED <<FrozenRequest, data, work, step, outcome, earlyAck, ReadResult>>
Commit ==
 /\ phase = "Commit" /\ data' = work
 /\ outcome' = "Success" /\ phase' = "Done"
 /\ UNCHANGED <<FrozenRequest, work, step, earlyAck, ReadResult>>
Abort ==
 /\ phase = "Abort" /\ data' = before (* RollbackAllTables *)
 /\ outcome' = "Refused" /\ phase' = "Done"
 /\ UNCHANGED <<FrozenRequest, work, step, earlyAck, ReadResult>>

\* Resolution, authorization, cursor lookup and rows use the same read snapshot.
Resolve ==
 /\ phase = "Resolve"
 /\ LET idAllowed == wanted \in before.hist
                     /\ Region(wanted) = requestRegion (* ExactRegion *)
        names == {g \in before.live : StackName(g) = StackName(wanted)
                                      /\ Region(g) = requestRegion}
        named == IF names = {} THEN "None" ELSE CHOOSE g \in names : TRUE
        id == IF requestKind = "ID" THEN IF idAllowed THEN wanted ELSE "None"
              ELSE named (* LiveNameResolution *)
    IN /\ resolved' = id
       /\ phase' = IF id = "None" THEN "ReadRefuse" ELSE "Authorize"
 /\ UNCHANGED <<FrozenRequest, data, work, step, outcome, earlyAck,
                cursorBound, page, nextCursor>>
Authorize ==
 /\ phase = "Authorize"
 /\ phase' = IF authorized (* ExplicitAuthorization *) THEN "Cursor" ELSE "ReadRefuse"
 /\ UNCHANGED <<FrozenRequest, data, work, step, outcome, earlyAck, ReadResult>>
Cursor ==
 /\ phase = "Cursor"
 /\ LET matches == {e \in before.events : e.seq = cursor
                                          /\ e.gen = resolved} (* CursorGenerationScope *)
        valid == cursor = 0 \/ matches # {}
    IN /\ cursorBound' = IF cursor = 0 THEN 21 ELSE cursor
       /\ phase' = IF valid THEN "Page" ELSE "ReadRefuse"
 /\ UNCHANGED <<FrozenRequest, data, work, step, outcome, earlyAck,
                resolved, page, nextCursor>>
Page ==
 /\ phase = "Page"
 /\ LET rows == RowsBefore(before, resolved, cursorBound) (* StableCursorBound *)
        selected == FirstTwo(rows) (* DescendingPage *)
    IN /\ page' = selected
       /\ nextCursor' = IF Cardinality(rows) > 2
                        THEN selected[Len(selected)] ELSE 0 (* LastReturnedCursor *)
       /\ outcome' = "ReadSuccess" /\ phase' = "Done"
 /\ UNCHANGED <<FrozenRequest, data, work, step, earlyAck, resolved, cursorBound>>
ReadRefuse ==
 /\ phase = "ReadRefuse"
 /\ outcome' = "ReadRefused" /\ phase' = "Done"
 /\ UNCHANGED <<FrozenRequest, data, work, step, earlyAck, ReadResult>>
Next == Begin \/ AppendEvent \/ Finish \/ Check \/ Commit \/ Abort
        \/ Resolve \/ Authorize \/ Cursor \/ Page \/ ReadRefuse

FrameOK(s) ==
 /\ s.hist \subseteq Gens /\ s.deleted \subseteq Gens
 /\ s.live \subseteq Gens /\ s.resources \subseteq Gens /\ s.claims \subseteq Gens
 /\ s.last \in [Gens -> 0..20]
 /\ s.events \subseteq [gen : Gens, seq : 1..20, operation : {"Create", "Delete"},
                       kind : {"Stack", "Resource"}, status : {"InProgress", "Complete"}]
TypeOK ==
 /\ mode \in {"Write", "Read"} /\ op \in {"Create", "Delete"}
 /\ fault \in {"None", "WriteError", "IgnoredEvent", "IgnoredHistory", "PruneError",
               "IgnoredPrune", "PruneNewest", "LateRestore"}
 /\ phase \in {"Begin", "Append", "Finish", "Check", "Commit", "Abort", "Done",
               "Resolve", "Authorize", "Cursor", "Page", "ReadRefuse"}
 /\ FrameOK(before) /\ FrameOK(data) /\ FrameOK(work)
 /\ step \in 1..4 /\ earlyAck \in BOOLEAN
 /\ outcome \in {"Pending", "Success", "Refused", "ReadSuccess", "ReadRefused"}
 /\ requestKind \in {"Name", "ID"} /\ wanted \in Gens \cup {"Absent"}
 /\ requestRegion \in {"East", "West"} /\ authorized \in BOOLEAN /\ cursor \in 0..21
 /\ resolved \in Gens \cup {"None"} /\ cursorBound \in 0..21
 /\ page \in Seq(1..20) /\ Len(page) <= 2 /\ nextCursor \in 0..20
PendingFrameUnchanged == outcome = "Pending" => data = before
FailureAtomic == outcome = "Refused" => data = before
LivePeerPreserved ==
 /\ "Peer" \in data.hist /\ "Peer" \in data.live
 /\ "Peer" \in data.resources /\ "Peer" \in data.claims
 /\ {e \in data.events : e.gen = "Peer"} = {e \in before.events : e.gen = "Peer"}
EventParentExists == \A e \in data.events : e.gen \in data.hist
RetentionBound == outcome = "Success" => Cardinality(data.deleted) <= 1
LatestDeletedRetained == outcome = "Success" /\ op = "Delete" =>
                        "Current" \in data.hist /\ "Current" \in data.deleted
SuccessfulLifecycleExact == outcome = "Success" =>
 LET expected == IF op = "Create" THEN Batch("Current", 13, "Create")
                 ELSE Batch("Current", 13, "Create") \cup Batch("Current", 17, "Delete")
 IN /\ {e \in data.events : e.gen = "Current"} = expected
    /\ ("Current" \in data.live) = (op = "Create")
    /\ ("Current" \in data.claims) = (op = "Create")
    /\ ("Current" \in data.deleted) = (op = "Delete")
    /\ (op = "Create" => "Current" \in data.resources)
HistoryTracksLatest == \A g \in data.hist :
                      Numbers(data, g) # {} /\ data.last[g] = Maximum(Numbers(data, g))
CleanupAfterAllWrites == outcome = "Success" /\ op = "Delete" =>
                        "Current" \notin data.resources
ReadSnapshotUnchanged == mode = "Read" => data = before
NoUnauthorizedDisclosure == outcome = "ReadSuccess" => authorized
ResolvedGenerationExact == outcome = "ReadSuccess" =>
                           resolved = ExpectedID(before, requestKind, wanted, requestRegion)
CursorScoped == outcome = "ReadSuccess" /\ cursor # 0 => cursor \in Numbers(before, resolved)
PageExact == outcome = "ReadSuccess" =>
             page = FirstTwo(RowsBefore(before, resolved, IF cursor = 0 THEN 21 ELSE cursor))
NextCursorExact == outcome = "ReadSuccess" =>
 LET rows == RowsBefore(before, resolved, IF cursor = 0 THEN 21 ELSE cursor)
     expected == FirstTwo(rows)
 IN nextCursor = IF Cardinality(rows) > 2 THEN expected[Len(expected)] ELSE 0
Spec == Init /\ [][Next]_vars
=============================================================================
