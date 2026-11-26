--------------------- MODULE EC2NativeRouteAssociationLifecycle ---------------------
EXTENDS FiniteSets
\* Two mutable native tables and subnets share v1; v2, west and CFN
\* supply boundary witnesses. Endpoints start valid; only deletes are modeled.
VARIABLES rows, live, last
Regions == {"east", "west"}
EastTables == {"t1", "t2"}
EastSubnets == {"s1", "s2"}
Tables == EastTables \cup {"tOther", "tWest", "tCfn"}
Subnets == EastSubnets \cup {"sWest", "sCfn"}
Vpc == [k \in Tables \cup Subnets |->
       CASE k = "tOther" -> "v2"
         [] k \in {"tWest", "sWest"} -> "vWest"
         [] k \in {"tCfn", "sCfn"} -> "vCfn"
         [] OTHER -> "v1"]
Resources == Tables \cup Subnets \cup {"v1", "v2", "vWest", "vCfn"}
LegacyKeys == {"tCfn", "sCfn", "vCfn"}
MutableParents == EastTables \cup EastSubnets \cup {"v1"} \cup LegacyKeys
Region(k) == IF k \in {"tWest", "sWest", "vWest"} THEN "west" ELSE "east"
Slots == {"first", "second"}
IDs == Slots \cup {"peer", "legacy", "unknown"}
None == "none"
Binding(r, id, t, s, owner) ==
  [region |-> r, id |-> id, tab |-> t, sub |-> s, owner |-> owner]
Peer == Binding("west", "peer", "tWest", "sWest", "native")
Legacy == Binding("east", "legacy", "tCfn", "sCfn", "cfn")
Missing == Binding("east", None, None, None, "absent")
KnownRows == [region: Regions, id: IDs, tab: Tables, sub: Subnets,
              owner: {"native", "cfn"}]
Find(rs, r, id) ==
  LET found == {b \in rs : b.region = r /\ b.id = id}
  IN IF found = {} THEN Missing ELSE CHOOSE b \in found : TRUE
NativeFor(rs, r, s) == {b \in rs : b.region = r /\ b.sub = s /\ b.owner = "native"}
Matching(rs, r, t, s) == {b \in NativeFor(rs, r, s) : b.tab = t}
\* Symbolic IDs preserve simultaneous uniqueness, not random distribution or
\* tombstones. A retired symbolic slot can be reused by a later create.
NewID(r) ==
  IF Find(rows, r, "first") = Missing THEN "first"
  ELSE IF Find(rows, r, "second") = Missing THEN "second" ELSE None

Decision(available, t, s, detail) ==
  [available |-> available, tab |-> t, sub |-> s, detail |-> detail]
NoDecision == Decision(FALSE, None, None, FALSE)
Decisions(t, s) ==
  {Decision(TRUE, t, s, FALSE), Decision(TRUE, t, None, FALSE),
   Decision(TRUE, None, s, FALSE), Decision(TRUE, None, None, FALSE),
   Decision(FALSE, t, s, FALSE)}
AllowsPair(d, t, s) ==
  /\ d.available
  /\ d.tab = t
  /\ d.sub = s
AssocInputs == {<<"east", t, s>> : t \in EastTables, s \in EastSubnets}
  \cup {<<"east", "tOther", "s1">>, <<"east", "tWest", "sWest">>,
        <<"east", "tCfn", "sCfn">>}
\* The CFN pair shares its own VPC in a valid initial snapshot. Bypassing
\* ownership is not tested by assuming mismatched or corrupt live resources.
Event(op, r, result) ==
  [op |-> op, region |-> r, result |-> result, tab |-> None, sub |-> None,
   id |-> None, decision |-> NoDecision, commits |-> FALSE,
   parent |-> None, parentGrant |-> FALSE, readGrant |-> FALSE,
   seen |-> {}, previousRows |-> rows, previousLive |-> live]

Init ==
  /\ rows = {Peer, Legacy}
  /\ live = Resources
  /\ last = Event("init", "east", "none")

