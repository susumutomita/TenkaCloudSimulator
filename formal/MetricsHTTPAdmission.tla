------------------------ MODULE MetricsHTTPAdmission -------------------------
EXTENDS Integers, Sequences, FiniteSets
\* Original finite HTTP admission projection. Codec, UTF8 and byte-size flags
\* are assumed results, not gzip/Go/SQL/memory or authentication proofs.
Ids == 1..41
GzipIds == {2,3,16,17,18,19,20,21,22,23,24,25,33,34,35,41}
Input(i) == [scope |-> IF i \in {4,32} THEN "absent" ELSE IF i=5 THEN "error"
                         ELSE IF i=6 THEN "noAuthorizer" ELSE "valid",
 method |-> i#7, path |-> i#8, media |-> i#9, route |-> i \notin {10,11},
 encoding |-> IF i \in {12,40} THEN "other" ELSE IF i=13 THEN "multiple"
                         ELSE IF i \in GzipIds THEN "gzip" ELSE "identity",
 read |-> i \notin {14,40},
 wire |-> IF i \in {15,16,32,33} THEN "over" ELSE IF i \in {1,2} THEN "equal" ELSE "below",
 header |-> i \notin {17,41},
 expanded |-> IF i \in {15,22,34,41} THEN "over" ELSE IF i \in {1,3} THEN "equal" ELSE "below",
 codec |-> IF i \in {18,33,34,35} THEN "crc"
           ELSE IF i=19 THEN "truncated" ELSE IF i=20 THEN "trailing"
           ELSE IF i=21 THEN "members" ELSE "good",
 form |-> i \notin {23,24,25}, action |-> IF i=26 THEN "missing"
           ELSE IF i=28 THEN "unknown" ELSE IF i \in {3,39} THEN "read" ELSE "put",
 version |-> i#27, auth |-> IF i \in {29,35,36} THEN "deny"
                         ELSE IF i=30 THEN "error" ELSE "allow",
 schema |-> i \notin {31,36}, response |-> i#37, commit |-> i#38]
\* Independent literal expected sets do not call any operational guard below.
ExpectedReadIds == {1,2,3} \cup (14..31) \cup (33..39) \cup {41}
ExpectedBodyIds == {1,2,3} \cup (23..31) \cup (36..39)
ExpectedQueryIds == {1,2,3,29,30,31,36,37,38,39}
ExpectedSchemaIds == {1,2,3,31,37,38,39}
GoodIds == {1,2,3,39}
ExpectedStatus(i) == IF i \in GoodIds THEN "OK"
 ELSE IF i \in {4,32} THEN "401"
 ELSE IF i \in {5,6,30,37,38} THEN "500"
 ELSE IF i \in {7,8,28} THEN "404"
 ELSE IF i \in {15,16,22,33,34} THEN "413"
 ELSE IF i \in {29,36} THEN "403" ELSE "400"
Seed(n) == [rows |-> <<n>>, revision |-> n, cursorAuthority |-> n,
            peer |-> <<2,n>>]
Errors == {"400","401","403","404","413","500"}
Phases == {"scope","envelope","wire","wireResult","gzipHeader","decode","form",
           "auth","schema","schemaResult","prepare","commit","reply","done"}
VARIABLES id, seed, before, disk, phase, status, bodyRead, bodyAccepted,
          queryAccepted, authorized, schemaChecked, prepared, committed
vars == <<id,seed,before,disk,phase,status,bodyRead,bodyAccepted,queryAccepted,
          authorized,schemaChecked,prepared,committed>>
q == Input(id)
ScopeOK == q.scope="valid" \* CONTROL BypassTrustedScope
EnvelopeOK == q.method /\ q.path /\ q.media /\ q.route
              /\ q.encoding \in {"identity","gzip"} \* CONTROL BypassEncoding
WireOK == q.wire#"over" \* CONTROL BypassWireCap
HeaderOK == q.header \* CONTROL BypassGzipHeader
ExpandedOK == q.expanded#"over" \* CONTROL BypassExpandedCap
CodecOK == q.codec="good" \* CONTROL CRC / Truncated / Trailing / Members
FormOK == q.form \* CONTROL BypassForm
VersionOK == q.version \* CONTROL BypassVersion
ActionKnown == q.action \in {"put","read"} \* CONTROL BypassUnknownAction
AuthOK == q.auth="allow" \* CONTROL BypassActionPermission
SchemaOK == q.schema \* CONTROL BypassSemanticSchema
ResponseOK == q.response \* CONTROL BypassResponsePrepare
CommitOK == q.commit \* CONTROL AcknowledgeFailedCommit
Effect(d) == IF q.action="put"
 THEN [d EXCEPT !.rows=Append(@,9), !.revision=@+1] ELSE d
Init == /\ id \in Ids /\ seed \in 1..2 /\ before=Seed(seed) /\ disk=before
 /\ phase="scope" /\ status="pending"
 /\ bodyRead=FALSE /\ bodyAccepted=FALSE /\ queryAccepted=FALSE
 /\ authorized=FALSE /\ schemaChecked=FALSE /\ prepared=FALSE /\ committed=FALSE
Advance(to) == /\ phase'=to
 /\ UNCHANGED <<id,seed,before,disk,status,bodyRead,bodyAccepted,queryAccepted,
                authorized,schemaChecked,prepared,committed>>
Reject(code) == /\ status'=code /\ phase'="done"
 /\ disk'=disk \* CONTROL RejectedDurableWrite
 /\ UNCHANGED <<id,seed,before,bodyRead,bodyAccepted,queryAccepted,authorized,
                schemaChecked,prepared,committed>>
ScopeStep == /\ phase="scope"
 /\ (IF ScopeOK THEN Advance("envelope")
     ELSE Reject(IF q.scope="absent" THEN "401" ELSE "500"))
EnvelopeStep == /\ phase="envelope"
 /\ (IF EnvelopeOK THEN Advance("wire")
     ELSE Reject(IF ~q.method \/ ~q.path THEN "404" ELSE "400"))
ReadStep == /\ phase="wire" /\ bodyRead'=TRUE /\ phase'="wireResult"
 /\ UNCHANGED <<id,seed,before,disk,status,bodyAccepted,queryAccepted,
                authorized,schemaChecked,prepared,committed>>
AcceptBody == /\ bodyAccepted'=TRUE /\ phase'="form"
 /\ UNCHANGED <<id,seed,before,disk,status,bodyRead,queryAccepted,
                authorized,schemaChecked,prepared,committed>>
WireStep == /\ phase="wireResult"
 /\ (IF ~q.read THEN Reject("400") ELSE IF ~WireOK THEN Reject("413")
     ELSE IF q.encoding="gzip" THEN Advance("gzipHeader") ELSE AcceptBody)
HeaderStep == /\ phase="gzipHeader"
 /\ (IF HeaderOK THEN Advance("decode") ELSE Reject("400"))
DecodeStep == /\ phase="decode"
 /\ (IF ~ExpandedOK THEN Reject("413") ELSE IF ~CodecOK THEN Reject("400")
     ELSE AcceptBody)
AcceptQuery == /\ queryAccepted'=TRUE /\ phase'="auth" \* CONTROL SchemaBeforeAuth
 /\ UNCHANGED <<id,seed,before,disk,status,bodyRead,bodyAccepted,
                authorized,schemaChecked,prepared,committed>>
FormStep == /\ phase="form"
 /\ (IF ~FormOK \/ q.action="missing" \/ ~VersionOK THEN Reject("400")
     ELSE IF ~ActionKnown THEN Reject("404") ELSE AcceptQuery)
Authorize == /\ authorized'=TRUE /\ phase'="schema"
 /\ UNCHANGED <<id,seed,before,disk,status,bodyRead,bodyAccepted,queryAccepted,
                schemaChecked,prepared,committed>>
AuthStep == /\ phase="auth"
 /\ (IF AuthOK THEN Authorize
     ELSE Reject(IF q.auth="deny" THEN "403" ELSE "500"))
CheckSchema == /\ phase="schema" /\ schemaChecked'=TRUE /\ phase'="schemaResult"
 /\ UNCHANGED <<id,seed,before,disk,status,bodyRead,bodyAccepted,queryAccepted,
                authorized,prepared,committed>>
SchemaStep == /\ phase="schemaResult"
 /\ (IF SchemaOK THEN Advance("prepare") ELSE Reject("400"))
Prepare == /\ prepared'=TRUE /\ phase'="commit"
 /\ UNCHANGED <<id,seed,before,disk,status,bodyRead,bodyAccepted,queryAccepted,
                authorized,schemaChecked,committed>>
PrepareStep == /\ phase="prepare"
 /\ (IF ResponseOK THEN Prepare ELSE Reject("500"))
Commit == /\ committed'=TRUE /\ disk'=Effect(disk) /\ phase'="reply"
 /\ UNCHANGED <<id,seed,before,status,bodyRead,bodyAccepted,queryAccepted,
                authorized,schemaChecked,prepared>>
CommitStep == /\ phase="commit"
 /\ (IF CommitOK THEN Commit ELSE Reject("500"))
Reply == /\ phase="reply" /\ status'="OK" /\ phase'="done"
 /\ UNCHANGED <<id,seed,before,disk,bodyRead,bodyAccepted,queryAccepted,
                authorized,schemaChecked,prepared,committed>>
Next == ScopeStep \/ EnvelopeStep \/ ReadStep \/ WireStep \/ HeaderStep \/ DecodeStep
 \/ FormStep \/ AuthStep \/ CheckSchema \/ SchemaStep \/ PrepareStep
 \/ CommitStep \/ Reply \/ (phase="done" /\ UNCHANGED vars)
Spec == Init /\ [][Next]_vars
FrameType == [rows:Seq(1..9),revision:1..3,cursorAuthority:1..2,peer:Seq(1..2)]
TypeOK == /\ id \in Ids /\ seed \in 1..2 /\ before \in FrameType /\ disk \in FrameType
 /\ phase \in Phases /\ status \in Errors \cup {"pending","OK"}
 /\ bodyRead \in BOOLEAN /\ bodyAccepted \in BOOLEAN /\ queryAccepted \in BOOLEAN
 /\ authorized \in BOOLEAN /\ schemaChecked \in BOOLEAN
 /\ prepared \in BOOLEAN /\ committed \in BOOLEAN
ScopeBeforeBody == bodyRead => id \notin {4,5,6,32}
EnvelopeBeforeBody == bodyRead => id \in ExpectedReadIds
BodyAdmissionExact == bodyAccepted => id \in ExpectedBodyIds
QueryAdmissionExact == queryAccepted => id \in ExpectedQueryIds
AuthorizationBeforeSchema == schemaChecked => authorized /\ id \in ExpectedSchemaIds
NoPrematureEffect == ~committed => disk=before
FailureFrame == status \in Errors => disk=before
ReadFrame == id \in {3,39} => disk=before
PeerAndCursorFrame == disk.peer=before.peer /\ disk.cursorAuthority=before.cursorAuthority
CommitRequiresAdmission == committed => id \in GoodIds /\ authorized /\ schemaChecked /\ prepared
SuccessfulEffectExact == committed =>
 IF id \in {1,2} THEN disk.rows=Append(before.rows,9) /\ disk.revision=before.revision+1
 ELSE disk=before
ReplyAfterCommit == status="OK" => committed
StatusExact == status#"pending" => status=ExpectedStatus(id)
=============================================================================
