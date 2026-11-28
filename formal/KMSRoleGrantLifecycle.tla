---------------------- MODULE KMSRoleGrantLifecycle ----------------------
EXTENDS Naturals
\* One representative DescribeKey grant slot per local key. A second role is
\* represented by a nonmatching caller. Generation zero means no row/session.
Keys == {1, 2}
Generations == {1, 2}
Actions == {"create", "list", "revoke"}
Managers == {"allowed", "denied", "foreign scope"}
Requests == {"canonical", "unsigned", "foreign scope", "other target", "expired"}
Events == Actions \cup {"idle","describe","restart"}

VARIABLES grants, enabled, rolePresent, generation, session, last
vars == <<grants, enabled, rolePresent, generation, session, last>>
Empty == [k \in Keys |-> 0]
Event(op, success, scope, keyOn, liveRole, frame, signed, noDeny, entitled) ==
    [op |-> op, success |-> success, scope |-> scope, keyOn |-> keyOn, liveRole |-> liveRole,
     frame |-> frame, signed |-> signed, noDeny |-> noDeny, entitled |-> entitled]
Idle == Event("idle",FALSE,TRUE,TRUE,TRUE,TRUE,TRUE,TRUE,FALSE)
Init ==
    /\ grants = Empty /\ enabled = Keys
    /\ rolePresent = TRUE /\ generation = 1 /\ session = 0 /\ last = Idle

Manage(op, k, manager) ==
    LET authorized == manager = "allowed"
        canCreate == rolePresent /\ k \in enabled /\ grants[k] = 0
        canRevoke == grants[k] # 0
        success == authorized /\ CASE op = "create" -> canCreate
                                   [] op = "revoke" -> canRevoke
                                   [] OTHER -> TRUE
        eligible == manager = "allowed" /\ CASE op = "create" -> rolePresent /\ k \in enabled /\ grants[k] = 0
                                                [] op = "revoke" -> grants[k] # 0
                                                [] OTHER -> TRUE
        nextGrants == IF ~success THEN grants
                      ELSE CASE op = "create" -> [grants EXCEPT ![k] = generation]
                             [] op = "revoke" -> [grants EXCEPT ![k] = 0]
                             [] OTHER -> grants
        \* Independent expected frame checks both the target and the peer.
        correct == \A j \in Keys: nextGrants[j] =
                      IF j # k \/ ~success THEN grants[j]
                      ELSE CASE op = "create" -> generation
                             [] op = "revoke" -> 0
                             [] OTHER -> grants[j]
    IN /\ grants' = nextGrants
       /\ last' = Event(op, success, manager = "allowed", k \in enabled, rolePresent,
                        correct, TRUE, TRUE, eligible)
       /\ UNCHANGED <<enabled, rolePresent, generation, session>>

Describe(k, request, sameRole, direct, denied) ==
    LET sessionOK == rolePresent /\ session # 0 /\ session = generation
        signedOK == request = "canonical"
        bound == sameRole /\ grants[k] = generation
        success == sessionOK /\ signedOK /\ ~denied /\ (direct \/ bound)
        \* The oracle consults the live grant at the selected key, independently
        \* of the admission formula. Direct identity permission remains distinct.
        entitled == direct \/ (sameRole /\ grants[k] # 0 /\ grants[k] = generation)
    IN /\ last' = Event("describe", success, TRUE, TRUE, rolePresent, TRUE,
                        request = "canonical" /\ rolePresent /\ session # 0 /\ session = generation,
                        ~denied, entitled)
       /\ UNCHANGED <<grants, enabled, rolePresent, generation, session>>

ChangeKey(k) == /\ enabled' = IF k \in enabled THEN enabled \ {k} ELSE enabled \cup {k}
                /\ last' = Idle /\ UNCHANGED <<grants, rolePresent, generation, session>>
Assume == /\ rolePresent /\ session' = generation /\ last' = Idle
          /\ UNCHANGED <<grants, enabled, rolePresent, generation>>
DeleteRole == /\ rolePresent /\ rolePresent' = FALSE /\ last' = Idle
              /\ UNCHANGED <<grants, enabled, generation, session>>
RecreateRole == /\ ~rolePresent /\ generation = 1 /\ rolePresent' = TRUE
                /\ generation' = 2 /\ last' = Idle /\ UNCHANGED <<grants, enabled, session>>
Restart ==
    LET persisted == grants
        retainedSession == 0
    IN /\ grants' = persisted /\ session' = retainedSession
       /\ last' = Event("restart", TRUE, TRUE, TRUE, TRUE,
                        persisted = grants /\ retainedSession = 0, TRUE, TRUE, FALSE)
       /\ UNCHANGED <<enabled, rolePresent, generation>>
Next == (\E op \in Actions, k \in Keys, a \in Managers: Manage(op,k,a))
     \/ (\E k \in Keys, req \in Requests, role, direct, denied \in BOOLEAN:
            Describe(k,req,role,direct,denied))
     \/ (\E k \in Keys: ChangeKey(k))
     \/ Assume \/ DeleteRole \/ RecreateRole \/ Restart

TypeOK == /\ grants \in [Keys -> 0..2] /\ enabled \subseteq Keys
          /\ rolePresent \in BOOLEAN /\ generation \in Generations /\ session \in 0..2
          /\ last \in [op: Events, success: BOOLEAN, scope: BOOLEAN, keyOn: BOOLEAN, liveRole: BOOLEAN,
                       frame: BOOLEAN, signed: BOOLEAN, noDeny: BOOLEAN, entitled: BOOLEAN]
ManagementAuthorized == last.op \in Actions /\ last.success => last.scope
CreateRequiresEnabled == last.op = "create" /\ last.success => last.keyOn
GrantToLiveRole == last.op = "create" /\ last.success => last.liveRole
PermittedManagementWorks == last.op \in Actions /\ last.entitled => last.success
GrantWriteFrame == last.op \in Actions => last.frame
VerifiedDescribe == last.op = "describe" /\ last.success => last.signed
ExplicitDenyWins == last.op = "describe" /\ last.success => last.noDeny
DirectOrBoundGrant == last.op = "describe" /\ last.success => last.entitled
PermittedDescribeWorks == last.op = "describe" /\ last.signed /\ last.noDeny /\ last.entitled => last.success
RestartFrame == last.op = "restart" => last.frame
=============================================================================
