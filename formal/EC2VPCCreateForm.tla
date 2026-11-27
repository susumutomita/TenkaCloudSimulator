----------------------------- MODULE EC2VPCCreateForm -----------------------------
EXTENDS Naturals
Actors == {"operator", "native-only"}
Modes == {"vpc-only", "vpc-subnet"}
Epochs == 0..2
NoEpoch == 3
Empty == [phase |-> "list", mode |-> "vpc-only", valid |-> TRUE,
          confirmed |-> FALSE, from |-> NoEpoch]
NoWrite == [from |-> NoEpoch, mode |-> "vpc-only", action |-> "none", valid |-> FALSE]
VARIABLES actor, epoch, view, pending
vars == <<actor, epoch, view, pending>>
Init == /\ actor \in Actors /\ epoch = 0 /\ view = Empty /\ pending = NoWrite

Open == /\ view.phase = "list"
        /\ view' = [Empty EXCEPT !.phase = "form"]
        /\ UNCHANGED <<actor, epoch, pending>>
CanChoose(mode) == mode = "vpc-only" \/ actor = "operator"
Select(mode) == /\ view.phase = "form" /\ CanChoose(mode)
                /\ view' = [view EXCEPT !.mode = mode, !.valid = (mode = "vpc-only")]
                /\ UNCHANGED <<actor, epoch, pending>>
ChangeDraft(valid) == /\ view.phase = "form" /\ view' = [view EXCEPT !.valid = valid]
                     /\ UNCHANGED <<actor, epoch, pending>>

CanSubmit == view.valid /\ CanChoose(view.mode)
ActionFor(mode) == IF mode = "vpc-only" THEN "CreateVpc" ELSE "CreateStack"
Submit == /\ view.phase = "form" /\ pending = NoWrite /\ CanSubmit
          /\ pending' = [from |-> epoch, mode |-> view.mode,
                           action |-> ActionFor(view.mode), valid |-> view.valid]
          /\ view' = [view EXCEPT !.phase = "busy"]
          /\ UNCHANGED <<actor, epoch>>
Leave(nextActor) == /\ epoch < 2
                    /\ actor' = nextActor /\ epoch' = epoch + 1 /\ view' = Empty
                    /\ UNCHANGED pending
CurrentRead == pending.from = epoch /\ view.phase = "busy"
CanReport(exact) == exact
Observe(exact) ==
    /\ pending # NoWrite
    /\ view' = IF CurrentRead
        THEN IF CanReport(exact)
          THEN [view EXCEPT !.phase = "reported", !.confirmed = exact, !.from = pending.from]
          ELSE [view EXCEPT !.phase = "form"]
        ELSE view
    /\ pending' = NoWrite /\ UNCHANGED <<actor, epoch>>
Next == Open \/ Submit
        \/ \E mode \in Modes: Select(mode)
        \/ \E flag \in BOOLEAN: ChangeDraft(flag) \/ Observe(flag)
        \/ \E nextActor \in Actors: Leave(nextActor)
Spec == Init /\ [][Next]_vars

TypeOK == /\ actor \in Actors /\ epoch \in Epochs
          /\ view \in [phase: {"list", "form", "busy", "reported"}, mode: Modes,
                       valid: BOOLEAN, confirmed: BOOLEAN, from: Epochs \cup {NoEpoch}]
          /\ pending \in [from: Epochs \cup {NoEpoch}, mode: Modes,
                          action: {"none", "CreateVpc", "CreateStack"}, valid: BOOLEAN]
PairedNeedsOperator == view.mode = "vpc-subnet" => actor = "operator"
InvalidDraftNeverWrites == pending # NoWrite => pending.valid
NativeAndStackRemainDistinct ==
    pending # NoWrite => ((pending.mode = "vpc-only" /\ pending.action = "CreateVpc")
                           \/ (pending.mode = "vpc-subnet" /\ pending.action = "CreateStack"))
DiscardRetiredRead == view.phase = "reported" => view.from = epoch
ConfirmedBeforeSuccess == view.phase = "reported" => view.confirmed
==================================================================================
