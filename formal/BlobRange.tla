------------------------------- MODULE BlobRange -------------------------------
EXTENDS Integers, Sequences
CONSTANTS NoValue, ChecksumLimit, MaxOffset
Bodies == {SubSeq(<<0,1,2>>,1,n): n \in 0..3}
Conditions == {"Pass","NotModified","Failed"}
Kinds == {"Closed","Open","Invalid","NoRange"}
Requests == [authorized: BOOLEAN, condition: Conditions, kind: Kinds,
             start: 0..MaxOffset, finish: 0..MaxOffset, checksum: BOOLEAN]
VARIABLES stored, request, response, phase
vars == <<stored,request,response,phase>>
Min(a,b) == IF a < b THEN a ELSE b
\* Totalized response construction keeps intentionally broken range controls
\* well-defined; baseline gates admit only genuine in-domain selections.
Selected(body,start,stop) ==
 [i \in 1..(IF stop>=start THEN stop-start+1 ELSE 0) |->
   IF i+start \in DOMAIN body THEN body[i+start] ELSE 0]
EmptyResponse == [status |-> 0, payload |-> <<>>, objectLength |-> NoValue,
 contentRange |-> NoValue, etag |-> FALSE, checksumKind |-> "None", before |-> <<>>]
Decide(body,q) ==
 LET total == Len(body)
     stop == IF q.kind="Open" THEN total-1 ELSE Min(q.finish,total-1)
     status == IF ~q.authorized THEN 403
       ELSE IF q.condition="NotModified" THEN 304
       ELSE IF q.condition="Failed" THEN 412
       ELSE IF q.kind="NoRange" THEN IF q.checksum THEN 400 ELSE 200
       ELSE IF q.kind="Invalid" THEN 400
       ELSE IF q.start >= total \/ (q.kind="Closed" /\ q.finish < q.start) THEN 416
       ELSE IF q.checksum /\ stop-q.start+1 > ChecksumLimit THEN 400
       ELSE 206
     selected == IF status=200 THEN body
                 ELSE IF status=206 THEN Selected(body,q.start,stop) ELSE <<>>
 IN [status |-> status, payload |-> selected,
     objectLength |-> IF status \in {200,206} THEN Len(selected) ELSE NoValue,
     contentRange |-> IF status=206 THEN <<q.start,stop,total>> ELSE NoValue,
     etag |-> status \in {200,206,304},
     checksumKind |-> IF status \in {200,304} THEN "Full"
                      ELSE IF status=206 /\ q.checksum THEN "Range" ELSE "None",
     before |-> body]
Init ==
 /\ stored \in Bodies /\ request \in Requests
 /\ response = EmptyResponse /\ phase = "Ready"
Evaluate ==
 /\ phase="Ready"
 /\ response' = Decide(stored,request)
 /\ phase' = "Done"
 /\ UNCHANGED <<stored,request>>
Terminal == phase="Done" /\ UNCHANGED vars
Next == Evaluate \/ Terminal
TypeOK ==
 /\ stored \in Bodies /\ request \in Requests /\ phase \in {"Ready","Done"}
 /\ response.status \in {0,200,206,304,400,403,412,416}
 /\ response.payload \in Seq({0,1,2})
 /\ response.objectLength \in {NoValue} \cup 0..5
 /\ (response.contentRange=NoValue \/ response.contentRange \in (0..4) \X (-1..4) \X (0..3))
 /\ response.etag \in BOOLEAN /\ response.checksumKind \in {"None","Range","Full"}
 /\ response.before \in Bodies
Frame == phase="Done" => stored=response.before
DeniedNoDisclosure == (phase="Done" /\ ~request.authorized) =>
 /\ response.status=403 /\ response.payload= <<>>
 /\ response.objectLength=NoValue /\ response.contentRange=NoValue
 /\ ~response.etag /\ response.checksumKind="None"
ConditionBeforeRange == (phase="Done" /\ request.authorized /\ request.condition#"Pass") =>
 /\ response.status=IF request.condition="NotModified" THEN 304 ELSE 412
 /\ response.payload= <<>> /\ response.objectLength=NoValue /\ response.contentRange=NoValue
InvalidRefused == (phase="Done" /\ request.authorized /\ request.condition="Pass" /\ request.kind="Invalid") => response.status=400
OutOfBoundsRefused ==
 (phase="Done" /\ request.authorized /\ request.condition="Pass" /\ request.kind \in {"Closed","Open"} /\
  (request.start >= Len(response.before) \/ (request.kind="Closed" /\ request.finish < request.start))) => response.status=416
ExactSelectedBytes == (phase="Done" /\ response.status=206) =>
 LET stop == IF request.kind="Open" THEN Len(response.before)-1
             ELSE Min(request.finish,Len(response.before)-1)
 IN /\ request.authorized /\ request.condition="Pass" /\ request.kind \in {"Closed","Open"}
    /\ request.start < Len(response.before) /\ stop >= request.start
    /\ response.payload=SubSeq(response.before,request.start+1,stop+1)
    /\ response.objectLength=stop-request.start+1
    /\ response.contentRange= <<request.start,stop,Len(response.before)>>
    /\ response.etag
ChecksumBound ==
 /\ ((phase="Done" /\ request.authorized /\ request.condition="Pass" /\ request.kind \in {"Closed","Open"} /\
      request.start < Len(response.before) /\
      (request.kind="Open" \/ request.finish >= request.start) /\ request.checksum) =>
     LET stop == IF request.kind="Open" THEN Len(response.before)-1
                 ELSE Min(request.finish,Len(response.before)-1)
     IN IF stop-request.start+1 > ChecksumLimit THEN response.status=400
        ELSE response.status=206 /\ response.checksumKind="Range")
 /\ ((phase="Done" /\ response.status=206 /\ request.checksum) =>
       response.objectLength <= ChecksumLimit /\ response.checksumKind="Range")
MissingRangeChecksumRefused ==
 (phase="Done" /\ request.authorized /\ request.condition="Pass" /\
  request.kind="NoRange" /\ request.checksum) => response.status=400
ExactNormalRead == (phase="Done" /\ response.status=200) =>
 /\ request.authorized /\ request.condition="Pass" /\ request.kind="NoRange" /\ ~request.checksum
 /\ response.payload=response.before /\ response.objectLength=Len(response.before)
 /\ response.contentRange=NoValue /\ response.etag /\ response.checksumKind="Full"
NoFullMD5OnPartial == (phase="Done" /\ response.status=206) =>
 /\ response.checksumKind#"Full"
 /\ (request.checksum => response.checksumKind="Range")
 /\ (~request.checksum => response.checksumKind="None")
=============================================================================
