---------------------- MODULE MetricsRequestAdmission ------------------------
EXTENDS Integers, Sequences, FiniteSets
CONSTANT NoResult
\* Original bounded request/normalization projection. Name/UTF8/size, float,
\* number/date lexer and long-list cardinality classes are assumed results;
\* this is not their Go algorithms, IEEE754, byte math or a SQL/auth proof.
Ids == 1..71
WireIds == {1,2,4,6,43,44,48,52,69,71} \cup (15..23)
StatsIds == {4,5,9,52,53,54,55,56,57,58,59,60,61,62,63}
ListIds == {6,10,64,65,66,67,68,69}
D(n) == [name |-> n,value |-> n+10]
Dims(i) == IF i=8 THEN [k \in 1..30 |-> D(31-k)]
 ELSE IF i=36 THEN [k \in 1..31 |-> D(k)]
 ELSE IF i=10 THEN [k \in 1..10 |-> D(11-k)]
 ELSE IF i=64 THEN [k \in 1..11 |-> D(k)]
 ELSE IF i \in {37,65} THEN <<D(1),[name |-> 1,value |-> 22]>>
 ELSE IF i=6 THEN <<[name |-> 2,value |-> 0],D(1)>> ELSE <<D(2),D(1)>>
Input(i) == [entry |-> IF i \in WireIds THEN "wire" ELSE "typed",
 op |-> IF i \in StatsIds THEN "stats" ELSE IF i \in ListIds THEN "list" ELSE "put",
 required |-> i \notin {15,16,17,52}, single |-> i#18,
 closed |-> i \notin {19,69},
 indexes |-> IF i=20 THEN {"01"} ELSE IF i=21 THEN {"0"}
             ELSE IF i=22 THEN {"1","3"} ELSE IF i=23 THEN {"x"}
             ELSE IF i \in {2,4,6,7,71} THEN {"2","1"} ELSE {"1"},
 indexBound |-> i#71,
 count |-> IF i=24 THEN "empty" ELSE IF i \in {25,71} THEN "over"
           ELSE IF i=7 THEN "equal" ELSE "under",
 ns |-> IF i=12 THEN " Local " ELSE IF i=6 THEN "absent" ELSE "Local",
 name |-> IF i=11 THEN "Metric<&é" ELSE IF i=6 THEN "absent" ELSE "Metric",
 names |-> i \notin ((26..35) \cup {38,39,66,67,68}),
 dims |-> Dims(i), unit |-> IF i=40 THEN "" ELSE IF i=41 THEN "bad"
              ELSE IF i=2 THEN "Count" ELSE IF i=3 THEN "None" ELSE "absent",
 resolution |-> IF i=42 THEN 1 ELSE IF i \in {2,3} THEN 60 ELSE 0,
 parsed |-> i \notin {43,44,48},
 value |-> IF i=13 THEN "upper" ELSE IF i=14 THEN "lower"
           ELSE IF i=2 THEN "positive" ELSE "zero",
 finite |-> i \notin {45,46,47}, stamp |-> IF i=2 THEN "pastEdge"
                 ELSE IF i=3 THEN "futureEdge" ELSE "absent",
 nano |-> i#49, ingest |-> i \notin {50,51},
 period |-> IF i=53 THEN 1 ELSE IF i=54 THEN 61 ELSE IF i=55 THEN 0
             ELSE IF i=56 THEN 1296001 ELSE IF i=9 THEN 1296000 ELSE 60,
 range |-> i \notin {57,58,59}, start |-> IF i=5 THEN 120 ELSE 121,
 stats |-> IF i=60 THEN <<>> ELSE IF i=61 THEN <<"Sum","Sum","Sum","Sum","Sum","Sum">>
            ELSE IF i=62 THEN <<"Bogus">> ELSE IF i=63 THEN <<"Sum","Sum">>
            ELSE IF i=4 THEN <<"Sum","Average">> ELSE <<"Sum">>,
 allDatums |-> i#70]
\* Independent closed oracles use literal fixture IDs/values, not acceptance,
\* sorting, defaults or key operators below. Only IDs1..14 are valid fixtures.
GoodIds == 1..14
ExpectedDecodeIds == {1,2,4,6}
ExpectedNames == [i \in 1..14 |-> IF i=11 THEN "Metric<&é" ELSE IF i=6 THEN "absent" ELSE "Metric"]
ExpectedNS == [i \in 1..14 |-> IF i=12 THEN " Local " ELSE IF i=6 THEN "absent" ELSE "Local"]
ExpectedDimNames(i) == IF i=8 THEN <<1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30>>
 ELSE IF i=10 THEN <<1,2,3,4,5,6,7,8,9,10>> ELSE <<1,2>>
ExpectedValues == <<11,12,13,14,15,16,17,18,19,20,21,22,23,24,25,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40>>
ExpectedUnit(i) == IF i=2 THEN "Count" ELSE IF i \in {4,5,6,9,10} THEN "absent" ELSE "None"
ExpectedValue(i) == IF i \in {4,5,6,9,10} THEN "notApplicable" ELSE IF i=13 THEN "upper" ELSE IF i=14 THEN "lower" ELSE IF i=2 THEN "positive" ELSE "zero"
ExpectedStamp(i) == IF i \in {4,5,6,9,10} THEN "notApplicable" ELSE IF i=2 THEN "pastEdge" ELSE IF i=3 THEN "futureEdge" ELSE "now"
ExpectedStats(i) == IF i=4 THEN <<"Sum","Average">> ELSE IF i \in {5,9} THEN <<"Sum">> ELSE <<>>
ExpectedDimValues(i) == IF i=6 THEN <<11,0>> ELSE ExpectedValues
ExpectedDims(i) == [j \in 1..Len(ExpectedDimNames(i)) |-> [name |-> ExpectedDimNames(i)[j],value |-> ExpectedDimValues(i)[j]]]
Numbers(ts) == {CASE t \in {"1","01"} -> 1 [] t="2" -> 2 [] t="3" -> 3 [] OTHER -> 0:t \in ts}
CanonicalIndexes(r) == r.indexes \subseteq {"1","2","3"} \* CONTROL IndexCanonical
ContiguousIndexes(r) == Numbers(r.indexes)=1..Cardinality(Numbers(r.indexes)) \* CONTROL IndexContiguous
FieldsOK(r) == r.required /\ r.single \* CONTROL RequiredMultiplicity
ClosedOK(r) == r.closed \* CONTROL UnknownField
ParseOK(r) == r.parsed \* CONTROL NumberDateParse
IndexBoundOK(r) == r.indexBound \* CONTROL IndexLimitClass
DecodeOK(r) == IndexBoundOK(r) /\ FieldsOK(r) /\ ClosedOK(r) /\ CanonicalIndexes(r) /\ ContiguousIndexes(r) /\ ParseOK(r)
CountOK(r) == r.count \in {"under","equal"} \* CONTROL DatumCount
NamesOK(r) == r.names \* CONTROL NameClasses
DimensionsOK(r) == Len(r.dims)<=(IF r.op="list" THEN 10 ELSE 30) \* CONTROL DimensionCount
UniqueNames(r) == Cardinality({r.dims[j].name:j \in 1..Len(r.dims)})=Len(r.dims) \* CONTROL DuplicateDimension
UnitOK(r) == r.unit \in {"absent","None","Count","Seconds"} \* CONTROL UnitPolicy
ResolutionOK(r) == r.resolution \in {0,60} \* CONTROL Resolution
FiniteOK(r) == r.finite \* CONTROL FiniteMagnitude
TimeOK(r) == r.nano /\ r.ingest \* CONTROL TimestampWindow
PeriodOK(r) == r.period>=60 /\ r.period<=1296000 /\ r.period%60=0 \* CONTROL Period
RangeOK(r) == r.range \* CONTROL QueryTimeRange
StatisticsOK(r) == Len(r.stats)>=1 /\ Len(r.stats)<=5
 /\ Cardinality({r.stats[j]:j \in 1..Len(r.stats)})=Len(r.stats)
 /\ \A j \in 1..Len(r.stats):r.stats[j] \in {"SampleCount","Sum","Minimum","Maximum","Average"} \* CONTROL Statistics
SchemaOK(r) == NamesOK(r) /\ DimensionsOK(r) /\ UniqueNames(r) /\ UnitOK(r)
 /\ (IF r.op="put" THEN CountOK(r) /\ ResolutionOK(r) /\ FiniteOK(r) /\ TimeOK(r) /\ r.allDatums
     ELSE IF r.op="stats" THEN PeriodOK(r) /\ RangeOK(r) /\ StatisticsOK(r) ELSE TRUE)
Rank(ds,j) == 1+Cardinality({k \in 1..Len(ds):ds[k].name<ds[j].name \/ (ds[k].name=ds[j].name /\ k<j)})
Sorted(ds) == [i \in 1..Len(ds) |-> ds[CHOOSE j \in 1..Len(ds):Rank(ds,j)=i]]
Plan(r) == [op |-> r.op, ns |-> r.ns, name |-> r.name,
 dims |-> Sorted(r.dims), \* CONTROL SortDimensions
 unit |-> IF r.op="put" /\ r.unit="absent" THEN "None" ELSE r.unit, \* CONTROL UnitDefault
 value |-> IF r.op="put" THEN r.value ELSE "notApplicable", \* CONTROL PreserveValue
 stamp |-> IF r.op#"put" THEN "notApplicable" ELSE IF r.stamp="absent" THEN "now" ELSE r.stamp, \* CONTROL TimestampDefault
 period |-> IF r.op="stats" THEN r.period ELSE 0,
 start |-> IF r.op="stats" THEN r.start-(r.start%60) ELSE 0, \* CONTROL MinuteRound
 stats |-> IF r.op="stats" THEN r.stats ELSE <<>>, \* CONTROL StatisticOrder
 key |-> <<r.ns,r.name,Sorted(r.dims)>>, \* CONTROL UnitNotIdentity
 count |-> IF r.op="put" THEN r.count ELSE "notApplicable"]
VARIABLES id, original, request, phase, status, working, published, decoded
vars == <<id,original,request,phase,status,working,published,decoded>>
Init == /\ id \in Ids /\ original=Input(id) /\ request=original
 /\ phase=(IF Input(id).entry="wire" THEN "decode" ELSE "normalize")
 /\ status="pending" /\ working=(IF Input(id).entry="wire" THEN NoResult ELSE request)
 /\ published=NoResult /\ decoded=FALSE
Decode == /\ phase="decode"
 /\ (IF DecodeOK(request)
     THEN /\ decoded'=TRUE /\ working'=request /\ phase'="normalize" /\ status'="pending" /\ published'=NoResult \* CONTROL EarlyPublish
     ELSE /\ decoded'=FALSE /\ working'=NoResult /\ phase'="done" /\ status'="rejected" /\ published'=NoResult)
 /\ UNCHANGED <<id,original,request>>
Normalize == /\ phase="normalize"
 /\ (IF SchemaOK(working)
     THEN /\ published'=Plan(working) /\ status'="accepted"
     ELSE /\ published'=NoResult /\ status'="rejected") \* CONTROL RefusalPublication
 /\ phase'="done"
 /\ request'=request \* CONTROL CallerCopy
 /\ UNCHANGED <<id,original,working,decoded>>
Next == Decode \/ Normalize \/ (phase="done" /\ UNCHANGED vars)
Spec == Init /\ [][Next]_vars
PlanType == [op:{"put","list","stats"},ns:STRING,name:STRING,
 dims:Seq([name:1..31,value:0..41]),unit:STRING,value:STRING,stamp:STRING,
 period:0..1296001,start:0..240,stats:Seq(STRING),count:STRING]
\* The heterogeneous identity tuple has two string positions followed by a
\* dimension sequence; the unit-in-identity negative adds one string position.
\* Check positions directly instead of TLC membership in a mixed STRING union.
KeyCarrier(k) == /\ Len(k) \in {3,4}
 /\ k[1] \in STRING /\ k[2] \in STRING
 /\ k[3] \in Seq([name:1..31,value:0..41])
 /\ (Len(k)=4 => k[4] \in STRING)
PlanCarrier(p) == /\ [f \in DOMAIN p \ {"key"} |-> p[f]] \in PlanType
 /\ KeyCarrier(p.key)
\* A configured model value is distinct from every record and wire string.
Absent(v) == v=NoResult
TypeOK == /\ id \in Ids /\ original=Input(id) /\ phase \in {"decode","normalize","done"}
 /\ status \in {"pending","accepted","rejected"} /\ DOMAIN request=DOMAIN original /\ decoded \in BOOLEAN
 /\ (IF Absent(working) THEN TRUE ELSE working \in {Input(j):j \in Ids})
 /\ (IF Absent(published) THEN TRUE ELSE PlanCarrier(published))
InputPreserved == request=original
NoEarlyPublication == phase#"done" => Absent(published)
DecodedAdmissionExact == decoded => id \in ExpectedDecodeIds
AdmissionExact == phase="done" => (status="accepted" <=> id \in GoodIds)
NoRejectedPublication == status="rejected" => Absent(published)
DimensionOrderExact == status="accepted" =>
 [j \in 1..Len(published.dims) |-> published.dims[j].name]=ExpectedDimNames(id)
DimensionValuesExact == status="accepted" =>
 \A j \in 1..Len(published.dims):published.dims[j].value=ExpectedDimValues(id)[j]
NameIdentityExact == status="accepted" => published.ns=ExpectedNS[id] /\ published.name=ExpectedNames[id]
UnitExact == status="accepted" => published.unit=ExpectedUnit(id)
ScalarValueExact == status="accepted" => published.value=ExpectedValue(id)
TimestampExact == status="accepted" => published.stamp=ExpectedStamp(id)
StartNormalizationExact == status="accepted" => published.start=(IF id \in {4,5,9} THEN 120 ELSE 0)
StatisticOrderExact == status="accepted" => published.stats=ExpectedStats(id)
MetricIdentityExact == status="accepted" =>
 published.key = <<ExpectedNS[id],ExpectedNames[id],ExpectedDims(id)>>
PeriodExact == status="accepted" => published.period=(IF id=9 THEN 1296000 ELSE IF id \in {4,5} THEN 60 ELSE 0)
CountClassExact == status="accepted" => published.count=(IF id \in {4,5,6,9,10} THEN "notApplicable" ELSE IF id=7 THEN "equal" ELSE "under")
=============================================================================
