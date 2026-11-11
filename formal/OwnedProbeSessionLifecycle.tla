---------------- MODULE OwnedProbeSessionLifecycle ----------------
EXTENDS Naturals
\* One transferred session, one transaction, at most two facade calls.
\* Physical provider authority is an assumption, not an identity predicate.
VARIABLE s
Init == s = [terminal |-> FALSE, expired |-> FALSE, gate |-> FALSE,
 phase |-> "fresh", kind |-> "frame", called |-> FALSE, callback |-> "none",
 invalid |-> FALSE, appliedOK |-> FALSE, effects |-> 0, tentative |-> FALSE,
 evidence |-> FALSE, observed |-> FALSE, success |-> FALSE,
 badAdmission |-> FALSE, badPublication |-> FALSE, badVisible |-> FALSE,
 closing |-> FALSE, closes |-> 0, closeResult |-> "pending",
 io |-> 0, ioStarted |-> 0, joined |-> FALSE, released |-> FALSE,
 lateOnce |-> FALSE, poison |-> FALSE, binding |-> 1,
 tokenUsed |-> FALSE, replayTried |-> FALSE, configured |-> FALSE, local |-> FALSE, inbound |-> TRUE,
 tokenState |-> "absent", claimed |-> FALSE, childCanceled |-> FALSE,
 rawOK |-> FALSE, sharedLive |-> TRUE, completed |-> FALSE, eligible |-> FALSE,
 producerChecked |-> FALSE, cleanProducer |-> FALSE, markerMatches |-> TRUE, producerResult |-> "none", callbackResult |-> "none",
 propagation |-> "unset", safeFailure |-> FALSE, badException |-> FALSE,
 badReplay |-> FALSE, badNeedlessSeal |-> FALSE, badFailurePublication |-> FALSE]
Configure(local, inbound, token) == /\ s.phase="fresh" /\ ~s.configured /\ (local => token # "absent")
 /\ s'=[s EXCEPT !.configured=TRUE, !.local=local, !.inbound=inbound, !.tokenState=token]
ChildCancel == /\ ~s.childCanceled /\ s'=[s EXCEPT !.childCanceled=TRUE]
TransportSeal == /\ s.sharedLive /\ s'=[s EXCEPT !.sharedLive=FALSE]
Live == ~s.terminal /\ ~s.expired
Enter(k) == /\ s.phase="fresh" /\ Live /\ ~s.gate /\ s.tokenState \notin {"used","completed"}
 /\ s'=[s EXCEPT !.phase="provider", !.gate=TRUE, !.kind=k,
 !.badAdmission=@ \/ ~Live, !.claimed=(s.tokenState="fresh"), !.tokenUsed=(s.tokenState="fresh"),
 !.badReplay=@ \/ (s.tokenState \in {"used","completed"})]
Expire == /\ ~s.expired /\ s'=[s EXCEPT !.expired=TRUE]
Revoke == /\ ~s.terminal
 /\ s'=[s EXCEPT !.terminal=TRUE, !.closing=TRUE, !.closes=@+1]
CallbackBegin == /\ s.phase="provider" /\ s.callback="none" /\ ~s.called
 /\ s'=[s EXCEPT !.called=TRUE, !.callback=IF Live THEN "running" ELSE "done",
 !.appliedOK=FALSE]
Apply(ok) == /\ s.callback="running"
 /\ s'=[s EXCEPT !.callback="effect", !.effects=@+1,
 !.tentative=(s.kind="observation"), !.appliedOK=ok, !.rawOK=ok]
ProducerPostcheck == /\ s.callback="effect" /\ s.local /\ ~s.producerChecked
 /\ s'=[s EXCEPT !.producerChecked=TRUE,
 !.completed=s.rawOK /\ s.inbound,
 !.eligible=s.rawOK /\ s.inbound /\ s.sharedLive,
 !.cleanProducer=s.rawOK /\ s.inbound /\ s.sharedLive,
 !.producerResult=IF s.rawOK /\ s.inbound /\ s.sharedLive
 THEN IF s.childCanceled THEN "marker" ELSE "nil" ELSE "error"]
CallbackEnd == /\ s.callback="effect" /\ (~s.local \/ s.producerChecked)
 /\ s'=[s EXCEPT !.callback="done", !.appliedOK=@ /\ Live /\ ~s.childCanceled /\ (~s.local \/ s.producerResult="nil"),
 !.callbackResult=IF s.appliedOK /\ Live /\ ~s.childCanceled /\ (~s.local \/ s.producerResult="nil") THEN "nil" ELSE "wrapper"]
Duplicate == /\ s.phase="provider" /\ s.callback="done" /\ ~s.invalid
 /\ s'=[s EXCEPT !.invalid=TRUE, !.poison=TRUE, !.callback="misuse"]
ForeignMarker == /\ s.producerResult="marker" /\ s.markerMatches /\ s.callback="effect"
 /\ s'=[s EXCEPT !.markerMatches=FALSE]
ChoosePropagation(p) == /\ s.phase="provider" /\ s.callback \in {"none","done"} /\ s.propagation="unset"
 /\ s'=[s EXCEPT !.propagation=p]
SafeLocal == s.called /\ s.claimed /\ s.completed /\ s.eligible
 /\ (s.producerResult="nil" \/ (s.producerResult="marker" /\ s.markerMatches)) /\ s.inbound /\ s.kind="frame"
 /\ ((s.callbackResult="nil" /\ s.propagation="nil") \/
       (s.callbackResult="wrapper" /\ s.propagation="same"))
 /\ s.childCanceled /\ Live /\ ~s.invalid
