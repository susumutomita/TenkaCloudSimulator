--------------------------- MODULE EC2RouteTableRead ---------------------------
CONSTANT VPCs
VARIABLE seen
Good == [op |-> "DescribeRouteTables", allowed |-> TRUE, scoped |-> FALSE,
         local |-> TRUE, declared |-> TRUE, claimed |-> TRUE,
         rows |-> TRUE, faithful |-> TRUE, typedFlag |-> TRUE]
(* One bad admission/binding witness at a time; mixed parent relationships
   are enumerated independently. This models one atomic read, not SQL or IP. *)
Witnesses == {Good, [Good EXCEPT !.op = "other"],
              [Good EXCEPT !.allowed = FALSE], [Good EXCEPT !.scoped = TRUE],
              [Good EXCEPT !.local = FALSE], [Good EXCEPT !.declared = FALSE],
              [Good EXCEPT !.claimed = FALSE], [Good EXCEPT !.rows = FALSE],
              [Good EXCEPT !.faithful = FALSE], [Good EXCEPT !.typedFlag = FALSE]}
RouteStates == {"absent", "removed", "enabled"}
Init == seen = {}
Read(f, tableVPC, subnetVPC, gatewayVPC, explicit, savedRoute) ==
    /\ seen = {}
    /\ f.op = "DescribeRouteTables"
    /\ f.allowed /\ ~f.scoped /\ f.local
    /\ f.declared /\ f.claimed /\ f.rows /\ f.faithful
    /\ (savedRoute = "absent" \/ f.typedFlag)
    /\ (~explicit \/ subnetVPC = tableVPC)
    /\ (savedRoute = "absent" \/ gatewayVPC = tableVPC)
    /\ seen' = {[f |-> f, table |-> tableVPC, subnet |-> subnetVPC,
                 gateway |-> gatewayVPC, explicit |-> explicit, route |-> savedRoute,
                 associated |-> explicit, main |-> FALSE, routed |-> (savedRoute = "enabled")]}

Next == (\E f \in Witnesses, tv, sv, gv \in VPCs, ex \in BOOLEAN, rt \in RouteStates:
          Read(f, tv, sv, gv, ex, rt)) \/ UNCHANGED seen

TypeOK == seen \subseteq [f : Witnesses, table : VPCs, subnet : VPCs, gateway : VPCs,
                           explicit : BOOLEAN, route : RouteStates,
                           associated : BOOLEAN, main : BOOLEAN, routed : BOOLEAN]
Admission == \A r \in seen: r.f.op = "DescribeRouteTables" /\ r.f.allowed /\ ~r.f.scoped /\ r.f.local
BoundSnapshot == \A r \in seen: r.f.declared /\ r.f.claimed /\ r.f.rows /\ r.f.faithful
SameVPC == \A r \in seen:
    (~r.explicit \/ r.subnet = r.table) /\ (r.route = "absent" \/ r.gateway = r.table)
TypedDefault == \A r \in seen: r.route = "absent" \/ r.f.typedFlag
ExplicitOnly == \A r \in seen: ~r.main /\ (r.associated = r.explicit)
CurrentDefault == \A r \in seen: r.routed = (r.route = "enabled")
=============================================================================
