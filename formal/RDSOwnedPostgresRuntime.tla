--------------------- MODULE RDSOwnedPostgresRuntime ---------------------
VARIABLE s

TypeOK == s \in [
    pin: BOOLEAN, acquire: {"known", "unknown"}, proof: BOOLEAN, stopped: BOOLEAN,
    phase: {"empty", "refused", "starting", "checking", "ready", "stopping", "closed", "debt"},
    workspace: BOOLEAN, owned: BOOLEAN, revoked: BOOLEAN, active: BOOLEAN, ack: BOOLEAN]
Init == /\ TypeOK /\ s.phase="empty"
        /\ ~s.workspace /\ ~s.owned /\ ~s.revoked /\ ~s.active /\ ~s.ack

CanStart == s.pin
CanPublish == s.owned /\ s.proof
CanAcknowledge == ~s.revoked
CanStop == ~s.active
KeepDebt == ~s.stopped

Begin ==
  /\ s.phase="empty"
  /\ s'=[s EXCEPT !.phase=IF CanStart THEN "starting" ELSE "refused",
                 !.workspace=CanStart]
Acquire ==
  /\ s.phase="starting"
  /\ s'=[s EXCEPT !.owned=s.acquire="known",
                 !.phase=IF s.acquire="known" THEN "checking" ELSE "debt"]
Confirm ==
  /\ s.phase="checking"
  /\ s'=[s EXCEPT !.phase=IF CanPublish THEN "ready" ELSE "stopping",
                 !.revoked=~CanPublish]
BeginProbe ==
  /\ s.phase="ready" /\ ~s.active
  /\ s'=[s EXCEPT !.active=TRUE, !.ack=FALSE]
FinishProbe ==
  /\ s.active
  /\ s'=[s EXCEPT !.active=FALSE, !.ack=CanAcknowledge]
Revoke ==
  /\ s.phase="ready"
  /\ s'=[s EXCEPT !.revoked=TRUE, !.phase="stopping", !.ack=FALSE]
Cleanup ==
  /\ s.phase="stopping" /\ CanStop
  /\ s'=[s EXCEPT !.phase=IF KeepDebt THEN "debt" ELSE "closed",
                 !.workspace=KeepDebt, !.owned=KeepDebt]
Next == Begin \/ Acquire \/ Confirm \/ BeginProbe \/ FinishProbe \/ Revoke \/ Cleanup
Spec == Init /\ [][Next]_s

NoWorkWithoutPin == ~s.pin => (~s.workspace /\ ~s.owned)
ReadyNeedsOwnedSCRAM == s.phase="ready" => (s.owned /\ s.proof /\ s.workspace /\ ~s.revoked)
NoProofAfterRevocation == s.revoked => ~s.ack
CleanupAfterDrain == s.phase="closed" => (~s.active /\ s.revoked /\ s.stopped /\ ~s.workspace)
UncertaintyRetainsWorkspace == s.phase="debt" => s.workspace
=============================================================================
