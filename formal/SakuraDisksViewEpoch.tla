----------------------- MODULE SakuraDisksViewEpoch -----------------------
EXTENDS Naturals, FiniteSets
CONSTANT None
VARIABLE s
vars == <<s>>
Resources == 0..2
Panels == {"none","create","edit","delete"}
Phases == {"idle","pending","body","metadata","failed","done","obsolete"}
Capture(v) == [life |-> v.life,scope |-> v.scope,seq |-> v.seq,
               nav |-> v.nav,resource |-> v.resource]
View(v) == [resource |-> v.resource,panel |-> v.panel,draft |-> v.draft,
            display |-> v.display,error |-> v.error,loading |-> v.loading,
            busy |-> v.busy,notice |-> v.notice]
Initial == \E mode \in {"read","write"},resource \in 0..1 :
 s=[mode |-> mode,life |-> 0,scope |-> 0,seq |-> 0,nav |-> 0,
    resource |-> resource,panel |-> "none",draft |-> 0,opens |-> 0,
    display |-> IF resource=0 THEN None ELSE [life |-> 0,scope |-> 0,resource |-> 1],
    error |-> FALSE,loading |-> FALSE,busy |-> FALSE,notice |-> "none",
    requests |-> [i \in 1..2 |-> None],used |-> {},owner |-> FALSE,
    sends |-> 0,phase |-> "idle",op |-> "create",
    writeCapture |-> [life |-> 0,scope |-> 0,seq |-> 0,nav |-> 0,resource |-> 0],
    locale |-> "en",last |-> [kind |-> "none"]]
ReadCurrent(r) == r.life=s.life /\ r.scope=s.scope /\ r.seq=s.seq
ExpectedReadCurrent(r) == r.life=s.life /\ r.scope=s.scope /\ r.seq=s.seq
WriteCurrent(r) == r.life=s.life /\ r.scope=s.scope
ExpectedWriteCurrent(r) == r.life=s.life /\ r.scope=s.scope
Published(r) == [life |-> r.life,scope |-> r.scope,resource |-> r.resource]
BeginRead(i) ==
 /\ i \notin s.used /\ s.seq<3 /\ s.panel="none"
 /\ ~(s.phase="body" /\ ExpectedWriteCurrent(s.writeCapture) /\ s.nav=s.writeCapture.nav)
 /\ s'=[s EXCEPT !.seq=@+1,!.used=@ \cup {i},!.loading=TRUE,
        !.display=None,!.error=FALSE,
        !.requests[i]=[life |-> s.life,scope |-> s.scope,seq |-> s.seq+1,
                        nav |-> s.nav,resource |-> s.resource]]
ReadDone(i,ok) ==
 /\ s.requests[i]#None
 /\ LET r == s.requests[i]
        live == ReadCurrent(r)
        publish == live /\ ok
        candidate == [s EXCEPT !.requests[i]=None,
             !.display=IF publish THEN Published(r) ELSE @,
             !.error=IF live THEN ~ok ELSE @,
             !.loading=IF live THEN FALSE ELSE @,
             !.phase=IF live /\ s.phase="metadata" THEN (IF ok THEN "done" ELSE "failed") ELSE @]
    IN s'=[candidate EXCEPT !.last=[kind |-> "read",published |-> publish,
              expected |-> ExpectedReadCurrent(r),success |-> ok,
              before |-> View(s),after |-> View(candidate)]]
Navigate(resource) ==
 /\ resource \in Resources /\ s.nav<2 /\ s.seq<3
 /\ (resource=0 \/ (s.display#None /\ s.display.resource=0))
 /\ s'=[s EXCEPT !.resource=resource,!.seq=@+1,!.nav=@+1,
         !.panel="none",!.draft=0,!.display=None,!.error=FALSE,!.notice="none"]
OpenForm(panel) ==
 /\ s.mode="write" /\ ~s.busy /\ s.panel="none" /\ s.opens<2 /\ s.nav<2 /\ s.seq<3
 /\ ((panel="create" /\ s.resource=0) \/
    (panel \in {"edit","delete"} /\ s.resource#0 /\ s.display#None))
 /\ s'=[s EXCEPT !.panel=panel,!.draft=1,!.opens=@+1,!.nav=@+1,
         !.seq=@+1,!.loading=FALSE,!.error=FALSE,!.notice="none"]
Cancel ==
 /\ s.panel#"none" /\ ~s.busy /\ s.nav<2 /\ s.seq<3
 /\ LET candidate == [s EXCEPT !.panel="none",!.draft=0,
                           !.nav=@+1,!.seq=@+1,!.loading=FALSE,!.error=FALSE]
    IN s'=[candidate EXCEPT !.last=[kind |-> "cancel",after |-> View(candidate)]]
ScopeChange ==
 /\ s.life=0
 /\ s'=[s EXCEPT !.life=1,!.scope=1,!.seq=0,!.nav=0,!.resource=0,
         !.panel="none",!.draft=0,!.display=None,!.error=FALSE,
         !.loading=FALSE,!.busy=FALSE,!.notice="none",!.owner=FALSE]
Submit ==
 /\ s.mode="write" /\ s.panel#"none" /\ s.draft=1 /\ ~s.owner /\ s.sends=0
 /\ s'=[s EXCEPT !.owner=TRUE,!.busy=TRUE,!.sends=@+1,!.phase="pending",
         !.op=s.panel,!.writeCapture=Capture(s)]
SecondClick ==
 /\ s.owner /\ s.phase="pending"
 /\ s'=[s EXCEPT !.sends=@] (* SynchronousTicket *)
