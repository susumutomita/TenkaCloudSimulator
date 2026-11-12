-------------------- MODULE ProbeOperationLifecycle --------------------
EXTENDS Naturals, FiniteSets, Sequences
CONSTANTS HandleCap, WorkerCap, MaxDepth
Keys == 1..3
Owners == {1,2}
Targets == {1,2}
QueryKeys == Keys \cup {0}
Modes == {"raw", "scoped"}
Deadlines == {1, 2}
Results == {"none", "failure", "success"}
ASSUME Cardinality(Keys) = 3 /\ Cardinality(Owners) = 2 /\ Cardinality(Targets) = 2
       /\ HandleCap = 2 /\ WorkerCap = 1


\* One process-local NIC. Identities and tuple contents are opaque atoms.
\* Atomic actions abstract existing locks; not a Go memory/refinement proof.
BlankBound == [mode |-> "raw", owner |-> 0, target |-> 0, deadline |-> 1]
BlankOp == [present |-> FALSE, bound |-> BlankBound, valid |-> FALSE,
            started |-> FALSE, done |-> FALSE, route |-> 0, sends |-> 0,
            ready |-> FALSE, result |-> "none", settled |-> FALSE,
            at |-> 0, outcome |-> "none"]
BlankWire == [known |-> FALSE, key |-> 0, bound |-> BlankBound,
              route |-> 0, ready |-> FALSE, result |-> "none",
              settled |-> FALSE, ended |-> FALSE, at |-> 0]
VARIABLES depth, now, scope, originalScope, revoked, ops, original, issued, slots,
          firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted
vars == <<depth, now, scope, originalScope, revoked, ops, original, issued, slots,
          firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

State == [now |-> now, scope |-> scope, originalScope |-> originalScope, revoked |-> revoked, ops |-> ops, original |-> original, issued |-> issued, slots |-> slots, firstResult |-> firstResult, firstRoute |-> firstRoute, wire |-> wire, poisoned |-> poisoned, scored |-> scored, duplicateSafe |-> duplicateSafe, lookupAllowed |-> lookupAllowed, wireRouteExpected |-> wireRouteExpected, everPoisoned |-> everPoisoned, hostExpired |-> hostExpired, lateAccepted |-> lateAccepted]
EmptyState == [now |-> 0, scope |-> 0, originalScope |-> 0, revoked |-> FALSE,
 ops |-> [k \in QueryKeys |-> BlankOp], original |-> [k \in QueryKeys |-> BlankBound],
 issued |-> {}, slots |-> {}, firstResult |-> [k \in QueryKeys |-> "none"],
 firstRoute |-> [k \in QueryKeys |-> 0], wire |-> BlankWire, poisoned |-> {}, scored |-> {},
 duplicateSafe |-> TRUE, lookupAllowed |-> FALSE, wireRouteExpected |-> 0,
 everPoisoned |-> {}, hostExpired |-> {}, lateAccepted |-> {}]
SeedRecord(s, k, mode, target, deadline) ==
 LET b == [mode |-> mode, owner |-> IF mode = "raw" THEN 0 ELSE s.scope,
           target |-> target, deadline |-> deadline]
 IN [s EXCEPT !.ops[k] = [BlankOp EXCEPT !.present = TRUE, !.valid = TRUE, !.bound = b],
               !.original[k] = b, !.issued = @ \cup {k}]
SeedWire(v, k) == [known |-> TRUE, key |-> k, bound |-> v.bound, route |-> v.route,
 ready |-> v.ready, result |-> v.result, settled |-> v.settled,
 ended |-> (~v.started \/ v.done), at |-> v.at]
Sealed == [EmptyState EXCEPT !.scope = 1, !.originalScope = 1]
One == SeedRecord(EmptyState, 1, "raw", 1, 1)
ScopedOne == SeedRecord(Sealed, 1, "scoped", 1, 1)
Worker == [One EXCEPT !.ops[1].started = TRUE, !.slots = {1}]
Routed == [Worker EXCEPT !.ops[1].route = 1, !.firstRoute[1] = 1]
Dispatched == [Routed EXCEPT !.ops[1].sends = 1]
TimedOut == [Dispatched EXCEPT !.now = 1, !.ops[1].ready = TRUE,
                              !.ops[1].result = "failure", !.firstResult[1] = "failure"]
