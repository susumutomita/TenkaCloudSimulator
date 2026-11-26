--------------------- MODULE EntraVerificationFixture ---------------------
VARIABLES input, phase, active, startup, reply
vars == <<input, phase, active, startup, reply>>

Modes == {"gateway", "fixture"}
Binds == {"loopback", "remote"}
Intents == {"none", "valid", "invalid"}
Paths == {"metadata", "keys", "other"}
Methods == {"GET", "other"}
Inputs == [mode: Modes, bind: Binds, intent: Intents, path: Paths,
           method: Methods, loopbackPeer: BOOLEAN, exactHost: BOOLEAN, clean: BOOLEAN]
CanStart(i) == /\ i.mode = "fixture" /\ i.bind = "loopback" /\ i.intent # "invalid"
CanRead(i) == /\ i.path \in {"keys", "metadata"} /\ i.method = "GET"
              /\ i.loopbackPeer /\ i.exactHost /\ i.clean
Startup(i) == IF i.intent = "valid" THEN "synthetic" ELSE "none"
Projection(i) == IF i.path = "keys" THEN "publicJWK" ELSE "metadata"

Init == /\ input \in Inputs /\ phase = "configured" /\ active = FALSE
        /\ startup = "none" /\ reply = "none"
Start == /\ phase = "configured" /\ phase' = "started" /\ active' = CanStart(input)
         /\ startup' = IF active' THEN Startup(input) ELSE "none"
         /\ UNCHANGED <<input, reply>>
Serve == /\ phase = "started" /\ phase' = "observed"
         /\ reply' = IF active /\ CanRead(input) THEN Projection(input) ELSE "none"
         /\ UNCHANGED <<input, active, startup>>
Next == Start \/ Serve \/ (phase = "observed" /\ UNCHANGED vars)
Spec == Init /\ [][Next]_vars

FixtureIsolated == active => input.mode = "fixture"
NumericLoopback == active => input.bind = "loopback"
ExplicitValidToken == startup # "none" => input.mode = "fixture" /\ input.intent = "valid"
SyntheticTokenOnly == startup \in {"none", "synthetic"}
ReadOnlyAdmission ==
  reply # "none" => active /\ input.path \in {"keys", "metadata"}
                    /\ input.method = "GET" /\ input.loopbackPeer /\ input.exactHost /\ input.clean
PublicVerificationOnly == reply \in {"none", "metadata", "publicJWK"}
=============================================================================
