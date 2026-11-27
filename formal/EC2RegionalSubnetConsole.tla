------------------------- MODULE EC2RegionalSubnetConsole -------------------------
EXTENDS Naturals
Epochs == 0..2
NoEpoch == 3
Owners == {"none", "ec2", "cloudformation"}
States == {"list", "form", "waiting", "unconfirmed", "detail"}
Empty == [state |-> "list", parent |-> "none", valid |-> FALSE,
          inventory |-> FALSE, shownAt |-> NoEpoch, confirmed |-> FALSE]
NoWrite == [epoch |-> NoEpoch, owner |-> "none", read |-> FALSE, fromUnknown |-> FALSE]
VARIABLES epoch, view, pending
vars == <<epoch, view, pending>>
Init == /\ epoch = 0 /\ view = Empty /\ pending = NoWrite
Open == /\ view.state = "list" /\ view' = [Empty EXCEPT !.state = "form"]
        /\ UNCHANGED <<epoch, pending>>
Read(verified) == /\ view.state = "form" /\ view' = [view EXCEPT !.inventory = verified]
                  /\ UNCHANGED <<epoch, pending>>
Choose(owner, valid) == /\ view.state = "form"
                        /\ view' = [view EXCEPT !.parent = owner, !.valid = valid]
                        /\ UNCHANGED <<epoch, pending>>
FormIsActive == view.state = "form"
CurrentParent == view.parent = "ec2"
CurrentInventory == view.inventory
Dispatch == /\ FormIsActive /\ CurrentParent /\ CurrentInventory /\ view.valid /\ pending = NoWrite
            /\ pending' = [epoch |-> epoch, owner |-> view.parent, read |-> view.inventory,
                             fromUnknown |-> view.state = "unconfirmed"]
            /\ view' = [view EXCEPT !.state = "waiting"]
            /\ UNCHANGED epoch
Leave == /\ epoch < 2 /\ epoch' = epoch + 1 /\ view' = Empty
         /\ UNCHANGED pending
CurrentOperation == pending.epoch = epoch /\ view.state = "waiting"
ReceiptConfirmed(exact) == exact
Resolve(exact) ==
    /\ pending # NoWrite
    /\ view' = IF CurrentOperation
        THEN IF ReceiptConfirmed(exact)
          THEN [view EXCEPT !.state = "detail", !.shownAt = pending.epoch, !.confirmed = exact]
          ELSE [view EXCEPT !.state = "unconfirmed"]
        ELSE view
    /\ pending' = NoWrite
    /\ UNCHANGED epoch
Next == Open \/ Dispatch \/ Leave
        \/ \E ok \in BOOLEAN: Read(ok) \/ Resolve(ok)
        \/ \E owner \in Owners, valid \in BOOLEAN: Choose(owner, valid)
Spec == Init /\ [][Next]_vars
TypeOK ==
    /\ epoch \in Epochs
    /\ view \in [state: States, parent: Owners, valid: BOOLEAN, inventory: BOOLEAN,
                 shownAt: Epochs \cup {NoEpoch}, confirmed: BOOLEAN]
    /\ pending \in [epoch: Epochs \cup {NoEpoch}, owner: Owners, read: BOOLEAN, fromUnknown: BOOLEAN]
OnlyNativeParentWrites == pending # NoWrite => pending.owner = "ec2"
ConfirmedInventoryBeforeWrite == pending # NoWrite => pending.read
ReadbackBeforeDetail == view.state = "detail" => view.confirmed
CurrentScopeOnly == view.state = "detail" => view.shownAt = epoch
NoUnreconciledRetry == pending # NoWrite => ~pending.fromUnknown
==================================================================================