Ended == [Dispatched EXCEPT !.ops[1].done = TRUE, !.ops[1].outcome = "success", !.slots = {}]
Published == [Ended EXCEPT !.ops[1].ready = TRUE, !.ops[1].result = "success", !.firstResult[1] = "success"]
Settled == [Published EXCEPT !.ops[1].settled = TRUE]
Rejected == [One EXCEPT !.ops[1].ready = TRUE, !.ops[1].result = "failure", !.firstResult[1] = "failure"]
FailedEnded == [TimedOut EXCEPT !.ops[1].done = TRUE, !.ops[1].outcome = "failure", !.slots = {}]
FailedSettled == [FailedEnded EXCEPT !.ops[1].settled = TRUE]
WireSuccess == [Settled EXCEPT !.wire = SeedWire(Settled.ops[1], 1), !.lookupAllowed = TRUE, !.wireRouteExpected = 1]
WireFailure == [FailedSettled EXCEPT !.wire = SeedWire(FailedSettled.ops[1], 1), !.lookupAllowed = TRUE, !.wireRouteExpected = 1]
PoisonedSuccess == [WireSuccess EXCEPT !.poisoned = {1}, !.everPoisoned = {1}]
ExpiredSuccess == [WireSuccess EXCEPT !.hostExpired = {1}]
FullRetired == SeedRecord(Settled, 2, "raw", 2, 2)
BusySecond == SeedRecord(Worker, 2, "raw", 2, 2)
ExpiredRetained == SeedRecord([EmptyState EXCEPT !.now = 1], 1, "raw", 1, 1)
SeedTargets == <<EmptyState, Sealed, One, ScopedOne, Worker, Routed, Dispatched,
 TimedOut, Ended, Published, Settled, Rejected, FailedEnded, FailedSettled,
 WireSuccess, WireFailure, PoisonedSuccess, ExpiredSuccess, FullRetired, BusySecond, ExpiredRetained>>
Seeds == {SeedTargets[i]: i \in 1..Len(SeedTargets)}
Initialize(s) ==
 /\ now = s.now
 /\ scope = s.scope
 /\ originalScope = s.originalScope
 /\ revoked = s.revoked
 /\ ops = s.ops
 /\ original = s.original
 /\ issued = s.issued
 /\ slots = s.slots
 /\ firstResult = s.firstResult
 /\ firstRoute = s.firstRoute
 /\ wire = s.wire
 /\ poisoned = s.poisoned
 /\ scored = s.scored
 /\ duplicateSafe = s.duplicateSafe
 /\ lookupAllowed = s.lookupAllowed
 /\ wireRouteExpected = s.wireRouteExpected
 /\ everPoisoned = s.everPoisoned
 /\ hostExpired = s.hostExpired
 /\ lateAccepted = s.lateAccepted
Init == /\ depth = 0 /\ \E s \in Seeds: Initialize(s)

Bind(owner) ==
 /\ IF scope = 0 /\ ~revoked
    THEN /\ scope' = owner /\ originalScope' = owner
    ELSE /\ scope' = scope /\ UNCHANGED originalScope
 /\ UNCHANGED <<now, revoked, ops, original, issued, slots,
                firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

Bound(mode, owner, target, deadline) ==
 [mode |-> mode, owner |-> IF mode = "raw" THEN 0 ELSE owner,
  target |-> target, deadline |-> deadline]

