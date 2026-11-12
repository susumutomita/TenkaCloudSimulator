---------------- MODULE OwnedProbeAttachmentLifecycle ----------------
EXTENDS Naturals
VARIABLE s
Init == s=[phase |-> "idle", attempts |-> 0, reserved |-> FALSE,
 constructions |-> 0, terminal |-> FALSE, callerExpired |-> FALSE,
 ownerClosing |-> FALSE, ownerResult |-> "pending", transportResult |-> "pending",
 buildSuccess |-> FALSE, retainedTransport |-> FALSE, transferred |-> FALSE,
 transportClosing |-> FALSE, bound |-> FALSE, published |-> FALSE,
 returned |-> FALSE, handle |-> FALSE, returnSuccess |-> FALSE,
 ownerSlot |-> TRUE, transportSlot |-> FALSE, composedSuccess |-> FALSE,
 badAdmission |-> FALSE, badPublish |-> FALSE, descriptor |-> 1,
 requestLifetimeUsed |-> FALSE]
Admission == ~s.terminal /\ ~s.callerExpired /\ ~s.reserved
Attempt == /\ s.attempts<2
 /\ LET admit == Admission
    IN s'=[s EXCEPT !.attempts=@+1,
 !.phase=IF admit THEN "reserved" ELSE @,
 !.reserved=@ \/ admit, !.badAdmission=@ \/ (admit /\ (s.terminal \/ s.callerExpired \/ s.reserved))]
Construct == /\ s.phase="reserved"
 /\ s'=[s EXCEPT !.phase="constructing", !.constructions=@+1]
ExpireCaller == /\ ~s.callerExpired /\ s'=[s EXCEPT !.callerExpired=TRUE]
Revoke == /\ ~s.terminal
 /\ s'=[s EXCEPT !.terminal=TRUE, !.ownerClosing=TRUE]
BuildReturn(ok) == /\ s.phase="constructing"
 /\ s'=[s EXCEPT !.phase=IF ok THEN "build-success" ELSE "failed",
 !.buildSuccess=ok, !.transportSlot=ok]
RetainTransport == /\ s.phase="build-success"
 /\ s'=[s EXCEPT !.phase="retained", !.retainedTransport=TRUE, !.transferred=TRUE]
Bind(ok) == /\ s.phase="retained"
 /\ s'=[s EXCEPT !.phase=IF ok THEN "bound" ELSE "failed", !.bound=ok]
Publish == /\ s.phase="bound"
 /\ LET allowed == ~s.terminal /\ ~s.callerExpired
    IN s'=[s EXCEPT !.phase=IF allowed THEN "published" ELSE "failed",
 !.published=allowed, !.badPublish=@ \/ (allowed /\ (s.terminal \/ s.callerExpired))]
ReturnSuccess == /\ s.phase="published"
 /\ s'=[s EXCEPT !.phase="done", !.returned=TRUE, !.handle=TRUE, !.returnSuccess=TRUE]
FailureSeal == /\ s.phase="failed"
 /\ s'=[s EXCEPT !.phase="failure-sealed", !.terminal=TRUE, !.ownerClosing=TRUE]
FailureTransportClose == /\ s.phase="failure-sealed"
 /\ s'=[s EXCEPT !.phase="failure-closing", !.transportClosing=@ \/ s.retainedTransport]
ReturnFailure == /\ s.phase="failure-closing"
 /\ s'=[s EXCEPT !.phase="done", !.returned=TRUE, !.handle=TRUE]
\* Context shutdown notification can follow seal; no fairness/liveness assumed.
NotifyTransport == /\ s.terminal /\ s.buildSuccess /\ ~s.transportClosing
 /\ s'=[s EXCEPT !.transportClosing=TRUE]
CloseHandle == /\ s.returned /\ ~s.ownerClosing
 /\ s'=[s EXCEPT !.terminal=TRUE, !.ownerClosing=TRUE]
CloseHandleTransport == /\ s.returned /\ s.ownerClosing /\ s.retainedTransport /\ ~s.transportClosing
 /\ s'=[s EXCEPT !.transportClosing=TRUE]
OwnerDone(ok) == /\ s.ownerClosing /\ s.ownerResult="pending"
 /\ s'=[s EXCEPT !.ownerResult=IF ok THEN "ok" ELSE "error", !.ownerSlot=~ok]
TransportDone(ok) == /\ s.transportClosing /\ s.transportResult="pending"
 /\ s'=[s EXCEPT !.transportResult=IF ok THEN "ok" ELSE "error", !.transportSlot=~ok]
WaitSuccess == /\ s.returned /\ s.ownerResult="ok"
 /\ (~s.retainedTransport \/ s.transportResult="ok") /\ ~s.composedSuccess
 /\ s'=[s EXCEPT !.composedSuccess=TRUE]
Next == Attempt \/ Construct \/ ExpireCaller \/ Revoke
 \/ (\E ok \in BOOLEAN: BuildReturn(ok)) \/ RetainTransport
 \/ (\E ok \in BOOLEAN: Bind(ok)) \/ Publish \/ ReturnSuccess
 \/ FailureSeal \/ FailureTransportClose \/ ReturnFailure \/ NotifyTransport
 \/ CloseHandle \/ CloseHandleTransport \/ (\E ok \in BOOLEAN: OwnerDone(ok))
 \/ (\E ok \in BOOLEAN: TransportDone(ok)) \/ WaitSuccess
TypeOK == /\ s.attempts \in 0..2 /\ s.constructions \in 0..2
 /\ s.ownerResult \in {"pending","ok","error"}
 /\ s.transportResult \in {"pending","ok","error"}
NoInvalidAdmission == ~s.badAdmission
SingleAttach == s.constructions<=1
TransferredOnlySuccess == s.transferred => s.buildSuccess
ReturnedTransferExact == s.returned => (s.transferred=s.buildSuccess) /\ (s.retainedTransport=s.buildSuccess)
PublishedOnlyBound == s.published => s.bound /\ s.retainedTransport
NoLatePublish == ~s.badPublish
RetainedFailureHandle == s.returned => s.handle /\ s.reserved
IndependentJoins == s.composedSuccess => s.ownerResult="ok" /\ (~s.buildSuccess \/ s.transportResult="ok")
OwnerDebt == ~s.ownerSlot => s.ownerResult="ok"
TransportDebt == s.buildSuccess /\ ~s.transportSlot => s.transportResult="ok"
ImmutableDescriptor == s.descriptor=1
AdmissionOnlyContext == ~s.requestLifetimeUsed
Spec == Init /\ [][Next]_s
=============================================================================
