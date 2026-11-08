---------------------- MODULE IAMInstanceProfiles ----------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Profiles, Roles, P, Q, R, S, MaxRequests
VARIABLES db, last, requests
vars == <<db, last, requests>>

\* Names are local IAM names, not authenticated users or tenants. Generations
\* abstract retained provider IDs. This model does not issue guest credentials.
Blank == [live |-> FALSE, gen |-> 0]
Live == [live |-> TRUE, gen |-> 1]
Base == [profiles |-> [p \in Profiles |-> Blank],
         roles |-> [r \in Roles |-> Live], edges |-> {}]
Edge(p, pg, r, rg) == [p |-> p, pg |-> pg, r |-> r, rg |-> rg]
ForProfile(s, p) == {e \in s.edges : e.p = p}
ForRole(s, r) == {e \in s.edges : e.r = r}
SameRole(s, p, r) == {e \in s.edges : e.p = p /\ e.r = r}
ProfileOps == {"DeleteProfile", "AddRole", "RemoveRole"}
RoleOps == {"AddRole", "RemoveRole", "DeleteRole"}
Ops == ProfileOps \cup {"CreateProfile", "DeleteRole", "CreateRole"}
Req(op, p, r, freshP, freshR, allowed, passRole, storage) ==
  [op |-> op, p |-> p, r |-> r, freshP |-> freshP, freshR |-> freshR,
   allowed |-> allowed, passRole |-> passRole, storage |-> storage]
