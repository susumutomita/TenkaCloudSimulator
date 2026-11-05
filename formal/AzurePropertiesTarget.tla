---------------------- MODULE AzurePropertiesTarget ----------------------
EXTENDS Naturals, FiniteSets, TLC
VARIABLES phase, selected, request, detail
vars == <<phase, selected, request, detail>>
None == "none"
Container(n) == [kind |-> "container", parent |-> None, name |-> n]
Blob(p,n) == [kind |-> "blob", parent |-> p, name |-> n]
Targets == {Container("same"), Container("other"),
            Blob("same","same"), Blob("other","same")}
CommonFields == {"name","etag","modified","metadata"}
BlobFields == {"size","type","contentType"}
Identity(t) == <<t.kind,t.parent,t.name>> (* CompleteTypedIdentity *)
Route(t) == [container |-> IF t.kind="container" THEN t.name ELSE t.parent,
             blob |-> IF t.kind="blob" THEN t.name ELSE None,
             restype |-> IF t.kind="container" THEN "container" ELSE None]
(* Independent literal native-route oracle, not reuse of Route. *)
ExpectedRoute(t) ==
 CASE t=Container("same") -> [container |-> "same",blob |-> None,restype |-> "container"]
   [] t=Container("other") -> [container |-> "other",blob |-> None,restype |-> "container"]
   [] t=Blob("same","same") -> [container |-> "same",blob |-> "same",restype |-> None]
   [] OTHER -> [container |-> "other",blob |-> "same",restype |-> None]
Fields(t) == IF t.kind="blob" THEN CommonFields \cup BlobFields ELSE CommonFields
EmptyRequest == [present |-> FALSE, target |-> Container("same"),
                 identity |-> Identity(Container("same")),
                 route |-> ExpectedRoute(Container("same")), method |-> "HEAD"]
EmptyDetail == [present |-> FALSE, provenance |-> Container("same"),
                kind |-> "container", name |-> "same", fields |-> {}]

Init == /\ phase="Idle" /\ selected=Container("same")
        /\ request=EmptyRequest /\ detail=EmptyDetail

Open(t) == /\ phase \in {"Idle","Done"}
           /\ selected'=t /\ phase'="Selected"
           /\ request'=EmptyRequest /\ detail'=EmptyDetail

Issue == /\ phase="Selected"
         /\ request'=[present |-> TRUE,target |-> selected,
                      identity |-> Identity(selected),route |-> Route(selected),
                      method |-> "HEAD"] (* NativeReadOnly *)
         /\ phase'="Waiting"
         /\ UNCHANGED <<selected,detail>>

(* A successful parsed response is assumed to belong to the captured request.
   provenance is ghost URL/closure identity, NOT an actual detail.parent field. *)
Complete(ok) ==
 /\ phase="Waiting"
 /\ detail'=IF ok THEN
       [present |-> TRUE,provenance |-> request.target,
        kind |-> request.target.kind,name |-> request.target.name,
        fields |-> Fields(request.target)]
      ELSE EmptyDetail
 /\ phase'="Done"
 /\ UNCHANGED <<selected,request>>

Close == /\ phase \in {"Selected","Done"}
         /\ phase'="Idle" /\ request'=EmptyRequest /\ detail'=EmptyDetail
         /\ UNCHANGED selected

Next == \/ \E t \in Targets : Open(t)
        \/ Issue \/ \E ok \in BOOLEAN : Complete(ok) \/ Close

TypeOK == /\ phase \in {"Idle","Selected","Waiting","Done"}
          /\ selected \in Targets /\ request.target \in Targets
          /\ request.present \in BOOLEAN /\ request.method \in {"HEAD","GET"}
          /\ request.identity \in {<<t.kind,t.parent,t.name>> : t \in Targets} \cup {<<t.name>> : t \in Targets}
          /\ request.route \in [container : {"same","other"}, blob : {None,"same"}, restype : {None,"container"}]
          /\ detail.present \in BOOLEAN /\ detail.provenance \in Targets
          /\ detail.kind \in {"container","blob"} /\ detail.name \in {"same","other"}
          /\ detail.fields \subseteq CommonFields \cup BlobFields
NativeHeadOnly == request.present => request.method="HEAD"
RequestIdentityExact == request.present =>
 /\ request.target=selected
 /\ request.identity = <<selected.kind,selected.parent,selected.name>>
RouteMatchesTarget == request.present => request.route=ExpectedRoute(selected)
DistinctTargetCarriers == request.present =>
 \A a,b \in Targets : Route(a)=Route(b) => a=b
DetailCapturedIdentity == detail.present =>
 /\ detail.provenance=request.target /\ detail.kind=request.target.kind
 /\ detail.name=request.target.name
FieldShapeExact == detail.present =>
 detail.fields=IF request.target.kind="container" THEN CommonFields ELSE CommonFields \cup BlobFields
(* Actual rendering checks kind against the active property-pane kind.
   No independently detectable dropped-guard control is claimed. *)
Visible == detail.present /\ detail.kind=selected.kind
Spec == Init /\ [][Next]_vars
=============================================================================
