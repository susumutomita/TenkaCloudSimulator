------------------------------- MODULE AppEngineTraffic -------------------------------
\* Finite abstraction of the native App Engine store and data plane: one
\* permanent application per team, versions grouped in services, traffic
\* splits in whole shares, owner-only commits and requests that reach code only
\* through a deployed version with traffic (or an explicit version) and an
\* allowing firewall. Not a refinement of the Go, SQL or HTTP implementation.
EXTENDS Naturals, FiniteSets
CONSTANTS Teams, Actors, Owner, Services, Versions, Default, Total, MaxSteps
VARIABLES st, last, served, steps
vars == <<st, last, served, steps>>
Pairs == Services \X Versions
Empty == [app |-> FALSE, versions |-> {}, alloc |-> [p \in Pairs |-> 0], allow |-> TRUE]
None == [team |-> "none", actor |-> "none", before |-> [t \in Teams |-> Empty], after |-> [t \in Teams |-> Empty]]
NoServe == [team |-> "none", pair |-> <<Default, "none">>, deployed |-> TRUE, allowed |-> TRUE, routed |-> TRUE]

HasService(s, x) == \E v \in Versions : <<x, v>> \in s.versions
Share(s, x) == LET pairs == {p \in Pairs : p[1] = x} IN
    IF pairs = {} THEN 0 ELSE
    LET f[ps \in SUBSET pairs] == IF ps = {} THEN 0 ELSE LET p == CHOOSE q \in ps : TRUE IN s.alloc[p] + f[ps \ {p}]
    IN f[pairs]

Init == /\ st = [t \in Teams |-> Empty]
        /\ last = None
        /\ served = NoServe
        /\ steps = 0

\* Every mutation passes the owner check and changes only its own team.
Commit(a, t, new) ==
    /\ a = Owner /\ steps < MaxSteps
    /\ st' = [st EXCEPT ![t] = new]
    /\ last' = [team |-> t, actor |-> a, before |-> st, after |-> st']
    /\ steps' = steps + 1
    /\ UNCHANGED served

\* A refused request records an attempt and changes nothing.
Deny(a, t) ==
    /\ a # Owner /\ steps < MaxSteps
    /\ last' = [team |-> t, actor |-> a, before |-> st, after |-> st]
    /\ steps' = steps + 1
    /\ UNCHANGED <<st, served>>

CreateApp(a, t) == ~st[t].app /\ Commit(a, t, [st[t] EXCEPT !.app = TRUE])

\* The first version of a new service receives all of its traffic; a new
\* service other than the default one needs the default service first.
Deploy(a, t, x, v) ==
    LET s == st[t] IN
    /\ s.app /\ <<x, v>> \notin s.versions
    /\ x = Default \/ HasService(s, Default)
    /\ Commit(a, t, [s EXCEPT !.versions = @ \cup {<<x, v>>},
                              !.alloc = IF HasService(s, x) THEN @ ELSE [@ EXCEPT ![<<x, v>>] = Total]])

\* A split names only deployed versions of the service and sums to Total.
SetSplit(a, t, x, f) ==
    LET s == st[t] IN
    /\ s.app /\ HasService(s, x)
    /\ \A v \in Versions : f[v] > 0 => <<x, v>> \in s.versions
    /\ LET n == [s EXCEPT !.alloc = [p \in Pairs |-> IF p[1] = x THEN f[p[2]] ELSE @[p]]] IN
       /\ Share(n, x) = Total
       /\ Commit(a, t, n)

DeleteVersion(a, t, x, v) ==
    LET s == st[t] IN
    /\ s.app /\ <<x, v>> \in s.versions
    /\ s.alloc[<<x, v>>] = 0
    /\ Commit(a, t, [s EXCEPT !.versions = @ \ {<<x, v>>}])

\* Deleting a service removes its versions and split; default is permanent.
DeleteService(a, t, x) ==
    LET s == st[t] IN
    /\ s.app /\ HasService(s, x) /\ x # Default
    /\ Commit(a, t, [s EXCEPT !.versions = {p \in @ : p[1] # x},
                              !.alloc = [p \in Pairs |-> IF p[1] = x THEN 0 ELSE @[p]]])

SetFirewall(a, t, b) == st[t].app /\ Commit(a, t, [st[t] EXCEPT !.allow = b])

\* A request reaches code only through the split or an explicit deployed
\* version, and only when the firewall allows the client.
Serve(t, x, v, explicit) ==
    LET s == st[t] IN
    /\ s.app /\ s.allow
    /\ IF explicit THEN <<x, v>> \in s.versions ELSE s.alloc[<<x, v>>] > 0
    /\ served' = [team |-> t, pair |-> <<x, v>>, deployed |-> <<x, v>> \in s.versions,
                  allowed |-> s.allow, routed |-> explicit \/ s.alloc[<<x, v>>] > 0]
    /\ UNCHANGED <<st, last, steps>>

Next ==
    \/ \E a \in Actors, t \in Teams :
        \/ CreateApp(a, t) \/ Deny(a, t)
        \/ \E b \in BOOLEAN : SetFirewall(a, t, b)
        \/ \E x \in Services :
            \/ DeleteService(a, t, x)
            \/ \E f \in [Versions -> 0..Total] : SetSplit(a, t, x, f)
            \/ \E v \in Versions : Deploy(a, t, x, v) \/ DeleteVersion(a, t, x, v)
    \/ \E t \in Teams, x \in Services, v \in Versions, e \in BOOLEAN : Serve(t, x, v, e)
    \/ UNCHANGED vars

TypeOK == /\ steps \in 0..MaxSteps
          /\ \A t \in Teams : /\ st[t].app \in BOOLEAN /\ st[t].allow \in BOOLEAN
                              /\ st[t].versions \subseteq Pairs
                              /\ st[t].alloc \in [Pairs -> 0..Total]
\* Traffic never points at a version that does not exist.
AllocationsReferenceVersions == \A t \in Teams, p \in Pairs : st[t].alloc[p] > 0 => p \in st[t].versions
\* Every service with versions sends all of its traffic somewhere.
SplitComplete == \A t \in Teams, x \in Services : HasService(st[t], x) => Share(st[t], x) = Total
\* No service exists without the default service.
DefaultFirst == \A t \in Teams : (\E p \in st[t].versions : TRUE) => HasService(st[t], Default)
\* Versions exist only in an application.
ApplicationParent == \A t \in Teams : st[t].versions # {} => st[t].app
\* A refused caller changes nothing.
DenialAtomic == last.actor # Owner => last.before = last.after
\* A commit changes only its own team.
OtherTeamUnchanged == \A u \in Teams : u # last.team => last.before[u] = last.after[u]
\* The application is never deleted once created.
ApplicationPermanent == \A t \in Teams : last.before[t].app => last.after[t].app
\* Served requests ran a deployed version through a route, past the firewall.
ServedThroughRouteAndFirewall == served.deployed /\ served.allowed /\ served.routed
=======================================================================================
