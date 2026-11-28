---------------------- MODULE PCSQueueAssociations ----------------------
EXTENDS Naturals, FiniteSets, TLC

(* Abstract committed PCS configuration references, not a Go/SQLite proof. *)
Scopes == {1, 2}
Groups == {1, 2}
Queues == {1, 2}
Scope(id) == id
EmptyQueues == [q \in Queues |-> {}]
VARIABLES groups, queues, last, depth
vars == <<groups, queues, last, depth>>

Init == /\ groups = {} /\ queues = EmptyQueues /\ depth = 0
        /\ last = [kind |-> "Init", target |-> 1, requested |-> {},
                    allowed |-> TRUE, canceled |-> FALSE, success |-> FALSE,
                    oldGroups |-> {}, oldQueues |-> EmptyQueues]

Live(refs) == refs \subseteq groups
Local(q, refs) == \A g \in refs : Scope(g) = Scope(q)
Unused(g) == \A q \in Queues : g \notin queues[q]

Step(kind, target, requested, allowed, canceled) ==
 LET admissible == IF kind = "CreateGroup" THEN target \notin groups
                   ELSE IF kind = "PutQueue" THEN Live(requested) /\ Local(target, requested)
                   ELSE IF kind = "DeleteGroup" THEN target \in groups /\ Unused(target)
                   ELSE TRUE
     success == allowed /\ ~canceled /\ admissible
 IN /\ depth < 5
    /\ depth' = depth + 1
    /\ last' = [kind |-> kind, target |-> target, requested |-> requested,
                allowed |-> allowed, canceled |-> canceled, success |-> success,
                oldGroups |-> groups, oldQueues |-> queues]
    /\ groups' = IF success /\ kind = "CreateGroup" THEN groups \cup {target}
                 ELSE IF success /\ kind = "DeleteGroup" THEN groups \ {target}
                 ELSE groups
    /\ queues' = IF success /\ kind = "PutQueue" THEN [queues EXCEPT ![target] = requested]
                 ELSE IF success /\ kind = "DeleteQueue" THEN [queues EXCEPT ![target] = {}]
                 ELSE queues
Next == \E kind \in {"CreateGroup", "PutQueue", "DeleteGroup", "DeleteQueue"},
           target \in Groups, requested \in SUBSET Groups,
           allowed \in BOOLEAN, canceled \in BOOLEAN : Step(kind, target, requested, allowed, canceled)

TypeOK == /\ groups \subseteq Groups /\ queues \in [Queues -> SUBSET Groups]
          /\ depth \in 0..5
NoDanglingReferences == \A q \in Queues : queues[q] \subseteq groups
SameScope == \A q \in Queues : \A g \in queues[q] : Scope(g) = Scope(q)
DeniedUnchanged == ~last.allowed => groups = last.oldGroups /\ queues = last.oldQueues
CanceledUnchanged == last.canceled => groups = last.oldGroups /\ queues = last.oldQueues
RejectedUnchanged == ~last.success => groups = last.oldGroups /\ queues = last.oldQueues
DeletionGuard == last.kind = "DeleteGroup" /\ last.success =>
                 \A q \in Queues : last.target \notin last.oldQueues[q]
=============================================================================
