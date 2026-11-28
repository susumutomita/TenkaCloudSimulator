--------------------- MODULE CloudWatchAlarmEvaluation ---------------------
EXTENDS Integers, Sequences, FiniteSets, TLC
(* Scenario-bounded normalized requests and atomic SQL outcomes, not a Go/SQL,
   IAM, decimal/float, clock, HTTP or scheduling refinement proof. *)
VARIABLES scenario, stat, comparison, raw, alarm, gen, retired, epoch,
          phase, pending, before, last, pc, peer
vars == <<scenario,stat,comparison,raw,alarm,gen,retired,epoch,
          phase,pending,before,last,pc,peer>>
None == "none"
NoValue == [has |-> FALSE, num |-> 0, den |-> 1]
Stats == {"Average","Sum","SampleCount","Minimum","Maximum"}
Comparisons == {"GreaterThanThreshold","GreaterThanOrEqualToThreshold",
                "LessThanThreshold","LessThanOrEqualToThreshold"}
Cases == {"weighted","equality","statistics","time","scope-unit","cancel","fail",
          "denied-put","denied-read","update","retire"}
PeerInitial == [raw |-> <<9>>, state |-> "ALARM", generation |-> 1]
Base == [stat |-> "Average", comparison |-> "GreaterThanThreshold", threshold |-> 4]
NoAlarm == [exists |-> FALSE,id |-> 0,config |-> Base,state |-> "INSUFFICIENT_DATA",evaluated |-> FALSE,reported |-> NoValue,count |-> 0,evalRaw |-> <<>>,evalConfig |-> Base,evalEpoch |-> 0]
Weighted == [stat |-> "Average", comparison |-> "LessThanThreshold", threshold |-> 4]
EqualConfig == [stat |-> stat, comparison |-> comparison,
                threshold |-> IF stat = "SampleCount" THEN 1 ELSE 0]
Point(v,at,scope,unit) == [value |-> v, at |-> at, scope |-> scope, unit |-> unit]
EmptyPoint == Point(0,0,"target","Count")
Request(op,p,c,allow,outcome) == [op |-> op, point |-> p, config |-> c,
                               allow |-> allow, outcome |-> outcome]
Configure(c) == Request("configure",EmptyPoint,c,TRUE,"Commit")
Put(v) == Request("put",Point(v,1,"target","Count"),Base,TRUE,"Commit")
PutAt(v,at,scope,unit) == Request("put",Point(v,at,scope,unit),Base,TRUE,"Commit")
Read == Request("read",EmptyPoint,Base,TRUE,"Commit")
Advance == Request("advance",EmptyPoint,Base,TRUE,"Commit")
Delete == Request("delete",EmptyPoint,Base,TRUE,"Commit")
Program == CASE scenario = "weighted" -> <<Configure(Weighted),Put(1),Put(1),Put(9),Read>>
 [] scenario = "equality" -> <<Configure(EqualConfig),Put(0),Read>>
 [] scenario = "statistics" -> <<Configure([stat |-> stat,comparison |-> comparison,threshold |-> 4]),Put(1),Put(9),Put(1),Read>>
 [] scenario = "time" -> <<Configure(Base),PutAt(9,-1,"target","Count"),PutAt(1,0,"target","Count"),PutAt(9,2,"target","Count"),Advance,Read>>
 [] scenario = "scope-unit" -> <<Configure(Base),PutAt(9,1,"other","Count"),PutAt(9,1,"target","Bytes"),Put(1),Read>>
 [] scenario = "cancel" -> <<Configure(Base),Request("put",Point(9,1,"target","Count"),Base,TRUE,"Cancel"),Put(9),Read>>
 [] scenario = "fail" -> <<Configure(Base),Request("put",Point(9,1,"target","Count"),Base,TRUE,"Fail"),Put(9),Read>>
 [] scenario = "denied-put" -> <<Configure(Base),Put(1),Request("put",Point(9,1,"target","Count"),Base,FALSE,"Commit"),Read>>
 [] scenario = "denied-read" -> <<Configure(Base),Put(9),Advance,Request("read",EmptyPoint,Base,FALSE,"Commit")>>
 [] scenario = "update" -> <<Configure(Base),Put(9),Configure([Base EXCEPT !.threshold = 100]),Read>>
 [] scenario = "retire" -> <<Configure(Base),Put(9),Delete,Configure(Base),Read>>
RECURSIVE Total(_), Unique(_)
Total(p) == IF Len(p) = 0 THEN 0 ELSE Head(p).value + Total(Tail(p))
Unique(p) == IF Len(p) = 0 THEN <<>> ELSE
 IF Head(p) \in {Tail(p)[i]: i \in DOMAIN Tail(p)} THEN Unique(Tail(p)) ELSE <<Head(p)>> \o Unique(Tail(p))