\* New scoped calls require live finite contexts. Raw calls may instead retain
\* a later admission failure; valid abstracts the pre-retention validation only.
\* Even a validated scoped caller may inherit an already-expired NIC deadline.
\* The selected execution deadline therefore need not exceed now at retention.
Start(k, mode, target, deadline, valid) ==
 /\ ~ops[k].present
 /\ k \in Keys
 /\ valid
 /\ mode = "raw" \/ scope # 0
 /\ Cardinality(issued) < HandleCap
 /\ LET b == Bound(mode, scope, target, deadline) IN
     /\ ops' = [ops EXCEPT ![k] = [BlankOp EXCEPT !.present = TRUE,
                                               !.bound = b, !.valid = valid]]
     /\ original' = [original EXCEPT ![k] = b]
 /\ issued' = issued \cup {k}
 /\ UNCHANGED <<now, scope, originalScope, revoked, slots,
                firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

\* Exact duplicate returns identity without any dispatch or context renewal;
\* differing mode/owner/target returns no handle. Both leave retained work alone.
Duplicate(k, mode, owner, target, deadline) ==
 /\ ops[k].present
 /\ LET matched == ops[k].bound.mode = mode /\ ops[k].bound.target = target /\
                      (mode = "raw" \/ ops[k].bound.owner = owner)
        accepted == matched
    IN duplicateSafe' = (~accepted \/ matched)
 /\ ops' = ops
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued, slots,
                firstResult, firstRoute, wire, poisoned, scored, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

AdmitWorker(k) ==
 /\ ops[k].present /\ ~ops[k].started /\ ~ops[k].ready
 /\ ~revoked /\ now < ops[k].bound.deadline
 /\ Cardinality(slots) < WorkerCap
 /\ ops' = [ops EXCEPT ![k].started = TRUE]
 /\ slots' = slots \cup {k}
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued,
                firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

RejectBeforeWorker(k) ==
 /\ ops[k].present /\ ~ops[k].started /\ ~ops[k].ready
 /\ ops' = [ops EXCEPT ![k].ready = TRUE, ![k].result = "failure"]
 /\ firstResult' = [firstResult EXCEPT ![k] = "failure"]
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued, slots,
                firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

CaptureRoute(k) ==
 /\ ops[k].started /\ ~ops[k].done /\ ops[k].route = 0
 /\ ~ops[k].ready /\ ~revoked /\ now < ops[k].bound.deadline
 /\ ops' = [ops EXCEPT ![k].route = k]
 /\ firstRoute' = [firstRoute EXCEPT ![k] = k]
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued, slots,
                firstResult, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

\* sends counts one concrete observer/Dial invocation, never TCP packets or retransmissions.
Dispatch(k) ==
 /\ ops[k].started /\ ~ops[k].done /\ ops[k].sends = 0
 /\ ops[k].route # 0
 /\ ~ops[k].ready /\ ~revoked /\ now < ops[k].bound.deadline
 /\ ops' = [ops EXCEPT ![k].sends = @ + 1]
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued, slots,
                firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

Timeout(k) ==
 /\ ops[k].present /\ ~ops[k].ready
 /\ now >= ops[k].bound.deadline \/ revoked
 /\ ops' = [ops EXCEPT ![k].ready = TRUE, ![k].result = "failure"]
 /\ firstResult' = [firstResult EXCEPT ![k] = "failure"]
 /\ slots' = slots
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued,
                firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

\* The concrete observer releases its slot when its real worker ends; this
\* may precede the outer operation's result and settlement publication.
WorkerEnd(k, outcome) ==
 /\ ops[k].started /\ ~ops[k].done
 /\ outcome = "failure" \/
       (outcome = "success" /\ ops[k].sends = 1 /\ ~revoked /\ now < ops[k].bound.deadline)
 /\ ops' = [ops EXCEPT ![k].done = TRUE, ![k].outcome = outcome]
 /\ slots' = slots \ {k}
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued,
                firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

Publish(k) ==
 /\ ops[k].done /\ ~ops[k].ready
 /\ LET r == IF ops[k].outcome = "success" /\ ~revoked /\ now < ops[k].bound.deadline
             THEN "success" ELSE "failure" IN
     /\ ops' = [ops EXCEPT ![k].ready = TRUE, ![k].result = r, ![k].at = now]
     /\ firstResult' = IF firstResult[k] = "none" THEN [firstResult EXCEPT ![k] = r] ELSE firstResult
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued, slots,
                firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

