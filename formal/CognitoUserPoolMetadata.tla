------------------------ MODULE CognitoUserPoolMetadata ------------------------
EXTENDS Naturals, FiniteSets, Sequences

\* The local four-operation pool-metadata profile, not Cognito user authentication.
\* AWS IAM action/reference and ListUserPools MaxResults/NextToken:
\* https://docs.aws.amazon.com/service-authorization/latest/reference/list_cognito-idp.html
\* https://docs.aws.amazon.com/cognito-user-identity-pools/latest/APIReference/API_ListUserPools.html
\* IDs order symbolic suffixes within a region. Peer rows are isolated fixtures.
\* One independent cursor/admission defect at a time; no actual tokens or credentials.

CONSTANT StepLimit, Capacity, None
VARIABLE s
Accounts == {"local", "peer"}
Regions == {"east", "west"}
Ids == 1..3
PoolKeys == Accounts \X Regions \X Ids
Versions == 0..1
Actor == [credential : {"credential1", "credential2"}, exercise : {"writer", "reader"}]
FirstActor == [credential |-> "credential1", exercise |-> "writer"]
MetaOps == {"CreateUserPool", "DescribeUserPool", "ListUserPools", "DeleteUserPool"}
Operations == MetaOps \cup {"Unsupported"}
Proofs == {"verified", "unsigned", "dummy", "browser", "wrongService", "wrongPrincipal", "unboundHeaders"}
AuthOutcomes == {"unauthenticated", "unsupported", "input", "denied", "cursor"}
Statuses == AuthOutcomes \cup {"ok", "collision", "quota", "missing", "restart"}
Wildcard == <<"*", "n/a", 0>>
GrantKeys == PoolKeys \cup {Wildcard}
Pages == {0, 1, 2, 60, 61}
Cursors == [account : Accounts, region : Regions, actor : Actor, epoch : Versions,
            limit : Pages, after : Ids, integrity : BOOLEAN, afterFits : BOOLEAN]
Requests == [op : Operations, region : Regions, id : Ids, actor : Actor, proof : Proofs,
             shape : BOOLEAN, grantOp : Operations, grantKey : GrantKeys,
             limit : Pages, cursor : Cursors \cup {None}]

Key(r) == <<"local", r.region, r.id>>
ExpectedResource(r) == IF r.op \in {"CreateUserPool", "ListUserPools"} THEN Wildcard ELSE Key(r)
NativeResource(r) == ExpectedResource(r)
PoolScope(r) == <<"local", r.region>>
MatchesScope(p, r) == <<p[1], p[2]>> = PoolScope(r)

Default(op, region, id, limit) ==
  LET r == [op |-> op, region |-> region, id |-> id, actor |-> FirstActor, proof |-> "verified",
            shape |-> TRUE, grantOp |-> op, grantKey |-> Wildcard, limit |-> limit, cursor |-> None]
  IN [r EXCEPT !.grantKey = ExpectedResource(r)]

Regular == {Default(op, region, id, 1) : op \in MetaOps \ {"ListUserPools"}, region \in Regions, id \in Ids}
           \cup {Default("ListUserPools", region, 1, lim) : region \in Regions, lim \in {1, 2, 60}}
Probes ==
  LET create == Default("CreateUserPool", "west", 1, 1)
      describe == Default("DescribeUserPool", "east", 1, 1)
      delete == Default("DeleteUserPool", "east", 1, 1)
      list == Default("ListUserPools", "east", 1, 1)
  IN {[create EXCEPT !.proof = p] : p \in Proofs \ {"verified"}}
     \cup {[create EXCEPT !.shape = FALSE], [delete EXCEPT !.shape = FALSE],
            [describe EXCEPT !.grantKey = <<"peer", "east", 1>>],
            [describe EXCEPT !.grantKey = Wildcard],
            [delete EXCEPT !.grantOp = "DescribeUserPool"],
            [list EXCEPT !.grantOp = "DescribeUserPool"],
            [list EXCEPT !.limit = 0], [list EXCEPT !.limit = 61],
            Default("Unsupported", "east", 1, 1)}

Resume(c) ==
  LET q == [Default("ListUserPools", c.region, 1, c.limit) EXCEPT !.cursor = c, !.actor = c.actor]
  IN {q, [q EXCEPT !.grantOp = "DescribeUserPool"], [q EXCEPT !.proof = "browser"],
      [q EXCEPT !.cursor.account = "peer"], [q EXCEPT !.cursor.integrity = FALSE],
      [q EXCEPT !.cursor.afterFits = FALSE],
      [q EXCEPT !.region = IF c.region = "east" THEN "west" ELSE "east"],
      [q EXCEPT !.actor.credential = IF c.actor.credential = "credential1" THEN "credential2" ELSE "credential1"],
      [q EXCEPT !.actor.exercise = IF c.actor.exercise = "writer" THEN "reader" ELSE "writer"],
      [q EXCEPT !.limit = IF c.limit = 1 THEN 2 ELSE 1]}

