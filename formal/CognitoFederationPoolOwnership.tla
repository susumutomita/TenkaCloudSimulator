-------------------- MODULE CognitoFederationPoolOwnership --------------------
EXTENDS Naturals, FiniteSets

\* Private persistence only. One atomic action represents one SQLite transaction.
\* Cardinality-bounded metadata is a symbol, not an assertion or credential.
CONSTANT StepLimit, None
VARIABLE s
Target == <<"local", "east", 1>>
PeerAccount == <<"peer", "east", 1>>
PeerRegion == <<"local", "west", 1>>
PeerID == <<"local", "east", 2>>
Keys == {Target, PeerAccount, PeerRegion, PeerID}
InitialRevision(k) == CASE k = Target -> 1 [] k = PeerAccount -> 2
                        [] k = PeerRegion -> 3 [] OTHER -> 4
Revisions == 0..(Cardinality(Keys) + StepLimit)
Values == {"empty", "a", "b"}
Row == [live : BOOLEAN, rev : Revisions, data : Values]
Blank(live) == [live |-> live, rev |-> 0, data |-> "empty"]
InitialRows == [k \in Keys |-> [live |-> TRUE, rev |-> InitialRevision(k), data |-> "a"]]
Ops == {"open", "write", "fault", "delete", "create"}
Statuses == {"ok", "missing", "conflict", "fault", "exists"}
Req == [op : Ops, rev : Revisions, data : Values]
Inputs == {[op |-> op, rev |-> 0, data |-> "empty"] : op \in {"open", "create", "delete"}}
       \cup {[op |-> op, rev |-> r, data |-> v] : op \in {"write", "fault"}, r \in Revisions, v \in {"a", "b"}}
Init == s = [rows |-> InitialRows, issued |-> (1..Cardinality(Keys)), step |-> 0, last |-> None]

Current(r, prev) == prev.rev # 0 /\ r.rev = prev.rev
Status(r, prev) == IF r.op = "create" THEN (IF prev.live THEN "exists" ELSE "ok")
              ELSE IF ~prev.live THEN "missing"
              ELSE IF r.op \in {"write", "fault"} /\ ~Current(r, prev) THEN "conflict"
              ELSE IF r.op = "fault" THEN "fault" ELSE "ok"
Fresh == Cardinality(Keys) + s.step + 1
Apply(r, prior, result) ==
  IF result # "ok" THEN prior
  ELSE CASE r.op = "create" -> Blank(TRUE)
         [] r.op = "delete" -> Blank(FALSE)
         [] r.op = "open" -> IF prior.rev = 0 THEN [prior EXCEPT !.rev = Fresh] ELSE prior
         [] OTHER -> [prior EXCEPT !.rev = Fresh, !.data = r.data]
Install(rows, next) == [rows EXCEPT ![Target] = next]
Call(r) ==
  /\ s.step < StepLimit
  /\ LET prev == s.rows[Target]
         result == Status(r, prev)
         next == Apply(r, prev, result)
         emits == result = "ok" /\ r.op \in {"open", "write"}
     IN s' = [s EXCEPT !.rows = Install(s.rows, next), !.step = @ + 1,
                  !.issued = IF next.rev = 0 THEN @ ELSE @ \cup {next.rev},
                  !.last = [req |-> r, prior |-> s.rows, issued |-> s.issued, result |-> result,
                            rev |-> IF emits THEN next.rev ELSE 0,
                            data |-> IF emits THEN next.data ELSE "empty"]]
Next == (\E r \in Inputs : Call(r)) \/ UNCHANGED s

TypeOK ==
  /\ s.rows \in [Keys -> Row] /\ s.step \in 0..StepLimit /\ s.issued \subseteq Revisions \ {0}
  /\ s.last \in {None} \cup [req : Req, prior : [Keys -> Row], issued : SUBSET Revisions,
                             result : Statuses, rev : Revisions, data : Values]
PeerFrame == IF s.last = None THEN TRUE ELSE
  \A k \in Keys \ {Target} : s.rows[k] = s.last.prior[k]
\* Oracle uses the chosen operation, the independently recorded before image and
\* all revisions ever issued, NOT the transition's status/fresh/current helpers.
CanonicalTransition == IF s.last = None THEN TRUE ELSE
  LET w == s.last
      r == w.req
      prev == w.prior[Target]
      now == s.rows[Target]
      fresh == now.rev # 0 /\ now.rev \notin w.issued
      silent == w.rev = 0 /\ w.data = "empty"
      exact == w.rev = now.rev /\ w.data = now.data
      reject(code) == w.result = code /\ silent /\ now = prev
  IN IF r.op = "create" THEN
       IF prev.live THEN reject("exists") ELSE
          /\ w.result = "ok" /\ silent /\ now.live /\ now.rev = 0 /\ now.data = "empty"
     ELSE IF ~prev.live THEN reject("missing")
     ELSE CASE r.op = "delete" -> /\ w.result = "ok" /\ silent /\ ~now.live
                                 /\ now.rev = 0 /\ now.data = "empty"
            [] r.op = "open" -> /\ w.result = "ok" /\ exact /\ now.live /\ now.data = prev.data
                                /\ IF prev.rev = 0 THEN fresh ELSE now.rev = prev.rev
            [] OTHER -> IF prev.rev = 0 \/ r.rev # prev.rev THEN reject("conflict")
                        ELSE IF r.op = "fault" THEN reject("fault")
                        ELSE /\ w.result = "ok" /\ exact /\ now.live /\ fresh /\ now.data = r.data
=============================================================================
