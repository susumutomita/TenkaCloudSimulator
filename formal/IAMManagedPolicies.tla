------------------------- MODULE IAMManagedPolicies -------------------------
EXTENDS Naturals, FiniteSets

\* Parsed fragments for ONE fixed action/resource. 0 is absent (never a version).
\* Policy/role names model one local account, not tenant/authentication labels.
CONSTANTS Policies, Roles, P, Q, R, S, VersionIDs, MaxVersions, MaxRequests
VARIABLES db, last, requests
vars == <<db, last, requests>>
Docs == {1, 2, 3}
Allow(d) == IF d \in {1, 3} THEN 1 ELSE 0
Deny(d) == IF d \in {2, 3} THEN 1 ELSE 0
ZeroView == [allow |-> 0, deny |-> 0]
EmptyVersions == [v \in VersionIDs |-> 0]
EmptyPolicy == [live |-> FALSE, gen |-> 0, default |-> 0,
                next |-> 1, versions |-> EmptyVersions]
EmptyRole == [live |-> TRUE, gen |-> 1, inline |-> 0]
EmptyEdge == [rg |-> 0, pg |-> 0]
EmptyEdges == [r \in Roles |-> [p \in Policies |-> EmptyEdge]]
Attached(s, r, p) == s.edges[r][p].pg # 0
RoleAttachments(s, r) == {p \in Policies : Attached(s, r, p)}
PolicyRoles(s, p) == {r \in Roles : Attached(s, r, p)}
Retained(s, p) == {v \in VersionIDs : s.policies[p].versions[v] # 0}
DefaultDoc(s, p) ==
  IF s.policies[p].live /\ s.policies[p].default \in VersionIDs
  THEN s.policies[p].versions[s.policies[p].default] ELSE 0
ManagedAllow(s, r, p) == IF Attached(s,r,p) THEN Allow(DefaultDoc(s,p)) ELSE 0
ManagedDeny(s, r, p) == IF Attached(s,r,p) THEN Deny(DefaultDoc(s,p)) ELSE 0
ExpectedView(s, r) ==
  IF ~s.roles[r].live THEN ZeroView
  ELSE [allow |-> Allow(s.roles[r].inline) + ManagedAllow(s,r,P) + ManagedAllow(s,r,Q),
        deny |-> Deny(s.roles[r].inline) + ManagedDeny(s,r,P) + ManagedDeny(s,r,Q)]
\* Construction updates an actual state variable. The invariant does not DEFINE it.
BuildView(s, r) ==
  IF ~s.roles[r].live THEN ZeroView
  ELSE [allow |-> Allow(s.roles[r].inline) + ManagedAllow(s,r,P) + ManagedAllow(s,r,Q),
        deny |-> Deny(s.roles[r].inline) + ManagedDeny(s,r,P) + ManagedDeny(s,r,Q)]
BuildViews(s) == [r \in Roles |-> BuildView(s,r)]
AggregateFits(s) == \A r \in Roles : ExpectedView(s,r).allow + ExpectedView(s,r).deny <= 3
Decision(v) == IF v.deny > 0 THEN "explicitDeny"
               ELSE IF v.allow > 0 THEN "allowed" ELSE "implicitDeny"

Req(op,p,r,v,d,sd) == [op |-> op, p |-> p, r |-> r, v |-> v, doc |-> d, sd |-> sd]
Requests ==
  {Req("CreatePolicy",p,R,1,d,FALSE) : p \in Policies, d \in Docs}
  \cup {Req("CreateVersion",p,R,1,d,b) : p \in Policies, d \in Docs, b \in BOOLEAN}
  \cup {Req("SetDefault",p,R,v,1,FALSE) : p \in Policies, v \in VersionIDs}
  \cup {Req("DeleteVersion",p,R,v,1,FALSE) : p \in Policies, v \in VersionIDs}
  \cup {Req("Attach",p,r,1,1,FALSE) : p \in Policies, r \in Roles}
  \cup {Req("Detach",p,r,1,1,FALSE) : p \in Policies, r \in Roles}
  \cup {Req("DeletePolicy",p,R,1,1,FALSE) : p \in Policies}
  \cup {Req("DeleteRole",P,r,1,1,FALSE) : r \in Roles}
  \cup {Req("CreateRole",P,r,1,1,FALSE) : r \in Roles}
  \cup {Req("PutInline",P,r,1,d,FALSE) : r \in Roles, d \in {1,2}}
  \cup {Req("DeleteInline",P,r,1,1,FALSE) : r \in Roles}
RoleOps == {"Attach","Detach","DeleteRole","PutInline","DeleteInline"}
PolicyOps == {"CreateVersion","SetDefault","DeleteVersion","Attach","Detach","DeletePolicy"}
Flip(q) == q.op = "SetDefault" \/ (q.op = "CreateVersion" /\ q.sd)
NewVersion(s,p) == s.policies[p].next
\* Exploration stops allocating identities after these finite carrier sets end.
\* This is not an API error or a claim about the production sequence limit.
InBounds(s,q) ==
  /\ (q.op # "CreateVersion" \/ s.policies[q.p].next \in VersionIDs)
  /\ (q.op # "CreatePolicy" \/ s.policies[q.p].gen < 2)
  /\ (q.op # "CreateRole" \/ s.roles[q.r].gen < 2)
Missing(s,q) ==
  (q.op \in PolicyOps /\ ~s.policies[q.p].live)
  \/ (q.op \in RoleOps /\ ~s.roles[q.r].live)
  \/ (q.op \in {"SetDefault","DeleteVersion"} /\ q.v \notin Retained(s,q.p))
  \/ (q.op = "DeleteInline" /\ s.roles[q.r].inline = 0)
Conflict(s,q) ==
  (q.op = "CreatePolicy" /\ s.policies[q.p].live)
  \/ (q.op = "CreateRole" /\ s.roles[q.r].live)
  \/ (q.op = "DeleteVersion" /\ q.v = s.policies[q.p].default)
  \/ (q.op = "DeletePolicy" /\ (PolicyRoles(s,q.p) # {} \/ Cardinality(Retained(s,q.p)) # 1))
  \/ (q.op = "DeleteRole" /\ (RoleAttachments(s,q.r) # {} \/ s.roles[q.r].inline # 0))
VersionFull(s,q) == q.op = "CreateVersion" /\ Cardinality(Retained(s,q.p)) >= MaxVersions

Raw(s,q) ==
  CASE q.op = "CreatePolicy" ->
    [s EXCEPT !.policies[q.p] = [live |-> TRUE, gen |-> @.gen+1, default |-> 1,
      next |-> 2, versions |-> [EmptyVersions EXCEPT ![1] = q.doc]]]
  [] q.op = "CreateVersion" ->
    [s EXCEPT !.policies[q.p].versions[NewVersion(s,q.p)] = q.doc,
              !.policies[q.p].next = @+1,
              !.policies[q.p].default = IF q.sd THEN NewVersion(s,q.p) ELSE @]
  [] q.op = "SetDefault" -> [s EXCEPT !.policies[q.p].default = q.v]
  [] q.op = "DeleteVersion" -> [s EXCEPT !.policies[q.p].versions[q.v] = 0]
  [] q.op = "Attach" -> [s EXCEPT !.edges[q.r][q.p] =
                          [rg |-> s.roles[q.r].gen, pg |-> s.policies[q.p].gen]]
  [] q.op = "Detach" -> [s EXCEPT !.edges[q.r][q.p] = EmptyEdge]
  [] q.op = "DeletePolicy" -> [s EXCEPT !.policies[q.p].live = FALSE,
     !.policies[q.p].default = 0, !.policies[q.p].versions = EmptyVersions]
  [] q.op = "DeleteRole" -> [s EXCEPT !.roles[q.r].live = FALSE, !.roles[q.r].inline = 0]
  [] q.op = "CreateRole" -> [s EXCEPT !.roles[q.r] = [live |-> TRUE, gen |-> @.gen+1, inline |-> 0]]
  [] q.op = "PutInline" -> [s EXCEPT !.roles[q.r].inline = q.doc]
  [] q.op = "DeleteInline" -> [s EXCEPT !.roles[q.r].inline = 0]
Candidate(s,q) ==
  LET raw == Raw(s,q)
      views == IF Flip(q) THEN BuildViews(raw)
               ELSE IF q.op = "Attach" THEN BuildViews(raw)
               ELSE IF q.op = "Detach" THEN BuildViews(raw)
               ELSE BuildViews(raw)
  IN [raw EXCEPT !.views = views]
Result(s,q,authorized,storageOK) ==
  IF ~authorized THEN "Denied"
  ELSE IF Missing(s,q) THEN "Missing"
  ELSE IF Conflict(s,q) THEN "Conflict"
  ELSE IF VersionFull(s,q) THEN "Limit"
  ELSE IF ~AggregateFits(Raw(s,q)) THEN "Limit"
  ELSE IF ~storageOK THEN "StorageError"
  ELSE "Accepted"
Commit(s,q,result) == IF result = "Accepted" THEN Candidate(s,q) ELSE s
Step(q,authorized,storageOK) ==
  /\ requests < MaxRequests /\ InBounds(db,q)
  /\ LET result == Result(db,q,authorized,storageOK)
     IN /\ db' = Commit(db,q,result)
        /\ last' = [before |-> db, req |-> q, outcome |-> result]
  /\ requests' = requests+1

Base == [policies |-> [p \in Policies |-> EmptyPolicy],
         roles |-> [r \in Roles |-> EmptyRole], edges |-> EmptyEdges,
         views |-> [r \in Roles |-> ZeroView]]
Seed(which) ==
  LET initial == [Base EXCEPT !.roles[R].inline = 1]
      liveP == [live |-> TRUE, gen |-> 1, default |-> 1, next |-> 3,
                versions |-> [v \in VersionIDs |-> CASE v=1 -> 1 [] v=2 -> 2 [] OTHER -> 0]]
      liveQ == [live |-> TRUE, gen |-> 1, default |-> 1, next |-> 2,
                versions |-> [EmptyVersions EXCEPT ![1] = 1]]
      seeded == IF which = "Empty" THEN initial
                ELSE [initial EXCEPT !.policies[P] = liveP, !.policies[Q] = liveQ,
                        !.edges[R][P] = [rg |-> 1, pg |-> 1],
                        !.edges[S][IF which = "Shared" THEN P ELSE Q] = [rg |-> 1, pg |-> 1]]
  IN [seeded EXCEPT !.views = BuildViews(seeded)]
Init ==
  /\ db \in {Seed("Empty"),Seed("Shared"),Seed("Peer")}
  /\ last = [before |-> db, req |-> Req("Initial",P,R,1,1,FALSE), outcome |-> "Initial"]
  /\ requests = 0
Next == (\E q \in Requests, a \in BOOLEAN, ok \in BOOLEAN : Step(q,a,ok)) \/ UNCHANGED vars

StateType(s) ==
  /\ s.policies \in [Policies -> [live: BOOLEAN, gen: 0..2, default: 0..3, next: 1..4,
                                  versions: [VersionIDs -> 0..3]]]
  /\ s.roles \in [Roles -> [live: BOOLEAN, gen: 1..2, inline: 0..3]]
  /\ s.edges \in [Roles -> [Policies -> [rg: 0..2, pg: 0..2]]]
  /\ s.views \in [Roles -> [allow: 0..3, deny: 0..3]]
  /\ (\A p \in Policies : s.policies[p].live => s.policies[p].gen \in 1..2)
TypeOK == /\ StateType(db) /\ StateType(last.before) /\ requests \in 0..MaxRequests
          /\ last.outcome \in {"Initial","Accepted","Denied","Missing","Conflict","Limit","StorageError"}
FailureFrame == last.outcome \notin {"Initial","Accepted"} => db = last.before
DeleteDependencies ==
  last.outcome = "Accepted" =>
    /\ (last.req.op = "DeleteVersion" => last.req.v # last.before.policies[last.req.p].default)
    /\ (last.req.op = "DeletePolicy" =>
          PolicyRoles(last.before,last.req.p) = {} /\ Cardinality(Retained(last.before,last.req.p)) = 1)
    /\ (last.req.op = "DeleteRole" =>
          RoleAttachments(last.before,last.req.r) = {} /\ last.before.roles[last.req.r].inline = 0)
VersionIDsMonotonic ==
  (last.outcome = "Accepted" /\ last.req.op = "CreateVersion") =>
    LET p == last.req.p IN
      /\ db.policies[p].next = last.before.policies[p].next+1
      /\ {v \in VersionIDs : db.policies[p].versions[v] # 0 /\ last.before.policies[p].versions[v] = 0}
           = {last.before.policies[p].next}
FreshIncarnations == last.outcome = "Accepted" =>
  /\ (last.req.op = "CreatePolicy" =>
        db.policies[last.req.p].gen = last.before.policies[last.req.p].gen+1)
  /\ (last.req.op = "CreateRole" =>
        db.roles[last.req.r].gen = last.before.roles[last.req.r].gen+1)
VersionCap == \A p \in Policies : Cardinality(Retained(db,p)) <= MaxVersions
DefaultExists == \A p \in Policies : db.policies[p].live => db.policies[p].default \in Retained(db,p)
AttachmentAuthority == \A r \in Roles, p \in Policies : Attached(db,r,p) =>
  /\ db.roles[r].live /\ db.policies[p].live
  /\ db.edges[r][p].rg = db.roles[r].gen /\ db.edges[r][p].pg = db.policies[p].gen
ExplicitDenyWins == \A r \in Roles : ExpectedView(db,r).deny > 0 => Decision(db.views[r]) = "explicitDeny"
AggregateBudget == \A r \in Roles : db.views[r].allow + db.views[r].deny <= 3
RoleViewsExact == \A r \in Roles : db.views[r] = ExpectedView(db,r)
Affected(s,q) == IF q.op \in RoleOps \cup {"CreateRole"} THEN {q.r}
                 ELSE IF Flip(q) THEN PolicyRoles(s,q.p) ELSE {}
PeerFrame == last.outcome = "Accepted" =>
  /\ (\A p \in Policies \ {last.req.p} : db.policies[p] = last.before.policies[p])
  /\ (\A r \in Roles \ {last.req.r} : db.roles[r] = last.before.roles[r])
  /\ (\A r \in Roles \ Affected(last.before,last.req) :
        db.views[r] = last.before.views[r] /\ db.edges[r] = last.before.edges[r])
ImmutableVersions == \A p \in Policies, v \in VersionIDs :
  (db.policies[p].gen = last.before.policies[p].gen /\ db.policies[p].versions[v] # 0
    /\ last.before.policies[p].versions[v] # 0) =>
      db.policies[p].versions[v] = last.before.policies[p].versions[v]
=============================================================================
