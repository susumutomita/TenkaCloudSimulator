----------------------- MODULE SakuraDiskAdmission -----------------------
EXTENDS Naturals
CONSTANT None
Actors == {"owner", "wrongOwner", "wrongEnvironment", "wrongProject", "wrongZone", "missing", "operatorPeer", "headerSpoof"}
Markers == {"singleDummy", "missing", "duplicate", "otherCredential"}
Requests == {"read", "write", "unsupported", "malformed", "wrongRouteZone", "canceled"}
Phases == {"initial", "mapping", "actor", "marker", "admission", "store", "done"}
Statuses == {"pending", "forbidden", "unauthorized", "unsupported", "invalid", "notFound", "internal", "success"}
Modes == {"factory", "directLeaf"}
CommonCases == {"participantOwner", "participantPeer", "operatorOwner", "operatorPeer", "credentialOwnerExercisePeer", "participantMismatch", "missing", "invalidRole"}
Credential(k) == CASE k \in {"participantOwner","credentialOwnerExercisePeer"} -> "owner"
 [] k \in {"participantPeer","participantMismatch"} -> "peer"
 [] k \in {"operatorOwner","operatorPeer","invalidRole"} -> "host" [] OTHER -> None
Exercise(k) == CASE k \in {"participantOwner","operatorOwner","participantMismatch","invalidRole"} -> "owner"
 [] k \in {"participantPeer","operatorPeer","credentialOwnerExercisePeer"} -> "peer" [] OTHER -> None
Role(k) == CASE k \in {"participantOwner","participantPeer","participantMismatch"} -> "participant"
 [] k \in {"operatorOwner","operatorPeer","credentialOwnerExercisePeer"} -> "operator"
 [] k = "invalidRole" -> "invalid" [] OTHER -> None
ContextValid(k) == k \notin {"participantMismatch","missing","invalidRole"}
VARIABLES phase, actor, marker, request, found, storageOK, status, lookups, dbValue, returned,
          mode, commonCase, rawSubject, rawRealm, mappedSubject, mappedRealm, mappingDone
vars == <<phase,actor,marker,request,found,storageOK,status,lookups,dbValue,returned,
          mode,commonCase,rawSubject,rawRealm,mappedSubject,mappedRealm,mappingDone>>
ActorAllowed(kind) == kind = "owner"
MarkerAllowed(kind) == kind = "singleDummy"
LeafClass(subject,realm) == IF subject = None THEN "missing"
 ELSE IF realm # "configured" THEN "wrongEnvironment"
 ELSE IF subject = "owner" THEN "owner" ELSE "wrongOwner"
Init == /\ phase = "initial" /\ actor = "owner" /\ marker = "singleDummy"
        /\ request = "read" /\ found = TRUE /\ storageOK = TRUE
        /\ status = "pending" /\ lookups = 0 /\ dbValue = 0 /\ returned = None
        /\ mode = "directLeaf" /\ commonCase = "participantOwner"
        /\ rawSubject = "owner" /\ rawRealm = "configured"
        /\ mappedSubject = None /\ mappedRealm = None /\ mappingDone = FALSE
Choose(md,k,a,hs,hr,m,r,f,ok) ==
 /\ phase = "initial"
 /\ phase' = IF md = "factory" THEN "mapping" ELSE "actor"
 /\ mode' = md /\ commonCase' = k /\ rawSubject' = hs /\ rawRealm' = hr
 /\ actor' = IF md = "factory" THEN "missing" ELSE a
 /\ marker' = m /\ request' = r /\ found' = f /\ storageOK' = ok
 /\ UNCHANGED <<status,lookups,dbValue,returned,mappedSubject,mappedRealm,mappingDone>>
MapHostActor ==
 LET valid == ContextValid(commonCase)
     leafSubject == IF valid THEN Exercise(commonCase) ELSE None
     leafRealm == IF valid THEN "configured" ELSE None
 IN /\ phase = "mapping" /\ phase' = "actor" /\ mappingDone' = TRUE
    /\ mappedSubject' = leafSubject /\ mappedRealm' = leafRealm
    /\ actor' = LeafClass(leafSubject,leafRealm)
    /\ UNCHANGED <<marker,request,found,storageOK,status,lookups,dbValue,returned,
                   mode,commonCase,rawSubject,rawRealm>>
ActorCheck ==
 /\ phase = "actor"
 /\ phase' = IF ActorAllowed(actor) THEN "marker" ELSE "done"
 /\ status' = IF ActorAllowed(actor) THEN "pending" ELSE "forbidden"
 /\ UNCHANGED <<actor,marker,request,found,storageOK,lookups,dbValue,returned,
                mode,commonCase,rawSubject,rawRealm,mappedSubject,mappedRealm,mappingDone>>
MarkerCheck ==
 /\ phase = "marker"
 /\ phase' = IF MarkerAllowed(marker) THEN "admission" ELSE "done"
 /\ status' = IF MarkerAllowed(marker) THEN "pending" ELSE "unauthorized"
 /\ UNCHANGED <<actor,marker,request,found,storageOK,lookups,dbValue,returned,
                mode,commonCase,rawSubject,rawRealm,mappedSubject,mappedRealm,mappingDone>>
