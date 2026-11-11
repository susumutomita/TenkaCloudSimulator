---------------- MODULE ListenerLifecycle ----------------
EXTENDS Naturals, FiniteSets
Ports == {1,2}
Jobs == {1,2}
Kinds == {"syn","changed-syn","syn-payload","syn-flags","data","bad-tuple","bad-parse"}
VARIABLES phase, cancelled, now, attempts, sequences, firstSequences, sealed, everPorts, frames, bytes,
 chargedFrames, chargedBytes, calls, conns, everConns, resetCalls, resetConns, deadlines,
 shutdown, destroyed, reserved, workers, admissionViolation, pendingViolation
vars == <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
 chargedFrames,chargedBytes,calls,conns,everConns,resetCalls,resetConns,deadlines,
 shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
Active == phase = "ready" /\ ~cancelled /\ now < 1
Pending == {j \in Jobs: calls[j] = "pending"}
Debts == {j \in Jobs: calls[j] \in {"pending","registered","cleanup","reset-attempted"}}
Returned == {j \in Jobs: conns[j] \in {"returned-ok","returned-error"}}
Owned == {j \in Jobs: conns[j] # "absent"}
Init ==
 /\ phase = "new" /\ cancelled = FALSE /\ now = 0
 /\ attempts = {} /\ sequences = [p \in Ports |-> 0] /\ firstSequences = [p \in Ports |-> 0] /\ sealed = {} /\ everPorts = {}
 /\ frames = 0 /\ bytes = 0 /\ chargedFrames = 0 /\ chargedBytes = 0
 /\ calls = [j \in Jobs |-> "unused"] /\ conns = [j \in Jobs |-> "absent"]
 /\ everConns = {} /\ resetCalls = {} /\ resetConns = {} /\ deadlines = [j \in Jobs |-> 0]
 /\ shutdown = FALSE /\ destroyed = FALSE /\ reserved = FALSE /\ workers = 1
 /\ admissionViolation = FALSE /\ pendingViolation = FALSE
Witness(k,ok,valid) ==
 /\ admissionViolation' = (admissionViolation \/ (ok /\ (~Active \/ ~valid)))
 /\ pendingViolation' = (pendingViolation \/ (k = "accept-start" /\ ok /\ Pending # {}))
Create ==
 /\ phase = "new" /\ phase' = "ready" /\ reserved' = TRUE /\ workers' = workers+1
 /\ UNCHANGED <<cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,chargedFrames,
                chargedBytes,calls,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,admissionViolation,pendingViolation>>
Tick ==
 /\ now = 0 /\ now' = 1
 /\ UNCHANGED <<phase,cancelled,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,chargedFrames,
                chargedBytes,calls,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
SpecInject(p,k) ==
 CASE k = "data" -> p \in attempts
   [] k \in {"syn","changed-syn"} ->
       IF p \notin attempts THEN Cardinality(attempts) < 1
       ELSE p \notin sealed /\ sequences[p] = (IF k = "syn" THEN 1 ELSE 2)
   [] OTHER -> FALSE
Inject(p,k,size) ==
 /\ Active
 /\ LET fits == size \in {1,2} /\ frames < 2 /\ bytes+size <= 3
        valid == k \notin {"bad-tuple","bad-parse","syn-payload","syn-flags"}
          /\ (IF k = "data" THEN p \in attempts
               ELSE IF p \in attempts THEN p \notin sealed /\ sequences[p] = (IF k = "syn" THEN 1 ELSE 2)
               ELSE Cardinality(attempts) < 1)
        ok == fits /\ valid
    IN /\ frames' = frames + (IF fits THEN 1 ELSE 0)
       /\ bytes' = bytes + (IF fits THEN size ELSE 0)
       /\ chargedFrames' = chargedFrames + (IF fits THEN 1 ELSE 0)
       /\ chargedBytes' = chargedBytes + (IF fits THEN size ELSE 0)
       /\ attempts' = IF ok THEN attempts \cup {p} ELSE attempts
       /\ sequences' = IF ok /\ p \notin attempts THEN [sequences EXCEPT ![p] = IF k = "syn" THEN 1 ELSE 2] ELSE sequences
       /\ firstSequences' = IF ok /\ p \notin attempts THEN [firstSequences EXCEPT ![p] = IF k = "syn" THEN 1 ELSE 2] ELSE firstSequences
       /\ everPorts' = IF ok THEN everPorts \cup {p} ELSE everPorts
       /\ sealed' = IF ok /\ k = "data" THEN sealed \cup {p} ELSE sealed
       /\ cancelled' = (cancelled \/ ~ok)
       /\ Witness("inject",ok,SpecInject(p,k))
 /\ UNCHANGED <<phase,now,calls,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers>>
Output(p,valid,size) ==
 /\ Active
 /\ LET fits == size \in {1,2} /\ frames < 2 /\ bytes+size <= 3
        ok == fits /\ valid /\ p \in attempts
    IN /\ frames' = frames + (IF fits THEN 1 ELSE 0)
       /\ bytes' = bytes + (IF fits THEN size ELSE 0)
       /\ chargedFrames' = chargedFrames + (IF fits THEN 1 ELSE 0)
       /\ chargedBytes' = chargedBytes + (IF fits THEN size ELSE 0)
       /\ cancelled' = (cancelled \/ ~ok)
       /\ Witness("output",ok,valid /\ p \in attempts)
 /\ UNCHANGED <<phase,now,attempts,sequences,firstSequences,sealed,everPorts,calls,conns,everConns,resetCalls,resetConns,
                deadlines,shutdown,destroyed,reserved,workers>>
AcceptStart(j) ==
 /\ Active /\ Pending = {} /\ calls[j] = "unused"
 /\ calls' = [calls EXCEPT ![j] = "pending"]
 /\ Witness("accept-start",TRUE,TRUE)
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers>>
AcceptReturn(j,p,rawOK) ==
 /\ calls[j] = "pending"
 /\ LET ok == Active /\ rawOK /\ p \in attempts /\ Cardinality(Owned) < 1
    IN /\ calls' = [calls EXCEPT ![j] = IF ok THEN "registered" ELSE "cleanup"]
       /\ conns' = IF ok THEN [conns EXCEPT ![j] = "open"] ELSE conns
       /\ everConns' = IF ok THEN everConns \cup {j} ELSE everConns
       /\ sealed' = IF ok THEN sealed \cup {p} ELSE sealed
       /\ cancelled' = (cancelled \/ (Active /\ rawOK /\ Cardinality(Owned) >= 1))
       /\ Witness("accept-return",ok,rawOK /\ p \in attempts)
 /\ UNCHANGED <<phase,now,attempts,sequences,firstSequences,everPorts,frames,bytes,chargedFrames,
                chargedBytes,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers>>
AcceptComplete(j) ==
 /\ calls[j] = "registered" /\ calls' = [calls EXCEPT ![j] = "accepted"]
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
CleanupTimer(j) ==
 /\ calls[j] = "cleanup" /\ calls' = [calls EXCEPT ![j] = "reset-attempted"] /\ resetCalls' = resetCalls \cup {j}
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,conns,everConns,resetConns,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
CleanupReturn(j) ==
 /\ calls[j] = "reset-attempted" /\ calls' = [calls EXCEPT ![j] = "returned"]
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
ConnectionClose(j) ==
 /\ conns[j] = "open" /\ conns' = [conns EXCEPT ![j] = "closed-flag"]
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,calls,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
ConnectionTimer(j) ==
 /\ conns[j] = "closed-flag" /\ conns' = [conns EXCEPT ![j] = "reset-attempted"] /\ resetConns' = resetConns \cup {j}
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,calls,everConns,resetCalls,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
ConnectionReturn(j,error) ==
 /\ conns[j] = "reset-attempted"
 /\ conns' = [conns EXCEPT ![j] = IF error THEN "returned-error" ELSE "returned-ok"]
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,calls,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
Deadline(j,requested) ==
 /\ Active /\ conns[j] = "open" /\ deadlines[j] = 0
 /\ deadlines' = [deadlines EXCEPT ![j] = IF requested = 0 \/ requested > 1 THEN 1 ELSE requested]
 /\ Witness("deadline",TRUE,TRUE)
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,calls,conns,everConns,resetCalls,resetConns,shutdown,destroyed,reserved,workers>>
CloseStart ==
 /\ phase = "ready" /\ phase' = "closing" /\ cancelled' = TRUE
 /\ UNCHANGED <<now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,chargedFrames,
                chargedBytes,calls,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,reserved,workers,admissionViolation,pendingViolation>>
Shutdown ==
 /\ phase = "closing" /\ ~shutdown /\ shutdown' = TRUE
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,calls,conns,everConns,resetCalls,resetConns,deadlines,destroyed,reserved,workers,admissionViolation,pendingViolation>>
Destroy ==
 /\ phase = "closing" /\ shutdown /\ Debts = {} /\ Owned = Returned /\ ~destroyed
 /\ destroyed' = TRUE
 /\ UNCHANGED <<phase,cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,calls,conns,everConns,resetCalls,resetConns,deadlines,shutdown,reserved,workers,admissionViolation,pendingViolation>>
Release ==
 /\ phase = "closing" /\ destroyed /\ reserved
 /\ reserved' = FALSE /\ workers' = workers-1 /\ phase' = "released"
 /\ UNCHANGED <<cancelled,now,attempts,sequences,firstSequences,sealed,everPorts,frames,bytes,
                chargedFrames,chargedBytes,calls,conns,everConns,resetCalls,resetConns,deadlines,shutdown,destroyed,admissionViolation,pendingViolation>>
Next ==
 \/ Create \/ Tick \/ CloseStart \/ Shutdown \/ Destroy \/ Release
 \/ (Active /\ (\E p \in Ports,k \in Kinds,size \in {1,2,4}: Inject(p,k,size)))
 \/ (Active /\ (\E p \in Ports,valid \in BOOLEAN,size \in {1,2,4}: Output(p,valid,size)))
 \/ \E j \in Jobs: AcceptStart(j) \/ AcceptComplete(j) \/ CleanupTimer(j) \/ CleanupReturn(j)
      \/ ConnectionClose(j) \/ ConnectionTimer(j)
      \/ (\E p \in Ports,ok \in BOOLEAN: AcceptReturn(j,p,ok))
      \/ (\E error \in BOOLEAN: ConnectionReturn(j,error))
      \/ (\E requested \in {0,1,2}: Deadline(j,requested))
SequenceRetention == sequences = firstSequences
AttemptRetention == attempts = everPorts /\ Cardinality(attempts) <= 1 /\ sealed \subseteq attempts
OwnerRetention == Owned = everConns /\ Cardinality(Owned) <= 1
OnePendingAccept == Cardinality(Pending) <= 1
BudgetAccounting == frames = chargedFrames /\ bytes = chargedBytes /\ frames <= 2 /\ bytes <= 3
NoLateAdmission == ~admissionViolation
OneAcceptAdmission == ~pendingViolation
JoinBeforeDestroy == destroyed => shutdown /\ Debts = {} /\ Owned = Returned /\ phase \in {"closing","released"}
CapacityAfterJoin == phase = "released" => destroyed /\ Debts = {} /\ Owned = Returned
SharedCapacity == workers = 1 + (IF reserved THEN 1 ELSE 0)
NoEarlyRelease == phase \in {"ready","closing"} => reserved
TimerResetAttempted == (\A j \in Jobs: calls[j] = "returned" => j \in resetCalls)
 /\ Returned \subseteq resetConns
BoundedDeadline == \A j \in Jobs: deadlines[j] \in {0,1}
=============================================================================