ExceptionOracle == s.called /\ s.tokenState="fresh" /\ s.local /\ s.rawOK
 /\ s.inbound /\ s.kind="frame" /\ s.completed /\ s.eligible
 /\ s.cleanProducer /\ (s.producerResult="nil" \/ (s.producerResult="marker" /\ s.markerMatches))
 /\ s.childCanceled /\ ~s.terminal /\ ~s.expired /\ ~s.invalid
 /\ ((s.callbackResult="nil" /\ s.propagation="nil") \/
       (s.callbackResult="wrapper" /\ s.propagation="same"))
ProviderReturn(ok) == /\ (~s.local \/ (s.propagation # "unset" /\ ok=(s.propagation="nil"))) /\ s.phase="provider" /\ s.callback \in {"none","done"}
 /\ LET accepted == ok /\ s.called /\ ~s.invalid /\ s.appliedOK /\ Live /\ ~s.childCanceled
        poison == s.invalid \/ (ok /\ ~s.called) \/ (s.called /\ ~accepted /\ ~SafeLocal)
    IN s'=[s EXCEPT !.phase="classified", !.success=accepted,
      !.evidence=accepted /\ s.tentative,
      !.badPublication=@ \/ (accepted /\ (~Live \/ ~s.called \/ s.invalid \/ ~ok \/ ~s.appliedOK)),
      !.tentative=FALSE, !.poison=@ \/ poison,
 !.safeFailure=SafeLocal /\ ~accepted,
 !.badNeedlessSeal=@ \/ (ExceptionOracle /\ poison /\ ~s.poison),
 !.badException=@ \/ (SafeLocal /\ ~ExceptionOracle),
 !.badFailurePublication=@ \/ (SafeLocal /\ (accepted \/ s.tentative))]
MisuseReturn == /\ s.callback="misuse" /\ s.terminal
 /\ s'=[s EXCEPT !.callback="done"]
FinishReturn == /\ s.phase="classified" /\ (~s.poison \/ s.terminal)
 /\ s'=[s EXCEPT !.phase="returned"]
Leave == /\ s.phase="returned" /\ s'=[s EXCEPT !.phase="done", !.gate=FALSE]
LateCallback == /\ s.phase \in {"classified","returned","done"} /\ ~s.lateOnce
 /\ s'=[s EXCEPT !.lateOnce=TRUE, !.poison=TRUE]
ReadEvidence == /\ ~s.gate /\ ~s.observed /\ (s.evidence \/ s.tentative)
 /\ s'=[s EXCEPT !.observed=TRUE, !.badVisible=@ \/ s.tentative]
IOStart == /\ Live /\ s.ioStarted<2
 /\ s'=[s EXCEPT !.io=@+1, !.ioStarted=@+1, !.badAdmission=@ \/ ~Live]
IOEnd == /\ s.io>0 /\ s'=[s EXCEPT !.io=@-1]
CloseReturn(ok) == /\ s.closing /\ s.closeResult="pending"
 /\ s'=[s EXCEPT !.closeResult=IF ok THEN "ok" ELSE "error"]
Join == /\ s.closeResult # "pending" /\ s.io=0 /\ ~s.gate /\ ~s.joined
 /\ s'=[s EXCEPT !.joined=TRUE, !.released=(s.closeResult="ok")]
ReplayPriorSuccess == /\ s.phase="done" /\ s.success /\ s.local /\ s.claimed /\ ~s.replayTried
 /\ s'=[s EXCEPT !.replayTried=TRUE, !.badReplay=@ \/ ~s.tokenUsed]
Next == ReplayPriorSuccess \/ (\E local \in BOOLEAN, inbound \in BOOLEAN, token \in {"absent","fresh","used","completed"}: Configure(local,inbound,token))
 \/ ForeignMarker \/ ChildCancel \/ TransportSeal \/ ProducerPostcheck
 \/ (\E p \in {"nil","same","wrapped","other"}: ChoosePropagation(p)) \/ (\E k \in {"frame","observation"}: Enter(k)) \/ Expire \/ Revoke
 \/ CallbackBegin \/ (\E ok \in BOOLEAN: Apply(ok)) \/ CallbackEnd \/ Duplicate
 \/ (\E ok \in BOOLEAN: ProviderReturn(ok)) \/ Leave \/ LateCallback
 \/ MisuseReturn \/ FinishReturn \/ ReadEvidence \/ IOStart \/ IOEnd \/ (\E ok \in BOOLEAN: CloseReturn(ok)) \/ Join
TypeOK == /\ s.phase \in {"fresh","provider","classified","returned","done"}
 /\ s.io \in 0..2 /\ s.ioStarted \in 0..2 /\ s.effects \in 0..2
 /\ s.closes \in 0..2 /\ s.closeResult \in {"pending","ok","error"}
NoInvalidAdmission == ~s.badAdmission
NoInvalidPublication == ~s.badPublication
TentativeHidden == ~s.badVisible
CloseOnce == s.closes<=1
CloseBeforeJoin == s.joined => s.closing /\ s.closeResult # "pending"
DebtUntilDrain == s.released => s.joined /\ s.io=0 /\ ~s.gate /\ s.closeResult="ok"
ImmutableBinding == s.binding=1
OneApply == s.effects<=1
TerminalSeal == s.terminal => s.closing
EvidenceHasSuccess == s.evidence => s.success
NoNeedlessSeal == ~s.badNeedlessSeal
NoUnsafeException == ~s.badException
NoTokenReplay == ~s.badReplay
FailedPacketNotPublished == ~s.badFailurePublication
Spec == Init /\ [][Next]_s
=============================================================================
