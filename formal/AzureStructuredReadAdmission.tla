--------------------- MODULE AzureStructuredReadAdmission ---------------------
Versions == {"Legacy","XSM","Other"}
Markers == {"None","CRC","Other"}
Operations == {"GetBlob","Other"}
ReadResults == {"Whole","Range","LargeWhole","LargeRange",
                "NotModified","Forbidden","Precondition","ReadError"}
Requests == [version: Versions, marker: Markers, operation: Operations,
             plainChecksum: BOOLEAN, nativeRead: ReadResults]
VARIABLES req, done, response
vars == <<req,done,response>>

Envelopes == {"None","Legacy","FullXSM","RangedXSM"}
Reply(status, envelope, xsmTrailer, ranged, storedMD5) ==
    [status |-> status, envelope |-> envelope, xsmTrailer |-> xsmTrailer,
     ranged |-> ranged, storedMD5 |-> storedMD5]
Quiet(status) == Reply(status,"None",FALSE,FALSE,FALSE)
Approved(q) ==
   <<q.version,q.marker,q.operation,q.plainChecksum>> =
       <<"XSM","CRC","GetBlob",FALSE>>

VersionOK(q) == q.version="XSM"
MarkerOK(q) == q.marker="CRC"
OperationOK(q) == q.operation="GetBlob"
TransportOK(q) == ~q.plainChecksum
StructuredStatus(q) ==
    CASE q.nativeRead="NotModified" -> "304"
      [] q.nativeRead="Forbidden" -> "403"
      [] q.nativeRead="Precondition" -> "412"
      [] q.nativeRead="ReadError" -> "ReadError"
      [] q.nativeRead \in {"LargeWhole","LargeRange"} -> "400"
      [] q.nativeRead="Range" -> "206"
      [] OTHER -> "200"
Abrupt(q) == q.nativeRead \in {"NotModified","Forbidden","Precondition","ReadError"}
FailedRead(q) ==
    CASE q.nativeRead="NotModified" -> "304"
      [] q.nativeRead="Forbidden" -> "403"
      [] q.nativeRead="Precondition" -> "412"
      [] OTHER -> "ReadError"
Structured(q) ==
    LET status == StructuredStatus(q)
    IN CASE status="200" -> Reply(status,"FullXSM",TRUE,FALSE,FALSE)
         [] status="206" -> Reply(status,"RangedXSM",TRUE,TRUE,TRUE)
         [] OTHER -> Quiet(status)
Choose(q) ==
    IF q.version="Legacy"
    THEN IF q.marker="None" THEN Reply("Legacy","Legacy",FALSE,FALSE,FALSE) ELSE Quiet("400")
    ELSE IF VersionOK(q) /\ MarkerOK(q) /\ OperationOK(q) /\ TransportOK(q)
         THEN Structured(q) ELSE Quiet("400")

Init == /\ req \in Requests /\ done=FALSE /\ response=Quiet("Pending")
Next == ( /\ ~done /\ response'=Choose(req) /\ done'=TRUE /\ UNCHANGED req )
        \/ UNCHANGED vars
TypeOK ==
    /\ req \in Requests /\ done \in BOOLEAN
    /\ response \in [status: {"Pending","Legacy","200","206","304","400","403","412","ReadError"},
                      envelope: Envelopes, xsmTrailer: BOOLEAN, ranged: BOOLEAN, storedMD5: BOOLEAN]
ExactNegotiation == done /\ ~Approved(req) /\ req.version#"Legacy" => response=Quiet("400")
LegacyIsUnchanged == done /\ req.version="Legacy" =>
    response=(IF req.marker="None" THEN Reply("Legacy","Legacy",FALSE,FALSE,FALSE) ELSE Quiet("400"))
DeniedOrConditionalIsQuiet == done /\ Approved(req) /\ Abrupt(req) =>
    response=Quiet(FailedRead(req))
LargeReadsAreQuiet == done /\ Approved(req) /\ req.nativeRead \in {"LargeWhole","LargeRange"} =>
    response=Quiet("400")
FullHasNoRangeMetadata == done /\ Approved(req) /\ req.nativeRead="Whole" =>
    response=Reply("200","FullXSM",TRUE,FALSE,FALSE)
RangeHasRangeMetadata == done /\ Approved(req) /\ req.nativeRead="Range" =>
    response=Reply("206","RangedXSM",TRUE,TRUE,TRUE)
=============================================================================
