-------------------- MODULE EC2RouteTableConsoleEpoch --------------------
\* One selected pane, a pending regional read, and a response. Independent
\* changes keep every other scope field fixed, including refresh generation.
VARIABLES selected, pending, shown, moved
vars == <<selected, pending, shown, moved>>
Base == [who |-> "A", region |-> "east", vpc |-> "v1", gen |-> 0, gate |-> "signed-stack", subnet |-> "all"]
Gates == {"signed-stack", "unsigned", "unadvertised", "handoff", "native", "unverified"}
Scopes == {[Base EXCEPT !.who = "B"], [Base EXCEPT !.region = "west"],
           [Base EXCEPT !.vpc = "v2"], [Base EXCEPT !.gen = 1]}
          \cup {[Base EXCEPT !.gate = g] : g \in Gates}
          \cup {[Base EXCEPT !.subnet = x] : x \in {"first", "second", "invalid"}}
Reads == {"DescribeRouteTables", "other"}
Outcomes == {"rows", "empty", "denied", "error"}
VPCs == {"v1", "v2"}
Eligible(g) == g = "signed-stack"
Fresh(s) == /\ s.who = selected.who
            /\ s.region = selected.region
            /\ s.vpc = selected.vpc
            /\ s.gen = selected.gen
            /\ s.gate = selected.gate
            /\ s.subnet = selected.subnet
Init == /\ selected \in Scopes /\ pending = {} /\ shown = {} /\ moved = FALSE

Change(s) ==
  /\ ~moved /\ s # selected
  /\ selected' = s /\ moved' = TRUE
  /\ shown' = {} (* Scope change invalidates before another result. *)
  /\ UNCHANGED pending

Begin(a) ==
  /\ pending = {} /\ Eligible(selected.gate)
  /\ a = "DescribeRouteTables"
  /\ selected.subnet # "invalid"
  /\ pending' = {[scope |-> selected, op |-> a]} /\ shown' = {}
  /\ UNCHANGED <<selected, moved>>

Reply(o, v, hasRoute, hasAssoc, matched) ==
  /\ pending # {}
  /\ LET r == CHOOSE p \in pending : TRUE
         vpcStatus == IF o = "rows" /\ v # r.scope.vpc THEN "empty" ELSE o
         status == IF o = "rows" /\ r.scope.subnet # "all" /\ ~matched THEN "error" ELSE vpcStatus
         rows == status = "rows"
     IN IF Fresh(r.scope)
        THEN shown' = {[scope |-> r.scope, vpc |-> v, observed |-> o, status |-> status,
                        gotRoute |-> hasRoute, defaultRoute |-> (rows /\ hasRoute),
                        gotAssoc |-> hasAssoc, association |-> (rows /\ hasAssoc),
                        main |-> FALSE, matched |-> matched]}
        ELSE UNCHANGED shown
  /\ pending' = {} /\ UNCHANGED <<selected, moved>>

Next == \/ \E s \in Scopes: Change(s)
        \/ \E a \in Reads: Begin(a)
        \/ \E o \in Outcomes, v \in VPCs, d \in BOOLEAN, b \in BOOLEAN, m \in BOOLEAN: Reply(o,v,d,b,m)
        \/ UNCHANGED vars
TypeOK == /\ selected \in Scopes /\ moved \in BOOLEAN
          /\ pending \subseteq [scope: Scopes, op: Reads]
          /\ shown \subseteq [scope: Scopes, vpc: VPCs, observed: Outcomes, status: Outcomes,
                              gotRoute: BOOLEAN, defaultRoute: BOOLEAN, gotAssoc: BOOLEAN,
                              association: BOOLEAN, main: BOOLEAN, matched: BOOLEAN]
EligibleRead == \A r \in pending: r.scope.gate = "signed-stack"
FixedRead == \A r \in pending: r.op = "DescribeRouteTables"
CurrentIdentity == \A r \in shown: r.scope.who = selected.who
CurrentRegion == \A r \in shown: r.scope.region = selected.region
CurrentTarget == \A r \in shown: r.scope.vpc = selected.vpc
CurrentRevision == \A r \in shown: r.scope.gen = selected.gen
CurrentGate == \A r \in shown: r.scope.gate = selected.gate
StatusIsEvidence == \A r \in shown:
    r.status = (IF r.observed = "rows" /\ r.scope.subnet # "all" /\ ~r.matched THEN "error"
                ELSE IF r.observed = "rows" /\ r.vpc # r.scope.vpc THEN "empty" ELSE r.observed)
CurrentSubnet == \A r \in shown: r.scope.subnet = selected.subnet
ValidSubnetRequest == \A r \in pending: r.scope.subnet # "invalid"
DefaultIsRecorded == \A r \in shown: r.defaultRoute = (r.status = "rows" /\ r.gotRoute)
AssociationIsExplicit == \A r \in shown: r.association = (r.status = "rows" /\ r.gotAssoc)
NoImplicitMain == \A r \in shown: ~r.main
=============================================================================
