---------------------- MODULE SimpleDBSelectConsoleEpoch ----------------------
EXTENDS Naturals, FiniteSets

\* Browser-local refinement targets of the bounded names-only Select workflow.
\* AWS Select permits an empty successful page with NextToken:
\* https://docs.aws.amazon.com/AmazonSimpleDB/latest/DeveloperGuide/SDB_API_Select.html
\* One identity, domain, criteria, navigation or capability change at a time,
\* including a return to a previously used key. Write attempts retire the owner
\* before an acknowledgement. Pending browser promises may settle in any order.

CONSTANT MoveLimit, CallLimit, None
VARIABLE v
Base == [identity |-> "A", domain |-> "x", criteria |-> "all", away |-> FALSE, advertised |-> TRUE]
Contexts == {Base, [Base EXCEPT !.identity = "B"], [Base EXCEPT !.domain = "y"],
             [Base EXCEPT !.criteria = "filter"], [Base EXCEPT !.criteria = "invalid"],
             [Base EXCEPT !.away = TRUE], [Base EXCEPT !.advertised = FALSE]}
Tokens == {"t1", "t2"} \cup {None}
Names == {"old", "match"}
Pages == 1..CallLimit
Moves == 0..MoveLimit
Errors == {"denied", "transport", "invalid"}
Actions == {"Select", "GetAttributes", "PutAttributes", "DeleteAttributes", "none"}
Events == {"first", "more", "choose", "read", "write", "delete", "none"}
Modes == {"first", "more"}

Mounted(c) == ~c.away /\ c.advertised
Eligible(c, lock) == Mounted(c) /\ c.criteria # "invalid" /\ ~lock
Retire(cause) == TRUE
Signature(r) == [scope |-> r.scope, revision |-> r.revision]
Own(r) == r.revision = v.moves /\ r.scope = v.scope /\ Mounted(v.scope)

Init ==
  v = [scope |-> Base, moves |-> 0, owner |-> 0, issued |-> 0, pending |-> {},
       page |-> None, alert |-> None, locked |-> FALSE, item |-> "old", observed |-> TRUE,
       event |-> "none", effect |-> "none"]

NewContext(s, cause) ==
  [v EXCEPT !.scope = s, !.moves = @ + 1, !.owner = IF Retire(cause) THEN @ + 1 ELSE @,
      !.page = IF Retire(cause) THEN None ELSE @,
      !.alert = IF Retire(cause) THEN None ELSE @, !.event = "none", !.effect = "none"]

Change(s) ==
  /\ v.moves < MoveLimit
  /\ LET fields == {f \in DOMAIN s : s[f] # v.scope[f]}
         parentRemount == fields \subseteq {"identity", "domain", "away"}
     IN /\ Cardinality(fields) = 1
        /\ (fields = {"criteria"} => ~v.locked)
        /\ v' = [NewContext(s, CHOOSE f \in fields : TRUE) EXCEPT
                 !.locked = IF parentRemount THEN FALSE ELSE @,
                 !.observed = IF parentRemount THEN FALSE ELSE @]

Prior(p) == IF p = None THEN [scope |-> v.scope, revision |-> v.moves, cursor |-> None, number |-> 0] ELSE p
MoreAllowed(p) == p.cursor # None
SentCursor(m, p) == IF m = "more" THEN p.cursor ELSE None
RequestScope(m, p) == IF m = "more" THEN p.scope ELSE v.scope
RequestNumber(m, p) == IF m = "more" THEN p.number + 1 ELSE 1
SearchAction == "Select"
PageSize == 25
Active(q) == q.owner = v.owner

Begin(m) ==
  /\ v.issued < CallLimit /\ Eligible(v.scope, v.locked)
  /\ ~\E r \in v.pending : Active(r)
  /\ LET p == Prior(v.page)
     IN /\ (m = "first" \/ MoreAllowed(p))
        /\ LET q == [scope |-> v.scope, revision |-> v.moves, owner |-> v.owner,
                     ticket |-> v.issued + 1, mode |-> m, pageSize |-> PageSize,
                     sentScope |-> RequestScope(m, p), blocked |-> v.locked,
                     number |-> RequestNumber(m, p), prior |-> IF m = "more" THEN v.page ELSE None,
                     cursor |-> SentCursor(m, p)]
           IN v' = [v EXCEPT !.issued = @ + 1, !.pending = @ \cup {q},
                    !.page = None, !.alert = None, !.event = m, !.effect = SearchAction]

Replies ==
  {[kind |-> "ok", rows |-> rows, token |-> token] : rows \in {{}, {"match"}}, token \in Tokens}
  \cup {[kind |-> k, rows |-> {}, token |-> None] : k \in Errors}

Repeated(q, a) == a.token # None /\ a.token = q.cursor
Accepted(q, a) == a.kind = "ok" /\ ~Repeated(q, a)
ResultCursor(a) == a.token
FailedPage(q) == None
Publish(q) == Active(q)
Result(q, a) ==
  [scope |-> q.scope, revision |-> q.revision, number |-> q.number, used |-> q.cursor,
   ack |-> a, names |-> a.rows, cursor |-> ResultCursor(a)]

