---------------------- MODULE LocalSSOSyntheticLifecycle ----------------------
EXTENDS Naturals, FiniteSets, Sequences

CONSTANT Steps

Browsers == {"alice", "bob"}
Management == "privateManagement"
BrowserListeners == {"cognito", "entra", "sample"}
AllListeners == BrowserListeners \cup {Management}
Publication == {"sampleURL", "publicCAPath", "privateCodePath"}
CredentialValues == {"operatorValue", "privateCodeValue"}
InitOutput(local) == {}
IdPCodeAvailable(s0) == s0.issues = 0
AllowedACS == {"exact"}
ACSWitnesses == {"exact", "wrongOrigin", "wrongPin", "wrongRequest",
                 "wrongBrowser", "staleRegistration", "unusablePrincipal"}
AllowedRedeem == {"exact"}
RedeemWitnesses == {"exact", "wrongPKCE", "wrongClient", "wrongCallback",
                    "wrongBrowserState", "expired"}
AllowedApp == {"exact"}
AppWitnesses == {"exact", "wrongNonce", "wrongIssuerAudience", "invalidJWS"}

VARIABLE s

Init ==
    s = [step |-> 0, mode |-> "none", phase |-> "idle",
         listeners |-> {}, scratch |-> FALSE, privateCodeFile |-> FALSE,
         announcement |-> {}, setup |-> "none", started |-> {}, idpPending |-> {},
         cogPending |-> {}, issuedTo |-> {}, issues |-> 0, issuance |-> <<>>,
         promptLogs |-> <<>>,
         replay |-> {}, grantLogs |-> <<>>, codes |-> {}, tokens |-> {},
         exchangeLogs |-> <<>>, appLogs |-> <<>>]

Choose(local) ==
    /\ s.phase = "idle"
    /\ s' = [s EXCEPT !.step = @ + 1,
                      !.mode = IF local THEN "local" ELSE "default",
                      !.phase = IF local THEN "bootstrap" ELSE "default",
                      !.listeners = IF local THEN {Management} ELSE {},
                      !.scratch = local,
                      !.announcement = InitOutput(local)]

Publish(proof) ==
    /\ s.phase = "bootstrap"
    /\ proof = "verifiedBootstrap"
    /\ s' = [s EXCEPT !.step = @ + 1,
                      !.setup = proof, !.phase = "ready",
                      !.listeners = BrowserListeners,
                      !.privateCodeFile = TRUE,
                      !.announcement = Publication]

Authorize(b) ==
    /\ s.phase = "ready"
    /\ b \notin s.started
    /\ s' = [s EXCEPT !.step = @ + 1, !.started = @ \cup {b},
                      !.cogPending = @ \cup {b}]

IdPPrompt(b, request) ==
    /\ s.phase = "ready"
    /\ b \in s.cogPending /\ b \notin s.idpPending
    /\ request = "pinnedRequest" /\ IdPCodeAvailable(s)
    /\ s' = [s EXCEPT !.step = @ + 1, !.idpPending = @ \cup {b},
                      !.promptLogs = Append(@, request)]

IdPSubmit(bound, actor, proof) ==
    /\ s.phase = "ready"
    /\ bound \in s.idpPending /\ IdPCodeAvailable(s)
    /\ actor = bound /\ proof = "oneShotCode"
    /\ s' = [s EXCEPT !.step = @ + 1, !.issues = @ + 1,
                      !.issuedTo = @ \cup {bound}, !.idpPending = {},
                      !.issuance = Append(@, [flow |-> bound, who |-> actor, code |-> proof])]

AcceptACS(b, witness) ==
    /\ s.phase = "ready"
    /\ b \in s.cogPending \cap s.issuedTo /\ b \notin s.replay
    /\ witness \in AllowedACS
    /\ s' = [s EXCEPT !.step = @ + 1,
                      !.codes = @ \cup {b},
                      !.grantLogs = Append(@, [flow |-> b, shape |-> witness]),
                      !.cogPending = @ \ {b},
                      !.replay = @ \cup {b}]

Exchange(b, witness) ==
    /\ s.phase = "ready"
    /\ b \in s.codes /\ witness \in AllowedRedeem
    /\ s' = [s EXCEPT !.step = @ + 1, !.codes = @ \ {b},
                      !.tokens = @ \cup {b},
                      !.exchangeLogs = Append(@, [flow |-> b, shape |-> witness])]

ShowVerified(b, witness) ==
    /\ s.phase = "ready"
    /\ b \in s.tokens /\ witness \in AllowedApp
    /\ s' = [s EXCEPT !.step = @ + 1, !.tokens = @ \ {b},
                      !.appLogs = Append(@, [flow |-> b, shape |-> witness])]

