---------------------- MODULE BedrockRuntimeAdmission ----------------------
\* Finite admission abstraction of opt-in Bedrock Runtime local inference.
\* Each request is checked in the implementation's order (operation, enabled
\* backend, IAM, operator mapping, input, shutdown, concurrency slot) before
\* the one configured backend is called. Only that backend's result answers.
EXTENDS Naturals, FiniteSets
CONSTANTS Requests, Limit
Outcomes == {"ok", "unavailable", "missing", "timeout", "busy", "error"}
Refusals == {"unsupported", "disabled", "denied", "notMapped", "invalid", "closed", "throttled"}
Responses == {"none", "canceled"} \cup Outcomes \cup Refusals
NoAttr == [supported |-> FALSE, authorized |-> FALSE, mapped |-> FALSE, valid |-> FALSE]
VARIABLES enabled, closed, phase, attr, active, calls, used, closedAtCall, outcome, response, stored
vars == <<enabled, closed, phase, attr, active, calls, used, closedAtCall, outcome, response, stored>>

Init == /\ enabled \in BOOLEAN
        /\ closed = FALSE
        /\ phase = [r \in Requests |-> "new"]
        /\ attr = [r \in Requests |-> NoAttr]
        /\ active = 0
        /\ calls = {}
        /\ used = [r \in Requests |-> "none"]
        /\ closedAtCall = [r \in Requests |-> FALSE]
        /\ outcome = [r \in Requests |-> "none"]
        /\ response = [r \in Requests |-> "none"]
        /\ stored = {}

Arrive(r, s, a, m, v) ==
    /\ phase[r] = "new"
    /\ phase' = [phase EXCEPT ![r] = "arrived"]
    /\ attr' = [attr EXCEPT ![r] = [supported |-> s, authorized |-> a, mapped |-> m, valid |-> v]]
    /\ UNCHANGED <<enabled, closed, active, calls, used, closedAtCall, outcome, response, stored>>

Refusal(r) ==
    IF ~attr[r].supported THEN "unsupported"
    ELSE IF ~enabled THEN "disabled"
    ELSE IF ~attr[r].authorized THEN "denied"
    ELSE IF ~attr[r].mapped THEN "notMapped"
    ELSE IF ~attr[r].valid THEN "invalid"
    ELSE IF closed THEN "closed"
    ELSE IF active >= Limit THEN "throttled"
    ELSE "none"

Admit(r) ==
    /\ phase[r] = "arrived"
    /\ IF Refusal(r) # "none"
       THEN /\ phase' = [phase EXCEPT ![r] = "done"]
            /\ response' = [response EXCEPT ![r] = Refusal(r)]
            /\ UNCHANGED <<enabled, closed, attr, active, calls, used, closedAtCall, outcome, stored>>
       ELSE /\ phase' = [phase EXCEPT ![r] = "running"]
            /\ active' = active + 1
            /\ calls' = calls \cup {r}
            /\ used' = [used EXCEPT ![r] = "configured"]
            /\ closedAtCall' = [closedAtCall EXCEPT ![r] = closed]
            /\ UNCHANGED <<enabled, closed, attr, outcome, response, stored>>

\* The configured backend's own result, success or typed failure, is final.
Finish(r, result) ==
    /\ phase[r] = "running"
    /\ phase' = [phase EXCEPT ![r] = "done"]
    /\ active' = active - 1
    /\ outcome' = [outcome EXCEPT ![r] = result]
    /\ response' = [response EXCEPT ![r] = result]
    /\ stored' = stored
    /\ UNCHANGED <<enabled, closed, attr, calls, used, closedAtCall>>

Close == /\ ~closed
         /\ closed' = TRUE
         /\ UNCHANGED <<enabled, phase, attr, active, calls, used, closedAtCall, outcome, response, stored>>

Cancel(r) ==
    /\ phase[r] = "running" /\ closed
    /\ phase' = [phase EXCEPT ![r] = "done"]
    /\ active' = active - 1
    /\ response' = [response EXCEPT ![r] = "canceled"]
    /\ UNCHANGED <<enabled, closed, attr, calls, used, closedAtCall, outcome, stored>>

Next == \/ \E r \in Requests, s, a, m, v \in BOOLEAN : Arrive(r, s, a, m, v)
        \/ \E r \in Requests : Admit(r) \/ Cancel(r)
        \/ \E r \in Requests, result \in Outcomes : Finish(r, result)
        \/ Close
        \/ UNCHANGED vars

TypeOK == /\ enabled \in BOOLEAN /\ closed \in BOOLEAN
          /\ phase \in [Requests -> {"new", "arrived", "running", "done"}]
          /\ attr \in [Requests -> [supported : BOOLEAN, authorized : BOOLEAN, mapped : BOOLEAN, valid : BOOLEAN]]
          /\ active \in 0..Cardinality(Requests)
          /\ calls \subseteq Requests
          /\ used \in [Requests -> {"none", "configured", "other"}]
          /\ closedAtCall \in [Requests -> BOOLEAN]
          /\ outcome \in [Requests -> Outcomes \cup {"none"}]
          /\ response \in [Requests -> Responses]
          /\ stored \subseteq Requests

\* Unsupported operations, a disabled backend, unmapped models and invalid
\* input never reach the backend.
BackendOnlyAfterAdmission == \A r \in calls :
    attr[r].supported /\ enabled /\ attr[r].mapped /\ attr[r].valid
\* IAM is evaluated before the mapping, input, shutdown or slot is consulted.
AuthorizationFirst == \A r \in Requests :
    (r \in calls \/ response[r] \in {"notMapped", "invalid", "closed", "throttled"}) => attr[r].authorized
\* A closed runtime admits nothing new to the backend.
NoCallAfterClose == \A r \in calls : ~closedAtCall[r]
\* Slots never exceed the operator limit and are released on every ending.
ConcurrencyBound == /\ active <= Limit
                    /\ active = Cardinality({r \in Requests : phase[r] = "running"})
\* Success means the backend actually returned text; nothing is synthesized.
SuccessOnlyFromBackend == \A r \in Requests :
    response[r] = "ok" => r \in calls /\ outcome[r] = "ok"
\* Only the configured backend ever answers a request.
NoFallback == \A r \in calls : used[r] = "configured"
\* Backend results are reported as themselves. A refusal or a shutdown
\* cancellation never claims a backend result it did not receive.
TruthfulFailure == /\ \A r \in calls : (outcome[r] # "none" \/ response[r] \in Outcomes) => response[r] = outcome[r]
                   /\ \A r \in Requests \ calls : response[r] \notin Outcomes \ {"ok"}
\* No prompt or reply is persisted.
NoPersistence == stored = {}
=============================================================================
