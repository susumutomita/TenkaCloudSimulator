----------------------------- MODULE ProviderActor -----------------------------
EXTENDS Naturals

None == [present |-> FALSE, credential |-> "", exercise |-> "", role |-> ""]
Roles == {"operator", "participant"}
Selections == {"", "host", "reader", "other"}
Requests == [authenticated : BOOLEAN, role : Roles, selected : Selections,
             wireRole : Roles, capabilities : BOOLEAN]
Credential(r) == IF r.role = "operator" THEN "host" ELSE "reader"
Admission(r) == r.authenticated /\ (r.role = "operator" \/
                    r.selected \in {"", Credential(r)} \/ r.capabilities)
Exercise(r) == IF r.role = "participant" THEN Credential(r)
               ELSE IF r.selected = "" THEN Credential(r) ELSE r.selected
Attach(r) == IF Admission(r)
             THEN [present |-> TRUE, credential |-> Credential(r), exercise |-> Exercise(r), role |-> r.role]
             ELSE None

VARIABLE emission
vars == <<emission>>
Init == emission = [present |-> FALSE, request |-> [authenticated |-> FALSE, role |-> "operator",
                       selected |-> "", wireRole |-> "operator", capabilities |-> FALSE], actor |-> None]
Emit(r) == emission' = [present |-> TRUE, request |-> r, actor |-> Attach(r)]
Next == \E r \in Requests : Emit(r)
Spec == Init /\ [][Next]_vars

ActorValues == [present : {TRUE}, credential : {"host", "reader"}, exercise : {"host", "reader", "other"}, role : Roles]
TypeOK == emission \in [present : BOOLEAN, request : Requests, actor : ActorValues \cup {None}]
NoActorOnRejected == ~emission.present \/
  LET r == emission.request IN
    (IF ~r.authenticated \/ (r.role = "participant" /\
        r.selected \notin {"", Credential(r)} /\ ~r.capabilities)
     THEN ~emission.actor.present ELSE TRUE)
AuthenticatedRequestsAttach == ~emission.present \/
  LET r == emission.request IN
    (IF r.authenticated /\ (r.role = "operator" \/
       r.selected \in {"", Credential(r)} \/ r.capabilities)
     THEN emission.actor.present ELSE TRUE)
CredentialIdentityPreserved == ~emission.present \/ ~emission.actor.present \/
  emission.actor.credential = Credential(emission.request)
CredentialRoleNotWireRole == ~emission.present \/ ~emission.actor.present \/
  emission.actor.role = emission.request.role
ParticipantBound == ~emission.present \/ ~emission.actor.present \/
  (emission.request.role # "participant" \/
   emission.actor.exercise = Credential(emission.request))
OperatorExerciseSelection == ~emission.present \/ ~emission.actor.present \/
  (emission.request.role # "operator" \/
   emission.actor.exercise = IF emission.request.selected = ""
                            THEN Credential(emission.request) ELSE emission.request.selected)
=============================================================================
