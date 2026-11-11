------------------------ MODULE ELBTargetLifecycle ------------------------
\* Finite abstraction of one load balancer, one Lambda target group, one
\* forward listener and one registration per team database. Health checks
\* snapshot the epoch, run without state, and record only for that epoch.
EXTENDS Integers
CONSTANTS Teams, MaxEpoch
VARIABLES lb, tg, listener, target, epoch, health, checked, inflight, last
vars == <<lb, tg, listener, target, epoch, health, checked, inflight, last>>
state == <<lb, tg, listener, target, epoch, health, checked>>
States == {"initial", "healthy", "unhealthy"}
View == [t \in Teams |-> [l |-> lb[t], g |-> tg[t], s |-> listener[t],
                          r |-> target[t], e |-> epoch[t], h |-> health[t]]]
Off == [t \in Teams |-> FALSE]
Init == /\ lb = Off /\ tg = Off /\ listener = Off /\ target = Off
        /\ epoch = [t \in Teams |-> 0]
        /\ health = [t \in Teams |-> "initial"]
        /\ checked = [t \in Teams |-> 0]
        /\ inflight = [t \in Teams |-> -1]
        /\ last = [team |-> CHOOSE t \in Teams : TRUE, auth |-> TRUE,
                   before |-> View, after |-> View]

\* A new epoch restarts initial health checking and invalidates in-flight results.
Bump(t) == /\ epoch' = [epoch EXCEPT ![t] = @ + 1]
           /\ checked' = [checked EXCEPT ![t] = epoch[t] + 1]
           /\ health' = [health EXCEPT ![t] = "initial"]
Same(t) == UNCHANGED <<epoch, checked, health>>
Set(f, t, v) == [f EXCEPT ![t] = v]

CreateLB(t) == /\ ~lb[t] /\ lb' = Set(lb, t, TRUE)
               /\ UNCHANGED <<tg, listener, target>> /\ Same(t)
DeleteLB(t) == /\ lb[t] /\ (listener[t] => epoch[t] < MaxEpoch)
               /\ lb' = Set(lb, t, FALSE) /\ listener' = Set(listener, t, FALSE)
               /\ IF listener[t] THEN Bump(t) ELSE Same(t)
               /\ UNCHANGED <<tg, target>>
CreateTG(t) == /\ ~tg[t] /\ tg' = Set(tg, t, TRUE)
               /\ UNCHANGED <<lb, listener, target>> /\ Same(t)
DeleteTG(t) == /\ tg[t] /\ ~listener[t]
               /\ tg' = Set(tg, t, FALSE) /\ target' = Set(target, t, FALSE)
               /\ UNCHANGED <<lb, listener>> /\ Same(t)
CreateListener(t) == /\ lb[t] /\ tg[t] /\ ~listener[t] /\ epoch[t] < MaxEpoch
                     /\ listener' = Set(listener, t, TRUE) /\ Bump(t)
                     /\ UNCHANGED <<lb, tg, target>>
DeleteListener(t) == /\ listener[t] /\ epoch[t] < MaxEpoch
                     /\ listener' = Set(listener, t, FALSE) /\ Bump(t)
                     /\ UNCHANGED <<lb, tg, target>>
Register(t) == /\ tg[t] /\ ~target[t] /\ epoch[t] < MaxEpoch
               /\ target' = Set(target, t, TRUE) /\ Bump(t)
               /\ UNCHANGED <<lb, tg, listener>>
Deregister(t) == /\ target[t] /\ epoch[t] < MaxEpoch
                 /\ target' = Set(target, t, FALSE) /\ Bump(t)
                 /\ UNCHANGED <<lb, tg, listener>>
Modify(t) == /\ tg[t] /\ epoch[t] < MaxEpoch /\ Bump(t)
             /\ UNCHANGED <<lb, tg, listener, target>>
Effect(t) == CreateLB(t) \/ DeleteLB(t) \/ CreateTG(t) \/ DeleteTG(t)
             \/ CreateListener(t) \/ DeleteListener(t) \/ Register(t)
             \/ Deregister(t) \/ Modify(t)

\* Authorization completes before persistence; a refused request changes nothing.
Request(t, auth) ==
    /\ IF auth THEN Effect(t) ELSE UNCHANGED state
    /\ last' = [team |-> t, auth |-> auth, before |-> View, after |-> View']
    /\ UNCHANGED inflight

StartCheck(t) == /\ tg[t] /\ target[t] /\ listener[t] /\ inflight[t] = -1
                 /\ inflight' = Set(inflight, t, epoch[t])
                 /\ UNCHANGED <<lb, tg, listener, target, epoch, health, checked, last>>
FinishCheck(t, ok) ==
    /\ inflight[t] # -1
    /\ IF inflight[t] = epoch[t] /\ target[t] /\ tg[t]
       THEN /\ health' = Set(health, t, IF ok THEN "healthy" ELSE "unhealthy")
            /\ checked' = Set(checked, t, inflight[t])
       ELSE UNCHANGED <<health, checked>>
    /\ inflight' = Set(inflight, t, -1)
    /\ UNCHANGED <<lb, tg, listener, target, epoch, last>>

Next == \E t \in Teams :
          \/ \E auth \in BOOLEAN : Request(t, auth)
          \/ StartCheck(t)
          \/ \E ok \in BOOLEAN : FinishCheck(t, ok)

TypeOK == /\ lb \in [Teams -> BOOLEAN] /\ tg \in [Teams -> BOOLEAN]
          /\ listener \in [Teams -> BOOLEAN] /\ target \in [Teams -> BOOLEAN]
          /\ epoch \in [Teams -> 0..MaxEpoch] /\ checked \in [Teams -> 0..MaxEpoch]
          /\ health \in [Teams -> States] /\ inflight \in [Teams -> -1..MaxEpoch]
          /\ last.team \in Teams /\ last.auth \in BOOLEAN
ListenerReferencesExist == \A t \in Teams : listener[t] => lb[t] /\ tg[t]
TargetNeedsGroup == \A t \in Teams : target[t] => tg[t]
HealthCurrent == \A t \in Teams : checked[t] = epoch[t]
ReportedHealthInUse == \A t \in Teams :
    health[t] # "initial" => listener[t] /\ target[t] /\ tg[t]
OnlyAuthorizedChanges == last.auth \/ last.before = last.after
OtherTeamUnchanged == \A u \in Teams \ {last.team} : last.before[u] = last.after[u]
=============================================================================