Associate(r, t, s, d, commits) ==
  LET permitted == AllowsPair(d, t, s) /\ {t, s, Vpc[t]} \subseteq live
                   /\ {t, s} \cap LegacyKeys = {}
                   /\ Region(t) = r /\ Region(s) = r
                   /\ Vpc[t] = Vpc[s]
      existing == NativeFor(rows, r, s)
      matched == Matching(rows, r, t, s)
      create == permitted /\ commits /\ existing = {} /\ NewID(r) # None
      replay == permitted /\ commits /\ matched # {}
      result == IF create THEN "created" ELSE IF replay THEN "replay" ELSE "denied"
      receipt == IF create THEN NewID(r)
                 ELSE IF replay THEN (CHOOSE b \in matched : TRUE).id ELSE None
  IN /\ rows' = IF create THEN rows \cup {Binding(r, receipt, t, s, "native")} ELSE rows
     /\ UNCHANGED live
     /\ last' = [Event("associate", r, result) EXCEPT
                   !.tab = t, !.sub = s, !.id = receipt, !.decision = d, !.commits = commits]

Remove(r, id, d, commits) ==
  LET target == Find(rows, r, id)
      native == target # Missing /\ target.owner = "native"
      allowed == native /\ AllowsPair(d, target.tab, target.sub)
      removed == allowed /\ commits
      absent == ~native /\ d.available /\ d.detail
      result == IF removed THEN "removed" ELSE IF absent THEN "absent" ELSE "denied"
  IN /\ rows' = IF removed THEN rows \ {target} ELSE rows
     /\ UNCHANGED live
     /\ last' = [Event("remove", r, result) EXCEPT
                   !.id = id, !.decision = d, !.commits = commits]

RemovalDecisions(r, id) ==
  LET target == Find(rows, r, id)
  IN IF target = Missing
     THEN Decisions("t1", "s1") \cup {Decision(TRUE, "t1", "s1", TRUE)}
     ELSE Decisions(target.tab, target.sub)
          \cup {Decision(TRUE, target.tab, target.sub, TRUE), Decision(TRUE, "tOther", "s1", FALSE)}

Dependent(rs, ls, k) ==
  IF k \in Tables THEN \E b \in rs : b.tab = k
  ELSE IF k \in Subnets THEN \E b \in rs : b.sub = k
  ELSE \E e \in ls \cap (Tables \cup Subnets) : Vpc[e] = k

DeleteParent(k, granted, commits) ==
  LET ready == granted /\ k \in live /\ k \notin LegacyKeys
      remove == ready /\ commits /\ ~Dependent(rows, live, k)
      result == IF remove THEN "removed-parent"
                ELSE IF ready /\ Dependent(rows, live, k) THEN "dependent" ELSE "denied"
  IN /\ live' = IF remove THEN live \ {k} ELSE live
     /\ rows' = rows
     /\ last' = [Event("delete-parent", "east", result) EXCEPT
                   !.parent = k, !.parentGrant = granted, !.commits = commits]

Projection(rs, r) ==
  {[id |-> b.id, tab |-> b.tab, sub |-> b.sub, main |-> FALSE] :
    b \in {b \in rs : b.region = r}}
Read(r, granted) ==
  /\ last' = [Event("read", r, IF granted THEN "read" ELSE "denied") EXCEPT
                !.seen = IF granted THEN Projection(rows, r) ELSE {}, !.readGrant = granted]
  /\ UNCHANGED <<rows, live>>

Next ==
  \/ \E x \in AssocInputs, commits \in BOOLEAN:
       \E d \in Decisions(x[2], x[3]): Associate(x[1], x[2], x[3], d, commits)
  \/ \E id \in IDs, commits \in BOOLEAN:
       \E d \in RemovalDecisions("east", id): Remove("east", id, d, commits)
  \/ \E k \in MutableParents, granted \in BOOLEAN, commits \in BOOLEAN:
       DeleteParent(k, granted, commits)
  \/ \E r \in Regions, granted \in BOOLEAN: Read(r, granted)

TypeOK ==
  /\ rows \subseteq KnownRows /\ live \subseteq Resources
  /\ last.region \in Regions
  /\ last.result \in {"none", "created", "replay", "removed", "absent", "removed-parent",
                     "denied", "dependent", "read"}

OneBinding ==
  \A b, c \in rows:
    b.region = c.region /\ (b.id = c.id \/ (b.owner = "native" /\ c.owner = "native" /\ b.sub = c.sub))
      => b = c
