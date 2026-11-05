----------------------- MODULE SakuraDiskAdmission -----------------------
EXTENDS Naturals
CONSTANT None
Actors == {"owner", "wrongOwner", "wrongEnvironment", "wrongProject", "wrongZone", "missing", "operatorPeer", "headerSpoof"}
Markers == {"singleDummy", "missing", "duplicate", "otherCredential"}
Requests == {"read", "write", "unsupported", "malformed", "wrongRouteZone", "canceled"}
Phases == {"initial", "actor", "marker", "admission", "store", "done"}
Statuses == {"pending", "forbidden", "unauthorized", "unsupported", "invalid",
             "notFound", "internal", "success"}
VARIABLES phase, actor, marker, request, found, storageOK,
          status, lookups, dbValue, returned
vars == <<phase,actor,marker,request,found,storageOK,status,lookups,dbValue,returned>>
ActorAllowed(kind) == kind = "owner"
MarkerAllowed(kind) == kind = "singleDummy"
Init == /\ phase = "initial" /\ actor = "owner" /\ marker = "singleDummy"
        /\ request = "read" /\ found = TRUE /\ storageOK = TRUE
        /\ status = "pending" /\ lookups = 0 /\ dbValue = 0 /\ returned = None
Choose(a,m,r,f,ok) ==
 /\ phase = "initial" /\ phase' = "actor"
 /\ actor' = a /\ marker' = m /\ request' = r /\ found' = f /\ storageOK' = ok
 /\ UNCHANGED <<status,lookups,dbValue,returned>>
ActorCheck ==
 /\ phase = "actor"
 /\ phase' = IF ActorAllowed(actor) THEN "marker" ELSE "done"
 /\ status' = IF ActorAllowed(actor) THEN "pending" ELSE "forbidden"
 /\ UNCHANGED <<actor,marker,request,found,storageOK,lookups,dbValue,returned>>
MarkerCheck ==
 /\ phase = "marker"
 /\ phase' = IF MarkerAllowed(marker) THEN "admission" ELSE "done"
 /\ status' = IF MarkerAllowed(marker) THEN "pending" ELSE "unauthorized"
 /\ UNCHANGED <<actor,marker,request,found,storageOK,lookups,dbValue,returned>>
Admit ==
 LET admitted == request \in {"read","write"}
     result == CASE request = "unsupported" -> "unsupported"
        [] request = "malformed" -> "invalid"
        [] request = "wrongRouteZone" -> "notFound"
        [] request = "canceled" -> "internal" [] OTHER -> "pending"
 IN /\ phase = "admission"
    /\ phase' = IF admitted THEN "store" ELSE "done"
    /\ status' = result
    /\ UNCHANGED <<actor,marker,request,found,storageOK,lookups,dbValue,returned>>
Store ==
 LET success == storageOK /\ found
 IN /\ phase = "store" /\ phase' = "done"
    /\ lookups' = lookups + 1
    /\ status' = IF ~storageOK THEN "internal" ELSE IF ~found THEN "notFound" ELSE "success"
    /\ dbValue' = IF success /\ request = "write" THEN 1 ELSE dbValue
    /\ returned' = IF success /\ request = "read" THEN "private-metadata" ELSE None
    /\ UNCHANGED <<actor,marker,request,found,storageOK>>
Next == (\E a \in Actors, m \in Markers, r \in Requests,
            f \in BOOLEAN, ok \in BOOLEAN : Choose(a,m,r,f,ok)) \/
        ActorCheck \/ MarkerCheck \/ Admit \/ Store
TypeOK == /\ phase \in Phases /\ actor \in Actors /\ marker \in Markers
          /\ request \in Requests /\ found \in BOOLEAN /\ storageOK \in BOOLEAN
          /\ status \in Statuses /\ lookups \in 0..1 /\ dbValue \in 0..1
ActorBeforeDispatch == phase \in {"marker","admission","store"} => actor = "owner"
MarkerBeforeDispatch == phase \in {"admission","store"} => marker = "singleDummy"
NoUnauthorizedLookup == lookups > 0 => actor = "owner" /\ marker = "singleDummy"
DeniedFrame ==
 (phase = "done" /\ (actor # "owner" \/ marker # "singleDummy")) =>
 /\ dbValue = 0 /\ lookups = 0 /\ returned = None
AdmissionBeforeLookup == lookups > 0 => request \in {"read","write"}
UnsupportedNoMutation ==
 request \notin {"read","write"} => dbValue = 0 /\ returned = None
FailureNoDisclosure == (phase = "done" /\ status # "success") => returned = None /\ dbValue = 0
ActorFailureStatus == (phase = "done" /\ actor # "owner") => status = "forbidden"
MarkerFailureStatus ==
 (phase = "done" /\ actor = "owner" /\ marker # "singleDummy") => status = "unauthorized"
ConcreteResult ==
 (phase = "done" /\ actor = "owner" /\ marker = "singleDummy" /\ request \in {"read","write"}) =>
 /\ status = (IF ~storageOK THEN "internal" ELSE IF ~found THEN "notFound" ELSE "success")
 /\ (status = "success" /\ request = "read" => returned = "private-metadata")
 /\ (status = "success" /\ request = "write" => dbValue = 1)
Spec == Init /\ [][Next]_vars
=============================================================================
