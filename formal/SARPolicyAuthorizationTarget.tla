------------------------ MODULE SARPolicyAuthorizationTarget ------------------------
EXTENDS Naturals
VARIABLE receipt

Operations == {"Get", "Put"}
Targets == {"ParentArn", "StorageIdentity"}
States == {"Unchecked", "Refused", "NoPolicy", "Read", "Written"}
Initial == [checked |-> FALSE, op |-> "Get", parent |-> FALSE,
            target |-> "Unresolved", granted |-> {}, before |-> 0,
            after |-> 0, visible |-> FALSE, state |-> "Unchecked"]

Init == receipt = Initial

SelectTarget(hasParent) == IF hasParent THEN "ParentArn" ELSE "Unresolved"
IsPermitted(target, grants) == target = "ParentArn" /\ target \in grants

Check(op, hasParent, before, granted) ==
    LET target == SelectTarget(hasParent)
        permit == IsPermitted(target, granted)
        state == IF ~hasParent THEN "Refused"
                 ELSE IF ~permit THEN "Refused"
                 ELSE IF op = "Put" THEN "Written"
                 ELSE IF before = 0 THEN "NoPolicy" ELSE "Read"
        after == IF state = "Written" THEN before + 1 ELSE before
        visible == state = "Read"
    IN /\ ~receipt.checked
       /\ receipt' = [checked |-> TRUE, op |-> op, parent |-> hasParent,
                      target |-> target, granted |-> granted, before |-> before,
                      after |-> after, visible |-> visible, state |-> state]

Next ==
    \E op \in Operations, hasParent \in BOOLEAN, before \in 0..1, granted \in SUBSET Targets :
       /\ hasParent \/ before = 0
       /\ Check(op, hasParent, before, granted)

Spec == Init /\ [][Next]_receipt

TypeOK == receipt \in [checked: BOOLEAN, op: Operations, parent: BOOLEAN,
                       target: Targets \cup {"Unresolved"}, granted: SUBSET Targets,
                       before: 0..1, after: 0..2, visible: BOOLEAN, state: States]
ResolvedParentTarget ==
    receipt.checked /\ receipt.parent => receipt.target = "ParentArn"
NoUnpermittedEffects ==
    receipt.checked /\ ~(receipt.parent /\ "ParentArn" \in receipt.granted) =>
        ~receipt.visible /\ receipt.after = receipt.before
PermittedBehavior ==
    receipt.checked /\ receipt.parent /\ "ParentArn" \in receipt.granted =>
        IF receipt.op = "Put"
        THEN receipt.state = "Written" /\ receipt.after = receipt.before + 1
        ELSE IF receipt.before = 0 THEN receipt.state = "NoPolicy"
             ELSE receipt.state = "Read" /\ receipt.visible
ReadPreservesStoredPolicy ==
    receipt.checked /\ receipt.op = "Get" => receipt.after = receipt.before
=============================================================================
