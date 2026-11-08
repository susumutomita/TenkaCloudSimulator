---------------------- MODULE WAFAssociationAuthorization ----------------------
EXTENDS TLC
ACLs == {"public", "private"}
None == "none"
VARIABLES association, phase, locked, operation, snapshot, authorized,
          stageAllowed, returned, removed
vars == <<association, phase, locked, operation, snapshot, authorized,
          stageAllowed, returned, removed>>
Init == /\ association \in ACLs \cup {None}
        /\ phase = "idle" /\ locked = FALSE /\ operation = "get"
        /\ snapshot = None /\ authorized = FALSE /\ stageAllowed \in BOOLEAN
        /\ returned = None /\ removed = None
Begin(kind) == /\ phase = "idle" /\ kind \in {"get", "delete"}
               /\ operation' = kind /\ phase' = "checked" /\ locked' = TRUE
               /\ snapshot' = association
               /\ authorized' = (association \in {None, "public"})
                                  /\ (kind = "get" \/ stageAllowed)
               /\ UNCHANGED <<association, stageAllowed, returned, removed>>
Reassociate(next) == /\ ~locked /\ next \in ACLs \cup {None}
                     /\ association' = next
                     /\ UNCHANGED <<phase, locked, operation, snapshot, authorized,
                                    stageAllowed, returned, removed>>
Complete == /\ phase = "checked"
            /\ returned' = IF authorized /\ operation = "get" THEN association ELSE None
            /\ removed' = IF authorized /\ operation = "delete" THEN association ELSE None
            /\ association' = IF authorized /\ operation = "delete" THEN None ELSE association
            /\ phase' = "done" /\ locked' = FALSE
            /\ UNCHANGED <<operation, snapshot, authorized, stageAllowed>>
Reset == /\ phase = "done" /\ phase' = "idle"
         /\ snapshot' = None /\ authorized' = FALSE /\ returned' = None /\ removed' = None
         /\ UNCHANGED <<association, locked, operation, stageAllowed>>
Next == (\E kind \in {"get", "delete"}: Begin(kind))
        \/ (\E next \in ACLs \cup {None}: Reassociate(next))
        \/ Complete \/ Reset \/ UNCHANGED vars
TypeOK == /\ association \in ACLs \cup {None} /\ snapshot \in ACLs \cup {None}
          /\ returned \in ACLs \cup {None} /\ removed \in ACLs \cup {None}
          /\ phase \in {"idle", "checked", "done"}
          /\ locked \in BOOLEAN /\ authorized \in BOOLEAN /\ stageAllowed \in BOOLEAN
          /\ operation \in {"get", "delete"}
CheckedAssociationStable == phase = "checked" => association = snapshot
NoUnauthorizedRead == returned \in {None, "public"}
NoUnauthorizedDelete == removed \in {None, "public"}
StagePermissionRequired == removed # None => stageAllowed
DeniedLeavesState == (phase = "done" /\ ~authorized) => returned = None /\ removed = None
=============================================================================
