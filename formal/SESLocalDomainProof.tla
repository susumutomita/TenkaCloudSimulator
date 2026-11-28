-------------------------- MODULE SESLocalDomainProof --------------------------
EXTENDS Naturals, Sequences, FiniteSets

Scopes == {"east", "west", "peer"}
Teams == {"A", "B"}
Slots == 1..3
Team(s) == IF s = "peer" THEN "B" ELSE "A"
Empty == [gen |-> 0, zone |-> 0]
Row(t, z, s, g, k, owner, target) ==
    [team |-> t, zone |-> z, selectorScope |-> s, gen |-> g, slot |-> k,
     ownerOK |-> owner, targetOK |-> target]
Op(kind, s, z, g, k, owner, target, auth, live) ==
    [kind |-> kind, scope |-> s, zone |-> z, gen |-> g, slot |-> k,
     ownerOK |-> owner, targetOK |-> target, grant |-> auth, live |-> live]
Command(kind, s) == Op(kind, s, 1, 1, 1, TRUE, TRUE, "both", TRUE)
DNS(kind, s, z, g, k) == Op(kind, s, z, g, k, TRUE, TRUE, "both", TRUE)
Grant(kind, rights, live) == Op(kind, "east", 1, 1, 1, TRUE, TRUE, rights, live)
First == <<Command("Create","east")>>
Read == <<Command("Get","east")>>
AllDNS(kind, s, z, g) == [k \in Slots |-> DNS(kind, s, z, g, k)]
Publish == AllDNS("Put", "east", 1, 1)
Clear == AllDNS("Drop", "east", 1, 1)

Trials == {"revoke", "duplicate", "replace", "wrong", "peer", "region", "rights", "cancel"}
Steps(kind) ==
    CASE kind = "revoke" -> First \o Publish \o Read \o <<Command("Restart","east"),Command("List","east"),
                                               DNS("Drop","east",1,1,2), Command("Get","east")>>
      [] kind = "duplicate" -> First \o Publish \o Read \o
                <<DNS("OpenZone","east",2,1,1),Command("Get","east"),DNS("CloseZone","east",2,1,1),Command("Get","east")>>
      [] kind = "replace" -> First \o Publish \o Read \o Clear \o
                <<DNS("CloseZone","east",1,1,1), DNS("OpenZone","east",2,1,1)>> \o AllDNS("Put","east",2,1) \o Read \o
                <<Command("Delete","east"), Command("Get","east"), Command("Create","east"), Command("Get","east")>> \o
                AllDNS("Put","east",2,2) \o Read
      [] kind = "wrong" -> First \o <<DNS("Put","east",1,1,1), DNS("Put","east",1,1,2),
                Op("Put","east",1,1,3,FALSE,TRUE,"both",TRUE),Command("Get","east"),
                Op("Put","east",1,1,3,TRUE,FALSE,"both",TRUE),Command("List","east"),DNS("Put","east",1,1,3),Command("Get","east")>>
      [] kind = "peer" -> First \o <<Command("Create","peer")>> \o AllDNS("PeerPut","east",1,1) \o Read \o
                AllDNS("Put","peer",1,1) \o <<Command("Get","peer"), Command("Get","east")>>
      [] kind = "region" -> First \o <<Command("Create","west")>> \o Publish \o Read \o <<Command("Get","west")>>
      [] kind = "rights" -> <<Grant("Create","none",TRUE),Grant("Create","named",TRUE),Grant("Get","named",TRUE),
                Grant("List","named",TRUE),Grant("List","star",TRUE),Grant("Delete","none",TRUE),Grant("Delete","named",TRUE)>>
      [] OTHER -> <<Grant("Create","both",FALSE),Command("Create","east")>> \o Publish \o Read \o
                <<Grant("Delete","both",FALSE),Command("Restart","east"),Command("Get","east")>>

VARIABLES trial, step, ids, zones, dns, generations, last, before, observed
vars == <<trial,step,ids,zones,dns,generations,last,before,observed>>
Frame(i,z,d,g) == [ids |-> i, zones |-> z, dns |-> d, gens |-> g]
Current == Frame(ids,zones,dns,generations)
Seed == [s \in Scopes |-> Empty]
InitialZones == [t \in Teams |-> {1}]
InitialGen == [s \in Scopes |-> 0]
Init == /\ trial \in Trials /\ step = 0 /\ ids = Seed /\ zones = InitialZones /\ dns = {}
        /\ generations = InitialGen /\ before = Frame(Seed, InitialZones, {}, InitialGen)
        /\ last = Command("Init","east") /\ observed = "none"

