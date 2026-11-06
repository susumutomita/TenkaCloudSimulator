------------------------- MODULE MetricsTrustedScope --------------------------
EXTENDS Integers, Sequences, FiniteSets
CONSTANTS NoOutput, AckOutput, PolicyOutput
\* Original consumer-boundary projection. Private actor getter validity,
\* metadata lexer classes and core policy verdicts are assumed, not token,
\* SigV4/IAM parser/SQL/concurrency/Go refinement proofs. Policy version is
\* document provenance, NOT an identity_policies schema column.
Ids == 1..58
GetIds == {5,10,54,57}
PutIds == {1,2,4,8,26,27,28,35,36,37,38,39,40,41,47,48,51,52}
WriterIds == {26,27,38,41,47,48,52}
FactoryIds == {32,33,34,49}
Input(i) == [mode |-> IF i \in FactoryIds THEN "factory" ELSE "request",
 storeDB |-> IF i=32 THEN "none" ELSE IF i=33 THEN "B" ELSE "A",
 authorityDB |-> IF i=34 THEN "none" ELSE "A", handler |-> i \notin {45,46},
 actor |-> i \notin {6,7,29,42,46},
 exercise |-> IF i \in {1,39} THEN "admin" ELSE IF i \in WriterIds THEN "writer"
              ELSE IF i \in {6,29,42,46} THEN "none" ELSE "reader",
 credential |-> IF i \in {4,5} THEN "reader" ELSE "admin",
 role |-> IF i \in {4,5} THEN "participant" ELSE "operator",
 service |-> IF i=15 THEN "duplicate" ELSE IF i \in {16,29,30} THEN "wrong"
             ELSE IF i=50 THEN "comma" ELSE IF i=58 THEN "folded" ELSE IF i \in {26,44,53} THEN "absent" ELSE "monitoring",
 region |-> IF i \in {25,26,43} THEN "absent" ELSE IF i \in {27,41} THEN "west"
            ELSE IF i=24 THEN "moon" ELSE "east",
 regionSingle |-> i#17, authSingle |-> i#18, session |-> i=19,
 auth |-> IF i=14 THEN "malformed" ELSE IF i=20 THEN "realKey" ELSE IF i=53 THEN "signatureOnly"
          ELSE IF i \in {16,18,19,21,22,23,26,29,30,42,43} THEN "dummy" ELSE "none",
 signedService |-> i \notin {22,44,53}, date |-> i#21,
 signedRegion |-> IF i=43 THEN "absent" ELSE IF i \in {23,26} THEN "west" ELSE "east",
 wirePrincipal |-> IF i=8 THEN "admin" ELSE "absent",
 latePrincipal |-> IF i=40 THEN "admin" ELSE IF i=8 THEN "admin" ELSE "absent",
 lateRegion |-> IF i=41 THEN "east" ELSE "absent",
 form |-> i \notin {28,29,30}, actionKnown |-> i#31,
 action |-> IF i \in PutIds THEN "Put" ELSE IF i \in GetIds THEN "Get" ELSE "List",
 callbackContract |-> i#38, semantic |-> i \notin {51,52},
 initialPolicy |-> IF i \in {35,36,37} THEN (IF i=35 THEN "allowAll" ELSE "deny")
                  ELSE IF i=9 THEN "missing" ELSE IF i=10 THEN "sqlError"
                  ELSE IF i=11 THEN "corrupt" ELSE IF i=12 THEN "oversize"
                  ELSE IF i=13 THEN "mixed" ELSE IF i \in {54,56} THEN "listOnly" ELSE IF i \in {55,57} THEN "getOnly" ELSE "reader",
 changePolicy |-> IF i=35 THEN "deny" ELSE IF i=36 THEN "allowAll" ELSE "none",
 work |-> i#47, commit |-> i#48]
\* Independent literal expectations. Do not call policy/metadata guards below.
ExpectedFallback == {44,53}
ExpectedFactoryReady == {49}
ExpectedOK == {1,3,5,25,26,27,36,39,41,56,57}
ExpectedDenied == {2,4,8,9,13,35,37,40,51,54,55}
ExpectedFault == {10,11,12,38,47,48}
ExpectedDecision(i) == IF i \in {1,3,5,25,26,27,36,39,41,47,48,52,56,57} THEN "allowed"
 ELSE IF i \in {13,35,37} THEN "explicitDeny"
 ELSE IF i \in {2,4,8,9,40,51,54,55} THEN "implicitDeny"
 ELSE IF i \in {10,11,12,38} THEN "error" ELSE "none"
ExpectedStatus(i) == IF i \in ExpectedFallback THEN "fallback"
 ELSE IF i \in ExpectedFactoryReady THEN "factoryReady"
 ELSE IF i \in {32,33,34} THEN "factoryRefused"
 ELSE IF i \in ExpectedOK THEN "OK"
 ELSE IF i \in ExpectedDenied THEN "403"
 ELSE IF i \in ExpectedFault THEN "500"
 ELSE IF i \in {6,7,29,42} THEN "401"
 ELSE IF i \in {14,20} THEN "403" ELSE IF i=31 THEN "404"
 ELSE IF i \in {45,46} THEN "501" ELSE "400"
ExpectedSubject(i) == IF i \in {1,39} THEN "admin"
 ELSE IF i \in {26,27,38,41,47,48,52} THEN "writer" ELSE "reader"
ExpectedRegion(i) == IF i \in {26,27,41} THEN "west" ELSE "east"
ExpectedMetadataIds == {1,2,3,4,5,6,7,8,9,10,11,12,13,25,26,27,28,31,35,36,37,38,39,40,41,42,45,46,47,48,51,52,54,55,56,57}
Errors == {"400","401","403","404","500","501"}
Regions == {"east","west","ap","eu"}
Row(doc,v) == [document |-> doc,version |-> v]
Seed(i,n) == [catalog |-> [r \in Regions |-> IF r="east" THEN <<n>> ELSE IF r="west" THEN <<n+2>> ELSE <<n+4>>],
 points |-> [r \in Regions |-> IF r="east" THEN <<n+4>> ELSE IF r="west" THEN <<n+6>> ELSE <<n+4>>],
 revision |-> [r \in Regions |-> n],
 reader |-> Row(Input(i).initialPolicy,0),writer |-> Row("allowAll",0),
 peer |-> [rows |-> <<9>>,policy |-> "allowAll",version |-> 2]]
MonitoringIntent(r) == r.service \in {"monitoring","duplicate","comma","folded"}
                       \/ (r.auth#"none" /\ r.signedService) \* CONTROL Intent
FactoryBound(r) == r.storeDB#"none" /\ r.authorityDB#"none" /\ r.storeDB=r.authorityDB
PrivateActorOK(r) == r.actor \* CONTROL PrivateActor
CaptureSubject(r) == r.exercise \* CONTROL Credential / Role
ServiceOK(r) == r.service \in {"absent","monitoring"} \* CONTROL Service
RegionSingle(r) == r.regionSingle \* CONTROL RegionMultiplicity
AuthSingle(r) == r.authSingle \* CONTROL AuthMultiplicity
SessionAbsent(r) == ~r.session \* CONTROL Session
DummyFormat(r) == r.auth \in {"none","dummy"} \* CONTROL DummyFormat
DateOK(r) == r.date \* CONTROL Date
SignedRegionOK(r) == r.auth#"dummy" \/ r.signedRegion#"absent" \* CONTROL SignedEmpty
SignedServiceOK(r) == r.auth#"dummy" \/ r.signedService \* CONTROL SignedService
RegionsAgree(r) == r.auth#"dummy" \/ r.region="absent" \/ r.region=r.signedRegion \* CONTROL RegionAgreement
CandidateRegion(r) == IF r.auth="dummy" /\ r.signedRegion#"absent" THEN r.signedRegion
 ELSE IF r.region="absent" THEN "east" ELSE r.region
Metadata(r) == IF ~ServiceOK(r) \/ ~RegionSingle(r) \/ ~AuthSingle(r) \/ ~SessionAbsent(r) THEN "400"
 ELSE IF ~DummyFormat(r) THEN "403"
 ELSE IF ~DateOK(r) \/ ~SignedRegionOK(r) \/ ~SignedServiceOK(r) \/ ~RegionsAgree(r) THEN "400"
 ELSE IF CandidateRegion(r) \notin Regions THEN "400" ELSE "OK"
CallbackOK(r) == r.callbackContract \* CONTROL CallbackContract
PolicyVerdict(doc,action) == IF doc \in {"sqlError","corrupt","oversize"} THEN "error"
 ELSE IF doc="allowAll" \/ (doc="reader" /\ action \in {"List","Get"})
      \/ (doc="listOnly" /\ action="List") \/ (doc="getOnly" /\ action="Get") THEN "allowed"
 ELSE IF doc \in {"deny","mixed"} THEN "explicitDeny" ELSE "implicitDeny"
Lookup(f,subject) == IF subject="reader" THEN f.reader ELSE IF subject="writer" THEN f.writer ELSE Row("missing",0)
VARIABLES id,seed,frame,before,phase,status,subject,region,metadataAccepted,bodyRead,
 wirePrincipal,wireRegion,txOpen,decision,decisionSubject,snapshotVersion,readSource,
 readAttempt,nested,output
vars == <<id,seed,frame,before,phase,status,subject,region,metadataAccepted,bodyRead,
 wirePrincipal,wireRegion,txOpen,decision,decisionSubject,snapshotVersion,readSource,
 readAttempt,nested,output>>
q == Input(id)
PolicySubject == subject \* CONTROL RawPrincipal
PolicyAction == q.action \* CONTROL IAMAction
PolicySource == "borrowed" \* CONTROL OtherDatabase
SelectedRow == IF PolicySource#"other" THEN Lookup(frame,PolicySubject)
               ELSE Row(frame.peer.policy,frame.peer.version) \* CONTROL PreTxSnapshot
NativeRegion == region \* CONTROL LateRegion
NativeRows == IF q.action="List" THEN frame.catalog[NativeRegion] ELSE frame.points[NativeRegion]
NativeReplyKind == IF q.action="List" THEN "catalog" ELSE "statistics"
Effect(f) == IF q.action="Put" THEN [f EXCEPT !.catalog[NativeRegion]=Append(@,7),!.points[NativeRegion]=Append(@,7),!.revision[NativeRegion]=@+1] ELSE f
Init == /\ id \in Ids /\ seed \in 1..2 /\ frame=Seed(id,seed) /\ before=frame
 /\ phase=(IF Input(id).mode="factory" THEN "factory" ELSE "route") /\ status="pending"
 /\ subject="none" /\ region="none" /\ metadataAccepted=FALSE /\ bodyRead=FALSE
 /\ wirePrincipal=Input(id).wirePrincipal /\ wireRegion=Input(id).region
 /\ txOpen=FALSE /\ decision="none" /\ decisionSubject="none"
 /\ snapshotVersion=-1 /\ readSource="none" /\ readAttempt=FALSE /\ nested=FALSE /\ output=NoOutput
Advance(to) == /\ phase'=to
 /\ UNCHANGED <<id,seed,frame,before,status,subject,region,metadataAccepted,bodyRead,
 wirePrincipal,wireRegion,txOpen,decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested,output>>
Finish(code) == /\ phase'="done" /\ status'=code /\ txOpen'=FALSE /\ frame'=frame
 /\ output'=NoOutput \* CONTROL ErrorDisclosure
 /\ UNCHANGED <<id,seed,before,subject,region,metadataAccepted,bodyRead,
 wirePrincipal,wireRegion,decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested>>
FactoryStep == /\ phase="factory"
 /\ Finish(IF FactoryBound(q) THEN "factoryReady" ELSE "factoryRefused")
RouteStep == /\ phase="route"
 /\ (IF ~MonitoringIntent(q) THEN Finish("fallback")
     ELSE IF ~q.handler THEN Finish("501") ELSE Advance("actor"))
ActorStep == /\ phase="actor"
 /\ (IF PrivateActorOK(q)
     THEN /\ subject'=CaptureSubject(q) /\ phase'="metadata"
          /\ UNCHANGED <<id,seed,frame,before,status,region,metadataAccepted,bodyRead,
                        wirePrincipal,wireRegion,txOpen,decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested,output>>
     ELSE Finish("401"))
MetadataStep == /\ phase="metadata"
 /\ (IF Metadata(q)="OK"
     THEN /\ region'=CandidateRegion(q) /\ metadataAccepted'=TRUE /\ phase'="body"
          /\ UNCHANGED <<id,seed,frame,before,status,subject,bodyRead,
                        wirePrincipal,wireRegion,txOpen,decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested,output>>
     ELSE Finish(Metadata(q)))
BodyStep == /\ phase="body" /\ bodyRead'=TRUE /\ phase'="bodyPending"
 /\ UNCHANGED <<id,seed,frame,before,status,subject,region,metadataAccepted,
 wirePrincipal,wireRegion,txOpen,decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested,output>>
PendingEnvironment == /\ phase="bodyPending"
 /\ LET f == IF q.changePolicy="none" THEN frame
             ELSE [frame EXCEPT !.reader=Row(q.changePolicy,@.version+1)]
    IN /\ frame'=f /\ before'=f
 /\ wirePrincipal'=q.latePrincipal
 /\ wireRegion'=(IF q.lateRegion="absent" THEN wireRegion ELSE q.lateRegion)
 /\ phase'="form"
 /\ UNCHANGED <<id,seed,status,subject,region,metadataAccepted,bodyRead,txOpen,
 decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested,output>>
FormStep == /\ phase="form"
 /\ (IF ~q.form THEN Finish("400") ELSE IF ~q.actionKnown THEN Finish("404") ELSE Advance("begin"))
BeginStep == /\ phase="begin" /\ txOpen'=TRUE /\ phase'="authorize"
 /\ UNCHANGED <<id,seed,frame,before,status,subject,region,metadataAccepted,bodyRead,
 wirePrincipal,wireRegion,decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested,output>>
AuthorizeStep == /\ phase="authorize"
 /\ LET row == SelectedRow
        d == IF ~CallbackOK(q) THEN "error" ELSE IF PolicySubject="admin" THEN "allowed"
             ELSE PolicyVerdict(row.document,PolicyAction)
    IN /\ decision'=d /\ decisionSubject'=PolicySubject
       /\ snapshotVersion'=(IF d="error" \/ PolicySubject="admin" THEN -1 ELSE row.version)
       /\ readAttempt'=(CallbackOK(q) /\ PolicySubject#"admin")
       /\ readSource'=PolicySource
       /\ nested'=(PolicySource="nested") \* CONTROL NestedTransaction
       /\ phase'=(IF d="allowed" THEN "semantic" ELSE "done")
       /\ status'=(IF d="allowed" THEN "pending" ELSE IF d="error" THEN "500" ELSE "403")
       /\ txOpen'=(d="allowed") /\ output'=NoOutput
 /\ UNCHANGED <<id,seed,frame,before,subject,region,metadataAccepted,bodyRead,wirePrincipal,wireRegion>>
SemanticStep == /\ phase="semantic"
 /\ (IF q.semantic THEN Advance("commit") ELSE Finish("400"))
CommitStep == /\ phase="commit"
 /\ (IF q.work /\ q.commit
     THEN /\ frame'=Effect(frame) /\ phase'="done" /\ status'="OK" /\ txOpen'=FALSE
          /\ output'=(IF q.action \in {"List","Get"} THEN [kind |-> NativeReplyKind,region |-> NativeRegion,rows |-> NativeRows] ELSE AckOutput)
          /\ UNCHANGED <<id,seed,before,subject,region,metadataAccepted,bodyRead,
                        wirePrincipal,wireRegion,decision,decisionSubject,snapshotVersion,readSource,readAttempt,nested>>
     ELSE Finish("500"))
Next == FactoryStep \/ RouteStep \/ ActorStep \/ MetadataStep \/ BodyStep
 \/ PendingEnvironment \/ FormStep \/ BeginStep \/ AuthorizeStep \/ SemanticStep
 \/ CommitStep \/ (phase="done" /\ UNCHANGED vars)
Spec == Init /\ [][Next]_vars
Docs == {"allowAll","reader","listOnly","getOnly","deny","mixed","missing","sqlError","corrupt","oversize"}
RowType == [document:Docs,version:0..2]
FrameType == [catalog:[Regions->Seq(1..9)],points:[Regions->Seq(1..9)],revision:[Regions->1..3],reader:RowType,writer:RowType,
 peer:[rows:Seq(1..9),policy:Docs,version:0..2]]
TypeOK == /\ id \in Ids /\ seed \in 1..2 /\ frame \in FrameType /\ before \in FrameType
 /\ phase \in {"factory","route","actor","metadata","body","bodyPending","form","begin","authorize","semantic","commit","done"}
 /\ status \in Errors \cup {"pending","OK","fallback","factoryReady","factoryRefused"}
 /\ subject \in {"none","admin","reader","writer"} /\ region \in Regions \cup {"none","moon","absent"}
 /\ decision \in {"none","allowed","implicitDeny","explicitDeny","error"}
 /\ decisionSubject \in {"none","admin","reader","writer"} /\ snapshotVersion \in -1..2
 /\ readSource \in {"none","borrowed","nested","other"}
 /\ (IF output \in {NoOutput,AckOutput,PolicyOutput} THEN TRUE
      ELSE output \in [kind:{"catalog","statistics"},region:Regions,rows:Seq(1..9)])
 /\ metadataAccepted \in BOOLEAN /\ bodyRead \in BOOLEAN /\ txOpen \in BOOLEAN
 /\ readAttempt \in BOOLEAN /\ nested \in BOOLEAN
ActorBeforeBody == bodyRead => q.actor
MetadataAdmissionExact == metadataAccepted => id \in ExpectedMetadataIds
DecisionExact == decision#"none" => decision=ExpectedDecision(id)
NoNestedPolicyTransaction == ~nested
BorrowedPolicySource == readAttempt => readSource="borrowed"
CurrentPolicyRevision == readAttempt /\ decision#"error" => snapshotVersion=(IF id \in {35,36} THEN 1 ELSE 0)
DecisionSubjectExact == decision#"none" => decisionSubject=ExpectedSubject(id)
FailureFrame == status \in Errors \cup {"factoryRefused","fallback"} => frame=before
NoPolicyWrite == frame.reader=before.reader /\ frame.writer=before.writer
PeerFrame == frame.peer=before.peer
EffectRequiresPermission == frame#before => decision="allowed" /\ q.semantic /\ q.work /\ q.commit
ReadFrame == q.action \in {"List","Get"} => frame=before
ExpectedReadKind(i) == IF i \in {3,25,56} THEN "catalog" ELSE "statistics"
ExpectedReadRows(i,f) == IF i \in {3,25,56} THEN f.catalog[ExpectedRegion(i)] ELSE f.points[ExpectedRegion(i)]
ReadReplyExact == status="OK" /\ q.action \in {"List","Get"} => output.rows=ExpectedReadRows(id,before) /\ output.region=ExpectedRegion(id) /\ output.kind=ExpectedReadKind(id)
SuccessfulScopeExact == status="OK" => subject=ExpectedSubject(id) /\ NativeRegion=ExpectedRegion(id)
SuccessfulEffectExact == status="OK" =>
 IF id \in {1,26,27,36,39,41} THEN
  frame.catalog[ExpectedRegion(id)]=Append(before.catalog[ExpectedRegion(id)],7)
  /\ frame.points[ExpectedRegion(id)]=Append(before.points[ExpectedRegion(id)],7)
  /\ frame.revision[ExpectedRegion(id)]=before.revision[ExpectedRegion(id)]+1
  /\ \A r \in Regions \ {ExpectedRegion(id)}:frame.catalog[r]=before.catalog[r] /\ frame.points[r]=before.points[r] /\ frame.revision[r]=before.revision[r]
 ELSE frame=before
NoErrorDisclosure == status \in Errors => output=NoOutput
StatusExact == status#"pending" => status=ExpectedStatus(id)
=============================================================================
