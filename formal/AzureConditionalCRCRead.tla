------------------------- MODULE AzureConditionalCRCRead -------------------------
EXTENDS Naturals
\* Date is the requested HTTP-date relative to the persisted Last-Modified.
\* Short and Long abstract <=4 MiB and >4 MiB after normalization.
\* Other conditions (including ETags) and transport admission are outside scope.
Dates == {"None", "Older", "Equal", "Newer"}
Ranges == {"None", "Short", "Long", "Outside"}
Flags == {"None", "CRC64", "Malformed", "Both"}
Requests == [allowed: BOOLEAN, exists: BOOLEAN, date: Dates, range: Ranges, flag: Flags]
VARIABLES req, out, phase
vars == <<req, out, phase>>
RejectedFlag(q) == q.flag \in {"Malformed","Both"} \/ (q.flag="CRC64" /\ q.range="None")
NotModified(q) == q.date \in {"Equal","Newer"}
Status(q) ==
 IF ~q.allowed THEN 403
 ELSE IF RejectedFlag(q) THEN 400
 ELSE IF ~q.exists THEN 404
 ELSE IF NotModified(q) THEN 304
 ELSE IF q.range="None" THEN 200
 ELSE IF q.range="Outside" THEN 416
 ELSE IF q.flag="CRC64" /\ q.range="Long" THEN 400
 ELSE 206
Read(q) ==
 LET status == Status(q)
 IN [status |-> status, payload |-> IF status=206 THEN "Selected" ELSE IF status=200 THEN "Whole" ELSE "None",
     validator |-> status \in {200,206,304}, rangeHeader |-> status=206,
     crc64 |-> status=206 /\ q.flag="CRC64", blobMD5 |-> status=206]
Init == /\ req \in Requests /\ phase="Ready"
        /\ out=[status |-> 0,payload |-> "None", validator |-> FALSE,
          rangeHeader |-> FALSE,crc64 |-> FALSE,blobMD5 |-> FALSE]
Next == (phase="Ready" /\ out'=Read(req) /\ phase'="Done" /\ UNCHANGED req) \/ UNCHANGED vars
Spec == Init /\ [][Next]_vars
TypeOK ==
 /\ req \in Requests /\ phase \in {"Ready","Done"}
 /\ out \in [status: {0,200,206,304,400,403,404,416},
              payload: {"None","Whole","Selected"},validator: BOOLEAN,
              rangeHeader: BOOLEAN,crc64: BOOLEAN,blobMD5: BOOLEAN]
DeniedBeforeOptions ==
 phase="Done" /\ ~req.allowed =>
 /\ out.status=403 /\ out.payload="None" /\ ~out.validator
 /\ ~out.rangeHeader /\ ~out.crc64 /\ ~out.blobMD5
OptionsBeforeLookup ==
 phase="Done" /\ req.allowed /\ RejectedFlag(req) => out.status=400
MissingBeforeDate ==
 phase="Done" /\ req.allowed /\ ~RejectedFlag(req) /\ ~req.exists => out.status=404
CacheHitHasNoPartialOutput ==
 phase="Done" /\ req.allowed /\ ~RejectedFlag(req) /\ req.exists /\ NotModified(req) =>
 /\ out.status=304 /\ out.payload="None" /\ out.validator
 /\ ~out.rangeHeader /\ ~out.crc64 /\ ~out.blobMD5
EligibleSelectedCRC ==
 phase="Done" /\ req.allowed /\ req.exists /\ ~NotModified(req) /\ req.flag="CRC64" /\ req.range="Short" =>
 /\ out.status=206 /\ out.payload="Selected" /\ out.validator
 /\ out.rangeHeader /\ out.crc64 /\ out.blobMD5
CRCEmitsOnlyWhenRequested ==
 phase="Done" /\ out.crc64 =>
 /\ req.allowed /\ req.exists /\ ~NotModified(req) /\ req.flag="CRC64" /\ req.range="Short"
 /\ out.status=206 /\ out.rangeHeader
CRCRejectsOversize ==
 phase="Done" /\ req.allowed /\ req.exists /\ ~NotModified(req) /\ req.flag="CRC64" /\ req.range="Long" => out.status=400
=============================================================================
