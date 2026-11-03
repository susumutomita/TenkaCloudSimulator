--------------------------- MODULE StackOwnership ---------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Teams, Names
VARIABLES claims, present
vars == <<claims, present>>
Init == /\ claims = [t \in Teams |-> {}]
        /\ present = {}
Unclaimed(p) == \A t \in Teams : p \notin claims[t]
Create(t,p) == /\ Unclaimed(p)
               /\ claims' = [claims EXCEPT ![t] = @ \cup {p}]
               /\ present' = present \cup {p}
ManualDelete(p) == /\ p \in present
                    /\ present' = present \ {p}
                    /\ UNCHANGED claims
DeleteStack(t) == /\ claims[t] # {}
                   /\ present' = present \ claims[t]
                   /\ claims' = [claims EXCEPT ![t] = {}]
Next == (\E t \in Teams, p \in Names : Create(t,p))
        \/ (\E p \in Names : ManualDelete(p))
        \/ (\E t \in Teams : DeleteStack(t))
TypeOK == /\ claims \in [Teams -> SUBSET Names]
          /\ present \subseteq Names
OwnershipExclusive == \A a,b \in Teams : a # b => claims[a] \cap claims[b] = {}
Spec == Init /\ [][Next]_vars
=============================================================================