Settle(k) ==
 /\ ops[k].present /\ ops[k].ready /\ ~ops[k].settled
 /\ ~ops[k].started \/ ops[k].done
 /\ ops' = [ops EXCEPT ![k].settled = TRUE]
 /\ UNCHANGED <<now, scope, originalScope, revoked, original, issued, slots,
                firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

Lookup(k, mode, owner) ==
 /\ poisoned' = poisoned
 /\ wireRouteExpected' = IF ops[k].present /\ ops[k].bound.mode = mode /\
                  (mode = "raw" \/ ops[k].bound.owner = owner) THEN ops[k].route ELSE 0
 /\ lookupAllowed' = (ops[k].present /\ ops[k].bound.mode = mode /\
                      (mode = "raw" \/ ops[k].bound.owner = owner))
 /\ LET known == ops[k].present /\ ops[k].bound.mode = mode /\
                  (mode = "raw" \/ ops[k].bound.owner = owner)
        v == ops[k]
    IN wire' = IF known THEN
       [known |-> TRUE, key |-> k, bound |-> v.bound, route |-> v.route,
        ready |-> v.ready, result |-> v.result, settled |-> v.settled,
        ended |-> (~v.started \/ v.done), at |-> v.at]
       ELSE BlankWire
 /\ UNCHANGED <<now, scope, originalScope, revoked, ops, original, issued, slots,
                firstResult, firstRoute, scored, duplicateSafe, everPoisoned, hostExpired, lateAccepted>>

\* Abstract consumer correspondence only. Malformed read poisons scoring; it
\* neither creates a backend result nor releases backend ownership.
MalformedRead(k) ==
 /\ k \in issued /\ k \notin scored
 /\ poisoned' = poisoned \cup {k}
 /\ everPoisoned' = everPoisoned \cup {k}
 /\ UNCHANGED <<now, scope, originalScope, revoked, ops, original, issued, slots,
                firstResult, firstRoute, wire, scored, duplicateSafe, lookupAllowed, wireRouteExpected, hostExpired, lateAccepted>>

AcceptWire ==
 /\ wire.known /\ wire.ready /\ wire.settled /\ wire.result = "success"
 /\ wire.key \notin poisoned
 /\ wire.key \notin hostExpired
 /\ scored' = scored \cup {wire.key}
 /\ lateAccepted' = IF wire.key \in hostExpired THEN lateAccepted \cup {wire.key} ELSE lateAccepted
 /\ UNCHANGED <<now, scope, originalScope, revoked, ops, original, issued, slots,
                firstResult, firstRoute, wire, poisoned, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired>>

HostExpire(k) ==
 /\ k \in issued /\ k \notin hostExpired
 /\ hostExpired' = hostExpired \cup {k}
 /\ UNCHANGED <<now, scope, originalScope, revoked, ops, original, issued, slots,
                firstResult, firstRoute, wire, poisoned, scored, duplicateSafe,
                lookupAllowed, wireRouteExpected, everPoisoned, lateAccepted>>

Tick == /\ now < 2 /\ now' = now + 1
        /\ UNCHANGED <<scope, originalScope, revoked, ops, original, issued, slots,
                       firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>
Revoke == /\ ~revoked /\ revoked' = TRUE
          /\ UNCHANGED <<now, scope, originalScope, ops, original, issued, slots,
                         firstResult, firstRoute, wire, poisoned, scored, duplicateSafe, lookupAllowed, wireRouteExpected, everPoisoned, hostExpired, lateAccepted>>