Reply(q, a) ==
  LET good == Accepted(q, a)
  IN v' = [v EXCEPT !.pending = @ \ {q},
            !.page = IF Publish(q) THEN IF good THEN Result(q, a) ELSE FailedPage(q) ELSE @,
            !.alert = IF Publish(q) THEN IF good THEN None ELSE Signature(q) ELSE @,
            !.event = IF Publish(q) THEN "none" ELSE @, !.effect = IF Publish(q) THEN "none" ELSE @]

ChoiceEffect == "none"
ChoiceObserved == FALSE
Choose(n) == /\ v.page # None /\ n \in v.page.names /\ ~v.locked /\ Mounted(v.scope)
             /\ v' = [v EXCEPT !.item = n, !.observed = ChoiceObserved,
                      !.event = "choose", !.effect = ChoiceEffect]

\* These are deliberate item actions; SimpleDBConsoleEpoch owns their chain.
\* The lookup here abstracts the acknowledged case, not its request/response race.
ReadItem == /\ ~v.locked /\ ~v.scope.away
            /\ v' = [v EXCEPT !.locked = TRUE, !.observed = TRUE, !.event = "read",
                     !.effect = "GetAttributes"]
BeginWrite(op) ==
  /\ ~v.locked /\ v.observed /\ ~v.scope.away /\ v.moves < MoveLimit
  /\ v' = [NewContext(v.scope, "write") EXCEPT !.locked = TRUE, !.observed = FALSE,
           !.event = IF op = "PutAttributes" THEN "write" ELSE "delete", !.effect = op]
Unlock == /\ v.locked /\ v' = [v EXCEPT !.locked = FALSE, !.event = "none", !.effect = "none"]

Next == \/ \E c \in Contexts : Change(c)
        \/ \E m \in Modes : Begin(m)
        \/ \E r \in v.pending, a \in Replies : Reply(r, a)
        \/ \E n \in Names : Choose(n)
        \/ ReadItem
        \/ \E op \in {"PutAttributes", "DeleteAttributes"} : BeginWrite(op)
        \/ Unlock
        \/ UNCHANGED v

Page == [scope : Contexts, revision : Moves, number : Pages, used : Tokens, ack : Replies,
         names : SUBSET Names, cursor : Tokens]
Notice == [scope : Contexts, revision : Moves]
Requests == [scope : Contexts, revision : Moves, owner : Moves, ticket : Pages, mode : Modes, pageSize : {25, 26}, sentScope : Contexts,
             blocked : BOOLEAN, number : Pages, prior : Page \cup {None}, cursor : Tokens]
TypeOK ==
  v \in [scope : Contexts, moves : Moves, owner : Moves, issued : 0..CallLimit,
         pending : SUBSET Requests, page : Page \cup {None}, alert : Notice \cup {None},
         locked : BOOLEAN, item : Names, observed : BOOLEAN, event : Events, effect : Actions]
ReadAdmission == \A q \in v.pending : Mounted(q.scope) /\ q.scope.criteria # "invalid" /\ ~q.blocked
BoundedReadRequest == \A q \in v.pending : q.pageSize = 25
SingleCurrentRead == Cardinality({q \in v.pending : Active(q)}) <= 1
CursorOrigin == \A q \in v.pending :
  /\ q.sentScope = q.scope
  /\ IF q.mode = "first" THEN q.prior = None /\ q.cursor = None /\ q.number = 1
     ELSE /\ q.prior # None
          /\ q.prior.scope = q.scope /\ q.prior.revision = q.revision
          /\ q.prior.cursor # None /\ q.cursor = q.prior.cursor /\ q.number = q.prior.number + 1
OnlyCurrentOwner == /\ IF v.page = None THEN TRUE ELSE Own(v.page)
                    /\ IF v.alert = None THEN TRUE ELSE Own(v.alert)
OnlyAcceptedPages == IF v.page = None THEN TRUE ELSE
  /\ v.page.ack.kind = "ok"
  /\ v.page.ack.token = None \/ v.page.ack.token # v.page.used
  /\ v.page.names = v.page.ack.rows
CursorMirrorsReply == IF v.page = None THEN TRUE ELSE v.page.cursor = v.page.ack.token
NextOfferedExactly == IF v.page = None THEN ~MoreAllowed(Prior(v.page))
                      ELSE MoreAllowed(v.page) = (v.page.ack.token # None)
FailureClearsPage == v.alert # None => v.page = None
EffectIsolation ==
  v.effect = CASE v.event \in {"first", "more"} -> "Select"
                  [] v.event = "read" -> "GetAttributes"
                  [] v.event = "write" -> "PutAttributes"
                  [] v.event = "delete" -> "DeleteAttributes"
                  [] OTHER -> "none"
ChoiceDoesNotObserve == v.event = "choose" => ~v.observed
=============================================================================
