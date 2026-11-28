----------------------- MODULE KMSScheduledDeletion -----------------------
EXTENDS Naturals
VARIABLES db, day, last
vars == <<db, day, last>>

\* Sample a single-region customer-managed key, its live sibling and two
\* aliases/grants. The movable alias can be retargeted before expiry.
Days == {0, 6, 7, 8, 29, 30, 31}
Omitted == 0
Windows == {Omitted, 6, 7, 30, 31}
States == {"Enabled","Disabled","Pending","Gone"}
Initial(s) == [state |-> s, deadline |-> 0, material |-> TRUE, grant |-> TRUE,
               alias |-> "target", peerMaterial |-> TRUE, peerGrant |-> TRUE,
               peerAlias |-> TRUE]
Note(kind, expect, want, ok) == [kind |-> kind, expect |-> expect, want |-> want, ok |-> ok]
Init ==
 /\ db \in {Initial("Enabled"), Initial("Disabled")}
 /\ day = 0
 /\ last = Note("start",db,TRUE,TRUE)

Waiting == db.state = "Pending"
Due == Waiting /\ day >= db.deadline
\* Public observation of a due key must first pass the trusted atomic reap.
Schedule(window, permitted, fault) ==
 LET days == IF window = Omitted THEN 30 ELSE window
     inputOK == days \in 7..30
     allowed == permitted /\ inputOK /\ db.state \in {"Enabled","Disabled"}
     success == allowed /\ ~fault
     written == [db EXCEPT !.state = "Pending", !.deadline = day + days]
     promisedDays == IF window = Omitted THEN 30 ELSE window
     expectOK == permitted /\ promisedDays \in 7..30 /\ db.state \in {"Enabled","Disabled"} /\ ~fault
     expected == IF expectOK THEN [db EXCEPT !.state = "Pending", !.deadline = day + promisedDays] ELSE db
 IN /\ ~Due
    /\ db' = IF success THEN written ELSE db
    /\ last' = Note("schedule",expected,expectOK,success)
    /\ UNCHANGED day

Cancel(permitted, fault) ==
 LET success == permitted /\ Waiting /\ ~fault
     changed == [db EXCEPT !.state = "Disabled", !.deadline = 0]
     expectOK == permitted /\ db.state = "Pending" /\ day < db.deadline /\ ~fault
     expected == IF expectOK THEN [db EXCEPT !.state = "Disabled", !.deadline = 0] ELSE db
 IN /\ ~Due
    /\ db' = IF success THEN changed ELSE db
    /\ last' = Note("cancel",expected,expectOK,success)
    /\ UNCHANGED day

\* A fault on any component of the purge represents rollback of the unit.
Reap(fault) ==
 LET crossed == Waiting /\ day >= db.deadline
     commit == crossed /\ ~fault
     purged == [db EXCEPT !.state = "Gone", !.deadline = 0, !.material = FALSE,
                  !.grant = FALSE, !.alias = IF @ = "target" THEN "none" ELSE @]
     shouldPurge == db.state = "Pending" /\ db.deadline <= day /\ ~fault
     expectedAlias == IF shouldPurge /\ db.alias = "target" THEN "none" ELSE db.alias
     expected == [state |-> IF shouldPurge THEN "Gone" ELSE db.state,
                  deadline |-> IF shouldPurge THEN 0 ELSE db.deadline,
                  material |-> db.material /\ ~shouldPurge, grant |-> db.grant /\ ~shouldPurge,
                  alias |-> expectedAlias, peerMaterial |-> db.peerMaterial,
                  peerAlias |-> db.peerAlias, peerGrant |-> db.peerGrant]
 IN /\ db' = IF commit THEN purged ELSE db
    /\ last' = Note("reap",expected,shouldPurge,commit)
    /\ UNCHANGED day

\* These representative operations check pending-state visibility and the
\* source/destination alias distinction; the grant model owns signed identity.
Operations == {"read","crypto","enable","disable","signedDescribe","revoke",
               "createGrant","createAlias","deleteAlias","moveAway","moveOnto"}
