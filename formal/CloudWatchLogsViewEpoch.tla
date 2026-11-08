----------------------- MODULE CloudWatchLogsViewEpoch -----------------------
EXTENDS Naturals, FiniteSets
CONSTANT Contexts
VARIABLES epoch, current, alive, pending, receipts
vars == <<epoch,current,alive,pending,receipts>>
Init == /\ epoch = 0 /\ current \in Contexts /\ alive = TRUE
        /\ pending = {} /\ receipts = {}
Begin(c) == /\ alive /\ epoch < 2
            /\ epoch' = epoch + 1 /\ current' = c
            /\ pending' = pending \cup {[version |-> epoch+1, context |-> c]}
            /\ UNCHANGED <<alive,receipts>>
Unmount == /\ alive /\ alive' = FALSE
           /\ UNCHANGED <<epoch,current,pending,receipts>>
Reply(r) == /\ alive /\ r.version = epoch
            /\ receipts' = receipts \cup {[requestEpoch |-> r.version,
                 viewEpoch |-> epoch, requestContext |-> r.context,
                 viewContext |-> current, wasAlive |-> alive]}
            /\ UNCHANGED <<epoch,current,alive,pending>>
Next == (\E c \in Contexts : Begin(c)) \/ Unmount
        \/ (\E r \in pending : Reply(r)) \/ UNCHANGED vars
TypeOK == /\ epoch \in 0..2 /\ current \in Contexts /\ alive \in BOOLEAN
          /\ pending \subseteq [version:1..2,context:Contexts]
          /\ receipts \subseteq [requestEpoch:1..2,viewEpoch:0..2,
                requestContext:Contexts,viewContext:Contexts,wasAlive:BOOLEAN]
FreshResults == \A r \in receipts : r.requestEpoch = r.viewEpoch /\ r.requestContext = r.viewContext
NoUnmountedResults == \A r \in receipts : r.wasAlive
=============================================================================
