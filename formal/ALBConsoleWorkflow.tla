------------------------- MODULE ALBConsoleWorkflow -------------------------
EXTENDS Naturals, FiniteSets

Kinds == {"loadBalancers","nextPage","balancerDetail","groupDetail"}
Results == {"ok","empty","unhealthy","denied","failed","wrongArn","listenerFailed","groupsFailed","healthFailed"}
Axes == {"source","region","principal","section","albSelection","groupSelection","remount"}
Home == [source |-> 0, region |-> 0, principal |-> 0, section |-> 0,
         alb |-> "A", group |-> "A", mount |-> 0]
Move(s, axis) ==
 CASE axis = "source" -> [s EXCEPT !.source = 1]
   [] axis = "region" -> [s EXCEPT !.region = 1]
   [] axis = "principal" -> [s EXCEPT !.principal = 1]
   [] axis = "section" -> [s EXCEPT !.section = 1]
   [] axis = "albSelection" -> [s EXCEPT !.alb = "B"]
   [] axis = "groupSelection" -> [s EXCEPT !.group = "B"]
   [] OTHER -> [s EXCEPT !.mount = 1]
Ticket(s,n,k) == [scope |-> s, id |-> n, kind |-> k]
EmptyView == [value |-> "none", cursor |-> FALSE, available |-> FALSE]
Ready(v,c) == [value |-> v, cursor |-> c, available |-> TRUE]
Rest == [phase |-> "idle", sent |-> 0, arn |-> "none", outcome |-> "none",
         verified |-> FALSE, health |-> "none", seenPath |-> "none"]
VARIABLE ui,last
Mark(step,k,result) == [step |-> step, kind |-> k, result |-> result, before |-> ui]
Init ==
 /\ ui \in {[lane |-> lane, scope |-> Home, moved |-> FALSE, count |-> 0,
             serial |-> 0, active |-> {}, retired |-> {}, view |-> EmptyView,
             w |-> Rest, target |-> "old", sibling |-> "other", retiredWrite |-> FALSE]
             : lane \in {"read","write"}}
 /\ last = Mark("init","","")

StartRead(k) ==
 /\ ui.lane = "read" /\ ui.count < 3 /\ ui.active = {} /\ k \in Kinds
 /\ k = "nextPage" => ui.view.cursor
 /\ LET q == Ticket(ui.scope,ui.serial+1,k)
    IN ui' = [ui EXCEPT !.count = @+1, !.serial = @+1, !.active = {q},
                       !.view = EmptyView]
 /\ last' = Mark("read",k,"")
RetireRead ==
 /\ ui.lane = "read" /\ ui.active # {} /\ ui.retired = {}
 /\ ui' = [ui EXCEPT !.retired = ui.active, !.active = {}]
 /\ last' = Mark("retire","","")
Current(q) == q \in ui.active /\ q.scope = ui.scope
Projection(k,r) ==
 CASE r = "denied" -> EmptyView
   [] r = "empty" -> Ready("empty",FALSE)
   [] r = "unhealthy" -> Ready("unhealthy",FALSE)
   [] r = "ok" -> CASE k = "groupDetail" -> Ready("healthy",FALSE)
                    [] k = "loadBalancers" -> Ready("loadBalancers",TRUE)
                    [] k = "nextPage" -> Ready("bothPages",FALSE)
                    [] OTHER -> Ready(k,FALSE)
   [] OTHER -> EmptyView
FinishRead(q,r) ==
 /\ q \in ui.active \cup ui.retired /\ r \in Results
 /\ r = "unhealthy" => q.kind = "groupDetail"
 /\ r \in {"listenerFailed","groupsFailed"} => q.kind = "balancerDetail"
 /\ r = "healthFailed" => q.kind = "groupDetail"
 /\ ui' = [ui EXCEPT !.view = IF Current(q) THEN Projection(q.kind,r) ELSE @,
                    !.active = @ \ {q}, !.retired = @ \ {q}]
 /\ last' = Mark("readReply",q,r)

