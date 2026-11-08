---------------------------- MODULE SQSConsoleEpoch ----------------------------
EXTENDS Naturals, FiniteSets
CONSTANT MaxEpoch
Contexts == {"queue-a", "queue-b"}
Requests == [context : Contexts, epoch : 0..MaxEpoch]
Captures == [request : Requests, mounted : BOOLEAN, context : Contexts, epoch : 0..MaxEpoch]
VARIABLES context, epoch, mounted, pending, shown, handles, rendered, accepted
vars == <<context, epoch, mounted, pending, shown, handles, rendered, accepted>>

\* Guards correspond to view acceptance/custody, not native receipt validity.
ReplyEpochOK(r) == r.epoch = epoch
MountedOK == mounted
AdvanceUnmountEpoch == TRUE
ClearSwitchHandles == TRUE
CopyRawReceiptIntoRows == FALSE

Init == /\ context = "queue-a" /\ epoch = 0 /\ mounted = TRUE
        /\ pending = {} /\ shown = {} /\ handles = {} /\ rendered = {} /\ accepted = {}
Issue == /\ mounted
         /\ pending' = pending \cup {[context |-> context, epoch |-> epoch]}
         /\ UNCHANGED <<context, epoch, mounted, shown, handles, rendered, accepted>>
Deliver == \E r \in pending:
             /\ MountedOK /\ ReplyEpochOK(r)
             /\ pending' = pending \ {r}
             /\ shown' = shown \cup {r}
             /\ handles' = handles \cup {r}
             /\ rendered' = IF CopyRawReceiptIntoRows THEN rendered \cup {r} ELSE rendered
             /\ accepted' = accepted \cup {[request |-> r, mounted |-> mounted, context |-> context, epoch |-> epoch]}
             /\ UNCHANGED <<context, epoch, mounted>>
Switch == /\ mounted /\ epoch < MaxEpoch
          /\ context' \in Contexts \ {context} /\ epoch' = epoch + 1
          /\ shown' = {} /\ rendered' = {}
          /\ handles' = IF ClearSwitchHandles THEN {} ELSE handles
          /\ UNCHANGED <<mounted, pending, accepted>>
Cancel == /\ mounted /\ epoch < MaxEpoch /\ epoch' = epoch + 1
          /\ shown' = {} /\ handles' = {} /\ rendered' = {}
          /\ UNCHANGED <<context, mounted, pending, accepted>>
Release == /\ mounted /\ shown # {}
           /\ shown' = {} /\ handles' = {} /\ rendered' = {}
           /\ UNCHANGED <<context, epoch, mounted, pending, accepted>>
Unmount == /\ mounted /\ mounted' = FALSE
           /\ epoch' = IF AdvanceUnmountEpoch /\ epoch < MaxEpoch THEN epoch + 1 ELSE epoch
           /\ shown' = {} /\ handles' = {} /\ rendered' = {}
           /\ UNCHANGED <<context, pending, accepted>>
Next == Issue \/ Deliver \/ Switch \/ Cancel \/ Release \/ Unmount \/ UNCHANGED vars

TypeOK == /\ context \in Contexts /\ epoch \in 0..MaxEpoch /\ mounted \in BOOLEAN
          /\ pending \subseteq Requests /\ shown \subseteq Requests
          /\ handles \subseteq Requests /\ rendered \subseteq Requests /\ accepted \subseteq Captures
OnlyCurrentReplies == /\ \A r \in shown: r.context = context /\ r.epoch = epoch
                      /\ \A a \in accepted: a.request.context = a.context /\ a.request.epoch = a.epoch
NoUnmountedAcceptance == \A a \in accepted: a.mounted
ReceiptCustodyBound == handles \subseteq shown
RawReceiptsPrivate == rendered = {}
=============================================================================
