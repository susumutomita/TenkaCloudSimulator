-------------------------- MODULE BattleLoaderDeadline --------------------------
EXTENDS Naturals

Kinds == {"Wall", "Monotonic"}
NoParent == [kind |-> "Absent", tick |-> 0]

VARIABLES sample, configured, parent, outcome
vars == <<sample, configured, parent, outcome>>
Unbound == [kind |-> "Unbound", tick |-> 0]
ParentOptions(t) == {NoParent} \cup [kind : Kinds, tick : (t - 1)..(t + 3)]

Init ==
    /\ sample \in 1..2
    /\ configured \in (sample + 1)..(sample + 2)
    /\ parent \in ParentOptions(sample)
    /\ outcome = Unbound

Bind ==
    /\ outcome = Unbound
    /\ LET selected == IF parent # NoParent /\ parent.tick < configured
                       THEN parent.tick ELSE configured
       IN outcome' = [kind |-> "Monotonic", tick |-> selected]
    /\ UNCHANGED <<sample, configured, parent>>

Next == Bind
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ sample \in 1..2
    /\ configured \in (sample + 1)..(sample + 2)
    /\ parent \in ParentOptions(sample)
    /\ outcome \in {Unbound} \cup [kind : Kinds, tick : 0..5]

DeadlineIsMonotonic == outcome # Unbound => outcome.kind = "Monotonic"

(* Check the exact minimum independently of Bind's selection expression:
   the result is an input instant and cannot be greater than either input. *)
ExactDeadlineInstant ==
    outcome # Unbound =>
       /\ outcome.tick <= configured
       /\ (parent # NoParent => outcome.tick <= parent.tick)
       /\ (outcome.tick = configured \/
           (parent # NoParent /\ outcome.tick = parent.tick))
=============================================================================
