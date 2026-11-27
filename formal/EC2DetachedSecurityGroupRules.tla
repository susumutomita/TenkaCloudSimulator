---------------- MODULE EC2DetachedSecurityGroupRules ----------------
EXTENDS Naturals, FiniteSets

CONSTANT Steps, RuleLimit, None
VARIABLE s

Directions == {"ingress", "egress"}
Rules == {1,2}
Operations == {"authorize","revoke","describe","delete"}
Failure == {"foreign-region","stack-owned","denied","outside-parent","unconfirmed"}
Requests == [op:Operations, direction:Directions, rule:Rules, failure:Failure\cup{None}]
Empty == [d \in Directions |-> {}]
Peer == [d \in Directions |-> IF d="egress" THEN {1} ELSE {}]
Stack == [d \in Directions |-> IF d="ingress" THEN {2} ELSE {}]
Init == s = [present|->TRUE, saved|->Empty, peer|->Peer, stack|->Stack, step|->0, last|->None]

Scoped(r) == r.failure # "foreign-region" /\ r.failure # "stack-owned"
Grant(r) == r.failure # "denied"
WithinParent(r) == r.failure # "outside-parent"
CanAdd(r) == r.rule \notin s.saved[r.direction] /\ Cardinality(s.saved[r.direction]) < RuleLimit
CanRemove(r) == r.rule \in s.saved[r.direction]
Admit(r) ==
  /\ s.present /\ Scoped(r) /\ Grant(r) /\ r.failure # "unconfirmed"
  /\ CASE r.op = "authorize" -> WithinParent(r) /\ CanAdd(r)
       [] r.op = "revoke" -> WithinParent(r) /\ CanRemove(r)
       [] OTHER -> TRUE

Project == s.saved
Updated(r, ok) ==
  IF ~ok THEN s.saved ELSE
    CASE r.op = "authorize" -> [s.saved EXCEPT ![r.direction] = @ \cup {r.rule}]
      [] r.op = "revoke" -> [s.saved EXCEPT ![r.direction] = @ \ {r.rule}]
      [] r.op = "delete" -> Empty
      [] OTHER -> s.saved

Inputs ==
  {[op|->op,direction|->d,rule|->k,failure|->f] :
        op \in {"authorize","revoke"}, d \in Directions, k \in Rules, f \in Failure\cup{None}}
  \cup {[op|->op,direction|->"ingress",rule|->1,failure|->f] :
        op \in {"describe","delete"}, f \in (Failure\{"outside-parent"})\cup{None}}
Call(r) ==
  /\ s.step < Steps
  /\ LET ok == Admit(r) IN
     s' = [s EXCEPT !.present = s.present /\ ~(ok /\ r.op="delete"),
                !.saved = Updated(r, ok), !.step = @+1,
                !.last = [req|->r, ok|->ok, before|->s.saved, live|->s.present,
                          output|->IF ok /\ r.op="describe" THEN Project ELSE None]]
Next == (\E r \in Inputs: Call(r)) \/ UNCHANGED s

TypeOK ==
  s \in [present:BOOLEAN, saved:[Directions->SUBSET Rules], peer:[Directions->SUBSET Rules],
        stack:[Directions->SUBSET Rules], step:0..Steps,
        last:{None}\cup[req:Requests, ok:BOOLEAN, before:[Directions->SUBSET Rules],
                      live:BOOLEAN, output:{None}\cup[Directions->SUBSET Rules]]]
OwnershipFrame == s.peer=Peer /\ s.stack=Stack /\ (~s.present => s.saved=Empty)
DirectionBounds == \A d \in Directions: Cardinality(s.saved[d]) <= RuleLimit
ExactTransition == IF s.last = None THEN TRUE ELSE
  LET last == s.last
      r == last.req
      b == last.before
      selected == b[r.direction]
      accepts == last.live /\
         IF r.op \in {"authorize","revoke"} THEN r.failure=None /\
             IF r.op="authorize"
               THEN r.rule \notin selected /\ Cardinality(selected) < RuleLimit
               ELSE r.rule \in selected
         ELSE r.failure=None
      result == IF ~accepts THEN b ELSE
        [d \in Directions |-> CASE r.op="delete" -> {}
             [] d=r.direction /\ r.op="authorize" -> {k \in Rules : k=r.rule \/ k\in selected}
             [] d=r.direction /\ r.op="revoke" -> {k \in selected : k#r.rule}
             [] OTHER -> b[d]]
  IN /\ last.ok=accepts
     /\ s.present=(last.live /\ ~(accepts /\ r.op="delete"))
     /\ s.saved=result
     /\ last.output=(IF accepts /\ r.op="describe" THEN b ELSE None)
=======================================================================
