------------------------- MODULE STSSessionLifecycle -------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Tokens, MaxTime, MaxGeneration, SessionTTL, ChainLimit,
          SkipGeneration, SkipExpiry, SkipSignature, SkipTrust, SkipExternal,
          SkipPermission, SkipTenant, SkipChain
VARIABLES now, present, generation, issued, binding, deadlines, admitted,
          lastAccepted, lastExpected
vars == <<now,present,generation,issued,binding,deadlines,admitted,
          lastAccepted,lastExpected>>

Init == /\ now = 0 /\ present = TRUE /\ generation = 1
        /\ issued = {} /\ binding = [t \in Tokens |-> 0]
        /\ deadlines = [t \in Tokens |-> 0]
        /\ admitted = [t \in Tokens |-> FALSE]
        /\ lastAccepted = FALSE /\ lastExpected = FALSE

\* Booleans represent independent checks, never instructions from request data.
\* bound is the result of enforcing role/session-duration limits.
Issue(t,trust,external,permission,chained,duration) ==
 /\ present /\ t \notin issued /\ now + duration <= MaxTime
 /\ (trust \/ SkipTrust) /\ (external \/ SkipExternal)
 /\ (permission \/ SkipPermission)
 /\ (~chained \/ duration <= ChainLimit \/ SkipChain)
 /\ issued' = issued \cup {t}
 /\ binding' = [binding EXCEPT ![t] = generation]
 /\ deadlines' = [deadlines EXCEPT ![t] = now + duration]
 /\ admitted' = [admitted EXCEPT ![t] =
       trust /\ external /\ permission /\ (~chained \/ duration <= ChainLimit)]
 /\ UNCHANGED <<now,present,generation,lastAccepted,lastExpected>>

\* Storage=true means role generation and policy authorization were read at
\* one coherent use-time boundary. The implementation must establish this;
\* separate callback and policy reads do not implement this atomic transition.
Use(t,signature,tenant,selectors,storage,permission) ==
 /\ lastAccepted' = (t \in issued /\ present /\ storage /\ selectors
       /\ permission /\ (signature \/ SkipSignature) /\ (tenant \/ SkipTenant)
       /\ (binding[t] = generation \/ SkipGeneration)
       /\ (now < deadlines[t] \/ SkipExpiry))
 /\ lastExpected' = (t \in issued /\ present /\ storage /\ selectors
       /\ permission /\ signature /\ tenant /\ binding[t] = generation
       /\ now < deadlines[t])
 /\ UNCHANGED <<now,present,generation,issued,binding,deadlines,admitted>>
Delete == /\ present /\ present' = FALSE
          /\ UNCHANGED <<now,generation,issued,binding,deadlines,admitted,lastAccepted,lastExpected>>
Recreate == /\ ~present /\ generation < MaxGeneration
            /\ present' = TRUE /\ generation' = generation + 1
            /\ UNCHANGED <<now,issued,binding,deadlines,admitted,lastAccepted,lastExpected>>
Advance == /\ now < MaxTime /\ now' = now + 1
           /\ UNCHANGED <<present,generation,issued,binding,deadlines,admitted,lastAccepted,lastExpected>>
Restart == /\ issued' = {}
           /\ binding' = [t \in Tokens |-> 0]
           /\ deadlines' = [t \in Tokens |-> 0]
           /\ admitted' = [t \in Tokens |-> FALSE]
           /\ UNCHANGED <<now,present,generation,lastAccepted,lastExpected>>
Next == (\E t \in Tokens,trust,external,permission,chained \in BOOLEAN,
             duration \in 1..SessionTTL:
             Issue(t,trust,external,permission,chained,duration))
        \/ (\E t \in Tokens,signature,tenant,selectors,storage,permission \in BOOLEAN:
             Use(t,signature,tenant,selectors,storage,permission))
        \/ Delete \/ Recreate \/ Advance \/ Restart \/ UNCHANGED vars
TypeOK == /\ now \in 0..MaxTime /\ present \in BOOLEAN
          /\ generation \in 1..MaxGeneration /\ issued \subseteq Tokens
          /\ binding \in [Tokens -> 0..MaxGeneration]
          /\ deadlines \in [Tokens -> 0..MaxTime]
          /\ admitted \in [Tokens -> BOOLEAN]
          /\ lastAccepted \in BOOLEAN /\ lastExpected \in BOOLEAN
IssueRequiresAllGuards == \A t \in issued: admitted[t]
UseRequiresAllGuards == lastAccepted => lastExpected
=============================================================================
