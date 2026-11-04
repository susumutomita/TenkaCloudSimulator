----------------------- MODULE KMSProtocolAdmission -----------------------
EXTENDS Naturals
CONSTANT NoValue
Requests == [target: {"KMS","Other","None"},
 hint: {"KMS","Other","None"}, signing: {"KMS","Other","None"},
 lookup: {"Present","Missing","Error","Canceled"},
 credentialValid: BOOLEAN, regionMatches: BOOLEAN, mutates: BOOLEAN]
VARIABLES request,phase,route,policyLive,permit,captured,flips,status,visible,resource
vars == <<request,phase,route,policyLive,permit,captured,flips,status,visible,resource>>
SelectKMS(q) == q.target="KMS" \/ q.hint="KMS" \/ q.signing="KMS"
\* ExpectedMetadataOK is used only by invariants, not the admission actions.
ExpectedMetadataOK == request.hint#"Other" /\ request.signing#"Other" /\
 (request.signing#"KMS" \/ request.credentialValid) /\ request.regionMatches
Init ==
 /\ request \in Requests /\ policyLive \in BOOLEAN /\ phase="Select"
 /\ route=NoValue /\ permit=NoValue /\ captured=NoValue /\ flips=0
 /\ status=NoValue /\ visible=FALSE /\ resource=0
Select ==
 /\ phase="Select"
 /\ route'=IF SelectKMS(request) THEN "KMS" ELSE "Other"
 /\ phase'=IF SelectKMS(request) THEN "Metadata" ELSE "Done"
 /\ UNCHANGED <<request,policyLive,permit,captured,flips,status,visible,resource>>
Metadata ==
 LET code == IF request.hint="Other" THEN 400
       ELSE IF request.signing="Other" \/ (request.signing="KMS" /\ ~request.credentialValid) THEN 403
       ELSE IF ~request.regionMatches THEN 400 ELSE NoValue
 IN /\ phase="Metadata"
    /\ status'=code /\ phase'=IF code=NoValue THEN "Lookup" ELSE "Done"
    /\ UNCHANGED <<request,route,policyLive,permit,captured,flips,visible,resource>>
Lookup ==
 /\ phase="Lookup"
 /\ permit'=IF request.lookup="Present" THEN policyLive ELSE permit
 /\ captured'=IF request.lookup="Present" THEN policyLive ELSE captured
 /\ status'=IF request.lookup="Present" THEN NoValue ELSE IF request.lookup="Missing" THEN 403 ELSE 500
 /\ phase'=IF request.lookup="Present" THEN "Execute" ELSE "Done"
 /\ UNCHANGED <<request,route,policyLive,flips,visible,resource>>
FlipPolicy ==
 /\ phase="Execute" /\ flips=0
 /\ policyLive'=~policyLive /\ flips'=1
 /\ UNCHANGED <<request,phase,route,permit,captured,status,visible,resource>>
Execute ==
 LET targetOK == request.target="KMS"
     allowed == permit=TRUE
     code == IF ~targetOK THEN 400 ELSE IF allowed THEN 200 ELSE 403
 IN /\ phase="Execute"
    /\ status'=code /\ visible'=(code=200)
    /\ resource'=IF code=200 /\ request.mutates THEN 1 ELSE resource
    /\ phase'="Done"
    /\ UNCHANGED <<request,route,policyLive,permit,captured,flips>>
Terminal == phase="Done" /\ UNCHANGED vars
Next == Select \/ Metadata \/ Lookup \/ FlipPolicy \/ Execute \/ Terminal
TypeOK ==
 /\ request \in Requests /\ policyLive \in BOOLEAN
 /\ phase \in {"Select","Metadata","Lookup","Execute","Done"}
 /\ route \in {NoValue,"KMS","Other"} /\ permit \in BOOLEAN \cup {NoValue}
 /\ captured \in BOOLEAN \cup {NoValue} /\ flips \in 0..1
 /\ status \in {NoValue,200,400,403,500} /\ visible \in BOOLEAN /\ resource \in 0..1
NoCrossServiceFallback ==
 (phase#"Select" /\ (request.target="KMS" \/ request.hint="KMS" \/ request.signing="KMS")) => route="KMS"
NoAuthorityFromWireMetadata ==
 /\ ((phase \in {"Execute","Done"} /\ captured \in BOOLEAN) => permit=captured)
 /\ ((phase="Done" /\ route="KMS" /\ ExpectedMetadataOK /\ request.lookup="Present" /\ request.target="KMS") =>
       status=(IF captured=TRUE THEN 200 ELSE 403))
 /\ ((phase="Done" /\ route="KMS" /\ status=200) => captured=TRUE /\ visible)
 /\ ((phase="Done" /\ route="KMS" /\ status#200) => ~visible /\ resource=0)
InfraNotDeny ==
 (phase="Done" /\ route="KMS" /\ ExpectedMetadataOK /\ request.lookup \in {"Error","Canceled"}) =>
 /\ status=500 /\ ~visible /\ resource=0
MissingIdentityDenied ==
 (phase="Done" /\ route="KMS" /\ ExpectedMetadataOK /\ request.lookup="Missing") =>
 /\ status=403 /\ ~visible /\ resource=0
NoSuccessWithoutKMSTarget ==
 /\ ((phase="Done" /\ route="KMS" /\ status=200) => request.target="KMS" /\ ExpectedMetadataOK)
 /\ ((phase="Done" /\ route="KMS" /\ request.hint="Other") => status=400)
 /\ ((phase="Done" /\ route="KMS" /\ request.hint#"Other" /\
       (request.signing="Other" \/ (request.signing="KMS" /\ ~request.credentialValid))) => status=403)
 /\ ((phase="Done" /\ route="KMS" /\ request.hint#"Other" /\ request.signing#"Other" /\
       (request.signing#"KMS" \/ request.credentialValid) /\ ~request.regionMatches) => status=400)
 /\ ((phase="Done" /\ route="KMS" /\ ExpectedMetadataOK /\ request.lookup="Present" /\ request.target#"KMS") => status=400)
=============================================================================
