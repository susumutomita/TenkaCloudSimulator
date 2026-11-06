---------------------- MODULE CloudWatchMetricsRawLedger ----------------------
EXTENDS Integers, Sequences, FiniteSets
CONSTANT MaxRequests, MaxRows, MaxSeries, MaxBytes
\* Original bounded model of the independently authored metrics leaf. Not a
\* parser, IAM, floating-point, cryptographic, SQLite or Go refinement proof.
Regions == 1..2
Keys == 1..8
Units == {"None", "Count"}
R(k) == IF k<=4 THEN 1 ELSE 2
N(k) == IF k \in {1,2,5,6} THEN 1 ELSE 2
D(k) == IF k \in {1,3,5,7} THEN 1 ELSE 2
Key(r,n,d) == (r-1)*4+(n-1)*2+d
Point(k,u,s,v) == [key |-> k, unit |-> u, stamp |-> s, value |-> v]
PointType == [key:Keys, unit:Units, stamp:-2..2, value:{-1,0,2}]
\* Value-equivalent standard selection and index folds avoid repeated Tail
\* copies. They preserve order, duplicates and all finite input values.
Keep(ps, allowed) == SelectSeq(ps,LAMBDA p:p \in allowed)
RECURSIVE SumAt(_, _), WeightAt(_, _)
SumAt(ps,i) == IF i>Len(ps) THEN 0 ELSE ps[i].value+SumAt(ps,i+1)
WeightAt(ps,i) == IF i>Len(ps) THEN 0 ELSE
 (IF ps[i].unit="Count" THEN 2 ELSE 1)+WeightAt(ps,i+1)
Sum(ps) == SumAt(ps,1)
Weight(ps) == WeightAt(ps,1)
AsSet(ps) == {ps[i] : i \in 1..Len(ps)}
\* Clock 0 is T; 1/2 are T+1h/T+2h. -1 is exactly T-15d,
\* -2 is older, 0 is T-14d, and 1/2 are T/T+1minute.
Live(p,t) == p.stamp>=0 \/ (p.stamp=-1 /\ t=0)
Active(age,t) == age="fresh" \/ (age="boundary" /\ t=0)
\* Literal oracle tables intentionally do not invoke Live or Active.
VisibleStamps(t) == IF t=0 THEN {-1,0,1,2} ELSE {0,1,2}
VisibleAges(t) == IF t=0 THEN {"fresh","boundary"} ELSE {"fresh"}
EmptyCatalog == [k \in Keys |-> "absent"]
Catalog(ps,age) == [k \in Keys |-> IF \E i \in 1..Len(ps):ps[i].key=k
                                               THEN age ELSE "absent"]
Disk(ps,age,rev) == [rows |-> ps, catalog |-> Catalog(ps,age),
 initialized |-> {R(ps[i].key):i \in 1..Len(ps)}, revision |-> [r \in Regions |-> rev]]
