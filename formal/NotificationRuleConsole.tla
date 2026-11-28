---------------------- MODULE NotificationRuleConsole ----------------------
EXTENDS Naturals, FiniteSets

\* The trusted scope combines principal, region and mounted service view.
\* Read trials exercise two numbered requests; mutation trials exercise
\* one user's command and at most one further physical write.
ReadKinds == {"firstPage", "nextPage", "detailA", "detailB"}
Ops == {"create", "update", "delete"}
ACKs == {"ok", "rejected", "lost", "badAck"}
Facts == {"fieldsMatch", "fieldsDiffer", "missing", "wrongARN", "denied", "failed"}
Ticket(id, scope, kind) == [id |-> id, scope |-> scope, kind |-> kind]
Fresh(lane) ==
 [lane |-> lane, scope |-> 0, issued |-> 0, readCount |-> 0, waiting |-> {}, active |-> 0,
  view |-> "none", stage |-> "idle", op |-> "none", arn |-> "none",
  body |-> "none", token |-> "none", sends |-> 0, retried |-> FALSE,
  verified |-> FALSE, retired |-> FALSE]
VARIABLE ui, last
Stamp(step, mode, item, outcome) ==
 [step |-> step, mode |-> mode, item |-> item, outcome |-> outcome, before |-> ui]
Init == /\ ui \in {Fresh("read"), Fresh("write")}
        /\ last = Stamp("init","",0,"")
ReadTicket == ui.issued+1
BeginRead(k) ==
 /\ ui.lane = "read" /\ ui.active = 0 /\ ui.readCount < 2
 /\ k # "nextPage" \/ ui.view = "firstPage"
 /\ LET q == Ticket(ReadTicket, ui.scope, k)
    IN ui' = [ui EXCEPT !.issued = q.id, !.readCount = @+1, !.waiting = @ \cup {q}, !.active = q.id,
                       !.view = IF k = "nextPage" THEN @ ELSE "none"]
 /\ last' = Stamp("beginRead",k,ReadTicket,"began")
CancelRead ==
 /\ ui.lane = "read" /\ ui.active > 0
 /\ ui' = [ui EXCEPT !.active = 0]
 /\ last' = Stamp("cancelRead","",ui.active,"canceled")
Current(q) == q.id = ui.active /\ q.scope = ui.scope
ReadValue(q) == IF q.kind = "nextPage" THEN "bothPages" ELSE q.kind
AnswerRead(q,status) ==
 /\ q \in ui.waiting /\ status \in {"ok","denied","malformed","failed","wrongARN"}
 /\ status = "wrongARN" => q.kind \in {"detailA","detailB"}
 /\ LET current == Current(q)
        publish == current /\ status = "ok"
    IN ui' = [ui EXCEPT !.waiting = @ \ {q}, !.active = IF current THEN 0 ELSE @,
                       !.view = IF publish THEN ReadValue(q) ELSE @]
 /\ last' = Stamp("readReply",status,q,"settled")

Switch ==
 /\ ui.scope = 0
 /\ ui' = [Fresh(ui.lane) EXCEPT !.scope = 1, !.readCount = ui.readCount,
           !.waiting = ui.waiting, !.sends = ui.sends,
           !.retired = ui.stage \in {"sending","checking"}]
 /\ last' = Stamp("switch","",0,"newScope")
LateWriteReply ==
 /\ ui.retired
 /\ ui' = [ui EXCEPT !.retired = FALSE]
 /\ last' = Stamp("lateWrite","",0,"discarded")

WriteOpen == ui.stage = "idle"
BeginWrite(k) ==
 /\ ui.lane = "write" /\ ui.sends < 2 /\ k \in Ops
 /\ LET go == WriteOpen
    IN /\ ui' = IF go THEN
        [ui EXCEPT !.sends = @+1, !.stage = "sending", !.op = k,
         !.arn = IF k = "create" THEN "none" ELSE "A",
         !.body = "original", !.token = IF k = "create" THEN "T" ELSE "none",
         !.verified = FALSE, !.retried = FALSE, !.view = "none"] ELSE ui
       /\ last' = Stamp("beginWrite",k,0,IF go THEN "sent" ELSE "blocked")
Uncertain == IF ui.arn = "none" THEN "unknownCreate" ELSE "known"
AnswerWrite(status) ==
 /\ ui.stage = "sending" /\ status \in ACKs
 /\ LET next == CASE status = "ok" -> "checking"
                 [] status = "rejected" /\ ~ui.retried -> "rejected"
                 [] OTHER -> Uncertain
    IN ui' = [ui EXCEPT !.stage = next, !.arn = IF status = "ok" THEN "A" ELSE @]
 /\ last' = Stamp("writeReply",status,ui.op,"settled")
Verifies(fact) == IF ui.op = "delete" THEN fact = "missing" ELSE fact = "fieldsMatch"
CheckSaved(fact) ==
 /\ ui.stage = "checking" /\ fact \in Facts
 /\ LET done == Verifies(fact)
    IN ui' = [ui EXCEPT !.verified = done, !.stage = IF done THEN "done" ELSE "known",
              !.view = IF done THEN (IF ui.op = "delete" THEN "absent" ELSE "current") ELSE "none"]
 /\ last' = Stamp("check",fact,ui.op,"observed")
RecoveryFacts == {"sameDetail", "same404", "otherDetail", "other404", "list",
                  "sameName", "denied", "failed"}
