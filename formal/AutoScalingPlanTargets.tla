---------------------- MODULE AutoScalingPlanTargets ----------------------
\* Finite abstraction of bounded Auto Scaling plan target updates. Each team
\* owns one database with one PROVISIONED table dimension. A staged create or
\* update either commits the capacity bound, ownership and receipt atomically
\* or changes nothing. Table recreation starts a new generation.
EXTENDS Naturals, FiniteSets
CONSTANTS Teams, Plans
Caps == 0..2
NoPlan == "none"
VARIABLES capacity, generation, owner, bounds, pending, receipts, steps
vars == <<capacity, generation, owner, bounds, pending, receipts, steps>>
Clamp(c, lo, hi) == IF c < lo THEN lo ELSE IF c > hi THEN hi ELSE c
Init == /\ capacity = [t \in Teams |-> 0]
        /\ generation = [t \in Teams |-> 1]
        /\ owner = [t \in Teams |-> NoPlan]
        /\ bounds = [t \in Teams |-> [low |-> 0, high |-> 2, gen |-> 0]]
        /\ pending = {}
        /\ receipts = {}
        /\ steps = 0
Stage(t, p, allowed, valid, lo, hi) ==
    /\ pending = {} /\ steps < 2 /\ lo <= hi
    /\ pending' = {[team |-> t, plan |-> p, authorized |-> allowed, valid |-> valid,
                     low |-> lo, high |-> hi, snapshot |-> <<capacity, owner>>]}
    /\ UNCHANGED <<capacity, generation, owner, bounds, receipts, steps>>
Commit(r) ==
    /\ r \in pending /\ r.authorized /\ r.valid
    /\ owner[r.team] \in {NoPlan, r.plan}
    /\ capacity' = [capacity EXCEPT ![r.team] = Clamp(@, r.low, r.high)]
    /\ owner' = [owner EXCEPT ![r.team] = r.plan]
    /\ bounds' = [bounds EXCEPT ![r.team] = [low |-> r.low, high |-> r.high, gen |-> generation[r.team]]]
    /\ receipts' = receipts \cup {[kind |-> "apply", team |-> r.team, plan |-> r.plan,
                                    authorized |-> r.authorized, valid |-> r.valid,
                                    ownerBefore |-> owner[r.team], framed |-> TRUE,
                                    before |-> capacity, after |-> capacity']}
    /\ pending' = {} /\ steps' = steps + 1
    /\ UNCHANGED generation
\* Denied, invalid, conflicting or failed requests roll back every write.
Reject(r) ==
    /\ r \in pending
    /\ capacity' = capacity /\ owner' = owner /\ bounds' = bounds
    /\ receipts' = receipts \cup {[kind |-> "reject", team |-> r.team, plan |-> r.plan,
                                    authorized |-> r.authorized, valid |-> r.valid,
                                    ownerBefore |-> owner[r.team],
                                    framed |-> owner' = owner /\ bounds' = bounds,
                                    before |-> capacity, after |-> capacity']}
    /\ pending' = {} /\ steps' = steps + 1
    /\ UNCHANGED generation
\* Deleting a plan removes its ownership and keeps the table capacity.
DeletePlan(t) ==
    /\ pending = {} /\ owner[t] # NoPlan /\ steps < 2
    /\ capacity' = capacity
    /\ owner' = [owner EXCEPT ![t] = NoPlan]
    /\ receipts' = receipts \cup {[kind |-> "delete", team |-> t, plan |-> owner[t],
                                    authorized |-> TRUE, valid |-> TRUE,
                                    ownerBefore |-> owner[t], framed |-> TRUE,
                                    before |-> capacity, after |-> capacity']}
    /\ steps' = steps + 1
    /\ UNCHANGED <<generation, bounds, pending>>
\* DynamoDB deletes and recreates the table with arbitrary provisioned capacity.
RecreateTable(t, c) ==
    /\ pending = {} /\ generation[t] < 2
    /\ generation' = [generation EXCEPT ![t] = @ + 1]
    /\ capacity' = [capacity EXCEPT ![t] = c]
    /\ UNCHANGED <<owner, bounds, pending, receipts, steps>>
Next == \/ \E t \in Teams, p \in Plans, a, v \in BOOLEAN, lo, hi \in Caps : Stage(t, p, a, v, lo, hi)
        \/ \E r \in pending : Commit(r) \/ Reject(r)
        \/ \E t \in Teams : DeletePlan(t)
        \/ \E t \in Teams, c \in Caps : RecreateTable(t, c)
        \/ UNCHANGED vars
TypeOK == /\ capacity \in [Teams -> Caps]
          /\ generation \in [Teams -> 1..2]
          /\ owner \in [Teams -> Plans \cup {NoPlan}]
          /\ Cardinality(pending) <= 1
          /\ steps \in 0..2
\* An owned target of the recorded generation stays within its plan bounds.
BoundedWhileOwned == \A t \in Teams :
    owner[t] # NoPlan /\ bounds[t].gen = generation[t] =>
        capacity[t] \in bounds[t].low..bounds[t].high
OnlyAuthorizedValid == \A r \in receipts : r.kind # "reject" => r.authorized /\ r.valid
NoOwnershipSteal == \A r \in receipts : r.kind = "apply" => r.ownerBefore \in {NoPlan, r.plan}
DeleteKeepsCapacity == \A r \in receipts : r.kind = "delete" => r.before = r.after
RejectionFrame == \A r \in receipts : r.kind = "reject" => r.before = r.after /\ r.framed
OtherTeamUnchanged == \A r \in receipts :
    \A u \in Teams \ {r.team} : r.before[u] = r.after[u]
PendingInvisible == \A r \in pending : <<capacity, owner>> = r.snapshot
=============================================================================
