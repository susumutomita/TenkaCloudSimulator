------------------------- MODULE KMSAliasCatalog -------------------------
EXTENDS Naturals, FiniteSets, Sequences
CONSTANT MaxRequests
VARIABLES step, catalog, created, revisions, marker, last, reply
vars == <<step,catalog,created,revisions,marker,last,reply>>
Regions == 1..2
Names == 1..3
Keys == 1..2
Slots == Regions \X Names
Outcomes == {"ok","ignored","commitfail"}
(* The native authorizer is a captured pure decision on action+resource.
   Old/new names referring to one concrete key MUST share one decision. All four
   two-key maps are retained; alias-resource permission is independent. *)
Permissions == {[alias |-> a,keys |-> keyMap] : a \in BOOLEAN,keyMap \in [Keys -> BOOLEAN]}
AllPermit == [alias |-> TRUE,keys |-> [k \in Keys |-> TRUE]]
Empty == [s \in Slots |-> 0]
SeedOne == [s \in Slots |-> IF s = <<1,1>> THEN 1 ELSE 0]
SeedTwo == [s \in Slots |-> CASE s = <<1,1>> -> 1 [] s = <<1,2>> -> 2 [] OTHER -> 0]
SeedRegions == [s \in Slots |-> IF s = <<1,1>> \/ s = <<2,1>> THEN 1 ELSE 0]
Seeds == {Empty,SeedOne,SeedTwo,SeedRegions}
Count(c) == Cardinality({s \in Slots : c[s]#0})
KeyCount(c,r,k) == Cardinality({n \in Names : c[<<r,n>>]=k})
Frame == [catalog |-> catalog,created |-> created,revisions |-> revisions]
EmptyMarker == [exists |-> FALSE,authority |-> 1,integrity |-> TRUE,
                region |-> 1,filter |-> 0,limit |-> 1,revision |-> 0,after |-> 0]
EmptyReply == [success |-> FALSE,page |-> <<>>,more |-> FALSE]
Init == /\ step=0 /\ catalog \in Seeds
        /\ created=[s \in Slots |-> IF catalog[s]=0 THEN 0 ELSE s[2]]
        /\ revisions=[r \in Regions |-> Cardinality({n \in Names : catalog[<<r,n>>]#0})]
        /\ marker=EmptyMarker /\ last=[attempted |-> FALSE] /\ reply=EmptyReply

KeyPermit(p,k) == IF k \in Keys THEN p.keys[k] ELSE FALSE
Allowed(op,p,old,k) == CASE op="create" -> p.alias /\ KeyPermit(p,k)
                        [] op="update" -> p.alias /\ KeyPermit(p,old) /\ KeyPermit(p,k)
                        [] OTHER -> p.alias /\ KeyPermit(p,old)
ExpectedPermit(op,p,old,k) == CASE op="create" -> p.alias /\ KeyPermit(p,k)
                               [] op="update" -> p.alias /\ KeyPermit(p,old) /\ KeyPermit(p,k)
                               [] OTHER -> p.alias /\ KeyPermit(p,old)
Concrete(kind) == kind="concrete"
GlobalRoom(op,c) == Count(c)+(IF op="create" THEN 1 ELSE 0)<=2
KeyRoom(c,r,k) == KeyCount(c,r,k)+1<=1
Capacity(op,c,r,n,k) == IF op="delete" \/ (op="update" /\ c[<<r,n>>]=k)
                       THEN TRUE ELSE GlobalRoom(op,c) /\ KeyRoom(c,r,k)
ValidMutation(op,r,n,k,kind,p) ==
    Allowed(op,p,catalog[<<r,n>>],k) /\ (IF op="create" THEN catalog[<<r,n>>]=0 ELSE catalog[<<r,n>>]#0)
    /\ (op="delete" \/ Concrete(kind)) /\ Capacity(op,catalog,r,n,k)
WriteRegion(r) == r
NextCatalog(op,r,n,k) == [catalog EXCEPT ![<<WriteRegion(r),n>>]=IF op="delete" THEN 0 ELSE k]
NextCreated(op,r,n) == [created EXCEPT ![<<r,n>>]=
                       CASE op="delete" -> 0 [] op="create" -> step+3 [] OTHER -> @]
Committed(admitted,outcome) == admitted /\ outcome="ok"
RevisionAfter(r,committed) == IF committed THEN [revisions EXCEPT ![r]=@+1] ELSE revisions
(* Failed request identity is deliberately not retained. Only the actual before
   frame and operation category remain; no checked invariant reads failed input
   identities. Native error priority/message parsing is outside this model. *)
Mutation(op,r,n,k,kind,p,outcome) ==
 /\ step<MaxRequests
 /\ LET admitted == ValidMutation(op,r,n,k,kind,p)
        committed == Committed(admitted,outcome)
        nextCatalog == IF committed THEN NextCatalog(op,r,n,k) ELSE catalog
        nextCreated == IF committed THEN NextCreated(op,r,n) ELSE created
        nextRevisions == RevisionAfter(r,committed)
    IN /\ catalog'=nextCatalog /\ created'=nextCreated /\ revisions'=nextRevisions
       /\ reply'=[success |-> committed,page |-> <<>>,more |-> FALSE]
       /\ last'=[attempted |-> TRUE,read |-> FALSE,outcome |-> outcome,
           before |-> Frame,
           op |-> IF committed THEN op ELSE "create",
           region |-> IF committed THEN r ELSE 1,name |-> IF committed THEN n ELSE 1,
           target |-> IF committed THEN k ELSE 1,kind |-> IF committed THEN kind ELSE "concrete",
           permit |-> IF committed THEN p ELSE AllPermit]
 /\ step'=step+1 /\ UNCHANGED marker

(* Selected-key MAC validity is abstract. Native secrets are per-region.
   Under these four reachable seeds a foreign region also mismatches revision,
   so dropping only BoundRegion is not an independent sensitivity control.
   BypassMACIntegrity instead accepts a same-body/current-revision tamper. *)
ValidMAC(t) == t.authority=1 /\ t.integrity
BoundRevision(t,r) == t.revision=revisions[r]
BoundRegion(t,r) == t.region=r
BoundFilter(t,f) == t.filter=f
BoundLimit(t,l) == t.limit=l
Usable(t,r,f,l) == ~t.exists \/ (ValidMAC(t) /\ BoundRevision(t,r)
                       /\ BoundRegion(t,r) /\ BoundFilter(t,f) /\ BoundLimit(t,l))
CandidateNames(c,r,f,after) == SelectSeq(<<1,2,3>>,
    LAMBDA n : c[<<r,n>>]#0 /\ n>after /\ (f=0 \/ c[<<r,n>>]=f))
Page(c,r,f,after,l) == LET names == CandidateNames(c,r,f,after)
                     IN SubSeq(names,1,IF Len(names)<l THEN Len(names) ELSE l)
OracleSet(c,r,f,after) == {n \in Names : c[<<r,n>>]#0 /\ n>after /\ (f=0 \/ c[<<r,n>>]=f)}
OraclePage(c,r,f,after,l) == SelectSeq(<<1,2,3>>,LAMBDA n :
    n \in OracleSet(c,r,f,after) /\ Cardinality({m \in OracleSet(c,r,f,after) : m<n})<l)
ReadPage(admitted,outcome,page) == IF admitted /\ outcome="ok" THEN page ELSE <<>>
Cursor(mode) == CASE mode="first" -> EmptyMarker
                 [] mode="foreign" -> [marker EXCEPT !.authority=2]
                 [] mode="tamper" -> [marker EXCEPT !.integrity=FALSE]
                 [] OTHER -> marker
Listing(mode,r,f,l,allowed,outcome) ==
 /\ step<MaxRequests /\ (mode="first" \/ marker.exists)
 /\ LET t == Cursor(mode)
        admitted == allowed /\ Usable(t,r,f,l)
        after == IF t.exists THEN t.after ELSE 0
        rawPage == Page(catalog,r,f,after,l)
        page == ReadPage(admitted,outcome,rawPage)
        success == admitted /\ outcome="ok"
        more == success /\ Len(CandidateNames(catalog,r,f,after))>l
        newMarker == IF more THEN [exists |-> TRUE,authority |-> 1,integrity |-> TRUE,
                     region |-> r,filter |-> f,limit |-> l,revision |-> revisions[r],
                     after |-> page[Len(page)]] ELSE marker
    IN /\ marker'=newMarker
       /\ reply'=[success |-> success,page |-> page,more |-> more]
       /\ last'=[attempted |-> TRUE,read |-> TRUE,outcome |-> outcome,
           before |-> Frame,
           allowed |-> IF success THEN allowed ELSE FALSE,
           used |-> IF success THEN t.exists ELSE FALSE,
           cursor |-> IF success THEN t ELSE EmptyMarker,
           region |-> IF success THEN r ELSE 1,filter |-> IF success THEN f ELSE 0,
           limit |-> IF success THEN l ELSE 1,afterName |-> IF success THEN after ELSE 0]
 /\ step'=step+1 /\ UNCHANGED <<catalog,created,revisions>>
Next == (step<MaxRequests /\ (\E op \in {"create","update","delete"},r \in Regions,n \in Names,k \in Keys,
              kind \in {"concrete","alias"},p \in Permissions,outcome \in Outcomes : Mutation(op,r,n,k,kind,p,outcome)))
        \/ (step<MaxRequests /\ (\E mode \in {"first","saved","foreign","tamper"},r \in Regions,
              f \in 0..2,l \in 1..2,allowed \in BOOLEAN,outcome \in {"ok","readfail"} : Listing(mode,r,f,l,allowed,outcome)))
        \/ UNCHANGED vars
TypeOK == /\ step \in 0..MaxRequests /\ catalog \in [Slots -> 0..2]
          /\ created \in [Slots -> 0..5] /\ revisions \in [Regions -> 0..5]
          /\ marker.exists \in BOOLEAN /\ marker.authority \in 1..2
          /\ marker.integrity \in BOOLEAN /\ marker.region \in Regions
          /\ marker.filter \in 0..2 /\ marker.limit \in 1..2
          /\ marker.revision \in 0..5 /\ marker.after \in 0..3
          /\ reply.success \in BOOLEAN /\ reply.page \in Seq(Names) /\ reply.more \in BOOLEAN
AcknowledgedSuccess == IF last.attempted THEN reply.success => last.outcome="ok" ELSE TRUE
MutationAuthority == IF last.attempted THEN
    ~last.read /\ reply.success => ExpectedPermit(last.op,last.permit,last.before.catalog[<<last.region,last.name>>],last.target) ELSE TRUE
ConcreteTarget == IF last.attempted THEN
    ~last.read /\ reply.success /\ last.op#"delete" => last.kind="concrete" ELSE TRUE
FailureFrame == IF last.attempted THEN ~reply.success => Frame=last.before ELSE TRUE
PeerRegionFrame == IF last.attempted THEN ~last.read /\ reply.success =>
    \A r \in Regions \ {last.region},n \in Names :
       catalog[<<r,n>>]=last.before.catalog[<<r,n>>] /\ created[<<r,n>>]=last.before.created[<<r,n>>]
       /\ revisions[r]=last.before.revisions[r] ELSE TRUE
MutationEffect == IF last.attempted THEN ~last.read /\ reply.success =>
    catalog[<<last.region,last.name>>]=(IF last.op="delete" THEN 0 ELSE last.target) ELSE TRUE
CreationIdentity == IF last.attempted THEN ~last.read /\ reply.success /\ last.op="update" =>
    created[<<last.region,last.name>>]=last.before.created[<<last.region,last.name>>] ELSE TRUE
AdmissionBudgets == IF last.attempted THEN ~last.read /\ reply.success =>
    Count(catalog)<=2 /\ (last.op="delete" \/ KeyCount(catalog,last.region,last.target)<=1) ELSE TRUE
RevisionFresh == IF last.attempted THEN ~last.read /\ reply.success =>
    revisions[last.region]=last.before.revisions[last.region]+1 ELSE TRUE
ListAuthorized == IF last.attempted THEN last.read /\ reply.success => last.allowed ELSE TRUE
CursorBound == IF last.attempted THEN last.read /\ reply.success /\ last.used =>
    last.cursor.authority=1 /\ last.cursor.integrity /\ last.cursor.region=last.region
    /\ last.cursor.filter=last.filter /\ last.cursor.limit=last.limit
    /\ last.cursor.revision=last.before.revisions[last.region] ELSE TRUE
ListPageExact == IF last.attempted THEN last.read /\ reply.success =>
    reply.page=OraclePage(last.before.catalog,last.region,last.filter,last.afterName,last.limit) ELSE TRUE
ListMoreExact == IF last.attempted THEN last.read /\ reply.success =>
    reply.more=(Cardinality(OracleSet(last.before.catalog,last.region,last.filter,last.afterName))>last.limit) ELSE TRUE
ContinuationAfterLast == IF last.attempted THEN last.read /\ reply.more =>
    marker.after=reply.page[Len(reply.page)] ELSE TRUE
ReadFrame == IF last.attempted THEN last.read => Frame=last.before ELSE TRUE
NoPartialRead == IF last.attempted THEN last.read /\ ~reply.success => reply.page = <<>> /\ ~reply.more ELSE TRUE
=============================================================================