Can(a) == a.grant \in {"both","star"} \/ (a.grant = "named" /\ a.kind # "List")
Proof(s) ==
    LET i == ids[s] IN
    /\ i.gen > 0 /\ zones[Team(s)] = {i.zone}
    /\ \A k \in Slots: \E r \in dns:
           /\ r.team = Team(s) /\ r.zone = i.zone /\ r.selectorScope = s /\ r.gen = i.gen
           /\ r.slot = k /\ r.ownerOK /\ r.targetOK

\* The independent projection names the three literal expected RRsets. It does
\* not call the implementation's Proof predicate or authorization helper.
Oracle(f, s) ==
    LET i == f.ids[s]
        exact == {Row(Team(s),i.zone,s,i.gen,k,TRUE,TRUE): k \in Slots}
    IN IF i.gen = 0 THEN "absent"
       ELSE IF f.zones[Team(s)] = {i.zone} /\ exact \subseteq f.dns THEN "success" ELSE "pending"
Authorized(a) == IF a.kind = "List" THEN a.grant \in {"star","both"} ELSE a.grant # "none"
Response(a) == IF ~Can(a) THEN "denied" ELSE IF ~a.live THEN "abort"
               ELSE IF a.kind \in {"Get","List"} THEN
                    IF ids[a.scope].gen = 0 THEN "absent" ELSE IF Proof(a.scope) THEN "success" ELSE "pending"
               ELSE "ack"

Scheduled ==
    /\ step < Len(Steps(trial))
    /\ LET a == Steps(trial)[step+1]
           s == a.scope
           team == IF a.kind = "PeerPut" THEN "B" ELSE Team(s)
           allowed == Can(a) /\ a.live
           issued == generations[s]+1
           r == Row(team,a.zone,s,a.gen,a.slot,a.ownerOK,a.targetOK)
           key == {v \in dns: v.team = team /\ v.zone = a.zone /\ v.selectorScope = s /\
                               v.gen = a.gen /\ v.slot = a.slot /\ v.ownerOK = a.ownerOK}
       IN /\ step' = step+1 /\ last' = a /\ before' = Current /\ observed' = Response(a)
          /\ ids' = IF allowed /\ a.kind = "Create" THEN
                       [ids EXCEPT ![s] = [gen |-> issued, zone |-> CHOOSE z \in zones[team]: TRUE]]
                    ELSE IF allowed /\ a.kind = "Delete" THEN [ids EXCEPT ![s] = Empty] ELSE ids
          /\ generations' = IF allowed /\ a.kind = "Create" THEN [generations EXCEPT ![s] = issued] ELSE generations
          /\ dns' = IF allowed /\ a.kind \in {"Put","PeerPut"} THEN (dns \ key) \cup {r}
                    ELSE IF allowed /\ a.kind = "Drop" THEN dns \ key ELSE dns
          /\ zones' = IF a.kind = "OpenZone" THEN [zones EXCEPT ![team] = @ \cup {a.zone}]
                      ELSE IF a.kind = "CloseZone" THEN [zones EXCEPT ![team] = @ \ {a.zone}] ELSE zones
    /\ UNCHANGED trial
Probe ==
    \E s \in Scopes, kind \in {"Get","List"}, grant \in {"none","named","star"}:
      LET a == Op(kind,s,1,1,1,TRUE,TRUE,grant,TRUE)
      IN /\ last' = a /\ before' = Current /\ observed' = Response(a)
         /\ UNCHANGED <<trial,step,ids,zones,dns,generations>>
Next == Scheduled \/ Probe
Spec == Init /\ [][Next]_vars

TypeOK == /\ trial \in Trials /\ step \in 0..Len(Steps(trial))
          /\ ids \in [Scopes -> [gen:0..2,zone:0..2]] /\ generations \in [Scopes -> 0..2]
          /\ zones \in [Teams -> SUBSET {1,2}]
          /\ observed \in {"none","absent","success","pending","ack","abort","denied"}
ReadCurrentProof == last.kind \in {"Get","List"} /\ Authorized(last) /\ last.live => observed = Oracle(before,last.scope)
Rights == step > 0 => ((observed = "denied") = ~Authorized(last))
RefusalFrame == step > 0 /\ (~Authorized(last) \/ ~last.live) => Current = before
ReadRestartFrame == last.kind \in {"Get","List","Restart"} => Current = before
PeerIdentityFrame == \A s \in Scopes \ {last.scope}: ids[s] = before.ids[s] /\ generations[s] = before.gens[s]
FreshGeneration == last.kind = "Create" /\ Authorized(last) /\ last.live =>
                     ids[last.scope].gen = before.gens[last.scope]+1
DeleteFrame == last.kind = "Delete" /\ Authorized(last) /\ last.live =>
                  ids[last.scope] = Empty /\ dns = before.dns /\ zones = before.zones
AbortVisible == ~last.live /\ Authorized(last) => observed = "abort"
PhysicalZoneCapture == last.kind = "Create" /\ Authorized(last) /\ last.live =>
                         before.zones[Team(last.scope)] = {ids[last.scope].zone}
DNSWriteFrame == last.kind \in {"Put","PeerPut","Drop"} =>
                   ids = before.ids /\ zones = before.zones /\ generations = before.gens
=============================================================================
