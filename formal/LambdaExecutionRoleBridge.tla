--------------------- MODULE LambdaExecutionRoleBridge ---------------------
\* Decision source for SDK calls a Lambda guest makes through the trusted
\* execution-role bridge (internal/aws/lambda_bridge.go). The invocation may
\* arrive under a caller's signed SDK session, console or terminal boundary;
\* the bridge request keeps only the invocation's cancellation and deadline.
EXTENDS FiniteSets
CONSTANT Resources
VARIABLE result
vars == <<result>>
Init == result = {}
\* roleGrant: what the saved execution role allows.
\* callerGrant: what the caller's request context would allow (session policy
\* snapshot and any console/terminal boundary), which must not be consulted.
Call(res, roleGrant, callerGrant, canceled) ==
 /\ ~canceled
 /\ result' = {[res |-> res, role |-> roleGrant, caller |-> callerGrant,
                canceled |-> canceled, allowed |-> res \in roleGrant]}
Next == (\E r \in Resources, g, c \in SUBSET Resources, k \in BOOLEAN:
          Call(r, g, c, k)) \/ UNCHANGED vars
TypeOK == result \subseteq [res: Resources, role: SUBSET Resources,
                            caller: SUBSET Resources, canceled: BOOLEAN,
                            allowed: BOOLEAN]
NoCallerEscalation == \A x \in result: x.allowed => x.res \in x.role
NoCallerNarrowing == \A x \in result: x.res \in x.role => x.allowed
CanceledNotDispatched == \A x \in result: ~x.canceled
=============================================================================