LegacyAndPeerFrame ==
  /\ {Legacy, Peer} \subseteq rows
  /\ \A b \in rows : b.owner = "native" => {b.tab, b.sub} \cap LegacyKeys = {}
RegionalBindings == \A b \in rows : Region(b.tab) = b.region /\ Region(b.sub) = b.region
SameVPC == \A b \in rows : Vpc[b.tab] = Vpc[b.sub]

LiveEndpoints == \A b \in rows : {b.tab, b.sub, Vpc[b.tab]} \subseteq live
ExactPairAuthority ==
  /\ last.op = "associate" /\ last.result \in {"created", "replay"} =>
       last.decision.available /\ last.decision.tab = last.tab /\ last.decision.sub = last.sub
  /\ last.op = "remove" /\ last.result = "removed" =>
       \E b \in last.previousRows :
         b.region = last.region /\ b.id = last.id /\ b.owner = "native"
         /\ last.decision.available /\ last.decision.tab = b.tab /\ last.decision.sub = b.sub

CommitFrame ==
  last.op \in {"associate", "remove", "delete-parent"} /\ ~last.commits =>
    rows = last.previousRows /\ live = last.previousLive
    /\ last.result \notin {"created", "replay", "removed", "removed-parent"}

CreateOrReplay ==
  last.op = "associate" =>
    LET oldMatches == Matching(last.previousRows, last.region, last.tab, last.sub)
        eligible == {last.tab, last.sub, Vpc[last.tab]} \subseteq last.previousLive
                    /\ {last.tab, last.sub} \cap LegacyKeys = {}
                    /\ Region(last.tab) = last.region /\ Region(last.sub) = last.region
                    /\ Vpc[last.tab] = Vpc[last.sub]
        vacant == NativeFor(last.previousRows, last.region, last.sub) = {}
                  /\ \E id \in Slots : Find(last.previousRows, last.region, id) = Missing
        authorized == last.decision.available /\ last.decision.tab = last.tab
                      /\ last.decision.sub = last.sub
    IN /\ live = last.previousLive
       /\ last.result = "created" =>
            last.id \in Slots
            /\ rows = last.previousRows \cup {Binding(last.region, last.id, last.tab, last.sub, "native")}
       /\ last.result # "created" => rows = last.previousRows
       /\ authorized /\ last.commits /\ eligible /\ vacant => last.result = "created"
       /\ authorized /\ last.commits /\ oldMatches # {} =>
            last.result = "replay" /\ \E b \in oldMatches : last.id = b.id

ExactRemoval ==
  last.op = "remove" =>
    /\ live = last.previousLive
    /\ rows = (IF last.result = "removed"
               THEN {b \in last.previousRows : b.region # last.region \/ b.id # last.id}
               ELSE last.previousRows)
    /\ (\E b \in last.previousRows :
           b.region = last.region /\ b.id = last.id /\ b.owner = "native"
           /\ last.decision.available /\ last.decision.tab = b.tab /\ last.decision.sub = b.sub
           /\ last.commits) => last.result = "removed"

MissingIsOpaque ==
  last.op = "remove" =>
    LET b == Find(last.previousRows, last.region, last.id)
    IN (b = Missing \/ b.owner # "native") =>
       last.result = (IF last.decision.available /\ last.decision.detail THEN "absent" ELSE "denied")

ParentSafety ==
  last.op = "delete-parent" =>
    LET k == last.parent
        granted == last.parentGrant /\ k \in last.previousLive /\ k \notin LegacyKeys
        expected == granted /\ last.commits /\ ~Dependent(last.previousRows, last.previousLive, k)
    IN /\ rows = last.previousRows
       /\ last.result = (IF expected THEN "removed-parent"
                         ELSE IF granted /\ Dependent(last.previousRows, last.previousLive, k)
                              THEN "dependent" ELSE "denied")
       /\ live = (IF expected THEN last.previousLive \ {k} ELSE last.previousLive)

ScopedExplicitRead ==
  last.op = "read" =>
    /\ rows = last.previousRows /\ live = last.previousLive
    /\ last.seen = (IF last.readGrant
                    THEN {[id |-> b.id, tab |-> b.tab, sub |-> b.sub, main |-> FALSE] :
                           b \in {b \in rows : b.region = last.region}}
                    ELSE {})
=============================================================================