\* The write trial begins at a selected, saved Lambda target group.
StartWrite ==
 /\ ui.lane = "write" /\ ~ui.moved /\ ui.w.sent = 0
 /\ ui' = [ui EXCEPT !.w.phase = "sending", !.w.sent = 1, !.w.arn = ui.scope.group]
 /\ last' = Mark("send","","")
Server(outcome) ==
 /\ ui.w.phase = "sending" /\ outcome \in {"applied","refused"}
 /\ ui' = [ui EXCEPT !.w.phase = "awaitAck", !.w.outcome = outcome,
                    !.target = IF outcome = "applied" THEN "desired" ELSE @]
 /\ last' = Mark("server","",outcome)
Ack(r) ==
 /\ ui.w.phase = "awaitAck"
 /\ IF ui.w.outcome = "applied" THEN r \in {"matching","wrongArn","wrongPath","lost"}
                                ELSE r \in {"denied","lost"}
 /\ LET phase == CASE r = "matching" -> "checkingPath"
                   [] r = "denied" -> "refused"
                   [] OTHER -> "uncertain"
    IN ui' = [ui EXCEPT !.w.phase = phase]
 /\ last' = Mark("ack","",r)
ReadPath(r) ==
 /\ ui.w.phase = "checkingPath" /\ r \in {"matching","different","wrongArn","denied","failed"}
 /\ ui' = [ui EXCEPT !.w.phase = IF r = "matching" THEN "checkingHealth" ELSE "uncertain",
                    !.w.seenPath = IF r = "matching" THEN "desired" ELSE "none"]
 /\ last' = Mark("path","",r)
HealthResults == {"healthy","unhealthy","empty","wrongArn","denied","failed"}
ReadHealth(r) ==
 /\ ui.w.phase = "checkingHealth" /\ r \in HealthResults
 /\ LET typed == r \in {"healthy","unhealthy","empty"}
    IN ui' = [ui EXCEPT !.w.phase = IF typed THEN "verified" ELSE "uncertain",
                       !.w.verified = typed, !.w.health = IF typed THEN r ELSE "none"]
 /\ last' = Mark("health","",r)
RemoveElsewhere ==
 /\ ui.w.phase = "uncertain" /\ ui.target # "absent"
 /\ ui' = [ui EXCEPT !.target = "absent"]
 /\ last' = Mark("remove","","")
Recover(k) ==
 /\ ui.w.phase = "uncertain" /\ k \in {"exact","absent","otherArn","list","denied","failed","automatic","resend"}
 /\ k = "exact" => ui.target # "absent"
 /\ k = "absent" => ui.target = "absent"
 /\ LET exact == k \in {"exact","absent"}
    IN ui' = IF exact THEN [ui EXCEPT !.w.phase = "observed", !.w.seenPath = IF k = "absent" THEN "absent" ELSE ui.target]
                       ELSE ui
 /\ last' = Mark("recover",k,"")

Switch(axis) ==
 /\ ~ui.moved /\ axis \in Axes /\ (ui.retired = {} \/ ui.active = {})
 /\ ui' = [ui EXCEPT !.scope = Move(@,axis), !.moved = TRUE,
                    !.serial = 0, !.retired = ui.retired \cup ui.active, !.active = {},
                    !.view = EmptyView, !.w = Rest,
                    !.retiredWrite = ui.w.phase \in {"awaitAck","checkingPath","checkingHealth"}]
 /\ last' = Mark("switch",axis,"")
LateWrite ==
 /\ ui.retiredWrite
 /\ ui' = [ui EXCEPT !.retiredWrite = FALSE]
 /\ last' = Mark("lateWrite","","")

Next == \/ RemoveElsewhere \/ (\E k \in Kinds : StartRead(k)) \/ RetireRead
        \/ (\E q \in ui.active \cup ui.retired, r \in Results : FinishRead(q,r))
        \/ StartWrite \/ (\E r \in {"applied","refused"} : Server(r))
        \/ (\E r \in {"matching","wrongArn","wrongPath","lost","denied"} : Ack(r))
        \/ (\E r \in {"matching","different","wrongArn","denied","failed"} : ReadPath(r))
        \/ (\E r \in HealthResults : ReadHealth(r))
        \/ (\E k \in {"exact","absent","otherArn","list","denied","failed","automatic","resend"} : Recover(k))
        \/ (\E axis \in Axes : Switch(axis)) \/ LateWrite