Admit ==
 LET admitted == request \in {"read","write"}
     result == CASE request = "unsupported" -> "unsupported"
        [] request = "malformed" -> "invalid" [] request = "wrongRouteZone" -> "notFound"
        [] request = "canceled" -> "internal" [] OTHER -> "pending"
 IN /\ phase = "admission"
    /\ phase' = (IF admitted THEN "store" ELSE "done")
    /\ status' = result
    /\ UNCHANGED <<actor,marker,request,found,storageOK,lookups,dbValue,returned,
                   mode,commonCase,rawSubject,rawRealm,mappedSubject,mappedRealm,mappingDone>>
Store ==
 LET success == storageOK /\ found
 IN /\ phase = "store" /\ phase' = "done" /\ lookups' = lookups + 1
    /\ status' = IF ~storageOK THEN "internal" ELSE IF ~found THEN "notFound" ELSE "success"
    /\ dbValue' = IF success /\ request = "write" THEN 1 ELSE dbValue
    /\ returned' = IF success /\ request = "read" THEN "private-metadata" ELSE None
    /\ UNCHANGED <<actor,marker,request,found,storageOK,
                   mode,commonCase,rawSubject,rawRealm,mappedSubject,mappedRealm,mappingDone>>
Next == (\E k \in CommonCases, hs \in {"owner","peer"}, hr \in {"configured","other"},
            m \in Markers, r \in Requests, f \in BOOLEAN, ok \in BOOLEAN :
            Choose("factory",k,"missing",hs,hr,m,r,f,ok)) \/
        (\E a \in Actors, m \in Markers, r \in Requests, f \in BOOLEAN, ok \in BOOLEAN :
            Choose("directLeaf","participantOwner",a,"owner","configured",m,r,f,ok)) \/
        MapHostActor \/ ActorCheck \/ MarkerCheck \/ Admit \/ Store
TypeOK == /\ phase \in Phases /\ actor \in Actors /\ marker \in Markers
          /\ request \in Requests /\ found \in BOOLEAN /\ storageOK \in BOOLEAN
          /\ status \in Statuses /\ lookups \in 0..1 /\ dbValue \in 0..1
          /\ mode \in Modes /\ commonCase \in CommonCases
          /\ rawSubject \in {"owner","peer"} /\ rawRealm \in {"configured","other"}
          /\ mappedSubject \in {None,"owner","peer","host"}
          /\ mappedRealm \in {None,"configured","other"} /\ mappingDone \in BOOLEAN
MappedSubjectUsesExerciseIdentity ==
 (mode = "factory" /\ mappingDone /\ ContextValid(commonCase)) => mappedSubject = Exercise(commonCase)
MappedRealmUsesCopiedHostConfig ==
 (mode = "factory" /\ mappingDone /\ ContextValid(commonCase)) => mappedRealm = "configured"
NoDefaultActorOnExtractionFailure ==
 (mode = "factory" /\ mappingDone /\ ~ContextValid(commonCase)) =>
 mappedSubject = None /\ mappedRealm = None /\ actor = "missing"
WireHeadersDoNotMapAuthority ==
 (mode = "factory" /\ mappingDone) => actor =
 (IF commonCase \in {"participantOwner","operatorOwner"} THEN "owner"
  ELSE IF commonCase \in {"participantPeer","operatorPeer","credentialOwnerExercisePeer"} THEN "wrongOwner"
  ELSE "missing")
ActorBeforeDispatch == phase \in {"marker","admission","store"} => actor = "owner"
MarkerBeforeDispatch == phase \in {"admission","store"} => marker = "singleDummy"
NoUnauthorizedLookup == lookups > 0 => actor = "owner" /\ marker = "singleDummy"
DeniedFrame == (phase = "done" /\ (actor # "owner" \/ marker # "singleDummy")) =>
 /\ dbValue = 0 /\ lookups = 0 /\ returned = None
AdmissionBeforeLookup == lookups > 0 => request \in {"read","write"}
UnsupportedNoMutation == request \notin {"read","write"} => dbValue = 0 /\ returned = None
FailureNoDisclosure == (phase = "done" /\ status # "success") => returned = None /\ dbValue = 0
ActorFailureStatus == (phase = "done" /\ actor # "owner") => status = "forbidden"
MarkerFailureStatus == (phase = "done" /\ actor = "owner" /\ marker # "singleDummy") => status = "unauthorized"
ConcreteResult ==
 (phase = "done" /\ actor = "owner" /\ marker = "singleDummy" /\ request \in {"read","write"}) =>
 /\ status = (IF ~storageOK THEN "internal" ELSE IF ~found THEN "notFound" ELSE "success")
 /\ (status = "success" /\ request = "read" => returned = "private-metadata")
 /\ (status = "success" /\ request = "write" => dbValue = 1)
Spec == Init /\ [][Next]_vars
=============================================================================