Recovers(fact) == fact \in {"sameDetail","same404"}
Recover(fact) ==
 /\ ui.stage \in {"known","unknownCreate"} /\ fact \in RecoveryFacts
 /\ LET recovered == ui.stage = "known" /\ Recovers(fact)
    IN ui' = [ui EXCEPT !.stage = IF recovered THEN "observed" ELSE @,
              !.view = IF recovered THEN (IF fact = "same404" THEN "absent" ELSE "current") ELSE @]
 /\ last' = Stamp("recover",fact,ui.op,"observed")
ReplayModes == {"explicitIdentical", "automatic", "changedBody", "changedToken", "wrongScope"}
MayReplay(m) == ui.stage = "unknownCreate" /\ m = "explicitIdentical"
Replay(m) ==
 /\ ui.stage \in {"known","unknownCreate"} /\ ui.sends < 2 /\ m \in ReplayModes
 /\ LET send == MayReplay(m)
    IN /\ ui' = IF send THEN [ui EXCEPT !.sends = @+1, !.stage = "sending",
                          !.retried = TRUE] ELSE ui
       /\ last' = Stamp("replay",m,ui.op,IF send THEN "sent" ELSE "blocked")
Next == \/ (\E k \in ReadKinds : BeginRead(k)) \/ CancelRead
        \/ (\E q \in ui.waiting, s \in {"ok","denied","malformed","failed","wrongARN"} : AnswerRead(q,s))
        \/ Switch \/ LateWriteReply \/ (\E k \in Ops : BeginWrite(k))
        \/ (\E s \in ACKs : AnswerWrite(s)) \/ (\E f \in Facts : CheckSaved(f))
        \/ (\E f \in RecoveryFacts : Recover(f)) \/ (\E m \in ReplayModes : Replay(m))

TypeOK ==
 /\ ui.scope \in 0..1 /\ ui.sends \in 0..2 /\ ui.issued \in 0..2 /\ ui.readCount \in 0..2 /\ ui.active \in 0..2
 /\ ui.stage \in {"idle","sending","checking","rejected","known","unknownCreate","done","observed"}
 /\ ui.verified \in BOOLEAN /\ ui.retired \in BOOLEAN /\ Cardinality(ui.waiting) <= 2
ReadFrame == last.step # "readReply" \/
  LET q == last.item
      now == q.scope = last.before.scope /\ q.id = last.before.active
      value == IF q.kind = "nextPage" THEN "bothPages" ELSE q.kind
  IN /\ ui.active = (IF now THEN 0 ELSE last.before.active)
     /\ ui.view = (IF now /\ last.mode = "ok" THEN value ELSE last.before.view)
     /\ ui.waiting = last.before.waiting \ {q}
CanceledRead == last.step # "cancelRead" \/
                (ui.active = 0 /\ ui.view = last.before.view /\ ui.waiting = last.before.waiting)
ScopeReset == last.step # "switch" \/
              (ui.scope = 1 /\ ui.active = 0 /\ ui.view = "none" /\ ui.stage = "idle" /\
               ~ui.verified /\ ui.arn = "none" /\ ui.token = "none" /\ ui.body = "none")
LateWriteFrame == last.step # "lateWrite" \/ ui = [last.before EXCEPT !.retired = FALSE]
WriteLock == last.step # "beginWrite" \/
 (IF last.before.stage = "idle" THEN
     ui.sends = last.before.sends+1 /\ ui.stage = "sending" /\ ui.op = last.mode
  ELSE ui = last.before /\ last.outcome = "blocked")
AckState == last.step # "writeReply" \/
 /\ ~ui.verified /\ ui.sends = last.before.sends
 /\ ui.stage = (CASE last.mode = "ok" -> "checking"
                 [] last.mode = "rejected" /\ ~last.before.retried -> "rejected"
                 [] last.before.arn = "none" -> "unknownCreate" [] OTHER -> "known")
 /\ ui.arn = (IF last.mode = "ok" THEN "A" ELSE last.before.arn)
SavedState == last.step # "check" \/
 LET exact == (last.before.op = "delete" /\ last.mode = "missing") \/
              (last.before.op # "delete" /\ last.mode = "fieldsMatch")
 IN /\ ui.verified = exact /\ ui.sends = last.before.sends
    /\ ui.stage = (IF exact THEN "done" ELSE "known")
    /\ ui.view = (IF ~exact THEN "none" ELSE IF last.before.op = "delete" THEN "absent" ELSE "current")
RecoveryState == last.step # "recover" \/
 LET exact == last.before.stage = "known" /\ last.mode \in {"sameDetail","same404"}
 IN /\ ui.stage = (IF exact THEN "observed" ELSE last.before.stage)
    /\ ui.view = (IF exact THEN (IF last.mode = "same404" THEN "absent" ELSE "current") ELSE last.before.view)
    /\ ~ui.verified /\ ui.sends = last.before.sends
ExactReplay == last.step # "replay" \/
 (IF last.before.stage = "unknownCreate" /\ last.mode = "explicitIdentical" THEN
    /\ ui.sends = last.before.sends+1 /\ ui.stage = "sending" /\ ui.retried
    /\ ui.token = last.before.token /\ ui.token = "T" /\ ui.body = last.before.body /\ ui.body = "original"
  ELSE ui = last.before /\ last.outcome = "blocked")
=============================================================================
