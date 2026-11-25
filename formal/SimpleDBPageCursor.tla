------------------------- MODULE SimpleDBPageCursor -------------------------
EXTENDS Naturals, Sequences

Scopes == 1..3
Names == 1..3
Ordered == <<1, 2, 3>>
Generations == 1..2
Sizes == {0, 1, 2, 3}
NoCursor == <<0, 0, 0>>
Cursors == {<<g,s,a>> : g \in Generations, s \in Scopes, a \in Names}
Check(b,p,r) == [binding |-> b, page |-> p, read |-> r]
VARIABLES rows, generation, saved, checks
vars == <<rows, generation, saved, checks>>

Init ==
  /\ rows = [s \in Scopes |-> {}]
  /\ generation = 1
  /\ saved = NoCursor
  /\ checks = Check(TRUE, TRUE, TRUE)

Admit(c,s) == c[1] = generation /\ c[2] = s
Eligible(name,anchor) == name > anchor
PageSize(size) == IF size = 0 THEN 3 ELSE size
Anchor(c) == IF c = NoCursor THEN 0 ELSE c[3]
Prefix(xs,n) == SubSeq(xs, 1, IF Len(xs) < n THEN Len(xs) ELSE n)

Reply(s,c,size) ==
  IF c # NoCursor /\ ~Admit(c,s)
  THEN [status |-> "InvalidNextToken", items |-> <<>>, next |-> NoCursor]
  ELSE LET n == PageSize(size)
           candidates == SelectSeq(Ordered, LAMBDA d: d \in rows[s] /\ Eligible(d,Anchor(c)))
           items == Prefix(candidates,n)
       IN [status |-> "OK", items |-> items,
           next |-> IF Len(candidates) > n THEN <<generation,s,items[n]>> ELSE NoCursor]

Observe(s,c,size,out,pre,post) ==
  LET valid == c = NoCursor \/ (c[1] = generation /\ c[2] = s)
      bound == IF valid THEN out.status = "OK"
               ELSE out.status = "InvalidNextToken" /\ out.items = <<>> /\ out.next = NoCursor
      exact == IF ~valid THEN TRUE
               ELSE LET n == IF size = 0 THEN 3 ELSE size
                        a == IF c = NoCursor THEN 0 ELSE c[3]
                        wanted == SelectSeq(Ordered, LAMBDA d: d \in pre[s] /\ d > a)
                        expected == SubSeq(wanted,1,IF Len(wanted) < n THEN Len(wanted) ELSE n)
                        next == IF Len(wanted) > n THEN <<generation,s,expected[n]>> ELSE NoCursor
                    IN out.items = expected /\ out.next = next
  IN Check(bound, exact, pre = post)

List(s,c,size,retain) ==
  LET out == Reply(s,c,size)
  IN /\ UNCHANGED <<rows,generation>>
     /\ checks' = Observe(s,c,size,out,rows,rows')
     /\ saved' = IF retain /\ out.next # NoCursor THEN out.next ELSE saved

Change(s,d,insert) ==
  /\ rows' = [rows EXCEPT ![s] = IF insert THEN @ \cup {d} ELSE @ \ {d}]
  /\ UNCHANGED <<generation,saved,checks>>

Reopen ==
  /\ generation = 1
  /\ generation' = 2
  /\ UNCHANGED <<rows,saved,checks>>

Next ==
  \/ \E s \in Scopes, size \in Sizes, retain \in BOOLEAN:
       \E c \in {NoCursor,saved}: List(s,c,size,retain)
  \/ \E s \in Scopes, d \in Names, insert \in BOOLEAN: Change(s,d,insert)
  \/ Reopen

Spec == Init /\ [][Next]_vars
TypeOK ==
  /\ rows \in [Scopes -> SUBSET Names]
  /\ generation \in Generations
  /\ saved \in Cursors \cup {NoCursor}
  /\ checks \in [binding : BOOLEAN, page : BOOLEAN, read : BOOLEAN]
CursorBinding == checks.binding
ExactPage == checks.page
ReadOnly == checks.read
=============================================================================