TypeOK == /\ ui.count \in 0..3 /\ ui.serial \in 0..3 /\ ui.w.sent \in 0..2
          /\ Cardinality(ui.active) <= 1 /\ Cardinality(ui.retired) <= 1
          /\ ui.target \in {"old","desired","absent"}
ReadAdmission == last.step # "readReply" \/
 LET q == last.kind
     current == q \in last.before.active /\ q.scope = last.before.scope
 IN /\ ui.view = (IF current THEN Projection(q.kind,last.result) ELSE last.before.view)
    /\ ui.active = last.before.active \ {q} /\ ui.retired = last.before.retired \ {q}
TruthfulHealth == last.step # "readReply" \/ last.kind.kind # "groupDetail" \/
                  last.kind \notin last.before.active \/ last.kind.scope # last.before.scope \/
 (CASE last.result = "ok" -> ui.view = Ready("healthy",FALSE)
    [] last.result = "unhealthy" -> ui.view = Ready("unhealthy",FALSE)
    [] last.result = "empty" -> ui.view = Ready("empty",FALSE)
    [] OTHER -> ui.view = EmptyView)
DeniedProjection == last.step # "readReply" \/ last.result # "denied" \/
                    last.kind \notin last.before.active \/ last.kind.scope # last.before.scope \/ ui.view = EmptyView
ScopeFrame == last.step # "switch" \/
              (ui.scope = Move(last.before.scope,last.kind) /\ ui.active = {} /\ ui.serial = 0
               /\ ui.view = EmptyView /\ ui.w = Rest)
RetiredWriteFrame == last.step # "lateWrite" \/ ui = [last.before EXCEPT !.retiredWrite = FALSE]
SingleSend == last.step # "send" \/
              (last.before.w.phase = "idle" /\ ui.w.sent = 1 /\ ui.w.phase = "sending" /\ ui.w.arn = last.before.scope.group)
NativeFrame == /\ ui.sibling = "other"
               /\ (last.step # "server" \/
                   ui.target = (IF last.result = "applied" THEN "desired" ELSE last.before.target))
               /\ (last.step # "remove" \/ ui.target = "absent")
               /\ (last.step \in {"init","server","remove"} \/ ui.target = last.before.target)
Acknowledgement == last.step # "ack" \/
                   (/\ ~ui.w.verified /\ ui.w.sent = last.before.w.sent
                    /\ ui.w.phase = (IF last.result = "matching" THEN "checkingPath"
                                     ELSE IF last.result = "denied" THEN "refused" ELSE "uncertain"))
CompleteDetail == last.step # "readReply" \/ last.kind \notin last.before.active \/ last.kind.scope # last.before.scope \/
                  last.result \notin {"listenerFailed","groupsFailed","healthFailed","wrongArn"} \/ ui.view = EmptyView
BoundReadback == last.step # "path" \/
                 (~ui.w.verified /\ ui.w.phase = (IF last.result = "matching" THEN "checkingHealth" ELSE "uncertain"))
CheckedHealth == last.step # "health" \/
                 LET valid == last.result \in {"healthy","unhealthy","empty"}
                 IN ui.w.verified = valid /\ ui.w.phase = (IF valid THEN "verified" ELSE "uncertain")
                    /\ ui.w.health = (IF valid THEN last.result ELSE "none")
ObservedOnly == last.step # "recover" \/
                (IF last.kind \in {"exact","absent"}
                 THEN /\ ui.w.phase = "observed" /\ ~ui.w.verified /\ ui.w.seenPath = (IF last.kind = "absent" THEN "absent" ELSE last.before.target)
                      /\ ui.w.arn = last.before.w.arn /\ ui.w.sent = last.before.w.sent
                 ELSE ui = last.before)
=============================================================================
