-------------------- MODULE CloudWatchMetricTransaction --------------------
EXTENDS Integers, Sequences, FiniteSets, TLC
CONSTANT MaxSteps
VARIABLES rows, oracle, phase, owner, staged, event, result, expected, step
vars == <<rows, oracle, phase, owner, staged, event, result, expected, step>>
Scopes == {"a", "b"}
Identities == {"x", "y"}
Units == {"Count", "Seconds"}
Point(s, i, u, t, v) == [scope |-> s, identity |-> i, unit |-> u, at |-> t, value |-> v]
Seed == <<Point("a","x","Count",0,3), Point("b","x","Count",0,1),
          Point("a","x","Count",1,1), Point("a","x","Count",1,1),
          Point("a","x","Count",2,3), Point("a","x","Count",3,3)>>
Prune(data, s) == SelectSeq(data, LAMBDA p: ~(p.scope = s /\ p.at = 0)) (* ScopedRetention *)
Peer(data,s) == SelectSeq(data,LAMBDA p: p.scope # s)
Batches(s,i,u) == {<<Point(s,i,u,1,1)>>,
                   <<Point(s,i,u,1,1),Point(s,i,u,2,3)>>,
                   <<Point(s,i,u,1,1),Point(s,i,u,1,1)>>}
Fits(data,s,batch) == Len(Prune(data,s)) + Len(batch) <= 7
RECURSIVE Total(_), Low(_), High(_)
Total(data) == IF Len(data)=0 THEN 0 ELSE Head(data).value + Total(Tail(data))
Low(data) == IF Len(data)=0 THEN 0 ELSE IF Len(data)=1 THEN Head(data).value
             ELSE IF Head(data).value < Low(Tail(data)) THEN Head(data).value ELSE Low(Tail(data))
High(data) == IF Len(data)=0 THEN 0 ELSE IF Len(data)=1 THEN Head(data).value
              ELSE IF Head(data).value > High(Tail(data)) THEN Head(data).value ELSE High(Tail(data))
Select(data,s,i,u) == SelectSeq(data,LAMBDA p:
    p.scope=s /\ p.identity=i /\ (u="all" \/ p.unit=u) /\ p.at>=1 /\ p.at<3) (* ExclusiveEnd *)
Bucket(p,period) == 1 + ((p.at-1) \div period)*period
Group(data,k,period) == SelectSeq(data,LAMBDA p: Bucket(p,period)=k.bucket /\ p.unit=k.unit)
Keys(data,period) == {[bucket |-> Bucket(data[n],period),unit |-> data[n].unit] : n \in 1..Len(data)}
(* An average is an exact rational witness. Floating point arithmetic, time
   parsing, dimension canonicalization and SQL are outside this finite model. *)
UnweightedNum(data) == LET a == SelectSeq(data,LAMBDA p:p.at=1)
                           b == SelectSeq(data,LAMBDA p:p.at=2)
                      IN IF Len(a)>0 /\ Len(b)>0
                         THEN Total(a)*Len(b)+Total(b)*Len(a) ELSE Total(data)
UnweightedDen(data) == LET a == SelectSeq(data,LAMBDA p:p.at=1)
                           b == SelectSeq(data,LAMBDA p:p.at=2)
                      IN IF Len(a)>0 /\ Len(b)>0 THEN 2*Len(a)*Len(b) ELSE Len(data)
AvgNum(data) == Total(data) (* WeightedNumerator *)
AvgDen(data) == Len(data) (* WeightedDenominator *)
Groups(data,period) == {[bucket |-> k.bucket,unit |-> k.unit,
    count |-> Len(Group(data,k,period)), sum |-> Total(Group(data,k,period)),
    minimum |-> Low(Group(data,k,period)), maximum |-> High(Group(data,k,period)),
    numerator |-> AvgNum(Group(data,k,period)), denominator |-> AvgDen(Group(data,k,period))] : k \in Keys(data,period)}
(* Independent read oracle retains its own raw-data selection and aggregates;
   it does not call Select, Group, Keys, AvgNum or AvgDen from the subject. *)
Expected(data,s,i,u,period) ==
 LET selected == SelectSeq(data,LAMBDA p:p.scope=s /\ p.identity=i /\ (u="all" \/ p.unit=u) /\ p.at\in{1,2})
     keys == {[bucket |-> 1+((selected[n].at-1) \div period)*period,unit |-> selected[n].unit] : n\in1..Len(selected)}
 IN {LET group == SelectSeq(selected,LAMBDA p:1+((p.at-1) \div period)*period=k.bucket /\ p.unit=k.unit)
     IN [bucket |-> k.bucket,unit |-> k.unit,count |-> Len(group),sum |-> Total(group),
         minimum |-> Low(group),maximum |-> High(group),numerator |-> Total(group),denominator |-> Len(group)] : k\in keys}
Init == /\ rows=Seed /\ oracle=Seed /\ phase="idle" /\ owner="a" /\ staged= <<>>
        /\ event=[kind |-> "Init",scope |-> "a",before |-> Seed,batch |-> <<>>]
        /\ result={} /\ expected={} /\ step=0
Mark(kind,s,batch) == /\ event'=[kind |-> kind,scope |-> s,before |-> rows,batch |-> batch]
                      /\ step'=step+1
Begin(s,batch) == /\ phase="idle" /\ Fits(rows,s,batch)
                  /\ phase'="staged" /\ owner'=s /\ staged'=batch
                  /\ rows'=rows (* NoEarlyPublication *)
                  /\ UNCHANGED <<oracle,result,expected>> /\ Mark("Begin",s,batch)
Commit == /\ phase="staged"
          /\ rows'=Prune(rows,owner) \o staged (* WholeBatch *)
          /\ oracle'=SelectSeq(oracle,LAMBDA p:~(p.scope=owner /\ p.at=0)) \o staged
          /\ phase'="idle" /\ staged'= <<>>
          /\ UNCHANGED <<owner,result,expected>> /\ Mark("Commit",owner,staged)
Rollback(kind) == /\ phase="staged" /\ kind\in{"Fail","Cancel"}
                  /\ rows'=rows (* RollbackRetainsOld *) /\ staged'= <<>> /\ phase'="idle"
                  /\ UNCHANGED <<owner,oracle,result,expected>> /\ Mark(kind,owner,staged)
Reject(kind,s,batch) == /\ phase="idle" /\ kind\in{"Denied","Invalid","Quota"}
                       /\ (kind="Quota" => ~Fits(rows,s,batch))
                       /\ rows'=rows (* RejectionRetainsOld *)
                       /\ UNCHANGED <<oracle,phase,owner,staged,result,expected>> /\ Mark(kind,s,batch)
Read(s,i,u,period) == /\ result'=Groups(Select(rows,s,i,u),period) (* ReadCommittedOnly *)
                     /\ expected'=Expected(oracle,s,i,u,period)
                     /\ rows'=rows (* ReadDoesNotWrite *)
                     /\ UNCHANGED <<oracle,phase,owner,staged>> /\ Mark("Read",s,<<>>)
Restart == /\ rows'=rows (* RestartRetainsRows *)
           /\ phase'="idle" /\ staged'= <<>>
           /\ UNCHANGED <<oracle,owner,result,expected>> /\ Mark("Restart",owner,<<>>)
Next == /\ step<MaxSteps
        /\ (Commit \/ Restart \/ (\E kind\in{"Fail","Cancel"}:Rollback(kind))
         \/ (\E s\inScopes,i\inIdentities,u\inUnits: \E batch\inBatches(s,i,u):
               Begin(s,batch) \/ (\E kind\in{"Denied","Invalid","Quota"}:Reject(kind,s,batch)))
         \/ (\E s\inScopes,i\inIdentities,u\inUnits\cup{"all"},period\in{1,2}:Read(s,i,u,period)))
TypeOK == /\ phase\in{"idle","staged"} /\ owner\inScopes /\ step\in0..MaxSteps
          /\ rows\inSeq([scope:Scopes,identity:Identities,unit:Units,at:0..3,value:{1,3}])
          /\ staged\inSeq([scope:Scopes,identity:Identities,unit:Units,at:1..2,value:{1,3}])
StagedInvisible == phase="staged" => rows=oracle
AtomicAccepted == event.kind="Commit" => rows=oracle
RepeatedObservationsCount == event.kind="Commit" => Len(rows)=Len(SelectSeq(event.before,LAMBDA p:~(p.scope=event.scope /\ p.at=0)))+Len(event.batch)
RejectedUnchanged == event.kind\in{"Fail","Cancel","Denied","Invalid","Quota"} => rows=event.before
PeerPreserved == event.kind\in{"Begin","Commit","Fail","Cancel","Denied","Invalid","Quota"} => Peer(rows,event.scope)=Peer(event.before,event.scope)
ReadAndRestartPreserve == event.kind\in{"Read","Restart"} => rows=event.before
BoundedRows == Len(rows)<=7
ReadAggregates == /\ Cardinality(result)=Cardinality(expected)
 /\ \A r\inresult: \E e\inexpected:
     /\ r.bucket=e.bucket /\ r.unit=e.unit /\ r.count=e.count /\ r.sum=e.sum
     /\ r.minimum=e.minimum /\ r.maximum=e.maximum
     /\ r.denominator>0 /\ r.numerator*e.denominator=e.numerator*r.denominator
=============================================================================
