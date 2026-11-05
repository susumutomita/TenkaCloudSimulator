---------------------- MODULE DynamoDBConsoleEpoch ----------------------
EXTENDS Naturals, TLC
(* Independent finite view-safety abstraction. No storage/IAM/JS refinement.
   Two request slots, epochs 0..2 (no wrap), one remount, one write intent.
   A published item is checked at acceptance, not against future native changes. *)
CONSTANT None
Contexts == 0..1
Request == [ctx:Contexts, inst:0..1, epoch:0..2, key:0..1, table:1..2]
Visible == [ctx:Contexts, inst:0..1, key:0..1]
Publication == [epochOK:BOOLEAN, contextOK:BOOLEAN, tableOK:BOOLEAN]
VARIABLE s
vars == <<s>>
Current(r) == r.ctx = s.ctx /\ r.inst = s.inst /\ r.epoch = s.epoch
Initial == \E mode \in {"read","write"} :
 s = [mode |-> mode,ctx |-> 0,inst |-> 0,epoch |-> 0,key |-> 0,table |-> 1,
 requests |-> [i \in 1..2 |-> None],used |-> {},loading |-> FALSE,
 shown |-> None,published |-> None,form |-> FALSE,draft |-> FALSE,
 opened |-> FALSE,owner |-> FALSE,writePhase |-> "idle",sends |-> 0,
 writeCapture |-> [ctx |-> 0,inst |-> 0,epoch |-> 0,key |-> 0,table |-> 1],
 ack |-> FALSE,ackCurrent |-> TRUE,retrySafe |-> TRUE,
 locale |-> "en",localeSafe |-> TRUE]
BeginRead(i) ==
 /\ s.mode = "read" /\ i \notin s.used /\ s.epoch < 2
 /\ s' = [s EXCEPT !.epoch = @+1, !.used = @ \cup {i}, !.loading = TRUE,
       !.shown = None, !.requests[i] = [ctx |-> s.ctx,inst |-> s.inst,
          epoch |-> s.epoch+1,key |-> s.key,table |-> s.table]]
CompleteRead(i) ==
 /\ s.requests[i] # None
 /\ LET r == s.requests[i]
        live == Current(r) (* ReadEpochGuard *)
        sameTable == r.table = s.table (* PostReadGenerationGuard *)
    IN s' = [s EXCEPT !.requests[i] = None,
       !.loading = IF live THEN FALSE ELSE @,
       !.shown = IF live /\ sameTable THEN
          [ctx |-> r.ctx,inst |-> r.inst,key |-> r.key] ELSE @,
       !.published = IF live /\ sameTable THEN
          [epochOK |-> r.epoch = s.epoch,
           contextOK |-> r.ctx = s.ctx /\ r.inst = s.inst,
           tableOK |-> r.table = s.table] ELSE @]
KeyEdit ==
 /\ s.mode = "read" /\ s.epoch < 2
 /\ s' = [s EXCEPT !.key = 1-@, !.epoch = @+1,
       !.shown = None, (* KeyEditClear *) !.loading = FALSE]
OpenForm ==
 /\ ~s.opened /\ ~s.owner /\ s.epoch < 2
 /\ s' = [s EXCEPT !.opened = TRUE, !.form = TRUE, !.draft = TRUE,
       !.epoch = @+1, !.loading = FALSE]
Cancel ==
 /\ ~s.owner /\ s.epoch < 2
 /\ s' = [s EXCEPT !.epoch = @+1, !.form = FALSE, !.draft = FALSE,
       !.loading = FALSE] (* CancelClearLoading *)
Remount ==
 /\ s.inst = 0
 /\ s' = [s EXCEPT !.ctx = 1, !.inst = 1, !.epoch = 0,
       !.shown = None, !.form = FALSE, !.draft = FALSE,
       !.owner = FALSE, !.loading = FALSE]
RecreateNativeTable ==
 /\ s.table = 1
 /\ s' = [s EXCEPT !.table = 2]