Retire ==
    /\ s.phase \in {"bootstrap", "ready"}
    /\ s' = [s EXCEPT !.step = @ + 1, !.phase = "retired",
                      !.listeners = {}, !.scratch = FALSE, !.privateCodeFile = FALSE,
                      !.announcement = {}, !.idpPending = {}, !.cogPending = {},
                      !.issuedTo = {}, !.replay = {}, !.codes = {}, !.tokens = {}]

Next ==
    /\ s.step < Steps
    /\ \/ \E local \in BOOLEAN : Choose(local)
       \/ \E proof \in {"verifiedBootstrap", "wrongBootstrap"} : Publish(proof)
       \/ \E b \in Browsers : Authorize(b)
       \/ \E b \in Browsers, req \in {"pinnedRequest", "unexpectedRequest"} : IdPPrompt(b, req)
       \/ \E b, actor \in Browsers, proof \in {"oneShotCode", "unexpectedCode"} : IdPSubmit(b, actor, proof)
       \/ \E b \in Browsers, witness \in ACSWitnesses : AcceptACS(b, witness)
       \/ \E b \in Browsers, witness \in RedeemWitnesses : Exchange(b, witness)
       \/ \E b \in Browsers, witness \in AppWitnesses : ShowVerified(b, witness)
       \/ Retire

TypeOK ==
    /\ s.step \in 0..Steps /\ s.issues \in 0..Steps
    /\ s.mode \in {"none", "default", "local"}
    /\ s.phase \in {"idle", "default", "bootstrap", "ready", "retired"}
    /\ s.listeners \subseteq AllListeners
    /\ s.scratch \in BOOLEAN /\ s.privateCodeFile \in BOOLEAN
    /\ s.announcement \subseteq Publication \cup CredentialValues
    /\ s.setup \in {"none", "verifiedBootstrap", "wrongBootstrap"}
    /\ \A fields \in {s.started, s.idpPending, s.cogPending, s.issuedTo,
                       s.replay, s.codes, s.tokens} : fields \subseteq Browsers
    /\ s.promptLogs \in Seq({"pinnedRequest", "unexpectedRequest"})
    /\ s.issuance \in Seq([flow : Browsers, who : Browsers,
                           code : {"oneShotCode", "unexpectedCode"}])
    /\ s.grantLogs \in Seq([flow : Browsers, shape : ACSWitnesses])
    /\ s.exchangeLogs \in Seq([flow : Browsers, shape : RedeemWitnesses])
    /\ s.appLogs \in Seq([flow : Browsers, shape : AppWitnesses])

ExplicitOnly ==
    s.mode # "local" =>
        (s.listeners = {} /\ ~s.scratch /\ ~s.privateCodeFile /\ s.announcement = {})

ReadyAfterPrivateBootstrap ==
    /\ (s.announcement # {} /\ s.mode = "local") =>
        (s.phase = "ready" /\ s.setup = "verifiedBootstrap" /\
         s.listeners = BrowserListeners /\ s.scratch /\ s.privateCodeFile)
    /\ s.phase = "ready" => Publication \subseteq s.announcement

NoPublicCredentialValues == s.announcement \cap CredentialValues = {}

PinnedPromptOnly ==
    \A i \in 1..Len(s.promptLogs) : s.promptLogs[i] = "pinnedRequest"

OneShotPerFixture == s.issues <= 1 /\ Len(s.issuance) = s.issues

IssuanceBoundToBrowserAndCode ==
    \A i \in 1..Len(s.issuance) :
        s.issuance[i].flow = s.issuance[i].who /\ s.issuance[i].code = "oneShotCode"

ExactACSWitness ==
    \A i \in 1..Len(s.grantLogs) : s.grantLogs[i].shape = "exact"

ACSConsumesPendingAndMarksReplay ==
    s.phase = "ready" =>
        \A i \in 1..Len(s.grantLogs) :
            LET b == s.grantLogs[i].flow
            IN b \notin s.cogPending /\ b \in s.replay

BoundSingleExchange ==
    /\ \A i \in 1..Len(s.exchangeLogs) :
           (s.exchangeLogs[i].shape = "exact" /\
            \E j \in 1..Len(s.grantLogs) : s.exchangeLogs[i].flow = s.grantLogs[j].flow)
    /\ \A i,j \in 1..Len(s.exchangeLogs) : i # j => s.exchangeLogs[i].flow # s.exchangeLogs[j].flow

AppRequiresVerifiedCallback ==
    \A i \in 1..Len(s.appLogs) :
        (s.appLogs[i].shape = "exact" /\
         \E j \in 1..Len(s.exchangeLogs) : s.appLogs[i].flow = s.exchangeLogs[j].flow)

RetiredHasNoLiveState ==
    s.phase = "retired" =>
        (s.listeners = {} /\ ~s.scratch /\ ~s.privateCodeFile /\ s.announcement = {} /\
         s.idpPending = {} /\ s.cogPending = {} /\ s.issuedTo = {} /\ s.replay = {} /\
         s.codes = {} /\ s.tokens = {})
=============================================================================
