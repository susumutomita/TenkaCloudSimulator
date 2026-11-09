----------------------- MODULE LambdaLogTailDisclosure -----------------------
EXTENDS Naturals
CONSTANT Contexts
VARIABLES optIn, kind, allowed, capturedSize, requestContext,
          epoch, current, alive, phase, exposed, tailSize, visible
vars == <<optIn,kind,allowed,capturedSize,requestContext,
          epoch,current,alive,phase,exposed,tailSize,visible>>

\* Size 1 denotes the 4096-byte boundary; 2 denotes overflow (4097 bytes).
\* A single request snapshots epoch 0 and its function/identity/region context.
Init == /\ optIn \in BOOLEAN /\ kind \in {"Sync","DryRun","Event"}
        /\ allowed \in BOOLEAN /\ capturedSize \in 0..2
        /\ current \in Contexts /\ requestContext = current
        /\ epoch = 0 /\ alive = TRUE /\ phase = "Waiting"
        /\ exposed = FALSE /\ tailSize = 0 /\ visible = FALSE

Respond == /\ phase = "Waiting"
           /\ phase' = "Ready"
           /\ exposed' = (optIn /\ kind = "Sync" /\ allowed) (* ResponseAdmission *)
           /\ tailSize' = IF exposed' THEN
                  (IF capturedSize > 1 THEN 1 ELSE capturedSize) (* TailCap *)
                  ELSE 0
           /\ UNCHANGED <<optIn,kind,allowed,capturedSize,requestContext,
                           epoch,current,alive,visible>>
Display == /\ phase = "Ready" /\ exposed
           /\ alive (* DisplayAlive *)
           /\ epoch = 0 (* DisplayEpoch *)
           /\ visible' = TRUE
           /\ UNCHANGED <<optIn,kind,allowed,capturedSize,requestContext,
                           epoch,current,alive,phase,exposed,tailSize>>
ChangeEpoch == /\ epoch = 0 /\ epoch' = 1
               /\ visible' = FALSE (* ClearEpoch *)
               /\ UNCHANGED <<optIn,kind,allowed,capturedSize,requestContext,
                               current,alive,phase,exposed,tailSize>>
ChangeContext(c) == /\ c \in Contexts /\ c # current /\ epoch = 0
                    /\ epoch' = 1 (* ContextInvalidation *)
                    /\ current' = c
                    /\ visible' = FALSE (* ClearContext *)
                    /\ UNCHANGED <<optIn,kind,allowed,capturedSize,requestContext,
                                    alive,phase,exposed,tailSize>>
Unmount == /\ alive /\ alive' = FALSE
           /\ visible' = FALSE (* ClearUnmount *)
           /\ UNCHANGED <<optIn,kind,allowed,capturedSize,requestContext,
                           epoch,current,phase,exposed,tailSize>>
Next == Respond \/ Display \/ ChangeEpoch
        \/ (\E c \in Contexts : ChangeContext(c)) \/ Unmount \/ UNCHANGED vars

TypeOK == /\ optIn \in BOOLEAN /\ kind \in {"Sync","DryRun","Event"}
          /\ allowed \in BOOLEAN /\ capturedSize \in 0..2
          /\ current \in Contexts /\ requestContext \in Contexts
          /\ epoch \in 0..1 /\ alive \in BOOLEAN
          /\ phase \in {"Waiting","Ready"}
          /\ exposed \in BOOLEAN /\ tailSize \in 0..2 /\ visible \in BOOLEAN
ResponseOptIn == exposed => optIn
ResponseSync == exposed => kind = "Sync"
ResponseAuthorized == exposed => allowed
TailBound == tailSize <= 1
DisplayCurrent == visible => (epoch = 0 /\ current = requestContext /\ alive)
DisplayHasResponse == visible => (phase = "Ready" /\ exposed)
=============================================================================