NoRequest == Req("None", P, R, TRUE, TRUE, TRUE, TRUE, TRUE)
InBounds(s, q) ==
  /\ (q.op # "CreateProfile" \/ s.profiles[q.p].gen < 2)
  /\ (q.op # "CreateRole" \/ s.roles[q.r].gen < 2)
Missing(s, q) ==
  (q.op \in ProfileOps /\ ~s.profiles[q.p].live)
  \/ (q.op \in RoleOps /\ ~s.roles[q.r].live)
  \/ (q.op = "RemoveRole" /\ SameRole(s, q.p, q.r) = {})
Conflict(s, q) ==
  (q.op = "CreateProfile" /\ s.profiles[q.p].live)
  \/ (q.op = "CreateRole" /\ s.roles[q.r].live)
  \/ (q.op = "DeleteProfile" /\ ForProfile(s, q.p) # {})
  \/ (q.op = "DeleteRole" /\ ForRole(s, q.r) # {})
  \/ (q.op = "AddRole" /\ ForProfile(s, q.p) # {})
Outcome(s, q) ==
  IF ~q.allowed THEN "Denied"
  ELSE IF q.op = "AddRole" /\ ~q.passRole THEN "Denied"
  ELSE IF Missing(s, q) THEN "Missing"
  ELSE IF q.op \in ProfileOps /\ ~q.freshP THEN "StaleProfile"
  ELSE IF q.op \in RoleOps /\ ~q.freshR THEN "StaleRole"
  ELSE IF Conflict(s, q) THEN "Conflict"
  ELSE IF ~q.storage THEN "StorageError"
  ELSE "Accepted"
NewProfileGeneration(s, p) == s.profiles[p].gen + 1
Candidate(s, q) ==
  CASE q.op = "CreateProfile" ->
    [s EXCEPT !.profiles[q.p] = [live |-> TRUE, gen |-> NewProfileGeneration(s, q.p)]]
  [] q.op = "DeleteProfile" -> [s EXCEPT !.profiles[q.p].live = FALSE]
  [] q.op = "CreateRole" -> [s EXCEPT !.roles[q.r] = [live |-> TRUE, gen |-> @.gen + 1]]
  [] q.op = "DeleteRole" -> [s EXCEPT !.roles[q.r].live = FALSE]
  [] q.op = "AddRole" -> [s EXCEPT !.edges = @ \cup
       {Edge(q.p, s.profiles[q.p].gen, q.r, s.roles[q.r].gen)}]
  [] q.op = "RemoveRole" -> [s EXCEPT !.edges = @ \ SameRole(s, q.p, q.r)]
Commit(s, q, result) == IF result = "Accepted" THEN Candidate(s, q) ELSE s
Recorded(q, result) == IF result = "Accepted" THEN q ELSE NoRequest
Step(q) ==
  /\ requests < MaxRequests /\ InBounds(db, q)
  /\ LET result == Outcome(db, q) IN
       /\ db' = Commit(db, q, result)
       /\ last' = [before |-> db, request |-> Recorded(q, result), result |-> result]
  /\ requests' = requests + 1

\* Four API-reachable seeds: no profile; two empty profiles; P attached to R
\* with empty Q; and deleted/recreated P at generation 2 with empty Q. The
\* last seed's prelude explicitly includes Create/Delete/Create P, not a proof
\* that arbitrary long histories are covered by the two-request suffix.
Seed(i) ==
  CASE i = 0 -> Base
  [] i = 1 -> [Base EXCEPT !.profiles = [p \in Profiles |-> Live]]
  [] i = 2 -> [Base EXCEPT !.profiles = [p \in Profiles |-> Live],
                          !.edges = {Edge(P, 1, R, 1)}]
  [] i = 3 -> [Base EXCEPT !.profiles = [p \in Profiles |-> Live],
                          !.profiles[P].gen = 2]
Init ==
  /\ \E i \in 0..3 : db = Seed(i)
  /\ last = [before |-> db, request |-> NoRequest, result |-> "Initial"]
  /\ requests = 0
Next ==
  (\E op \in Ops, p \in Profiles, r \in Roles,
      fp \in BOOLEAN, fr \in BOOLEAN, a \in BOOLEAN, pass \in BOOLEAN, ok \in BOOLEAN :
        Step(Req(op, p, r, fp, fr, a, pass, ok)))
  \/ (requests = MaxRequests /\ UNCHANGED vars)

TypeOK ==
  /\ db.profiles \in [Profiles -> [live : BOOLEAN, gen : 0..2]]
  /\ db.roles \in [Roles -> [live : BOOLEAN, gen : 0..2]]
  /\ db.edges \subseteq [p : Profiles, pg : 0..2, r : Roles, rg : 0..2]
  /\ requests \in 0..MaxRequests
Accepted == last.result = "Accepted"
AuthorizationRequired == Accepted => last.request.allowed
PassRoleRequired == Accepted /\ last.request.op = "AddRole" => last.request.passRole
ProfileGenerationGuard == Accepted /\ last.request.op \in ProfileOps => last.request.freshP
RoleGenerationGuard == Accepted /\ last.request.op \in RoleOps => last.request.freshR
FailureFrame == last.result \notin {"Accepted", "Initial"} => db = last.before
SingleRole == \A p \in Profiles : Cardinality(ForProfile(db, p)) <= 1
AttachmentAuthority == \A e \in db.edges :
  /\ db.profiles[e.p].live /\ db.roles[e.r].live
  /\ e.pg = db.profiles[e.p].gen /\ e.rg = db.roles[e.r].gen
FreshProfileIdentity == Accepted /\ last.request.op = "CreateProfile" =>
  db.profiles[last.request.p].gen = last.before.profiles[last.request.p].gen + 1
PeerFrame == Accepted =>
  /\ \A p \in Profiles \ {last.request.p} : db.profiles[p] = last.before.profiles[p]
  /\ \A r \in Roles \ {last.request.r} : db.roles[r] = last.before.roles[r]
  /\ \A p \in Profiles \ {last.request.p} : ForProfile(db, p) = ForProfile(last.before, p)
RemovalExact == Accepted /\ last.request.op = "RemoveRole" =>
  db.edges = last.before.edges \ SameRole(last.before, last.request.p, last.request.r)
=============================================================================
