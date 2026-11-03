--------------------------- MODULE ObjectStore ---------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS BucketNames, Keys
VARIABLES buckets, objects, allow, deny, changed, authorized
vars == <<buckets, objects, allow, deny, changed, authorized>>
Permitted == allow /\ ~deny
ObjectsFor(b) == {o \in objects: o[1] = b}
Init == /\ buckets = {} /\ objects = {}
        /\ allow = FALSE /\ deny = FALSE
        /\ changed = FALSE /\ authorized = FALSE
ChangePolicy == /\ allow' \in BOOLEAN /\ deny' \in BOOLEAN
                /\ changed' = FALSE /\ authorized' = FALSE
                /\ UNCHANGED <<buckets, objects>>
Create(b) == /\ Permitted /\ b \notin buckets
             /\ buckets' = buckets \cup {b} /\ UNCHANGED objects
             /\ changed' = TRUE /\ authorized' = TRUE
             /\ UNCHANGED <<allow, deny>>
Put(b,k) == /\ Permitted /\ b \in buckets
            /\ objects' = objects \cup {<<b,k>>} /\ UNCHANGED buckets
            /\ changed' = TRUE /\ authorized' = TRUE
            /\ UNCHANGED <<allow, deny>>
DeleteObject(b,k) == /\ Permitted /\ <<b,k>> \in objects
                     /\ objects' = objects \ {<<b,k>>} /\ UNCHANGED buckets
                     /\ changed' = TRUE /\ authorized' = TRUE
                     /\ UNCHANGED <<allow, deny>>
DeleteBucket(b) == /\ Permitted /\ b \in buckets /\ ObjectsFor(b) = {}
                   /\ buckets' = buckets \ {b} /\ UNCHANGED objects
                   /\ changed' = TRUE /\ authorized' = TRUE
                   /\ UNCHANGED <<allow, deny>>
Rejected == /\ ~Permitted /\ changed' = FALSE /\ authorized' = FALSE
            /\ UNCHANGED <<buckets, objects, allow, deny>>
Next == ChangePolicy \/ Rejected \/ (\E b \in BucketNames: Create(b) \/ DeleteBucket(b) \/ (\E k \in Keys: Put(b,k) \/ DeleteObject(b,k)))
TypeOK == /\ buckets \subseteq BucketNames
          /\ objects \subseteq (BucketNames \X Keys)
          /\ allow \in BOOLEAN /\ deny \in BOOLEAN
          /\ changed \in BOOLEAN /\ authorized \in BOOLEAN
NoOrphans == \A o \in objects: o[1] \in buckets
NoUnauthorizedMutation == changed => authorized
DenyWins == (changed /\ deny) => FALSE
Spec == Init /\ [][Next]_vars
=============================================================================
