------------------------ MODULE EC2InstanceStackConsole ------------------------
EXTENDS Naturals

Actors == {"operator", "exercise-reader", "participant", "handoff", "nonlocal", "anonymous"}
Phases == {"idle", "checked", "submitting", "accepted", "checking", "unknown", "unconfirmed", "verified"}
Epochs == 0..2
NoEpoch == 3
NoFlight == [kind |-> "none", epoch |-> NoEpoch]
EmptyView == [phase |-> "idle", allow |-> FALSE, qualified |-> FALSE,
              writes |-> 0, receiptFrom |-> NoEpoch, evidence |-> FALSE, guestClaim |-> FALSE]

VARIABLES actor, liveConfig, epoch, view, flight
vars == <<actor, liveConfig, epoch, view, flight>>

Init ==
    /\ actor = "operator"
    /\ liveConfig = FALSE
    /\ epoch = 0
    /\ view = EmptyView
    /\ flight = NoFlight

\* A fresh authenticated discovery samples configuration, not guest liveness.
SetRuntime(value) ==
    /\ liveConfig' = value
    /\ UNCHANGED <<actor, epoch, view, flight>>
Eligible == actor = "operator" /\ liveConfig
CheckCapability ==
    /\ view.phase \in {"idle", "checked"}
    /\ flight = NoFlight
    /\ view' = [view EXCEPT !.phase = "checked", !.allow = Eligible, !.qualified = Eligible]
    /\ UNCHANGED <<actor, liveConfig, epoch, flight>>
DispatchCreate ==
    /\ view.phase = "checked"
    /\ view.allow
    /\ view.writes = 0
    /\ flight = NoFlight
    /\ flight' = [kind |-> "write", epoch |-> epoch]
    /\ view' = [view EXCEPT !.phase = "submitting", !.writes = @ + 1]
    /\ UNCHANGED <<actor, liveConfig, epoch>>

AcceptReceipt ==
    /\ flight.kind = "write"
    /\ flight.epoch = epoch
    /\ view' = [view EXCEPT !.phase = "accepted", !.receiptFrom = flight.epoch]
    /\ flight' = NoFlight
    /\ UNCHANGED <<actor, liveConfig, epoch>>
LoseReceipt ==
    /\ flight.kind = "write"
    /\ flight.epoch = epoch
    /\ flight' = NoFlight
    /\ view' = [view EXCEPT !.phase = "unknown"]
    /\ UNCHANGED <<actor, liveConfig, epoch>>

\* Checking again after acknowledgement is read-only.
RequestRead ==
    /\ view.phase \in {"accepted", "unconfirmed"}
    /\ flight = NoFlight
    /\ flight' = [kind |-> "read", epoch |-> epoch]
    /\ view' = [view EXCEPT !.phase = "checking"]
    /\ UNCHANGED <<actor, liveConfig, epoch>>
FinishRead(completeMatch) ==
    /\ flight.kind = "read"
    /\ flight.epoch = epoch
    /\ flight' = NoFlight
    /\ view' = [view EXCEPT !.phase = IF completeMatch THEN "verified" ELSE "unconfirmed",
                            !.evidence = completeMatch, !.guestClaim = FALSE]
    /\ UNCHANGED <<actor, liveConfig, epoch>>

ChangeScope(nextActor) ==
    /\ epoch < 2
    /\ epoch' = epoch + 1
    /\ actor' = nextActor
    /\ view' = EmptyView
    /\ UNCHANGED <<liveConfig, flight>>
DiscardLate ==
    /\ flight # NoFlight
    /\ flight.epoch # epoch
    /\ flight' = NoFlight
    /\ UNCHANGED <<actor, liveConfig, epoch, view>>

Next ==
    \/ \E value \in BOOLEAN: SetRuntime(value)
    \/ CheckCapability
    \/ DispatchCreate
    \/ AcceptReceipt
    \/ LoseReceipt
    \/ RequestRead
    \/ \E match \in BOOLEAN: FinishRead(match)
    \/ \E nextActor \in Actors: ChangeScope(nextActor)
    \/ DiscardLate
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ actor \in Actors
    /\ liveConfig \in BOOLEAN
    /\ epoch \in Epochs
    /\ view \in [phase: Phases, allow: BOOLEAN, qualified: BOOLEAN, writes: 0..2,
                  receiptFrom: Epochs \cup {NoEpoch}, evidence: BOOLEAN, guestClaim: BOOLEAN]
    /\ flight \in {NoFlight} \cup [kind: {"write", "read"}, epoch: Epochs]
QualifiedMutation == view.writes > 0 => view.qualified
SingleMutation == view.writes <= 1
ReceiptBelongsToView == view.receiptFrom # NoEpoch => view.receiptFrom = epoch
VerifiedMeansMatch == view.phase = "verified" => view.receiptFrom = epoch /\ view.evidence
NoGuestLivenessClaim == ~view.guestClaim
===============================================================================
