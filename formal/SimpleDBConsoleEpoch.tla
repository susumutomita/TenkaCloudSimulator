-------------------------- MODULE SimpleDBConsoleEpoch -------------------------
EXTENDS Naturals, FiniteSets

\* One signed-in SimpleDB console only. A new React owner is created on
\* domain, principal or navigation switches; old promises can settle.
\* Requests/acks are symbolic; native IAM, transport, DOM and SQL are not modeled.
CONSTANT MoveLimit, CallLimit, None
Actors == {"owner", "reader"}
Domains == {"left", "right"}
Items == {"one", "two"}
Views == {"domains", "item", "away", "masked"}
Required == {"ListDomains", "CreateDomain", "GetAttributes", "PutAttributes"}
Cap == [console : BOOLEAN, region : {"local", "other"}, condition : BOOLEAN,
        handoff : BOOLEAN, operations : SUBSET Required]
Available(c) == c.console /\ c.region = "local" /\ c.condition /\ ~c.handoff
                /\ Required \subseteq c.operations
Tickets == 1..CallLimit
Calls == [ticket : Tickets, epoch : 0..MoveLimit, actor : Actors,
          domain : Domains \cup {None}, item : Items \cup {None},
          op : Required \cup {"Readback"}]

VARIABLES cap, v
vars == <<cap, v>>

Init == /\ cap \in Cap
        /\ v = [view |-> IF Available(cap) THEN "domains" ELSE "masked",
                actor |-> "owner", epoch |-> 0, issued |-> 0, active |-> None,
                domain |-> None, item |-> None, waiting |-> {}, acks |-> {},
                snapshot |-> None, notice |-> None]

Visible == v.view \in {"domains", "item"}
Live(q) == Visible /\ q.epoch = v.epoch
Exact(q) == Live(q) /\ q.actor = v.actor /\ q.domain = v.domain /\ q.item = v.item

\* Any navigation/identity change can unmount while a request is in flight.
\* Domain choice itself is disabled during a request, as is editing the item.
Mount(view, actor, domain) ==
  [v EXCEPT !.view = view, !.actor = actor, !.epoch = @ + 1, !.domain = domain,
     !.item = None, !.snapshot = None, !.notice = None, !.active = None]

Switch == /\ Visible /\ v.epoch < MoveLimit
          /\ v' \in {Mount("away", v.actor, None)}
                \cup {Mount("domains", a, None) : a \in Actors \ {v.actor}}
          /\ UNCHANGED cap

Return == /\ v.view = "away" /\ Available(cap) /\ v.epoch < MoveLimit
          /\ v' = Mount("domains", v.actor, None) /\ UNCHANGED cap

Open(d) == /\ v.view = "domains" /\ v.active = None /\ v.epoch < MoveLimit
           /\ v' = Mount("item", v.actor, d) /\ UNCHANGED cap

Name(i) == /\ v.view = "item" /\ v.active = None /\ v.item # i
           /\ v' = [v EXCEPT !.item = i, !.snapshot = None, !.notice = None]
           /\ UNCHANGED cap

MayStart(op) ==
  /\ v.active = None /\ v.issued < CallLimit
  /\ IF op \in {"ListDomains", "CreateDomain"} THEN v.view = "domains"
     ELSE /\ v.view = "item" /\ v.item \in Items
          /\ (op = "PutAttributes" => v.snapshot # None)

Start(op) ==
  /\ MayStart(op)
  /\ LET ticket == v.issued + 1
         q == [ticket |-> ticket, epoch |-> v.epoch, actor |-> v.actor,
               domain |-> v.domain, item |-> v.item, op |-> op]
     IN v' = [v EXCEPT !.issued = ticket, !.active = ticket,
                  !.waiting = @ \cup {q}, !.snapshot = None, !.notice = None]
  /\ UNCHANGED cap

Follow(status) == status = "ack"

\* A settled call from an older React owner cannot publish data or replace
\* the current chain. Treat follow-up initiation as visible-owner behavior;
\* an unmounted JS callback before passive cleanup is not modeled here.
Settle(q, status) ==
  /\ q \in v.waiting
  /\ LET current == Live(q) /\ v.active = q.ticket
         putAck == q.op = "PutAttributes" /\ status = "ack"
         follow == current /\ q.op = "PutAttributes" /\ Follow(status)
         next == [q EXCEPT !.op = "Readback"]
         publish == current /\ q.op \in {"GetAttributes", "Readback"} /\ status = "ack"
     IN v' = [v EXCEPT !.waiting = (@ \ {q}) \cup (IF follow THEN {next} ELSE {}),
               !.active = IF current /\ ~follow THEN None ELSE v.active,
               !.acks = @ \cup (IF putAck THEN {q.ticket} ELSE {}),
               !.snapshot = IF publish THEN q ELSE v.snapshot,
               !.notice = IF current /\ putAck THEN q ELSE v.notice]
  /\ UNCHANGED cap

Next == \/ Switch \/ Return
        \/ (\E d \in Domains : Open(d))
        \/ (\E i \in Items : Name(i))
        \/ (\E op \in Required : Start(op))
        \/ (\E q \in v.waiting, status \in {"ack", "reject"} : Settle(q, status))
        \/ UNCHANGED vars

TypeOK == /\ cap \in Cap
          /\ v \in [view : Views, actor : Actors, epoch : 0..MoveLimit,
                     issued : 0..CallLimit, active : Tickets \cup {None},
                     domain : Domains \cup {None}, item : Items \cup {None},
                     waiting : SUBSET Calls, acks : SUBSET Tickets,
                     snapshot : Calls \cup {None}, notice : Calls \cup {None}]

VisibleRequiresCapability == Visible => Available(cap)
OnlyCurrentScopeVisible ==
  /\ IF v.snapshot = None THEN TRUE ELSE Exact(v.snapshot)
  /\ IF v.notice = None THEN TRUE ELSE Exact(v.notice)
SingleCurrentChain ==
  LET current == {q \in v.waiting : Live(q)}
  IN /\ Cardinality(current) <= 1
     /\ IF v.active = None THEN current = {} ELSE {q.ticket : q \in current} = {v.active}
ReadbackOnlyAfterAck == \A q \in v.waiting : q.op = "Readback" => q.ticket \in v.acks
WriteNoticeRequiresAck == IF v.notice = None THEN TRUE ELSE v.notice.ticket \in v.acks
==============================================================================
