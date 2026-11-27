-------------------- MODULE EC2LegacyNativeMainRepair --------------------
EXTENDS Naturals

CustomRows == 2
OneAvailableSlot == 1
Events == {"idle", "plan", "apply", "reapply"}
Results == {"none", "planned", "applied", "already", "refused"}
Peers == [explicit |-> 1, static |-> 2, replay |-> 1, stack |-> 1, other |-> 1]
Legacy == [revision |-> 1, marker |-> FALSE, main |-> 0, localCustom |-> 0,
           extra |-> 0, retained |-> Peers]
Wishes == [operator: BOOLEAN, target: BOOLEAN, own: BOOLEAN, distinct: BOOLEAN,
           matches: BOOLEAN, slot: BOOLEAN, schema: BOOLEAN, late: BOOLEAN]
Permit(w) == w.operator /\ w.target /\ w.own /\ w.distinct /\ w.matches /\ w.slot /\ w.schema /\ ~w.late

VARIABLES net, prev, witness, event, result
vars == <<net, prev, witness, event, result>>

Init == /\ net = Legacy /\ prev = Legacy
        /\ witness = [operator |-> FALSE, target |-> FALSE, own |-> FALSE, distinct |-> FALSE,
                        matches |-> FALSE, slot |-> FALSE, schema |-> FALSE, late |-> FALSE]
        /\ event = "idle" /\ result = "none"

Plan == /\ prev' = net /\ net' = net
        /\ event' = "plan" /\ result' = "planned" /\ UNCHANGED witness

Apply(w) ==
  /\ ~net.marker /\ prev' = net /\ event' = "apply" /\ witness' = w
  /\ IF Permit(w)
        THEN /\ result' = "applied"
             /\ net' = [net EXCEPT !.revision = 2, !.marker = TRUE, !.main = 1,
                                  !.localCustom = CustomRows, !.extra = 1]
        ELSE /\ result' = "refused" /\ net' = net

Reapply == /\ net.marker /\ prev' = net /\ net' = net
           /\ event' = "reapply" /\ result' = "already" /\ UNCHANGED witness

Next == Plan \/ (\E w \in Wishes: Apply(w)) \/ Reapply
Spec == Init /\ [][Next]_vars

TypeOK == /\ event \in Events /\ result \in Results /\ witness \in Wishes
          /\ net.revision \in 1..2 /\ net.marker \in BOOLEAN
          /\ net.main \in 0..1 /\ net.localCustom \in 0..CustomRows /\ net.extra \in 0..2

AtomicRepair == IF net.marker THEN net.revision = 2 /\ net.main = 1 /\ net.localCustom = CustomRows
                             ELSE net.revision = 1 /\ net.main = 0 /\ net.localCustom = 0
OneStoredRow == net.extra = net.main /\ net.extra <= OneAvailableSlot
ExactAdmission == result = "applied" => Permit(witness)
NoPartialRefusal == result = "refused" => net = prev
PlanAndRepeatAreReadOnly == event \in {"plan", "reapply"} => net = prev
RetainedFrame == net.retained = Peers
=============================================================================
