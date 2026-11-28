------------------------ MODULE CloudTraceObservations ------------------------
EXTENDS Integers, Sequences, FiniteSets

Scopes == {1, 2}
TraceIDs == 1..3
Views == {"MINIMAL", "COMPLETE"}
ProjectLimit == 5
Other(s) == 3 - s
Obs(t, v) == [trace |-> t, span |-> 1, value |-> v]
Seed(s) == <<Obs(3, s), Obs(3, s), Obs(1, s)>>
InitialRows == [s \in Scopes |-> Seed(s)]
(* Every observation uses the same span ID; two identical documents are valid. *)
Batches(s) == {
    <<Obs(2, s), Obs(3, Other(s))>>,
    <<Obs(3, s), Obs(3, s)>>,
    <<Obs(3, Other(s)), Obs(3, s)>>,
    <<Obs(2, s), Obs(3, 0)>>,
    <<Obs(1, s), Obs(2, s), Obs(3, s)>>}
Valid(batch) == \A i \in 1..Len(batch) : batch[i].value \in Scopes
Allowed(target, actor, project) == target = actor /\ target = project
Fits(rs, batch) == Len(rs) + Len(batch) <= ProjectLimit
Body(rs, id) == SelectSeq(rs, LAMBDA row : row.trace = id)
Keys(rs, after) == {rs[i].trace : i \in {j \in 1..Len(rs) : rs[j].trace > after}}
Least(keys) == IF keys = {} THEN 0 ELSE CHOOSE k \in keys : \A j \in keys : k <= j
FirstKey(rs, after) == Least(Keys(rs, after))
Reply(id, body) == [visible |-> TRUE, key |-> id, body |-> body]
EmptyReply == [visible |-> FALSE, key |-> 0, body |-> <<>>]
Render(view, rs, id) == IF view = "COMPLETE" THEN Body(rs, id) ELSE <<>>
ReferenceReply(view, rs, id) ==
    Reply(id, IF view = "MINIMAL" THEN <<>> ELSE Body(rs, id))

VARIABLES rows, target, view, phase, high, after, batch, admitted,
          actor, project, issuer, requestedView, reply
vars == <<rows, target, view, phase, high, after, batch, admitted,
          actor, project, issuer, requestedView, reply>>

Init ==
    /\ target \in Scopes /\ view \in Views
    /\ rows = InitialRows /\ phase = "init" /\ high = 0 /\ after = 0
    /\ batch = <<>> /\ admitted = FALSE
    /\ actor = target /\ project = target /\ issuer = target /\ requestedView = view
    /\ reply = EmptyReply

(* First read before a new batch, with intentionally unsorted insertion order. *)
OpenPage ==
    /\ phase = "init"
    /\ LET rs == rows[target]
           id == FirstKey(rs, 0)
       IN /\ high' = Len(rs) /\ after' = id /\ reply' = Reply(id, Render(view, rs, id))
    /\ phase' = "open"
    /\ UNCHANGED <<rows, target, view, batch, admitted, actor, project, issuer, requestedView>>

Begin(a, p, b) ==
    /\ phase = "open"
    /\ actor' = a /\ project' = p /\ batch' = b
    /\ admitted' = (Allowed(target, a, p) /\ Valid(b) /\ Fits(rows[target], b))
    /\ rows' = rows
    /\ phase' = "staged"
    /\ UNCHANGED <<target, view, high, after, issuer, requestedView, reply>>

Commit ==
    /\ phase = "staged" /\ admitted
    /\ rows' = [rows EXCEPT ![target] = @ \o batch]
    /\ phase' = "committed"
    /\ UNCHANGED <<target, view, high, after, batch, admitted, actor, project, issuer, requestedView, reply>>

FinishWithoutCommit(kind) ==
    /\ phase = "staged" /\ IF kind = "refused" THEN ~admitted ELSE admitted
    /\ rows' = rows (* DiscardStaging *)
    /\ phase' = kind
    /\ UNCHANGED <<target, view, high, after, batch, admitted, actor, project, issuer, requestedView, reply>>

(* A fresh Get is unconstrained by an older list cursor. *)
Get(a, p) ==
    /\ phase \in {"committed", "refused", "canceled"}
    /\ actor' = a /\ project' = p
    /\ reply' = IF Allowed(target, a, p)
                THEN Reply(3, Body(rows[target], 3)) ELSE EmptyReply
    /\ phase' = "get"
    /\ UNCHANGED <<rows, target, view, high, after, batch, admitted, issuer, requestedView>>

ContinuePage(a, p, tokenIssuer, q) ==
    /\ phase \in {"committed", "refused", "canceled"}
    /\ actor' = a /\ project' = p /\ issuer' = tokenIssuer /\ requestedView' = q
    /\ LET rs == rows[target]
           atHigh == SubSeq(rs, 1, high)
           id == FirstKey(atHigh, after)
       IN reply' = IF Allowed(target, a, p) /\ tokenIssuer = target /\ q = view
                   THEN Reply(id, Render(q, atHigh, id)) ELSE EmptyReply
    /\ phase' = "next"
    /\ UNCHANGED <<rows, target, view, high, after, batch, admitted>>

Next ==
    \/ OpenPage
    \/ \E a,p \in Scopes, b \in Batches(target) : Begin(a, p, b)
    \/ Commit \/ FinishWithoutCommit("canceled") \/ FinishWithoutCommit("refused")
    \/ \E a,p \in Scopes : Get(a, p)
    \/ \E a,p,i \in Scopes, q \in Views : ContinuePage(a, p, i, q)
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ target \in Scopes /\ actor \in Scopes /\ project \in Scopes /\ issuer \in Scopes
    /\ view \in Views /\ requestedView \in Views /\ admitted \in BOOLEAN
    /\ phase \in {"init", "open", "staged", "committed", "canceled", "refused", "get", "next"}
    /\ high \in 0..3 /\ after \in 0..3 /\ DOMAIN rows = Scopes
    /\ \A s \in Scopes : \A j \in 1..Len(rows[s]) :
           rows[s][j] \in [trace : TraceIDs, span : {1}, value : 0..2]
FirstPageOrdered == phase = "open" => reply = ReferenceReply(view, Seed(target), 1) /\ after = 1
PendingInvisible == phase = "staged" => rows = InitialRows
AdmitEligible ==
    phase = "staged" /\ actor = target /\ project = target /\ Valid(batch)
    /\ Len(Seed(target)) + Len(batch) <= ProjectLimit => admitted
CallerBound == phase = "committed" => actor = target /\ project = target
ValidBatchOnly == phase = "committed" => Valid(batch)
ProjectBound == \A s \in Scopes : Len(rows[s]) <= ProjectLimit
PeerUnchanged == rows[Other(target)] = Seed(Other(target))
AllObservationsCommitted == phase = "committed" => rows[target] = Seed(target) \o batch
RollbackUnchanged == phase \in {"canceled", "refused"} => rows = InitialRows
GetCurrent ==
    phase = "get" => reply = IF actor = target /\ project = target
                            THEN Reply(3, Body(rows[target], 3)) ELSE EmptyReply
NextBound ==
    phase = "next" /\ (actor # target \/ project # target \/ issuer # target \/ requestedView # view)
    => reply = EmptyReply
NextStableKey ==
    phase = "next" /\ actor = target /\ project = target /\ issuer = target /\ requestedView = view
    => reply.visible /\ reply.key = 3
NextStableBody ==
    phase = "next" /\ reply.visible /\ reply.key = 3
    => reply.body = IF view = "MINIMAL" THEN <<>> ELSE Body(Seed(target), 3)
=============================================================================
