--------------------------- MODULE BrowserSessions ---------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Clients, BrowserTokens, OperatorToken, ParticipantToken, NoToken,
          BrowserTTL, ParentTTL, MaxTime
VARIABLES now, parentPresent, active, minted, cookies, binding, deadlines,
          revoked, sdkAuthority, lastAccepted, lastExpected, isolationOK
vars == <<now, parentPresent, active, minted, cookies, binding, deadlines,
          revoked, sdkAuthority, lastAccepted, lastExpected, isolationOK>>
Credentials == {OperatorToken, ParticipantToken}
ASSUME /\ BrowserTokens \cap Credentials = {}
       /\ NoToken \notin BrowserTokens \cup Credentials
       /\ ParentTTL < BrowserTTL /\ BrowserTTL <= MaxTime
ParentValid(p) == p = OperatorToken \/
                 (p = ParticipantToken /\ parentPresent /\ now < ParentTTL)
ExpiryFor(p) == IF p = ParticipantToken /\ ParentTTL < now + BrowserTTL
               THEN ParentTTL ELSE now + BrowserTTL
RemoveBrowser(t) == active \ {t}
RawCookieAllowed(t) == FALSE
SDKAfterLogout == sdkAuthority
AcceptCookie(t) == IF t \in BrowserTokens
                   THEN t \in active /\ now < deadlines[t] /\ ParentValid(binding[t])
                   ELSE RawCookieAllowed(t)
AcceptHeader(t) == t \in sdkAuthority /\ ParentValid(t)
\* Independent use-time oracle. Historical results stay historical as time moves.
ExpectedCookie(t) == IF t \in BrowserTokens
                     THEN t \in active /\ t \notin revoked /\ now < deadlines[t]
                          /\ (binding[t] = OperatorToken \/
                              (binding[t] = ParticipantToken /\ parentPresent /\ now < ParentTTL))
                     ELSE FALSE
Init == /\ now = 0 /\ parentPresent = TRUE
        /\ active = {} /\ minted = {} /\ revoked = {}
        /\ cookies = [c \in Clients |-> NoToken]
        /\ binding = [t \in BrowserTokens |-> OperatorToken]
        /\ deadlines = [t \in BrowserTokens |-> 0]
        /\ sdkAuthority = Credentials
        /\ lastAccepted = FALSE /\ lastExpected = FALSE /\ isolationOK = TRUE
Login(c,p,t) == /\ ParentValid(p) /\ t \notin minted /\ now + BrowserTTL <= MaxTime
               /\ active' = RemoveBrowser(cookies[c]) \cup {t}
               /\ minted' = minted \cup {t}
               /\ revoked' = revoked \cup ({cookies[c]} \cap BrowserTokens)
               /\ cookies' = [cookies EXCEPT ![c] = t]
               /\ binding' = [binding EXCEPT ![t] = p]
               /\ deadlines' = [deadlines EXCEPT ![t] = ExpiryFor(p)]
               /\ isolationOK' = (RemoveBrowser(cookies[c]) \ {cookies[c]} = active \ {cookies[c]})
               /\ UNCHANGED <<now,parentPresent,sdkAuthority,lastAccepted,lastExpected>>
Logout(c) == /\ active' = RemoveBrowser(cookies[c])
             /\ revoked' = revoked \cup ({cookies[c]} \cap BrowserTokens)
             /\ cookies' = [cookies EXCEPT ![c] = NoToken]
             /\ isolationOK' = (RemoveBrowser(cookies[c]) \ {cookies[c]} = active \ {cookies[c]})
             /\ sdkAuthority' = SDKAfterLogout
             /\ UNCHANGED <<now,parentPresent,minted,binding,deadlines,lastAccepted,lastExpected>>
UseCookie(t) == /\ lastAccepted' = AcceptCookie(t)
                /\ lastExpected' = ExpectedCookie(t)
                /\ UNCHANGED <<now,parentPresent,active,minted,cookies,binding,deadlines,revoked,sdkAuthority,isolationOK>>
InvalidateParent == /\ parentPresent /\ parentPresent' = FALSE
                    /\ UNCHANGED <<now,active,minted,cookies,binding,deadlines,revoked,sdkAuthority,lastAccepted,lastExpected,isolationOK>>
Advance == /\ now < MaxTime /\ now' = now + 1
           /\ UNCHANGED <<parentPresent,active,minted,cookies,binding,deadlines,revoked,sdkAuthority,lastAccepted,lastExpected,isolationOK>>
Next == (\E c \in Clients,p \in Credentials,t \in BrowserTokens: Login(c,p,t))
        \/ (\E c \in Clients: Logout(c))
        \/ (\E t \in BrowserTokens \cup Credentials: UseCookie(t))
        \/ InvalidateParent \/ Advance \/ UNCHANGED vars
TypeOK == /\ now \in 0..MaxTime /\ parentPresent \in BOOLEAN
          /\ active \subseteq minted /\ minted \subseteq BrowserTokens
          /\ revoked \subseteq minted
          /\ cookies \in [Clients -> BrowserTokens \cup {NoToken}]
          /\ binding \in [BrowserTokens -> Credentials]
          /\ deadlines \in [BrowserTokens -> 0..MaxTime]
          /\ sdkAuthority \subseteq Credentials
          /\ lastAccepted \in BOOLEAN /\ lastExpected \in BOOLEAN /\ isolationOK \in BOOLEAN
RevokedCookiesDenied == \A t \in revoked: ~AcceptCookie(t)
AcceptedOnlyLiveParent == lastAccepted => lastExpected
SDKAuthorityPreserved == sdkAuthority = Credentials
UnrelatedSessionsPreserved == isolationOK
NoRawSDKCookie == \A t \in Credentials: ~AcceptCookie(t)
NoBrowserSDKAuthority == \A t \in BrowserTokens: ~AcceptHeader(t)
ParentLifetimeBound == \A t \in minted: binding[t] = ParticipantToken => deadlines[t] <= ParentTTL
=============================================================================
