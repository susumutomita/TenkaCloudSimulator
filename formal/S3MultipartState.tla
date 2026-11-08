---------------------------- MODULE S3MultipartState ----------------------------
EXTENDS Naturals, FiniteSets, TLC
\* Finite transaction model: labels abstract bound bucket/key/upload scopes,
\* not concrete random IDs. A zero charge denotes an absent part/object.
\* Requests, validation, SQL execution and rollback are not implementation steps.
Uploads == {"a", "b"}
Parts == {1, 2}
MaxBytes == 3
MaxObject == 2
EmptyParts == [n \in Parts |-> 0]
\* The last rejected transaction records its actual target state on both sides.
\* Keeping only the last event makes the observation history finite.
Snapshot(a,p,o,u) == <<u \in a, p[u], o[u]>>
Snapshots == BOOLEAN \X [Parts -> 0..2] \X (0..MaxObject)
VARIABLES active, parts, objects, completed, freed, failures
vars == <<active, parts, objects, completed, freed, failures>>
Bytes(p,o) == p["a"][1]+p["a"][2]+p["b"][1]+p["b"][2]+o["a"]+o["b"]
PartBytes(p,u) == p[u][1]+p[u][2]
GuardBudget == TRUE
GuardParent == TRUE
ReleaseParts == TRUE
AtomicFailure == TRUE
GuardScope == TRUE
Init ==
    /\ active = {}
    /\ parts = [u \in Uploads |-> EmptyParts]
    /\ objects = [u \in Uploads |-> 0]
    /\ completed = {}
    /\ freed = {}
    /\ failures = [before |-> <<FALSE,EmptyParts,0>>,
                    after |-> <<FALSE,EmptyParts,0>>]

Create(u) ==
    /\ u \notin active
    /\ active' = active \cup {u}
    /\ completed' = completed \ {u}
    /\ freed' = freed \ {u}
    /\ UNCHANGED <<parts,objects,failures>>

Put(u,n,size) ==
    LET next == [parts EXCEPT ![u][n] = size] IN
    /\ (u \in active \/ ~GuardParent)
    /\ (Bytes(next,objects) <= MaxBytes \/ ~GuardBudget)
    /\ parts' = next
    /\ UNCHANGED <<active,objects,completed,freed,failures>>

Complete(u) ==
    /\ u \in active
    /\ PartBytes(parts,u)>0
    /\ PartBytes(parts,u)<=MaxObject
    /\ active' = active \ {u}
    /\ objects' = [objects EXCEPT ![u] = PartBytes(parts,u)]
    /\ parts' = IF ReleaseParts THEN [parts EXCEPT ![u] = EmptyParts] ELSE parts
    /\ completed' = completed \cup {u}
    /\ freed' = freed \cup {u}
    /\ UNCHANGED failures

Abort(u) ==
    /\ u \in active
    /\ active' = active \ {u}
    /\ parts' = [parts EXCEPT ![u] = EmptyParts]
    /\ freed' = freed \cup {u}
    /\ UNCHANGED <<objects,completed,failures>>

Refused(u) ==
    \* A rejected completion/fault must retain its upload, parts and old object.
    \* The mutant really deletes parts; it does not merely forge the observation.
    /\ u \in active
    /\ PartBytes(parts,u)>0
    /\ parts' = IF AtomicFailure THEN parts ELSE [parts EXCEPT ![u] = EmptyParts]
    /\ UNCHANGED <<active,objects,completed,freed>>
    /\ failures' = [before |-> Snapshot(active,parts,objects,u),
                    after |-> Snapshot(active',parts',objects',u)]

ScopedAbort(caller,u) ==
    \* A request outside the bound scope is rejected, including an empty upload.
    /\ caller # u
    /\ u \in active
    /\ active' = IF GuardScope THEN active ELSE active \ {u}
    /\ parts' = IF GuardScope THEN parts ELSE [parts EXCEPT ![u] = EmptyParts]
    /\ UNCHANGED <<objects,completed,freed>>
    /\ failures' = [before |-> Snapshot(active,parts,objects,u),
                    after |-> Snapshot(active',parts',objects',u)]

Next ==
    \/ (\E u \in Uploads: Create(u) \/ Complete(u) \/ Abort(u) \/ Refused(u))
    \/ (\E u \in Uploads,n \in Parts,size \in 1..2: Put(u,n,size))
    \/ (\E c,u \in Uploads: ScopedAbort(c,u))
    \/ UNCHANGED vars

TypeOK == /\ active \subseteq Uploads /\ parts \in [Uploads -> [Parts -> 0..2]]
          /\ objects \in [Uploads -> 0..MaxObject] /\ completed \subseteq Uploads /\ freed \subseteq Uploads
          /\ failures \in [before : Snapshots, after : Snapshots]
Budget == Bytes(parts,objects)<=MaxBytes
NoOrphans == \A u \in Uploads \ active: PartBytes(parts,u)=0
Cleanup == \A u \in freed \ active: PartBytes(parts,u)=0
RefusalAtomic == failures.before = failures.after
=============================================================================
