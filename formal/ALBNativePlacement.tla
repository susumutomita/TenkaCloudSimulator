--------------------------- MODULE ALBNativePlacement ---------------------------
EXTENDS Naturals, FiniteSets

\* One writer transaction per team DB. Names "one" and "two" can share the
\* same EC2 resources. A third historical ALB already contains their raw IDs.
Names == {"one", "two", "legacy"}
NewNames == Names \ {"legacy"}
Resources == 1..9
Primary == {1, 2, 3}
Kinds == {"ordinary","sameZone","otherVpc","otherGroup","otherRegion","stackSubnet","otherTeam","noGroup"}
\* Distinct local network IDs. A scope denotes a team DB and region together.
Zone(i) == IF i \in {1,4} THEN "a" ELSE "b"
Vpc(i) == IF i \in {5,6} THEN "other" ELSE "local"
Scope(i) == IF i = 7 THEN "otherRegion" ELSE IF i = 9 THEN "otherTeam" ELSE "here"
IsNative(i) == i # 8
Subnet(k) == CASE k = "sameZone" -> 4 [] k = "otherVpc" -> 5
               [] k = "otherRegion" -> 7 [] k = "stackSubnet" -> 8
               [] k = "otherTeam" -> 9 [] OTHER -> 2
Groups(k) == IF k = "noGroup" THEN {} ELSE {IF k = "otherGroup" THEN 6 ELSE 3}
Targets(k) == {1, Subnet(k)} \cup Groups(k)
Admits(k, live) ==
    /\ Targets(k) \subseteq live
    /\ \A i \in Targets(k) : Scope(i) = "here"
    /\ \A i \in Targets(k) : IsNative(i)
    /\ \A i \in Targets(k) : Vpc(i) = "local"
    /\ Zone(1) # Zone(Subnet(k))
    /\ Groups(k) # {}
NativeView == <<"local", "a", "b">>
LegacyView == <<"unknown", "", "">>
Absent == <<"absent", "", "">>
Edges(n, ids) == {<<n, i>> : i \in ids}
VARIABLES live, rows, refs, pending, request, last, step
vars == <<live, rows, refs, pending, request, last, step>>
durable == <<live, rows, refs>>
Audit(op, preserves) == [action |-> op, preserved |-> preserves]
Init ==
    /\ live = Resources
    /\ rows = [n \in Names |-> IF n = "legacy" THEN LegacyView ELSE Absent]
    /\ refs = {} /\ pending = "idle" /\ request = "ordinary"
    /\ last = Audit("start",TRUE) /\ step = 0

Begin(n,k) ==
    /\ pending = "idle" /\ rows[n] = Absent /\ Admits(k,live)
    /\ pending' = n /\ request' = k /\ UNCHANGED durable
    /\ last' = Audit("stage",TRUE)
Commit ==
    /\ pending # "idle"
    /\ rows' = [rows EXCEPT ![pending] = <<Vpc(1),Zone(1),Zone(Subnet(request))>>]
    /\ refs' = refs \cup Edges(pending,Targets(request))
    /\ live' = live /\ pending' = "idle" /\ UNCHANGED request
    /\ last' = Audit("commit",request = "ordinary" /\ Primary \subseteq live)
Cancel ==
    /\ pending # "idle"
    /\ rows' = rows /\ refs' = refs (* rollback partial references *)
    /\ live' = live /\ pending' = "idle" /\ UNCHANGED request
    /\ last' = Audit("cancel",durable' = durable)
DropALB(n) ==
    /\ pending = "idle" /\ rows[n] # Absent
    /\ rows' = [rows EXCEPT ![n] = Absent]
    /\ refs' = refs \ Edges(n,Resources)
    /\ UNCHANGED <<live,pending,request>> /\ last' = Audit("deleteALB",TRUE)
DropEC2(i) ==
    /\ pending = "idle" /\ i \in live
    /\ \A n \in Names : <<n,i>> \notin refs
    /\ live' = live \ {i}
    /\ UNCHANGED <<rows,refs,pending,request>> /\ last' = Audit("deleteEC2",TRUE)
RetryLegacy ==
    /\ pending = "idle" /\ rows["legacy"] = LegacyView
    /\ refs' = refs (* historical retry acquires no ownership *)
    /\ UNCHANGED <<live,rows,pending,request>> /\ last' = Audit("retryLegacy",TRUE)
Reopen ==
    /\ pending = "idle" /\ rows' = rows (* reopen durable projection *)
    /\ UNCHANGED <<live,refs,pending,request>> /\ last' = Audit("reopen",durable' = durable)

Next == /\ step < 8 /\ step' = step + 1
        /\ \/ \E n \in NewNames, k \in Kinds : Begin(n,k)
           \/ Commit \/ Cancel \/ (\E n \in Names : DropALB(n))
           \/ (\E i \in Primary : DropEC2(i)) \/ RetryLegacy \/ Reopen
Spec == Init /\ [][Next]_vars

TypeOK == /\ live \subseteq Resources /\ refs \subseteq (Names \X Resources)
          /\ DOMAIN rows = Names /\ request \in Kinds /\ pending \in NewNames \cup {"idle"}
          /\ step \in 0..8
AdmittedPlacement == last.action # "commit" \/ last.preserved
CanceledUnchanged == last.action # "cancel" \/ last.preserved
ReopenUnchanged == last.action # "reopen" \/ last.preserved
StageInvisible == pending = "idle" \/ rows[pending] = Absent
LegacyUnbound == rows["legacy"] \in {Absent,LegacyView} /\ refs \intersect Edges("legacy",Resources) = {}
ProjectionExact == \A n \in NewNames : rows[n] \in {Absent,NativeView}
CompleteReferences == \A n \in NewNames :
    refs \intersect Edges(n,Resources) = IF rows[n] = Absent THEN {} ELSE Edges(n,Primary)
ReferencesExist == \A e \in refs : e[2] \in live
================================================================================