Minimum(p) == CHOOSE v \in {p[i].value: i \in DOMAIN p}: \A i \in DOMAIN p: v <= p[i].value
Maximum(p) == CHOOSE v \in {p[i].value: i \in DOMAIN p}: \A i \in DOMAIN p: v >= p[i].value
Selected(data,w) == SelectSeq(data,LAMBDA p: p.scope = "target" /\ p.unit = "Count" /\ p.at >= 2*w /\ p.at < 2*w+2)
Measure(c,p) == IF Len(p) = 0 THEN NoValue ELSE
 [has |-> TRUE, num |-> CASE c.stat \in {"Average","Sum"} -> Total(p)
         [] c.stat = "SampleCount" -> Len(p)
         [] c.stat = "Minimum" -> Minimum(p)
         [] OTHER -> Maximum(p),
  den |-> IF c.stat = "Average" THEN Len(p) ELSE 1]
Compare(c,m) == CASE c.comparison = "GreaterThanThreshold" -> m.num > c.threshold*m.den
 [] c.comparison = "GreaterThanOrEqualToThreshold" -> m.num >= c.threshold*m.den
 [] c.comparison = "LessThanThreshold" -> m.num < c.threshold*m.den
 [] OTHER -> m.num <= c.threshold*m.den
NewAlarm(c,id) == [exists |-> TRUE,id |-> id, config |-> c, state |-> "INSUFFICIENT_DATA",
 evaluated |-> FALSE, reported |-> NoValue, count |-> 0,
 evalRaw |-> <<>>, evalConfig |-> c, evalEpoch |-> epoch]
Evaluate(a,data,w) ==
 LET points == Selected(data,w)
     measurement == Measure(a.config,points)
 IN [a EXCEPT !.state = IF ~measurement.has THEN "INSUFFICIENT_DATA" ELSE IF Compare(a.config,measurement) THEN "ALARM" ELSE "OK",
     !.evaluated = TRUE, !.reported = measurement, !.count = Len(points),
     !.evalRaw = data, !.evalConfig = a.config, !.evalEpoch = w]
Snapshot == [raw |-> raw, alarm |-> alarm, gen |-> gen, retired |-> retired, peer |-> peer]
InitialSnapshot == [raw |-> <<>>,alarm |-> NoAlarm,gen |-> 0,retired |-> {},peer |-> PeerInitial]
Prepare(req) ==
 LET nextRaw == IF req.op = "put" THEN Append(raw,req.point) ELSE raw
     nextGen == IF req.op = "configure" /\ ~alarm.exists THEN gen+1 ELSE gen
     baseAlarm == IF req.op = "configure" THEN
         IF ~alarm.exists THEN NewAlarm(req.config,nextGen) ELSE [alarm EXCEPT !.config = req.config]
       ELSE IF req.op = "delete" THEN NoAlarm ELSE alarm
     nextAlarm == IF req.op \in {"read","put"} /\ baseAlarm.exists THEN Evaluate(baseAlarm,nextRaw,epoch) ELSE baseAlarm
     nextRetired == IF req.op = "delete" /\ alarm.exists THEN retired \cup {alarm.id} ELSE retired
 IN [raw |-> nextRaw, alarm |-> nextAlarm, gen |-> nextGen, retired |-> nextRetired, peer |-> peer]
Init == /\ scenario \in Cases /\ stat \in Stats /\ comparison \in Comparisons
 /\ (scenario \notin {"equality","statistics"} => stat = "Average" /\ comparison = "GreaterThanThreshold")
 /\ raw = <<>> /\ alarm = NoAlarm /\ gen = 0 /\ retired = {} /\ epoch = 0
 /\ phase = "idle" /\ pc = 1 /\ peer = PeerInitial
 /\ pending = None /\ before = InitialSnapshot
 /\ last = [kind |-> "init",allow |-> TRUE,outcome |-> "none",before |-> InitialSnapshot,expected |-> InitialSnapshot]
Begin == LET req == Program[pc] IN
 /\ phase = "idle" /\ pc <= Len(Program) /\ req.op # "advance"
 /\ pending' = [request |-> req,candidate |-> Prepare(req)] /\ before' = Snapshot
 /\ phase' = "pending" /\ raw' = raw
 /\ UNCHANGED <<alarm,gen,retired,epoch,peer,scenario,stat,comparison,last,pc>>
