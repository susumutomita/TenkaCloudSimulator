----------------------- MODULE BattleDisruptionRoleCleanup -----------------------
CONSTANT Faults
VARIABLES fault, step, role, inline, work, observation, deleteWithPolicy
vars == <<fault, step, role, inline, work, observation, deleteWithPolicy>>
FailedInstalls == {"PutRejected", "PutLostReply"}
FailedWork == FailedInstalls \cup {"GradeFailed"}
Init == /\ fault \in Faults
        /\ step = "Create" /\ work = "Pending" /\ observation = "Unread"
        /\ role = IF fault = "CreateConflict" THEN "Other" ELSE "Absent"
        /\ inline = (role = "Other") /\ deleteWithPolicy = FALSE

Create == /\ step = "Create"
          /\ role' = IF fault = "CreateConflict" THEN role ELSE "Owned"
          /\ step' = IF fault \in {"CreateConflict", "CreateAmbiguous"} THEN "Refused" ELSE "Install"
          /\ UNCHANGED <<fault, inline, work, observation, deleteWithPolicy>>
Install == /\ step = "Install"
           /\ inline' = (fault # "PutRejected")
           /\ work' = IF fault \in FailedInstalls THEN "Failed" ELSE work
           /\ step' = IF fault \in FailedInstalls THEN "CheckOwner" ELSE "Grade"
           /\ UNCHANGED <<fault, role, observation, deleteWithPolicy>>
Grade == /\ step = "Grade"
         /\ work' = IF fault = "GradeFailed" THEN "Failed" ELSE "Passed"
         /\ role' = IF fault = "Replaced" THEN "Other" ELSE role
         /\ step' = "CheckOwner" (* GradeFinally *)
         /\ UNCHANGED <<fault, inline, observation, deleteWithPolicy>>

OwnerMatches == role = "Owned" /\ fault # "OwnerReadDenied"
CheckOwner == /\ step = "CheckOwner"
              /\ step' = IF OwnerMatches THEN "RemovePolicy" ELSE "Refused"
              /\ UNCHANGED <<fault, role, inline, work, observation, deleteWithPolicy>>
RemovePolicy == /\ step = "RemovePolicy"
                /\ IF fault = "PolicyRemovalDenied"
                      THEN /\ step' = "Refused" /\ UNCHANGED inline
                      ELSE /\ step' = "DeleteRole" /\ inline' = FALSE (* RemoveOrKnownAbsent *)
                /\ UNCHANGED <<fault, role, work, observation, deleteWithPolicy>>
DeleteRole == /\ step = "DeleteRole"
              /\ deleteWithPolicy' = inline
              /\ IF inline \/ fault = "DeleteDenied"
                    THEN /\ step' = "Refused" /\ UNCHANGED role
                    ELSE /\ step' = "ReadRole"
                         /\ role' = IF fault = "RoleSurvives" THEN role ELSE "Absent"
              /\ UNCHANGED <<fault, inline, work, observation>>
ReadRole == /\ step = "ReadRole"
            /\ observation' = CASE fault = "ReadUnqualified" -> "Unqualified"
                                [] role = "Absent" -> "NoSuchEntity404"
                                [] OTHER -> "Present"
            /\ step' = "Publish"
            /\ UNCHANGED <<fault, role, inline, work, deleteWithPolicy>>
AbsenceConfirmed == observation = "NoSuchEntity404"
GradingCompleted == work = "Passed"
Publish == /\ step = "Publish"
           /\ step' = IF AbsenceConfirmed /\ GradingCompleted THEN "Reported" ELSE "Refused"
           /\ UNCHANGED <<fault, role, inline, work, observation, deleteWithPolicy>>
Next == Create \/ Install \/ Grade \/ CheckOwner \/ RemovePolicy \/ DeleteRole \/ ReadRole \/ Publish
Spec == Init /\ [][Next]_vars

TypeOK == /\ fault \in Faults /\ inline \in BOOLEAN /\ deleteWithPolicy \in BOOLEAN
          /\ role \in {"Absent", "Owned", "Other"} /\ work \in {"Pending", "Failed", "Passed"}
          /\ observation \in {"Unread", "Unqualified", "NoSuchEntity404", "Present"}
          /\ step \in {"Create", "Install", "Grade", "CheckOwner", "RemovePolicy",
                        "DeleteRole", "ReadRole", "Publish", "Reported", "Refused"}
ForeignStatePreserved ==
    (fault = "CreateConflict" \/ (fault = "Replaced" /\ work = "Passed")) =>
      (role = "Other" /\ inline)
NoDeleteWithInlinePolicy == ~deleteWithPolicy
NoUnconfirmedCleanupReport ==
    step = "Reported" => (observation = "NoSuchEntity404" /\ role = "Absent" /\ ~inline)
NoFailedGradeReport == step = "Reported" => work = "Passed"
KnownRoleCleanedAfterFailedWork ==
    (fault \in FailedWork /\ step = "Refused") =>
      (role = "Absent" /\ ~inline /\ observation = "NoSuchEntity404")
==================================================================================
