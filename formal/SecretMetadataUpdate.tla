---------------------- MODULE SecretMetadataUpdate ----------------------
EXTENDS Naturals
VARIABLES phase, request, durable, pending, accepted
vars == <<phase, request, durable, pending, accepted>>
Keys == 1..2
Descriptions == {"old", "new", ""}
Initial == [k \in Keys |-> [description |-> "old", changed |-> 0,
  identity |-> k, created |-> 0, version |-> 1, value |-> k, stage |-> 1]]
Expected == [Initial EXCEPT ![request.target].description = request.description,
                           ![request.target].changed = 1]
Admitted == request.valid /\ request.present /\ request.allowed
Exact == pending = Expected
Init == /\ phase = "ready"
        /\ request \in [target: Keys, description: Descriptions,
             valid: BOOLEAN, present: BOOLEAN, allowed: BOOLEAN,
             ack: BOOLEAN, fault: {"none", "ignored", "drift"}]
        /\ durable = Initial /\ pending = Initial /\ accepted = FALSE
Stage == /\ phase = "ready"
         /\ pending' = IF Admitted THEN
               IF request.fault = "ignored" THEN Initial
               ELSE IF request.fault = "drift" THEN
                 [Expected EXCEPT ![request.target].created = 1]
               ELSE Expected
             ELSE Initial
         /\ phase' = "staged"
         /\ UNCHANGED <<request, durable, accepted>>
Finish == /\ phase = "staged"
          /\ LET success == Admitted /\ request.ack /\ Exact
             IN /\ accepted' = success
                /\ durable' = IF success THEN pending ELSE Initial
          /\ phase' = "done"
          /\ UNCHANGED <<request, pending>>
Next == Stage \/ Finish \/ (phase = "done" /\ UNCHANGED vars)
TypeOK == /\ phase \in {"ready", "staged", "done"}
          /\ accepted \in BOOLEAN
          /\ durable \in [Keys -> [description: Descriptions, changed: 0..1,
                 identity: Keys, created: 0..1, version: 1..2, value: Keys, stage: 1..2]]
Authorized == accepted => request.allowed
ValidPresent == accepted => request.valid /\ request.present
DurableAcknowledgement == accepted => request.ack /\ Exact
NoPartialVisibility == phase # "done" => durable = Initial
RefusalUnchanged == phase = "done" /\ ~accepted => durable = Initial
ExactMetadata == accepted =>
    durable[request.target].description = request.description /\
    durable[request.target].changed = 1
PeerPreserved == \A k \in Keys : k # request.target => durable[k] = Initial[k]
IdentityPreserved == \A k \in Keys : durable[k].identity = Initial[k].identity
CreatedPreserved == \A k \in Keys : durable[k].created = Initial[k].created
VersionPreserved == \A k \in Keys : durable[k].version = Initial[k].version
ValuePreserved == \A k \in Keys : durable[k].value = Initial[k].value
StagePreserved == \A k \in Keys : durable[k].stage = Initial[k].stage
=============================================================================
