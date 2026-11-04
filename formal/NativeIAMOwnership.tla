------------------------- MODULE NativeIAMOwnership -------------------------
EXTENDS FiniteSets
CONSTANT Names
VARIABLES legacy, claimed, active, permissions
vars == <<legacy, claimed, active, permissions>>
Init == /\ legacy = {} /\ claimed = {} /\ active = {} /\ permissions = {}
NativeCreate(n) == /\ n \notin legacy /\ n \notin active
                   /\ claimed' = claimed \cup {n} /\ active' = active \cup {n}
                   /\ UNCHANGED <<legacy, permissions>>
NativeGrant(n) == /\ n \in active /\ permissions' = permissions \cup {n}
                  /\ UNCHANGED <<legacy, claimed, active>>
NativeRemovePolicy(n) == /\ n \in active /\ permissions' = permissions \ {n}
                         /\ UNCHANGED <<legacy, claimed, active>>
NativeDelete(n) == /\ n \in active /\ n \notin permissions
                   /\ active' = active \ {n}
                   /\ UNCHANGED <<legacy, claimed, permissions>>
LegacyPut(n) == /\ n \notin claimed
                /\ legacy' = legacy \cup {n}
                /\ UNCHANGED <<claimed, active, permissions>>
LegacyDelete(n) == /\ n \notin claimed /\ legacy' = legacy \ {n}
                   /\ UNCHANGED <<claimed, active, permissions>>
Next == (\E n \in Names: NativeCreate(n) \/ NativeGrant(n) \/ NativeRemovePolicy(n)
         \/ NativeDelete(n) \/ LegacyPut(n) \/ LegacyDelete(n)) \/ UNCHANGED vars
TypeOK == /\ legacy \subseteq Names /\ claimed \subseteq Names
          /\ active \subseteq claimed /\ permissions \subseteq active
OwnershipExclusive == legacy \cap claimed = {}
=============================================================================