Aux(op) ==
 LET present == db.state # "Gone"
     stable == db.state \in {"Enabled","Disabled"}
     allowed == CASE op = "read" -> present
                  [] op = "signedDescribe" -> present /\ db.grant
                  [] op = "crypto" -> db.state = "Enabled"
                  [] op = "createGrant" -> db.state = "Enabled" /\ ~db.grant
                  [] op = "revoke" -> present /\ db.grant
                  [] op \in {"enable","disable"} -> stable
                  [] op = "createAlias" -> db.alias = "none" /\ stable
                  [] op = "deleteAlias" -> db.alias # "none"
                  [] op = "moveAway" -> db.alias = "target"
                  [] OTHER -> db.alias = "peer" /\ stable
     change == CASE op = "enable" -> [db EXCEPT !.state = "Enabled"]
                 [] op = "disable" -> [db EXCEPT !.state = "Disabled"]
                 [] op = "createGrant" -> [db EXCEPT !.grant = TRUE]
                 [] op = "revoke" -> [db EXCEPT !.grant = FALSE]
                 [] op = "deleteAlias" -> [db EXCEPT !.alias = "none"]
                 [] op = "moveAway" -> [db EXCEPT !.alias = "peer"]
                 [] op \in {"createAlias","moveOnto"} -> [db EXCEPT !.alias = "target"]
                 [] OTHER -> db
     expectedOK == ~Due /\ CASE op = "read" -> db.state \in {"Enabled","Disabled","Pending"}
                     [] op = "signedDescribe" -> db.grant /\ present
                     [] op \in {"crypto","createGrant"} ->
                          db.state = "Enabled" /\ (op = "crypto" \/ ~db.grant)
                     [] op = "revoke" -> db.grant /\ present
                     [] op = "createAlias" -> db.alias = "none" /\ db.state \in {"Enabled","Disabled"}
                     [] op = "deleteAlias" -> db.alias \in {"target","peer"}
                     [] op = "moveAway" -> "target" = db.alias
                     [] op = "moveOnto" -> db.alias = "peer" /\ db.state \in {"Enabled","Disabled"}
                     [] OTHER -> db.state \in {"Enabled","Disabled"}
     expected == IF ~expectedOK THEN db ELSE
                  [db EXCEPT !.alias = CASE op = "moveAway" -> "peer"
                                         [] op \in {"createAlias","moveOnto"} -> "target"
                                         [] op = "deleteAlias" -> "none" [] OTHER -> @,
                             !.grant = CASE op = "revoke" -> FALSE [] op = "createGrant" -> TRUE [] OTHER -> @,
                             !.state = CASE op = "enable" -> "Enabled" [] op = "disable" -> "Disabled" [] OTHER -> @]
 IN /\ ~Due
    /\ db' = IF allowed THEN change ELSE db
    /\ last' = Note("operation",expected,expectedOK,allowed)
    /\ UNCHANGED day

Clock(t) ==
 /\ t \in Days
 /\ day' = t
 /\ UNCHANGED db
 /\ last' = Note("clock",db,TRUE,TRUE)
Restart ==
 /\ db' = db
 /\ UNCHANGED day
 /\ last' = Note("restart",db,TRUE,TRUE)
Next ==
 \/ \E w \in Windows, p \in BOOLEAN, f \in BOOLEAN: Schedule(w,p,f)
 \/ \E p \in BOOLEAN, f \in BOOLEAN: Cancel(p,f)
 \/ \E fault \in BOOLEAN: Reap(fault)
 \/ \E op \in Operations: Aux(op)
 \/ \E t \in Days: Clock(t)
 \/ Restart

TypeOK ==
 /\ db.state \in States /\ db.deadline \in 0..61 /\ day \in Days
 /\ db.alias \in {"target","peer","none"}
 /\ \A flag \in {db.material,db.grant,db.peerAlias,db.peerMaterial,db.peerGrant}: flag \in BOOLEAN
 /\ last.ok \in BOOLEAN /\ last.want \in BOOLEAN
ScheduleContract == last.kind # "schedule" \/ (last.ok = last.want /\ db = last.expect)
CancelContract == last.kind # "cancel" \/ (last.ok = last.want /\ db = last.expect)
ExpiryBoundary == last.kind # "reap" \/ last.ok = last.want
AtomicExpiry == last.kind # "reap" \/ db = last.expect
OperationMatrix == last.kind # "operation" \/ (last.ok = last.want /\ db = last.expect)
RestartPreservesDeadline == last.kind # "restart" \/ db = last.expect
TimePreservesStorage == last.kind # "clock" \/ db = last.expect
=============================================================================
