---------------------- MODULE SecretsMutationRefresh ----------------------
EXTENDS Naturals, TLC
VARIABLES kind, formOpen, draftPresent, token, ack, phase, requests,
          retryHistory, retryDone, gen
vars == <<kind,formOpen,draftPresent,token,ack,phase,requests,
          retryHistory,retryDone,gen>>

Init == /\ kind \in {"Create","Put"}
        /\ formOpen = TRUE /\ draftPresent = TRUE /\ token = "T1"
        /\ ack = FALSE /\ phase = "Form" /\ requests = 0
        /\ retryHistory = [before |-> 0, after |-> 0]
        /\ retryDone = FALSE /\ gen = 0

Submit == /\ gen = 0 /\ phase = "Form" /\ ~ack
          /\ formOpen /\ draftPresent /\ token = "T1"
          /\ requests' = requests + 1 /\ phase' = "WritePending"
          /\ UNCHANGED <<kind,formOpen,draftPresent,token,ack,retryHistory,retryDone,gen>>

(* A live, current write ACK is known before the separate metadata request. *)
WriteACK == /\ phase = "WritePending" /\ ~ack
            /\ ack' = TRUE
            /\ formOpen' = FALSE (* AckFormExit *)
            /\ draftPresent' = FALSE (* AckDraftClear *)
            /\ token' = "None"
            /\ UNCHANGED <<kind,phase,requests,retryHistory,retryDone,gen>>

StartMetadataRead == /\ phase = "WritePending" /\ ack
                     /\ phase' = "ReadPending"
                     /\ UNCHANGED <<kind,formOpen,draftPresent,token,ack,requests,retryHistory,retryDone,gen>>

ReadSuccess == /\ phase = "ReadPending"
               /\ phase' = "Ready"
               /\ UNCHANGED <<kind,formOpen,draftPresent,token,ack,requests,retryHistory,retryDone,gen>>
ReadFailure == /\ phase = "ReadPending"
               /\ phase' = "ReadFailed"
               /\ UNCHANGED <<kind,formOpen,draftPresent,token,ack,requests,retryHistory,retryDone,gen>>

RetryMetadataRead ==
  /\ phase = "ReadFailed" /\ ~retryDone
  /\ retryDone' = TRUE /\ phase' = "ReadPending"
  /\ requests' = requests (* RetryReadOnly *)
  /\ retryHistory' = [before |-> requests, after |-> requests']
  /\ UNCHANGED <<kind,formOpen,draftPresent,token,ack,gen>>

(* A fresh form intent is represented, but no second write is in this bound. *)
FreshOpen == /\ gen = 0 /\ phase \in {"Ready","ReadFailed"}
             /\ gen' = 1 /\ phase' = "Form"
             /\ formOpen' = TRUE /\ draftPresent' = TRUE
             /\ token' = "T2" (* FreshIntentToken *)
             /\ UNCHANGED <<kind,ack,requests,retryHistory,retryDone>>

(* Explicit finite-scenario terminal observation, not a liveness claim. *)
Terminal == /\ gen = 1 \/ (phase = "ReadFailed" /\ retryDone)
            /\ UNCHANGED vars
Next == Submit \/ WriteACK \/ StartMetadataRead \/ ReadSuccess \/ ReadFailure
        \/ RetryMetadataRead \/ FreshOpen \/ Terminal

TypeOK == /\ kind \in {"Create","Put"}
          /\ formOpen \in BOOLEAN /\ draftPresent \in BOOLEAN /\ ack \in BOOLEAN
          /\ token \in {"T1","T2","None"}
          /\ phase \in {"Form","WritePending","ReadPending","ReadFailed","Ready"}
          /\ requests \in 0..2 /\ retryDone \in BOOLEAN /\ gen \in 0..1
          /\ retryHistory \in [before : 0..2, after : 0..2]
AckClosesForm == (ack /\ gen = 0) => ~formOpen
NoWriteOnReadRetry == retryHistory.before = retryHistory.after
AckClearsDraft == (ack /\ gen = 0) => (~draftPresent /\ token = "None")
FreshFormHasToken == (gen = 1 /\ formOpen) => token = "T2"
Spec == Init /\ [][Next]_vars
=============================================================================