NativeProof(r) == r.proof = "verified"
ShapeOK(r) == r.shape /\ (r.op = "ListUserPools" => r.limit \in 1..60)
PolicyOK(r) == r.grantOp = r.op /\ r.grantKey = NativeResource(r)
CursorOK(r, n) ==
  IF r.cursor = None THEN TRUE ELSE
    LET c == r.cursor IN
      /\ c.integrity /\ c.afterFits /\ c.epoch = n
      /\ c.account = "local" /\ c.region = r.region
      /\ c.actor.credential = r.actor.credential /\ c.actor.exercise = r.actor.exercise
      /\ c.limit = r.limit

Admit(r, n) ==
  IF ~NativeProof(r) THEN "unauthenticated"
  ELSE IF r.op \notin MetaOps THEN "unsupported"
  ELSE IF ~ShapeOK(r) THEN "input"
  ELSE IF ~PolicyOK(r) THEN "denied"
  ELSE IF ~CursorOK(r, n) THEN "cursor"
  ELSE "ok"

Lower(r) == IF r.cursor = None THEN 0 ELSE r.cursor.after
InPageRange(p, r) == MatchesScope(p, r) /\ p[3] > Lower(r)
PageRows(rows, r) == {p \in rows : InPageRange(p, r)}
ItemRows(rows, r) == {p \in rows : p = Key(r)}
CreateAllowed(rows, r) == Cardinality({p \in rows : MatchesScope(p, r)}) < Capacity
RECURSIVE Ordered(_)
Ordered(rows) ==
  IF rows = {} THEN <<>> ELSE
    LET p == CHOOSE p \in rows : \A q \in rows : p[3] <= q[3]
    IN <<p>> \o Ordered(rows \ {p})
AfterPage(r, n, last) ==
  [account |-> "local", region |-> r.region, actor |-> r.actor, epoch |-> n,
   limit |-> r.limit, after |-> last, integrity |-> TRUE, afterFits |-> TRUE]

Perform(r, n, rows) ==
  LET gate == Admit(r, n)
      p == Key(r)
      found == IF gate = "ok" /\ r.op \in {"DescribeUserPool", "DeleteUserPool"}
               THEN ItemRows(rows, r) ELSE {}
      candidates == IF gate = "ok" /\ r.op = "ListUserPools" THEN Ordered(PageRows(rows, r)) ELSE <<>>
      count == IF Len(candidates) > r.limit THEN r.limit ELSE Len(candidates)
      page == SubSeq(candidates, 1, count)
      status == IF gate # "ok" THEN gate
                ELSE CASE r.op = "CreateUserPool" ->
                    IF ~CreateAllowed(rows, r) THEN "quota" ELSE IF p \in rows THEN "collision" ELSE "ok"
                  [] r.op \in {"DescribeUserPool", "DeleteUserPool"} ->
                    IF found = {} THEN "missing" ELSE "ok"
                  [] OTHER -> "ok"
      next == IF Len(candidates) > count THEN AfterPage(r, n, page[count][3]) ELSE None
  IN [status |-> status, touched |-> gate = "ok", next |-> next,
      rows |-> IF status # "ok" THEN rows
               ELSE CASE r.op = "CreateUserPool" -> rows \cup {p}
                      [] r.op = "DeleteUserPool" -> rows \ found
                      [] OTHER -> rows,
      output |-> IF status # "ok" THEN <<>>
                 ELSE CASE r.op = "CreateUserPool" -> <<p>>
                        [] r.op = "DescribeUserPool" -> Ordered(found)
                        [] r.op = "ListUserPools" -> page
                        [] OTHER -> <<>>]

Init == s = [rows |-> {<<"local","east",1>>, <<"local","east",3>>, <<"local","west",2>>, <<"peer","east",2>>},
             epoch |-> 0, held |-> None, step |-> 0, last |-> None]
Call(r) ==
  /\ s.step < StepLimit
  /\ LET q == Perform(r, s.epoch, s.rows)
     IN s' = [s EXCEPT !.step = @ + 1, !.rows = q.rows,
               !.held = IF q.next = None THEN @ ELSE q.next,
               !.last = [request |-> r, prior |-> s.rows, retired |-> s.epoch,
                          status |-> q.status, touched |-> q.touched, output |-> q.output, next |-> q.next]]
Restart ==
  /\ s.epoch = 0 /\ s.step < StepLimit
  /\ s' = [s EXCEPT !.step = @ + 1, !.epoch = 1, !.rows = s.rows,
            !.last = [request |-> None, prior |-> s.rows, retired |-> s.epoch,
                      status |-> "restart", touched |-> FALSE, output |-> <<>>, next |-> None]]

Next == \/ \E r \in Regular \cup Probes : Call(r)
        \/ IF s.held = None THEN FALSE ELSE \E r \in Resume(s.held) : Call(r)
        \/ Restart
        \/ UNCHANGED s

