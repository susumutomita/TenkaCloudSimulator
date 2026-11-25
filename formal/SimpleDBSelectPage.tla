----------------------- MODULE SimpleDBSelectPage -----------------------
EXTENDS Naturals, Sequences

\* One first page and one resumption, with at most one intervening committed
\* insert/delete/match change. Peer bindings are perturbed independently. An
\* item state is 0=absent, 1=present/nonmatch, 2=present/match. The first
\* image is retained only as a specification witness, never in the token.
Names == 1..3
Images == [Names -> 0..2]
Phases == {"first", "between", "resume", "done"}
Directions == {"asc", "desc"}
Probes == {"same", "denied", "deniedBad", "syntax", "peer", "region",
           "expression", "consistent", "reopened", "otherAction", "tampered"}
VARIABLES phase, first, live, dir, room, saved, check
vars == <<phase, first, live, dir, room, saved, check>>
Checks(a,b,p,c) == [admission |-> a, binding |-> b, page |-> p, cursor |-> c]
OK == Checks(TRUE,TRUE,TRUE,TRUE)

Init == /\ phase = "first" /\ first \in Images /\ live = first
        /\ dir \in Directions /\ room \in 1..2 /\ saved = 0 /\ check = OK

\* A bound query includes its projection, predicate, order, limit and original
\* spelling. "expression" changes only that exact expression. The other axes
\* independently exercise fields checked in opening/signing or scope hashing.
Request(k) == [account |-> k # "peer", region |-> k # "region",
               expr |-> k # "expression", consistent |-> k # "consistent",
               epoch |-> k # "reopened", purpose |-> k # "otherAction",
               intact |-> k \notin {"tampered","deniedBad"},
               allowed |-> k \notin {"denied","deniedBad"}, valid |-> k # "syntax"]
Bound(r) == /\ r.account /\ r.region
            /\ r.expr /\ r.consistent
            /\ r.epoch /\ r.purpose /\ r.intact
Guard(r) == IF ~r.valid THEN "syntax"
            ELSE IF ~r.allowed THEN "denied"
            ELSE IF ~Bound(r) THEN "invalid" ELSE "ok"

Order(d) == IF d = "asc" THEN <<1,2,3>> ELSE <<3,2,1>>
After(n,a,d) == a = 0 \/ (IF d = "asc" THEN n > a ELSE n < a)
ReadImage(issued,current) == current
Result(items,anchor) == [items |-> items, anchor |-> anchor]
Reply(status,p,touched,image) == [status |-> status, rowsRead |-> touched, view |-> image,
                     items |-> p.items, anchor |-> p.anchor]
RECURSIVE Walk(_,_,_,_,_,_)
Walk(xs,rows,cap,budget,last,out) ==
  IF Len(xs) = 0 THEN Result(out,0)
  ELSE LET n == Head(xs)
       IN IF budget = 0 \/ (rows[n] = 2 /\ Len(out) = cap)
          THEN Result(out,last)
          ELSE Walk(Tail(xs),rows,cap,budget-1,n,
                    IF rows[n] = 2 THEN Append(out,n) ELSE out)

Select(k,a) ==
  LET status == Guard(Request(k))
  IN IF status # "ok" THEN Reply(status,Result(<<>>,0),FALSE,<<>>)
     ELSE LET view == ReadImage(first,live)
              xs == SelectSeq(Order(dir),LAMBDA n: view[n] # 0 /\ After(n,a,dir))
          IN Reply("ok",Walk(xs,view,room,2,0,<<>>),TRUE,view)

\* Independent denotation: greatest admissible prefix of the CURRENT ordered
\* candidates, not the implementation's scanning loop. A nonmatch consumes
\* budget even when it is the final inspected candidate on an empty page.
Oracle(a) ==
  LET xs == SelectSeq(IF dir = "asc" THEN <<1,2,3>> ELSE <<3,2,1>>,
                     LAMBDA n: live[n] # 0 /\
                                (a = 0 \/ (dir = "asc" /\ a < n) \/ (dir = "desc" /\ a > n)))
      Hits(k) == SelectSeq(SubSeq(xs,1,k),LAMBDA n: live[n] = 2)
      feasible == {k \in 0..Len(xs): k <= 2 /\ Len(Hits(k)) <= room}
      cut == CHOOSE k \in feasible: \A j \in feasible: j <= k
  IN Result(Hits(cut), IF cut = Len(xs) THEN 0 ELSE xs[cut])

Observe(k,out,a) ==
  LET expected == IF k = "syntax" THEN "syntax"
                  ELSE IF k \in {"denied","deniedBad"} THEN "denied"
                  ELSE IF k = "same" THEN "ok" ELSE "invalid"
      blocked == ~out.rowsRead /\ out.items = <<>> /\ out.anchor = 0
  IN Checks(IF expected \in {"syntax","denied"} THEN out.status = expected /\ blocked ELSE TRUE,
            IF expected = "invalid" THEN out.status = "invalid" /\ blocked ELSE TRUE,
            IF expected = "ok" THEN out.status = "ok" /\ out.rowsRead /\ out.view = live /\ out.items = Oracle(a).items ELSE TRUE,
            IF expected = "ok" THEN out.anchor = Oracle(a).anchor ELSE TRUE)

FirstPage == /\ phase = "first"
             /\ LET out == Select("same",0)
                IN /\ check' = Observe("same",out,0) /\ saved' = out.anchor
                   /\ phase' = IF out.anchor = 0 THEN "done" ELSE "between"
             /\ UNCHANGED <<first,live,dir,room>>
Change == /\ phase = "between"
          /\ \E n \in Names, state \in 0..2: live' = [live EXCEPT ![n] = state]
          /\ phase' = "resume" /\ UNCHANGED <<first,dir,room,saved,check>>
Resume(k) == /\ phase = "resume"
             /\ check' = Observe(k,Select(k,saved),saved)
             /\ phase' = "done" /\ UNCHANGED <<first,live,dir,room,saved>>
Next == FirstPage \/ Change \/ (\E k \in Probes: Resume(k))
        \/ (phase = "done" /\ UNCHANGED vars)
Spec == Init /\ [][Next]_vars
TypeOK == /\ phase \in Phases /\ first \in Images /\ live \in Images
          /\ dir \in Directions /\ room \in 1..2 /\ saved \in 0..3
          /\ check \in [admission: BOOLEAN, binding: BOOLEAN, page: BOOLEAN, cursor: BOOLEAN]
AdmissionBeforeRead == check.admission
CursorScopeBound == check.binding
ExactCurrentPage == check.page
ExactConsumedCursor == check.cursor
=========================================================================