WriteACK ==
 /\ s.phase="pending"
 /\ LET r == s.writeCapture
        live == WriteCurrent(r)
        sameNav == r.nav=s.nav
        candidate == [s EXCEPT
           !.phase=IF live /\ sameNav THEN "body" ELSE "obsolete",
           !.panel=IF live /\ sameNav THEN "none" ELSE @,
           !.draft=IF live /\ sameNav THEN 0 ELSE @,
           !.display=IF live /\ sameNav THEN None ELSE @,
           !.notice=IF live /\ sameNav THEN s.op ELSE @,
           !.loading=IF live /\ sameNav THEN TRUE ELSE @,
           !.busy=IF live /\ ~sameNav THEN FALSE ELSE @,
           !.owner=IF live /\ ~sameNav THEN FALSE ELSE @]
    IN s'=[candidate EXCEPT !.last=[kind |-> "ack",
         expected |-> ExpectedWriteCurrent(r),sameNav |-> sameNav,
         before |-> View(s),after |-> View(candidate)]]
NextResource == CASE s.op="create" -> 2 [] s.op="delete" -> 0 [] OTHER -> s.writeCapture.resource
BodyDone(ok) ==
 /\ s.phase="body"
 /\ LET r == s.writeCapture
        live == WriteCurrent(r) /\ r.nav=s.nav
        candidate == [s EXCEPT !.phase=IF live THEN (IF ok THEN "metadata" ELSE "failed") ELSE "obsolete",
          !.resource=IF live /\ ok THEN NextResource ELSE @,
          !.error=IF live THEN ~ok ELSE @,
          !.loading=IF live THEN ok ELSE @,
          !.busy=IF ExpectedWriteCurrent(r) THEN FALSE ELSE @,!.owner=FALSE]
    IN s'=[candidate EXCEPT !.last=[kind |-> "body",
         expected |-> ExpectedWriteCurrent(r),sameNav |-> r.nav=s.nav,
         before |-> View(s),after |-> View(candidate)]]
RetryRead ==
 /\ s.phase="failed" /\ ~s.busy /\ s.panel="none"
 /\ LET nextSends == s.sends (* RetryOnlyGET *)
        candidate == [s EXCEPT !.phase="metadata",!.error=FALSE,!.sends=nextSends]
    IN s'=[candidate EXCEPT !.last=[kind |-> "retry",beforeSends |-> s.sends,afterSends |-> candidate.sends]]
Locale ==
 LET nextDraft == s.draft (* LocaleDraftPreserved *)
     candidate == [s EXCEPT !.locale=IF @="en" THEN "ja" ELSE "en",!.draft=nextDraft]
 IN s'=[candidate EXCEPT !.last=[kind |-> "locale",beforeDraft |-> s.draft,afterDraft |-> candidate.draft]]
Next == (\E i \in 1..2 : BeginRead(i) \/ (\E ok \in BOOLEAN : ReadDone(i,ok)))
        \/ (\E r \in Resources : Navigate(r)) \/ (\E p \in {"create","edit","delete"} : OpenForm(p))
        \/ Cancel \/ ScopeChange \/ Submit \/ SecondClick \/ WriteACK
        \/ (\E ok \in BOOLEAN : BodyDone(ok)) \/ RetryRead \/ Locale \/ UNCHANGED s
TypeOK == /\ s.mode \in {"read","write"} /\ s.life \in 0..1 /\ s.scope \in 0..1
          /\ s.seq \in 0..3 /\ s.nav \in 0..2 /\ s.resource \in Resources
          /\ s.panel \in Panels /\ s.draft \in 0..1 /\ s.opens \in 0..2
          /\ s.loading \in BOOLEAN /\ s.error \in BOOLEAN /\ s.busy \in BOOLEAN
          /\ s.owner \in BOOLEAN /\ s.sends \in 0..2 /\ s.phase \in Phases
          /\ s.locale \in {"en","ja"} /\ s.used \subseteq 1..2
AcceptedReadScope == IF s.last.kind="read" THEN s.last.published => s.last.expected ELSE TRUE
SuccessfulReadOnly == IF s.last.kind="read" THEN s.last.published => s.last.success ELSE TRUE
RenderedScopeBound == s.display=None \/ (s.display.life=s.life /\ s.display.scope=s.scope)
RenderedResourceBound == s.display=None \/ s.display.resource=s.resource
StaleReadFrame == IF s.last.kind="read" THEN ~s.last.expected => s.last.before=s.last.after ELSE TRUE
StaleWriteFrame == IF s.last.kind \in {"ack","body"} THEN ~s.last.expected => s.last.before=s.last.after ELSE TRUE
NavigationACKFrame == IF s.last.kind="ack" THEN s.last.expected /\ ~s.last.sameNav =>
    s.last.before.resource=s.last.after.resource /\ s.last.before.panel=s.last.after.panel
    /\ s.last.before.draft=s.last.after.draft ELSE TRUE
AckClosesForm == s.notice#"none" => s.panel="none"
AckClearsDraft == s.notice#"none" => s.draft=0
ReadRetryOnly == IF s.last.kind="retry" THEN s.last.beforeSends=s.last.afterSends ELSE TRUE
SingleWriteSend == s.phase="pending" => s.sends=1
LocaleFrame == IF s.last.kind="locale" THEN s.last.beforeDraft=s.last.afterDraft ELSE TRUE
CancelDraftClear == IF s.last.kind="cancel" THEN s.last.after.panel="none" /\ s.last.after.draft=0 ELSE TRUE
=============================================================================