TypeOK ==
  /\ s \in [rows : SUBSET PoolKeys, epoch : Versions, held : Cursors \cup {None}, step : 0..StepLimit,
            last : [request : Requests \cup {None}, prior : SUBSET PoolKeys, retired : Versions,
                    status : Statuses, touched : BOOLEAN, output : Seq(PoolKeys), next : Cursors \cup {None}]
                   \cup {None}]

OracleAdmission(r, epoch) ==
  LET c == r.cursor
      fits == IF c = None THEN TRUE ELSE
                 c.integrity /\ c.afterFits /\ <<c.account, c.region>> = <<"local", r.region>>
                 /\ c.actor = r.actor /\ c.limit = r.limit /\ c.epoch = epoch
      failures == <<r.proof # "verified",
                    r.op \notin MetaOps,
                    ~r.shape \/ (r.op = "ListUserPools" /\ r.limit \notin 1..60),
                    r.grantOp # r.op \/ r.grantKey # ExpectedResource(r),
                    ~fits>>
      classes == <<"unauthenticated", "unsupported", "input", "denied", "cursor">>
      failed == {i \in DOMAIN failures : failures[i]}
  \* More than one gate can fail (e.g. wrong action and retired cursor).
  \* Pick the uniquely least rank, not an unspecified matching CASE arm.
  IN IF failed = {} THEN "ok"
     ELSE classes[CHOOSE i \in failed : \A j \in failed : i <= j]

Attempt == IF s.last = None THEN FALSE ELSE s.last.request # None
AdmissionBeforeStorage == IF ~Attempt THEN TRUE ELSE
  LET w == s.last
      e == OracleAdmission(w.request, s.epoch)
  IN /\ w.touched = (e = "ok")
     /\ IF e = "ok" THEN w.status \notin AuthOutcomes
        ELSE /\ w.status = e /\ w.output = <<>> /\ w.next = None

ScopedCRUD == IF ~Attempt THEN TRUE ELSE
  LET w == s.last
      r == w.request
      k == <<"local", r.region, r.id>>
      clear == w.output = <<>>
      auth == OracleAdmission(r, s.epoch) = "ok"
      before == w.prior
      allKeys == {p \in before : p[1] = "local" /\ p[2] = r.region}
  IN IF ~auth THEN s.rows = before
     ELSE CASE r.op = "CreateUserPool" ->
               IF Cardinality(allKeys) >= Capacity
                 THEN /\ w.status = "quota" /\ s.rows = before /\ clear
                 ELSE IF k \in before THEN /\ w.status = "collision" /\ s.rows = before /\ clear
                      ELSE /\ w.status = "ok" /\ s.rows = before \cup {k} /\ w.output = <<k>>
           [] r.op = "DeleteUserPool" ->
               /\ clear /\ s.rows = before \ {k}
               /\ w.status = (IF k \in before THEN "ok" ELSE "missing")
           [] r.op = "DescribeUserPool" ->
               /\ s.rows = before
               /\ w.output = (IF k \in before THEN <<k>> ELSE <<>>)
               /\ w.status = (IF k \in before THEN "ok" ELSE "missing")
           [] OTHER -> w.status = "ok" /\ s.rows = before

EligibleKeys(w) ==
  LET r == w.request
      after == IF r.cursor = None THEN 0 ELSE r.cursor.after
  IN {p \in w.prior : p[1] = "local" /\ p[2] = r.region /\ p[3] > after}

SuccessfulList == IF ~Attempt THEN FALSE ELSE s.last.request.op = "ListUserPools" /\ s.last.status = "ok"
ExactOrderedPage == IF ~SuccessfulList THEN TRUE ELSE
  LET w == s.last
      all == EligibleKeys(w)
      n == w.request.limit
      expected == {p \in all : Cardinality({q \in all : q[3] < p[3]}) < n}
  IN /\ {w.output[i] : i \in DOMAIN w.output} = expected
     /\ Len(w.output) = Cardinality(expected)
     /\ \A i \in 1..(Len(w.output)-1) : w.output[i][3] < w.output[i+1][3]

ExactNextToken == IF ~SuccessfulList THEN TRUE ELSE
  LET w == s.last
      r == w.request
      eligible == EligibleKeys(w)
      c == w.next
  IN IF Cardinality(eligible) <= r.limit THEN c = None
     ELSE /\ c # None /\ Len(w.output) > 0
          /\ <<c.account, c.region, c.actor, c.limit, c.epoch>> = <<"local", r.region, r.actor, r.limit, s.epoch>>
          /\ c.integrity /\ c.afterFits /\ c.after = w.output[Len(w.output)][3]

RestartPersistsRows == IF s.last = None THEN TRUE ELSE
  s.last.status = "restart" => s.epoch = s.last.retired + 1 /\ s.rows = s.last.prior
=============================================================================
