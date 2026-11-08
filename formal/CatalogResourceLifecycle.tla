---------------------- MODULE CatalogResourceLifecycle ----------------------
(* Finite projection of the model-driven AWS catalog store.                *)
(* One scope is one (service, region) partition. A step authorizes, then    *)
(* commits atomically or leaves the database unchanged. Parents and         *)
(* children share a scope; children reference live parents only.           *)
EXTENDS Naturals, FiniteSets
CONSTANTS ScopeA, ScopeB, None, MaxSteps, Quota
Scopes == {ScopeA, ScopeB}
IDs == 1..3
Kids == {"k1", "k2"}
Names == {"same", "other", "third"}
Tokens == {"t1"}
Gens == 0..3
Row == [name : Names, gen : Gens]
RecordSet == [rows : [IDs -> Row \cup {None}], kids : [Kids -> IDs \cup {None}],
              next : 1..4, tokens : [Tokens -> IDs \cup {None}]]
Ops == {"Create", "Update", "Delete", "AddChild", "RemoveChild"}
Statuses == {"Init", "Accepted", "Replay", "Denied", "Fault", "Quota",
             "Conflict", "Missing", "Stale", "Dependents"}
VARIABLES db, steps, last
vars == <<db, steps, last>>
Empty == [rows |-> [i \in IDs |-> None], kids |-> [k \in Kids |-> None],
          next |-> 1, tokens |-> [t \in Tokens |-> None]]
Init == /\ db = [s \in Scopes |-> Empty] /\ steps = 0
        /\ last = [op |-> "Create", scope |-> ScopeA, id |-> 1, status |-> "Init", token |-> "none",
                   allowed |-> TRUE, observed |-> 0, before |-> [s \in Scopes |-> Empty]]
Live(rows) == {i \in IDs : rows[i] # None}
NewID(s) == db[s].next
Children(before, id) == {k \in Kids : before.kids[k] = id}
Step(op, s, id, name, kid, token, observed, allowed, ok, cascade) ==
 LET before == db[s]
     exists == before.rows[id] # None
     current == IF exists THEN before.rows[id].gen ELSE 0
     replay == op = "Create" /\ token # "none" /\ before.tokens[token] # None
     room == Cardinality(Live(before.rows)) < Quota /\ before.next \in IDs
     taken == \E i \in Live(before.rows) : before.rows[i].name = name
     status ==
       IF ~allowed THEN "Denied"
       ELSE IF replay THEN "Replay"
       ELSE IF op = "Create" /\ ~room THEN "Quota"
       ELSE IF op = "Create" /\ taken THEN "Conflict"
       ELSE IF op \in {"Update", "Delete", "AddChild"} /\ ~exists THEN "Missing"
       ELSE IF op = "RemoveChild" /\ before.kids[kid] = None THEN "Missing"
       ELSE IF op \in {"Update", "Delete"} /\ observed # current THEN "Stale"
       ELSE IF op = "Delete" /\ Children(before, id) # {} /\ ~cascade THEN "Dependents"
       ELSE IF op = "AddChild" /\ before.kids[kid] # None THEN "Conflict"
       ELSE IF ~ok THEN "Fault" ELSE "Accepted"
     candidate ==
       IF op = "Create" THEN
         [before EXCEPT !.rows[NewID(s)] = [name |-> name, gen |-> 0], !.next = @ + 1,
           !.tokens = IF token = "none" THEN @ ELSE [@ EXCEPT ![token] = NewID(s)]]
       ELSE IF op = "Update" THEN [before EXCEPT !.rows[id] = [@ EXCEPT !.gen = @ + 1]]
       ELSE IF op = "Delete" THEN
         [before EXCEPT !.rows[id] = None,
           !.kids = [k \in Kids |-> IF before.kids[k] = id THEN None ELSE before.kids[k]]]
       ELSE IF op = "AddChild" THEN [before EXCEPT !.kids[kid] = id]
       ELSE [before EXCEPT !.kids[kid] = None]
     committed == IF status = "Accepted" THEN candidate ELSE before
 IN /\ steps < MaxSteps
    /\ db' = [db EXCEPT ![s] = committed]
    /\ steps' = steps + 1
    /\ last' = [op |-> op, scope |-> s, id |-> IF op = "Create" THEN NewID(s) ELSE id, token |-> token,
                status |-> status, allowed |-> allowed, observed |-> observed, before |-> db]
Next ==
 \/ \E s \in Scopes, name \in Names, token \in Tokens \cup {"none"}, a, ok \in BOOLEAN :
      Step("Create", s, 1, name, "k1", token, 0, a, ok, FALSE)
 \/ \E s \in Scopes, id \in IDs, g \in 0..1, a, ok \in BOOLEAN :
      Step("Update", s, id, "same", "k1", "none", g, a, ok, FALSE)
 \/ \E s \in Scopes, id \in IDs, g \in 0..1, a, ok, c \in BOOLEAN :
      Step("Delete", s, id, "same", "k1", "none", g, a, ok, c)
 \/ \E s \in Scopes, id \in IDs, k \in Kids, a, ok \in BOOLEAN :
      Step("AddChild", s, id, "same", k, "none", 0, a, ok, FALSE)
 \/ \E s \in Scopes, k \in Kids, a, ok \in BOOLEAN :
      Step("RemoveChild", s, 1, "same", k, "none", 0, a, ok, FALSE)
Before == last.before[last.scope]
Accepted(op) == last.op = op /\ last.status = "Accepted"
TypeOK == /\ db \in [Scopes -> RecordSet] /\ steps \in 0..MaxSteps
          /\ last.op \in Ops /\ last.status \in Statuses
FailureFrame == last.status \notin {"Init", "Accepted"} => db = last.before
AuthorizationFirst == ~last.allowed => db = last.before /\ last.status = "Denied"
PeerFrame == \A s \in Scopes : s # last.scope => db[s] = last.before[s]
QuotaBound == \A s \in Scopes : Cardinality(Live(db[s].rows)) <= Quota
UniqueNames == \A s \in Scopes : \A i, j \in Live(db[s].rows) :
                 i # j => db[s].rows[i].name # db[s].rows[j].name
FreshIDs == /\ \A s \in Scopes : \A i \in Live(db[s].rows) : i < db[s].next
            /\ Accepted("Create") => /\ last.id = Before.next
                                     /\ Before.rows[last.id] = None
                                     /\ db[last.scope].next = Before.next + 1
StaleWriteRejected ==
 (last.op \in {"Update", "Delete"} /\ last.allowed /\ Before.rows[last.id] # None
  /\ last.observed # Before.rows[last.id].gen) => db = last.before
GenerationAdvances == Accepted("Update") =>
 db[last.scope].rows[last.id].gen = Before.rows[last.id].gen + 1
ChildGuard == \A s \in Scopes : \A k \in Kids :
 db[s].kids[k] # None => db[s].rows[db[s].kids[k]] # None
ReplayNoDuplicate == last.status = "Replay" => db = last.before
TokenOnce == (Accepted("Create") /\ last.token # "none") =>
 /\ Before.tokens[last.token] = None /\ db[last.scope].tokens[last.token] = last.id
TokenBinding == \A s \in Scopes : \A t \in Tokens :
 db[s].tokens[t] # None => db[s].tokens[t] < db[s].next
Spec == Init /\ [][Next]_vars
=============================================================================