Step == (\E owner \in Owners: Bind(owner))
 \/ (\E k \in QueryKeys, mode \in Modes, target \in Targets, deadline \in Deadlines,
         valid \in BOOLEAN: Start(k, mode, target, deadline, valid))
 \/ (\E k \in QueryKeys, mode \in Modes, owner \in Owners, target \in Targets,
         deadline \in Deadlines: Duplicate(k, mode, owner, target, deadline))
 \/ (\E k \in QueryKeys: AdmitWorker(k) \/ RejectBeforeWorker(k) \/ CaptureRoute(k)
                        \/ Dispatch(k) \/ Timeout(k) \/ Publish(k) \/ Settle(k))
 \/ (\E k \in QueryKeys, outcome \in {"failure", "success"}: WorkerEnd(k, outcome))
 \/ (\E k \in QueryKeys, mode \in Modes, owner \in Owners: Lookup(k, mode, owner))
 \/ (\E k \in Keys: MalformedRead(k)) \/ (\E k \in Keys: HostExpire(k)) \/ AcceptWire \/ Tick \/ Revoke

Next == /\ depth < MaxDepth /\ depth' = depth + 1 /\ Step

TypeOK == /\ depth \in 0..MaxDepth /\ now \in 0..2 /\ scope \in Owners \cup {0} /\ originalScope \in Owners \cup {0}
          /\ revoked \in BOOLEAN /\ issued \subseteq QueryKeys /\ slots \subseteq QueryKeys
          /\ poisoned \subseteq Keys /\ scored \subseteq Keys
          /\ \A k \in QueryKeys:
               /\ ops[k].present \in BOOLEAN /\ ops[k].valid \in BOOLEAN
               /\ ops[k].bound.mode \in Modes /\ ops[k].bound.owner \in Owners \cup {0}
               /\ ops[k].bound.target \in Targets \cup {0} /\ ops[k].bound.deadline \in Deadlines
               /\ ops[k].started \in BOOLEAN /\ ops[k].done \in BOOLEAN
               /\ ops[k].route \in QueryKeys /\ ops[k].sends \in 0..2
               /\ ops[k].ready \in BOOLEAN /\ ops[k].result \in Results
               /\ ops[k].settled \in BOOLEAN /\ ops[k].at \in 0..2
               /\ ops[k].outcome \in Results
ScopeImmutable == scope = originalScope
RetainedExact == issued = {k \in QueryKeys: ops[k].present}
RetainedBound == Cardinality(issued) <= HandleCap
ValidatedAdmission == \A k \in issued: k \in Keys /\ ops[k].valid
ScopedIdentity == \A k \in issued: ops[k].bound.mode = "scoped" =>
                  ops[k].bound.owner \in Owners /\ ops[k].bound.owner = originalScope
ImmutableBinding == \A k \in issued: ops[k].bound = original[k]
WorkerBound == Cardinality(slots) <= WorkerCap
WorkerDebt == slots = {k \in QueryKeys: ops[k].started /\ ~ops[k].done}
AtMostOnce == \A k \in QueryKeys: ops[k].sends <= 1
RouteBeforeDispatch == \A k \in QueryKeys: ops[k].sends > 0 => ops[k].route # 0
RouteRetained == \A k \in QueryKeys: ops[k].route = firstRoute[k]
ResultSticky == \A k \in QueryKeys: ops[k].ready => ops[k].result = firstResult[k]
SettlementEvidence == \A k \in QueryKeys: ops[k].settled =>
                       ops[k].ready /\ (~ops[k].started \/ ops[k].done)
TimelySuccess == \A k \in QueryKeys: ops[k].result = "success" =>
                  ops[k].ready /\ ops[k].done /\ ops[k].sends = 1 /\
                  ops[k].route # 0 /\ ops[k].at < original[k].deadline
WireUnknown == ~wire.known => ~wire.ready /\ ~wire.settled /\ wire.result = "none"
DuplicateIsolation == duplicateSafe
LookupIsolation == wire.known => lookupAllowed
WireIdentity == wire.known => wire.key \in issued /\ wire.bound = original[wire.key]
WireRoute == wire.route = wireRouteExpected
WireResult == wire.known /\ wire.ready => wire.result = firstResult[wire.key]
WireSettlement == wire.settled => wire.known /\ wire.ready /\ wire.ended
PoisonSticky == poisoned = everPoisoned
PoisonExclusion == scored \cap everPoisoned = {}
NoLateHostAcceptance == lateAccepted = {}
=============================================================================