EmptyDisk == Disk(<<>>,"absent",0)
Present(c) == {k \in Keys:c[k]#"absent"}
Bytes(ps,c) == Weight(ps)+Cardinality(Present(c))
Fits(ps,c) == Len(ps)<=MaxRows /\ Cardinality(Present(c))<=MaxSeries
             /\ Bytes(ps,c)<=MaxBytes
Foreign(ps,r,n) == Keep(ps,{p \in AsSet(ps):R(p.key)#r \/ N(p.key)#n})
Purge(ps,r,n,t) == Keep(ps,{p \in AsSet(ps):R(p.key)#r \/ N(p.key)#n \/ Live(p,t)})
PostCatalog(b,ps,r,n,t) == [k \in Keys |->
 IF R(k)=r /\ N(k)=n /\ ~Active(b.catalog[k],t)
    /\ ~ (\E i \in 1..Len(ps):ps[i].key=k)
 THEN "absent" ELSE b.catalog[k]]
Batch(k,style) == CASE style=1 -> <<Point(k,"None",1,0)>>
 [] style=2 -> <<Point(k,"None",1,2)>>
 [] style=3 -> <<Point(k,"None",1,2),Point(k,"None",1,-1)>>
 [] style=4 -> <<Point(k,"None",1,2),Point(k,"Count",1,2)>>
 [] OTHER -> <<Point(k,"Count",1,2)>>
\* Submitted-batch oracle is a separate literal table, not Batch's output.
ExpectedBatch(k,style) == CASE
 style=1 -> <<[key |-> k,unit |-> "None",stamp |-> 1,value |-> 0]>>
 [] style=2 -> <<[key |-> k,unit |-> "None",stamp |-> 1,value |-> 2]>>
 [] style=3 -> <<[key |-> k,unit |-> "None",stamp |-> 1,value |-> 2],
                [key |-> k,unit |-> "None",stamp |-> 1,value |-> -1]>>
 [] style=4 -> <<[key |-> k,unit |-> "None",stamp |-> 1,value |-> 2],
                [key |-> k,unit |-> "Count",stamp |-> 1,value |-> 2]>>
 [] OTHER -> <<[key |-> k,unit |-> "Count",stamp |-> 1,value |-> 2]>>
Candidate(b,r,n,d,style,t) ==
 LET collected == Purge(b.rows,r,n,t)
     ps == collected \o Batch(Key(r,n,d),style)
     c == [PostCatalog(b,collected,r,n,t) EXCEPT ![Key(r,n,d)]="fresh"]
 IN [rows |-> ps,catalog |-> c,initialized |-> b.initialized \cup {r},
     revision |-> [b.revision EXCEPT ![r]=@+1]]
\* Independent sequence-index oracle: retains duplicates and their order.
ExpectedRows(b,r,n,d,style,t) ==
 LET indexes == {i \in 1..Len(b.rows):R(b.rows[i].key)#r \/ N(b.rows[i].key)#n
                                      \/ b.rows[i].stamp \in VisibleStamps(t)}
     selected == SelectSeq(b.rows,LAMBDA p:p \in {b.rows[i]:i \in indexes})
 IN selected \o ExpectedBatch(Key(r,n,d),style)
ExpectedCatalog(b,r,n,d,t) == [k \in Keys |->
 IF k=Key(r,n,d) THEN "fresh" ELSE
 IF R(k)=r /\ N(k)=n /\ b.catalog[k] \notin VisibleAges(t)
 /\ ~ (\E i \in 1..Len(b.rows):b.rows[i].key=k /\ b.rows[i].stamp \in VisibleStamps(t))
 THEN "absent" ELSE b.catalog[k]]
Token(p,r,n,d,rev,t,after) == [present |-> p, region |-> r, ns |-> n, dim |-> d,
 revision |-> rev, at |-> t, after |-> after]
NoToken == Token(FALSE,1,0,0,0,0,1)
Options(k,r,n,d) == R(k)=r /\ (n=0 \/ N(k)=n) /\ (d=0 \/ D(k)=d)
ListKeys(b,r,n,d,t) == {k \in Present(b.catalog):Options(k,r,n,d)
 /\ Active(b.catalog[k],t) /\ (\E i \in 1..Len(b.rows):b.rows[i].key=k /\ Live(b.rows[i],t))}
\* Independent literal membership/order tables; no R/N/D/Options/Page calls.
ExpectedRegion(r) == CASE r=1 -> {1,2,3,4} [] OTHER -> {5,6,7,8}
ExpectedNamespace(n) == CASE n=0 -> {1,2,3,4,5,6,7,8}
 [] n=1 -> {1,2,5,6} [] OTHER -> {3,4,7,8}
ExpectedDimension(d) == CASE d=0 -> {1,2,3,4,5,6,7,8}
 [] d=1 -> {1,3,5,7} [] OTHER -> {2,4,6,8}
ExpectedOptions(k,r,n,d) == k \in ExpectedRegion(r)
 /\ k \in ExpectedNamespace(n) /\ k \in ExpectedDimension(d)
ExpectedAfter(k) == CASE k=1 -> {2,3,4,5,6,7,8} [] k=2 -> {3,4,5,6,7,8}
 [] k=3 -> {4,5,6,7,8} [] k=4 -> {5,6,7,8} [] k=5 -> {6,7,8}
 [] k=6 -> {7,8} [] k=7 -> {8} [] OTHER -> {}
ExpectedPage(s) == IF 1 \in s THEN <<1>> ELSE IF 2 \in s THEN <<2>>
 ELSE IF 3 \in s THEN <<3>> ELSE IF 4 \in s THEN <<4>>
 ELSE IF 5 \in s THEN <<5>> ELSE IF 6 \in s THEN <<6>>
 ELSE IF 7 \in s THEN <<7>> ELSE IF 8 \in s THEN <<8>> ELSE <<>>
ExpectedList(b,r,n,d,t) == {k \in Keys:ExpectedOptions(k,r,n,d)
 /\ b.catalog[k] \in VisibleAges(t)
 /\ (\E i \in 1..Len(b.rows):b.rows[i].key=k /\ b.rows[i].stamp \in VisibleStamps(t))}
Minimum(s) == CHOOSE k \in s: \A j \in s:k<=j
Page(s) == IF s={} THEN <<>> ELSE <<Minimum(s)>>
RangeOK(p,range) == range=0 \/ p.stamp \in {1,2}
Samples(b,k,u,range,t) == Keep(b.rows,{p \in AsSet(b.rows):p.key=k
 /\ (u="All" \/ p.unit=u) /\ RangeOK(p,range) /\ Live(p,t)})
Low(ps) == CHOOSE v \in {ps[i].value:i \in 1..Len(ps)}:
                  \A i \in 1..Len(ps):v<=ps[i].value
High(ps) == CHOOSE v \in {ps[i].value:i \in 1..Len(ps)}:
                  \A i \in 1..Len(ps):v>=ps[i].value
\* Statistics are partitioned by recorded unit and abstract timestamp bucket.
\* Filter the product first: Low/High are never evaluated on an empty group.
Groups(ps) == {<<p.unit,p.stamp>>:p \in AsSet(ps)}
Aggregate(ps) == {LET group == Keep(ps,{p \in AsSet(ps):p.unit=g[1] /\ p.stamp=g[2]})
 IN [unit |-> g[1],bucket |-> g[2],count |-> Len(group),sum |-> Sum(group),
 minimum |-> Low(group),maximum |-> High(group),average |-> <<Sum(group),Len(group)>>]
 :g \in Groups(ps)}
OracleStats(b,k,u,range,t) ==
 LET ix == {i \in 1..Len(b.rows):b.rows[i].key=k
             /\ (u="All" \/ b.rows[i].unit=u)
             /\ b.rows[i].stamp \in VisibleStamps(t)
             /\ (range=0 \/ b.rows[i].stamp \in {1,2})}
     groups == {<<b.rows[i].unit,b.rows[i].stamp>>:i \in ix}
 IN {LET ids == {i \in ix:<<b.rows[i].unit,b.rows[i].stamp>>=g}
         ones == Cardinality({i \in ids:b.rows[i].value=-1})
         twos == Cardinality({i \in ids:b.rows[i].value=2})
         zeros == Cardinality({i \in ids:b.rows[i].value=0})
         total == 2*twos-ones
         count == ones+twos+zeros
     IN [unit |-> g[1],bucket |-> g[2],count |-> count,sum |-> total,
         minimum |-> IF ones>0 THEN -1 ELSE IF zeros>0 THEN 0 ELSE 2,
         maximum |-> IF twos>0 THEN 2 ELSE IF zeros>0 THEN 0 ELSE -1,
         average |-> <<total,count>>] :g \in groups}
Reply(status,ls,stats,tok) == [status |-> status, list |-> ls,stats |-> stats,token |-> tok]
EmptyReply(s) == Reply(s,<<>>,{},NoToken)
Request(op,r,n,d,style,u,range,kind,resume,mac,t) ==
 [op |-> op,r |-> r,n |-> n,d |-> d,style |-> style,unit |-> u,range |-> range,
 kind |-> kind,resume |-> resume,mac |-> mac,at |-> t]
DefaultRequest == Request("Init",1,1,1,1,"All",0,"Success",FALSE,TRUE,0)
\* Each seed is a native-reachable Put/clock/List history, documented outside.
SeedRows == {
 <<>>,
 <<Point(1,"None",1,2)>>,
 <<Point(1,"None",1,2),Point(1,"Count",1,-1)>>,
 <<Point(1,"None",1,2),Point(1,"None",1,-1)>>,
 <<Point(1,"None",1,2),Point(1,"None",2,0)>>,
 <<Point(1,"None",1,2),Point(2,"None",1,-1)>>,
 <<Point(1,"None",-2,2),Point(3,"None",-2,-1)>>,
 <<Point(1,"None",-2,2),Point(6,"None",1,-1)>>,
 <<Point(1,"None",-2,2),Point(3,"None",1,-1)>>,
 <<Point(1,"None",-1,2),Point(1,"None",1,-1)>>,
 <<Point(1,"Count",1,2),Point(1,"Count",1,2)>>,
 <<Point(5,"None",-2,2),Point(5,"None",-2,2),Point(5,"None",-2,2)>>}
BoundarySeed == [Disk(<<Point(1,"None",1,2),Point(2,"None",0,-1)>>,"fresh",0)
                 EXCEPT !.catalog[2]="boundary"]
StaleSeed == Disk(<<Point(1,"None",1,2),Point(2,"None",1,-1),
                    Point(1,"None",1,0)>>,"fresh",1)
VARIABLES disk, before, clock, requests, last, reply, saved, captured
vars == <<disk,before,clock,requests,last,reply,saved,captured>>
Init ==
 /\ ( (\E ps \in SeedRows:
      disk=Disk(ps,IF \A i \in 1..Len(ps):ps[i].stamp=-2 THEN "old" ELSE "fresh",0))
       \/ disk=BoundarySeed \/ disk=StaleSeed)
 /\ before=disk /\ clock=0 /\ requests=0 /\ last=DefaultRequest
 /\ reply=EmptyReply("Initial")
 /\ saved=IF disk=StaleSeed THEN Token(TRUE,1,0,0,0,0,1) ELSE NoToken
 /\ captured=NoToken
Record(q) == /\ before'=disk /\ last'=q /\ requests'=requests+1
             /\ captured'=saved /\ UNCHANGED clock
Put(q) ==
 /\ requests<MaxRequests
 /\ LET c == Candidate(disk,q.r,q.n,q.d,q.style,clock)
        permitted == q.kind#"Denied" \* CONTROL SkipPutAuthorization
        valid == q.kind#"Invalid"
        fits == Fits(c.rows,c.catalog) \* CONTROL OmitPhysicalRowCap / OmitSeriesCap / OmitByteCap
        commit == permitted /\ valid /\ q.kind#"Fault" /\ fits
    IN /\ disk'=IF commit THEN c ELSE disk \* CONTROL CommitFailedCandidate / PublishWithoutCommit
       /\ reply'=EmptyReply(IF ~permitted THEN "Denied" ELSE IF ~valid THEN "Invalid"
              ELSE IF q.kind="Fault" THEN "StorageError" ELSE IF ~fits THEN "Quota" ELSE "Accepted")
 /\ UNCHANGED saved /\ Record(q)
Get(q) ==
 /\ requests<MaxRequests
 /\ LET ok == q.kind="Success"
        ps == Samples(disk,Key(q.r,q.n,q.d),q.unit,q.range,clock)
    IN reply'=IF ok THEN Reply("ReadOK",<<>>,Aggregate(ps),NoToken) ELSE EmptyReply(q.kind)
 /\ disk'=disk \* CONTROL ReadDurableWrite
 /\ UNCHANGED saved /\ Record(q)
List(q) ==
 /\ requests<MaxRequests
 /\ LET cursorOK == saved.present /\ q.mac /\ saved.region=q.r
             /\ saved.ns=q.n /\ saved.dim=q.d
             /\ saved.revision=disk.revision[q.r] \* CONTROL AcceptStaleCursor
             /\ saved.at<=clock /\ clock-saved.at<=1
        valid == ~q.resume \/ cursorOK
        ok == q.kind="Success" /\ valid
        at == IF q.resume THEN saved.at ELSE clock \* CONTROL IgnoreCapturedAsOf
        matches == ListKeys(disk,q.r,q.n,q.d,at)
        remaining == {k \in matches:~q.resume \/ k>saved.after}
        positionOK == ~q.resume \/ saved.after \in matches
        good == ok /\ positionOK
        first == Page(remaining)
        tok == IF good /\ Cardinality(remaining)>1
               THEN Token(TRUE,q.r,q.n,q.d,disk.revision[q.r],at,Head(first))
               ELSE NoToken
    IN /\ reply'=IF good THEN Reply("ReadOK",first,{},tok)
                  ELSE EmptyReply(IF q.kind#"Success" THEN q.kind ELSE "InvalidCursor")
       /\ saved'=IF tok.present THEN tok ELSE saved
 /\ disk'=disk /\ Record(q)
Tick == /\ clock<2 /\ clock'=clock+1
        /\ UNCHANGED <<disk,before,requests,last,reply,saved,captured>>
\* Every request action below starts with requests<MaxRequests. At exactly
\* the terminal count, only Tick/stutter can occur; avoid constructing their
\* disabled request records. The full successor tuples are unchanged.
Next == IF requests=MaxRequests THEN Tick \/ UNCHANGED vars ELSE Tick
 \/ (\E r \in Regions,n \in 1..2,d \in 1..2,style \in 1..5,kind \in {"Success","Fault"}:
       Put(Request("Put",r,n,d,style,"All",0,kind,FALSE,TRUE,clock)))
 \/ (\E r \in Regions,kind \in {"Denied","Invalid"}:
       Put(Request("Put",r,1,1,1,"All",0,kind,FALSE,TRUE,clock)))
 \/ (\E r \in Regions,n \in 1..2,d \in 1..2,u \in Units \cup {"All"},range \in 0..1:
       Get(Request("Get",r,n,d,1,u,range,"Success",FALSE,TRUE,clock)))
 \/ (\E r \in Regions,kind \in {"Denied","Invalid","Fault"}:
       Get(Request("Get",r,1,1,1,"All",0,kind,FALSE,TRUE,clock)))
 \/ (\E r \in Regions,n \in 0..2,d \in 0..2,resume \in BOOLEAN,mac \in BOOLEAN:
       (resume \/ mac) /\ List(Request("List",r,n,d,1,"All",0,"Success",resume,mac,clock)))
 \/ (\E r \in Regions,kind \in {"Denied","Invalid","Fault"}:
       List(Request("List",r,0,0,1,"All",0,kind,FALSE,TRUE,clock)))

Spec == Init /\ [][Next]_vars
TypeOK == /\ disk.rows \in Seq(PointType) /\ Len(disk.rows)<=7
 /\ disk.catalog \in [Keys->{"absent","old","boundary","fresh"}]
 /\ disk.initialized \subseteq Regions /\ disk.revision \in [Regions->0..(MaxRequests+1)]
 /\ clock \in 0..2 /\ requests \in 0..MaxRequests
 /\ reply.status \in {"Initial","Accepted","Denied","Invalid","StorageError","Quota",
                       "ReadOK","Fault","InvalidCursor"}
NoOrphans == \A i \in 1..Len(disk.rows):
 disk.catalog[disk.rows[i].key]#"absent" /\ R(disk.rows[i].key) \in disk.initialized
DeniedNoDisclosure == last.kind="Denied" =>
 reply.status="Denied" /\ reply.list = <<>> /\ reply.stats={} /\ ~reply.token.present
DeniedFrame == last.kind="Denied" => disk=before
InvalidFrame == last.kind="Invalid" => disk=before
ReadDurableFrame == last.op \in {"Get","List"} => disk=before
FailedPutFrame == last.op="Put" /\ reply.status#"Accepted" => disk=before
PutRowsExact == last.op="Put" /\ reply.status="Accepted" =>
 disk.rows=ExpectedRows(before,last.r,last.n,last.d,last.style,last.at)
PutCatalogExact == last.op="Put" /\ reply.status="Accepted" =>
 disk.catalog=ExpectedCatalog(before,last.r,last.n,last.d,last.at)
PhysicalRowBudget == reply.status="Accepted" => Len(disk.rows)<=MaxRows
SeriesBudget == reply.status="Accepted" => Cardinality(Present(disk.catalog))<=MaxSeries
PhysicalByteBudget == reply.status="Accepted" => Bytes(disk.rows,disk.catalog)<=MaxBytes
PeerFrame == last.op="Put" =>
 /\ Foreign(disk.rows,last.r,last.n)=Foreign(before.rows,last.r,last.n)
 /\ \A k \in Keys:R(k)#last.r \/ N(k)#last.n => disk.catalog[k]=before.catalog[k]
RevisionExact == last.op#"Init" => disk.revision=
 IF last.op="Put" /\ reply.status="Accepted"
 THEN [before.revision EXCEPT ![last.r]=@+1] ELSE before.revision
RegionInitializationExact == last.op#"Init" => disk.initialized=
 IF last.op="Put" /\ reply.status="Accepted" THEN before.initialized \cup {last.r}
 ELSE before.initialized
StatisticsExact == last.op="Get" /\ reply.status="ReadOK" =>
 reply.stats=OracleStats(before,Key(last.r,last.n,last.d),last.unit,last.range,last.at)
CursorAdmission == last.op="List" /\ last.resume /\ reply.status="ReadOK" =>
 /\ captured.present /\ last.mac /\ captured.region=last.r
 /\ captured.ns=last.n /\ captured.dim=last.d /\ captured.revision=before.revision[last.r]
 /\ captured.at<=last.at /\ last.at-captured.at<=1
 /\ captured.after \in ExpectedList(before,last.r,last.n,last.d,captured.at)
ListExact == last.op="List" /\ reply.status="ReadOK" =>
 LET at == IF last.resume THEN captured.at ELSE last.at
     expected == {k \in ExpectedList(before,last.r,last.n,last.d,at):
                    ~last.resume \/ k \in ExpectedAfter(captured.after)}
 IN /\ reply.list=ExpectedPage(expected)
    /\ reply.token=IF Cardinality(expected)>1
                   THEN Token(TRUE,last.r,last.n,last.d,before.revision[last.r],at,Head(reply.list))
                   ELSE NoToken
ErrorNoDisclosure == reply.status \notin {"ReadOK","Accepted","Initial"} =>
 reply.list = <<>> /\ reply.stats={} /\ ~reply.token.present
=============================================================================
