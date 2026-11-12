---------------- MODULE TerminalClientEpoch ----------------
EXTENDS Naturals, Sequences
VARIABLES d,last
vars == <<d,last>>
Blank == [component |-> 1, mounted |-> TRUE, disposed |-> FALSE,
 epoch |-> 0, operations |-> 0, started |-> FALSE,
 pending |-> "none", opComponent |-> 0, opEpoch |-> 0,
 remote |-> "none", known |-> FALSE, phase |-> "idle",
 channel |-> FALSE, revision |-> 0, socketRevision |-> 0,
 socketComponent |-> 0, opens |-> 0, uiPending |-> FALSE, uiComponent |-> 0]
Init == /\ d=Blank /\ last=[before |-> Blank,event |-> "init",current |-> TRUE]
Record(e,c) == last'=[before |-> d,event |-> e,current |-> c]
StartAllowed == ~d.disposed /\ d.mounted /\ ~d.started
ReceiptCurrent == ~d.disposed /\ d.mounted /\ d.opComponent=d.component /\ d.opEpoch=d.epoch
SocketCurrent(c) == ~d.disposed /\ d.mounted /\ d.channel /\ d.socketRevision=d.revision /\ d.socketComponent=d.component /\ c
UICurrent == d.mounted /\ d.uiComponent=d.component
Begin(kind) ==
 /\ d.operations<2 /\ d.pending="none" /\ ~d.disposed /\ d.mounted
 /\ IF kind="start" THEN StartAllowed ELSE d.known /\ (kind="terminate" \/ ~d.channel)
 /\ d'=[d EXCEPT !.operations=@+1,!.epoch=@+1,!.pending=kind,
   !.opComponent=d.component,!.opEpoch=d.epoch+1,
   !.started=IF kind="start" THEN TRUE ELSE @,
   !.remote=IF kind="start" THEN "unknown" ELSE @,
   !.channel=IF kind="terminate" THEN FALSE ELSE @,
   !.revision=IF kind="terminate" THEN @+1 ELSE @]
 /\ Record("begin-"\o kind,TRUE)
RemoteResult(success) ==
 /\ d.pending \in {"start","resume","terminate"}
 /\ d'=[d EXCEPT !.remote=IF success THEN (IF d.pending="terminate" THEN "terminated" ELSE "running") ELSE @,
   !.pending=IF success THEN "receipt" ELSE "failure"]
 /\ Record("remote-result",success)
Accept ==
 /\ d.pending="receipt" /\ ReceiptCurrent
 /\ d'=[d EXCEPT !.pending="none",!.known=TRUE,
   !.channel=d.remote#"terminated",!.phase=IF d.remote="terminated" THEN "terminated" ELSE "connecting",
   !.revision=@+1,!.socketRevision=d.revision+1,!.socketComponent=d.component,!.opens=0]
 /\ Record("accept",TRUE)
Reject ==
 /\ d.pending \in {"receipt","failure"}
 /\ (d.pending="failure" \/ ~ReceiptCurrent)
 /\ d'=[d EXCEPT !.pending="none",!.uiPending=TRUE,!.uiComponent=d.opComponent,
   !.phase=IF ReceiptCurrent THEN "unknown" ELSE @]
 /\ Record("reject",TRUE)
Dispose ==
 /\ ~d.disposed
 /\ d'=[d EXCEPT !.disposed=TRUE,!.epoch=@+1,!.revision=@+1,!.channel=FALSE,!.phase="disconnected"]
 /\ Record("dispose",TRUE)
Unmount == /\ d.mounted
 /\ d'=[d EXCEPT !.mounted=FALSE,!.disposed=TRUE,!.epoch=@+1,!.revision=@+1,!.channel=FALSE,!.phase="disconnected"]
 /\ Record("unmount",TRUE)
Remount == /\ ~d.mounted /\ d.component=1
 /\ d'=[d EXCEPT !.component=2,!.mounted=TRUE,!.disposed=FALSE,
   !.epoch=@+1,!.started=FALSE,!.known=FALSE,!.phase="idle"]
 /\ Record("remount",TRUE)
Close == /\ d.channel
 /\ d'=[d EXCEPT !.channel=FALSE,!.revision=@+1,!.phase="disconnected"]
 /\ Record("close",TRUE)
Open(c) == /\ SocketCurrent(c) /\ d.phase="connecting" /\ d.opens=0
 /\ d'=[d EXCEPT !.opens=@+1] /\ Record("open",c)
Output(c) == /\ SocketCurrent(c) /\ d.opens=1
 /\ d'=[d EXCEPT !.phase="open"] /\ Record("output",c)
UI == /\ d.uiPending /\ UICurrent
 /\ d'=[d EXCEPT !.uiPending=FALSE] /\ Record("ui",TRUE)
DropUI == /\ d.uiPending /\ ~UICurrent
 /\ d'=[d EXCEPT !.uiPending=FALSE] /\ Record("drop-ui",TRUE)
Next == (\E kind \in {"start","resume","terminate"}: Begin(kind))
 \/ (\E success \in BOOLEAN: RemoteResult(success)) \/ Accept \/ Reject
 \/ Dispose \/ Unmount \/ Remount \/ Close
 \/ (\E c \in BOOLEAN: Open(c) \/ Output(c)) \/ UI \/ DropUI
TypeOK == /\ d.component \in 1..2 /\ d.operations \in 0..2 /\ d.epoch \in Nat
 /\ d.remote \in {"none","unknown","running","terminated"}
 /\ d.pending \in {"none","start","resume","terminate","receipt","failure"}
ReceiptEvidence == last.event="accept" => ~last.before.disposed /\ last.before.mounted /\ last.before.opComponent=last.before.component /\ last.before.opEpoch=last.before.epoch
SocketEvidence == last.event \in {"open","output"} => ~last.before.disposed /\ last.before.mounted /\ last.before.channel /\ last.before.socketRevision=last.before.revision /\ last.before.socketComponent=last.before.component /\ last.current
SingleOpen == d.opens<=1
UIEvidence == last.event="ui" => last.before.mounted /\ last.before.uiComponent=last.before.component
DisposeNotRemoteTermination == last.event \in {"dispose","unmount"} => d.remote=last.before.remote
NoAutomaticStartRetry == last.event="begin-start" => ~last.before.started
DiscardPreservesIdentity == last.event="reject" => d.known=last.before.known
=====================================================================