Submit ==
 /\ s.mode = "write" /\ s.form /\ s.draft /\ ~s.owner /\ s.sends = 0
 /\ s' = [s EXCEPT !.owner = TRUE, !.sends = @+1, !.writePhase = "pending",
       !.writeCapture = [ctx |-> s.ctx,inst |-> s.inst,epoch |-> s.epoch,
          key |-> s.key,table |-> s.table]]
(* A second click before React renders busy still meets the synchronous ref. *)
SecondClick ==
 /\ s.owner /\ s.writePhase = "pending"
 /\ s' = [s EXCEPT !.sends = @] (* SynchronousOwnerLock *)
WriteACK ==
 /\ s.writePhase = "pending"
 /\ LET live == Current(s.writeCapture) (* WriteEpochGuard *)
    IN s' = [s EXCEPT !.owner = FALSE, !.writePhase = IF live THEN "metadata" ELSE "obsolete",
       !.ack = live, !.ackCurrent = IF live THEN Current(s.writeCapture) ELSE @,
       !.form = IF live THEN FALSE ELSE @, (* ACKCloseForm *)
       !.draft = IF live THEN FALSE ELSE @] (* ACKClearDraft *)
MetadataFailure ==
 /\ s.writePhase = "metadata"
 /\ s' = [s EXCEPT !.writePhase = "failed"]
MetadataSuccess ==
 /\ s.writePhase = "metadata"
 /\ s' = [s EXCEPT !.writePhase = "done"]
RetryRead ==
 /\ s.writePhase = "failed"
 /\ LET nextSends == s.sends (* RetryReadOnly *)
    IN s' = [s EXCEPT !.writePhase = "metadata", !.sends = nextSends,
       !.retrySafe = nextSends = s.sends]
SwitchLocale ==
 LET nextDraft == s.draft (* LocaleDraftFrame *)
 IN s' = [s EXCEPT !.locale = IF @ = "en" THEN "ja" ELSE "en",
       !.draft = nextDraft, !.localeSafe = nextDraft = s.draft]
Observe == UNCHANGED s
Next == (\E i \in 1..2 : BeginRead(i) \/ CompleteRead(i)) \/ KeyEdit \/
 OpenForm \/ Cancel \/ Remount \/ RecreateNativeTable \/ Submit \/ SecondClick \/
 WriteACK \/ MetadataFailure \/ MetadataSuccess \/ RetryRead \/ SwitchLocale \/ Observe
TypeOK ==
 /\ s.mode \in {"read","write"} /\ s.ctx \in Contexts /\ s.inst \in 0..1
 /\ s.epoch \in 0..2 /\ s.key \in 0..1 /\ s.table \in 1..2
 /\ s.requests \in [1..2 -> ({None} \cup Request)] /\ s.used \subseteq 1..2
 /\ s.loading \in BOOLEAN /\ s.shown \in {None} \cup Visible
 /\ s.published \in {None} \cup Publication
 /\ s.form \in BOOLEAN /\ s.draft \in BOOLEAN /\ s.opened \in BOOLEAN
 /\ s.owner \in BOOLEAN /\ s.writePhase \in {"idle","pending","metadata","failed","done","obsolete"}
 /\ s.sends \in 0..2 /\ s.writeCapture \in Request
 /\ s.ack \in BOOLEAN /\ s.ackCurrent \in BOOLEAN /\ s.retrySafe \in BOOLEAN
 /\ s.locale \in {"en","ja"} /\ s.localeSafe \in BOOLEAN
NoStalePublication == s.published = None \/ (s.published.epochOK /\ s.published.contextOK)
ReadGenerationVerified == s.published = None \/ s.published.tableOK
DisplayedKeyBound == s.shown = None \/
 (s.shown.ctx = s.ctx /\ s.shown.inst = s.inst /\ s.shown.key = s.key)
CancelNoOrphanLoading == s.loading =>
 \E i \in 1..2 : s.requests[i] # None /\ Current(s.requests[i])
AckClosesDraft == s.ack => (~s.form /\ ~s.draft)
AckBelongsToCurrentView == s.ackCurrent
ReadRetryNeverWrites == s.retrySafe
SingleSendBeforeACK == s.writePhase = "pending" => s.sends = 1
LocalePreservesDraft == s.localeSafe
Spec == Initial /\ [][Next]_vars
=============================================================================