MayPublish == pending.request.allow /\ pending.request.outcome = "Commit"
Finish == LET req == pending.request
              chosen == IF MayPublish THEN pending.candidate ELSE before
 IN /\ phase = "pending"
 /\ raw' = chosen.raw /\ alarm' = chosen.alarm /\ gen' = chosen.gen
 /\ retired' = chosen.retired /\ peer' = chosen.peer
 /\ last' = [kind |-> req.op,allow |-> req.allow,
     outcome |-> IF req.allow THEN req.outcome ELSE "Denied",before |-> before,expected |-> pending.candidate]
 /\ phase' = "idle" /\ pc' = pc+1 /\ pending' = None
 /\ UNCHANGED <<epoch,scenario,stat,comparison,before>>
AdvanceClock == /\ phase = "idle" /\ pc <= Len(Program) /\ Program[pc].op = "advance"
 /\ epoch' = 1 /\ pc' = pc+1
 /\ last' = [kind |-> "advance",allow |-> TRUE,outcome |-> "none",before |-> Snapshot,expected |-> Snapshot]
 /\ UNCHANGED <<raw,alarm,gen,retired,peer,phase,pending,before,scenario,stat,comparison>>
Next == Begin \/ Finish \/ AdvanceClock
(* Independent specification selector and rational calculation, not the
   editable implementation Selected/Measure/Compare operators above. *)
TruthPoints(a) == SelectSeq(a.evalRaw,LAMBDA p: p.scope = "target" /\ p.unit = "Count" /\ p.at >= 2*a.evalEpoch /\ p.at < 2*a.evalEpoch+2)
TruthMeasure(a) == LET p == TruthPoints(a) c == a.evalConfig IN
 IF Len(p) = 0 THEN NoValue ELSE
 [has |-> TRUE, num |-> IF c.stat = "Average" \/ c.stat = "Sum" THEN Total(p)
          ELSE IF c.stat = "SampleCount" THEN Len(p) ELSE IF c.stat = "Minimum" THEN Minimum(p) ELSE Maximum(p),
  den |-> IF c.stat = "Average" THEN Len(p) ELSE 1]
TruthState(a) == LET m == TruthMeasure(a) c == a.evalConfig IN
 IF ~m.has THEN "INSUFFICIENT_DATA" ELSE
 IF CASE c.comparison = "GreaterThanThreshold" -> m.num > c.threshold*m.den
      [] c.comparison = "GreaterThanOrEqualToThreshold" -> m.num >= c.threshold*m.den
      [] c.comparison = "LessThanThreshold" -> m.num < c.threshold*m.den
      [] OTHER -> m.num <= c.threshold*m.den
 THEN "ALARM" ELSE "OK"
TypeOK == /\ phase \in {"idle","pending"} /\ gen \in 0..2 /\ retired \subseteq {1,2}
 /\ epoch \in {0,1} /\ Len(raw) <= 3 /\ pc \in 1..(Len(Program)+1)
 /\ IF ~alarm.exists THEN TRUE ELSE alarm.id \in {1,2} /\ alarm.state \in {"OK","ALARM","INSUFFICIENT_DATA"}
PeerFrame == peer = PeerInitial
NoStagedPublish == phase = "pending" => Snapshot = before
RefusedFrame == (last.outcome \in {"Cancel","Fail","Denied"}) => Snapshot = last.before
AcknowledgedAtomic == (last.outcome = "Commit" /\ last.kind # "advance") => Snapshot = last.expected
ActualSampleState == IF ~alarm.exists THEN TRUE ELSE alarm.evaluated => alarm.state = TruthState(alarm)
MeasuredValueExact == IF ~alarm.exists THEN TRUE ELSE alarm.evaluated =>
 alarm.reported.has = TruthMeasure(alarm).has /\
 (alarm.reported.has => alarm.reported.num * TruthMeasure(alarm).den = TruthMeasure(alarm).num * alarm.reported.den) /\
 alarm.count = Len(TruthPoints(alarm))
InitialState == (last.kind = "configure" /\ last.outcome = "Commit" /\ ~last.before.alarm.exists) => alarm.state = "INSUFFICIENT_DATA"
UpdatePreservesState == (last.kind = "configure" /\ last.outcome = "Commit" /\ last.before.alarm.exists) =>
 <<alarm.state,alarm.evaluated,alarm.reported,alarm.evalRaw,alarm.evalConfig,alarm.evalEpoch>> =
 <<last.before.alarm.state,last.before.alarm.evaluated,last.before.alarm.reported,last.before.alarm.evalRaw,last.before.alarm.evalConfig,last.before.alarm.evalEpoch>>
DeleteKeepsSamples == (last.kind = "delete" /\ last.outcome = "Commit") => raw = last.before.raw
FreshIdentity == IF ~alarm.exists THEN TRUE ELSE alarm.id \notin retired
=============================================================================
