------------------------ MODULE SecretVersionListing ------------------------
EXTENDS Naturals, FiniteSets
CONSTANT MaxSteps
VARIABLES step, active, generation, versions, current, previous, token, last
vars == <<step,active,generation,versions,current,previous,token,last>>
Keys == 1..4
Kinds == {"First","Next","OtherDB","OtherRegion","Filter","Domain","Tamper","Expired"}
Frame == [active |-> active,gen |-> generation,versions |-> versions,
          current |-> current,previous |-> previous]
FirstKey(S) == IF S={} THEN {} ELSE {CHOOSE x \in S : \A y \in S : x<=y}
Labels == {current} \cup (IF previous=0 THEN {} ELSE {previous})
IsPrimary(db,region) == db=1 /\ region=1
TargetActive(db,region) == IF IsPrimary(db,region) THEN active ELSE TRUE
TargetGen(db,region) == IF IsPrimary(db,region) THEN generation ELSE 1
TargetVersions(db,region) == IF IsPrimary(db,region) THEN versions ELSE {2,4}
TargetLabels(db,region) == IF IsPrimary(db,region) THEN Labels ELSE {2,4}
EmptyToken == [exists |-> FALSE,db |-> 1,region |-> 1,gen |-> 1,
               include |-> FALSE,pos |-> 0,domain |-> "versions-v1",valid |-> TRUE,expired |-> FALSE]
Init == /\ step=0 /\ active=TRUE /\ generation=1 /\ versions={2,4}
        /\ current=4 /\ previous=2 /\ token=EmptyToken /\ last=[attempted |-> FALSE]

(* Parent resolution precedes saved-ARN authorization. Version/stage scans and
   cursor validation follow authorization; this is not auth-before-parent-read.
   The transaction is atomic in this projection; no SQL/IAM parser proof follows. *)
Query(kind,include,allowed) ==
  /\ step<MaxSteps /\ (kind="First" \/ token.exists)
  /\ LET db == IF kind="OtherDB" THEN 2 ELSE 1
         region == IF kind="OtherRegion" THEN 2 ELSE 1
         t == IF kind="First" THEN EmptyToken
              ELSE IF kind="Domain" THEN [token EXCEPT !.domain="secrets-list"]
              ELSE IF kind="Tamper" THEN [token EXCEPT !.valid=FALSE]
              ELSE IF kind="Expired" THEN [token EXCEPT !.expired=TRUE]
              ELSE token
         requestedFilter == IF kind="First" THEN include
                            ELSE IF kind="Filter" THEN ~t.include ELSE t.include
         resolved == TargetActive(db,region)
         actualGen == TargetGen(db,region)
         binding == ~t.exists \/ (t.valid /\ ~t.expired /\ t.domain="versions-v1"
                    /\ t.db=db /\ t.region=region /\ t.gen=actualGen
                    /\ t.include=requestedFilter)
         admitted == resolved /\ allowed /\ binding
         position == IF t.exists THEN t.pos ELSE 0
         qualified == {k \in TargetVersions(db,region) : k>position
                         /\ (requestedFilter \/ k \in TargetLabels(db,region))}
         page == IF admitted THEN FirstKey(qualified) ELSE {}
         more == admitted /\ Cardinality(qualified)>1
         newPosition == IF page={} THEN position ELSE CHOOSE k \in page : TRUE
     IN /\ token'=IF more THEN [exists |-> TRUE,db |-> db,region |-> region,
                    gen |-> actualGen,include |-> requestedFilter,pos |-> newPosition,
                    domain |-> "versions-v1",valid |-> TRUE,expired |-> FALSE] ELSE token
        /\ UNCHANGED <<active,generation,versions,current,previous>>
        /\ last'=[attempted |-> TRUE,resolved |-> resolved,allowed |-> allowed,
              read |-> admitted,accepted |-> admitted,usedToken |-> t.exists,
              signature |-> t.valid,expired |-> t.expired,domain |-> t.domain,
              db |-> db,region |-> region,gen |-> actualGen,include |-> requestedFilter,
              tokenDB |-> t.db,tokenRegion |-> t.region,tokenGen |-> t.gen,
              tokenInclude |-> t.include,pos |-> position,qualified |-> qualified,
              page |-> page,more |-> more,nextPosition |-> token'.pos,
              before |-> Frame,after |-> Frame']
  /\ step'=step+1

Add(k) == /\ step<MaxSteps /\ active /\ k \in Keys \ versions
          /\ versions'=versions \cup {k} /\ previous'=current /\ current'=k
          /\ step'=step+1 /\ UNCHANGED <<active,generation,token,last>>
Promote(k) == /\ step<MaxSteps /\ active /\ k \in versions /\ k#current
              /\ previous'=current /\ current'=k /\ step'=step+1
              /\ UNCHANGED <<active,generation,versions,token,last>>
Delete == /\ step<MaxSteps /\ active
          /\ active'=FALSE /\ versions'={} /\ current'=0 /\ previous'=0
          /\ step'=step+1 /\ UNCHANGED <<generation,token,last>>
Recreate == /\ step<MaxSteps /\ ~active /\ generation=1
            /\ active'=TRUE /\ generation'=2 /\ versions'={2,4}
            /\ current'=4 /\ previous'=2 /\ step'=step+1 /\ UNCHANGED <<token,last>>
Reopen == /\ step<MaxSteps /\ step'=step+1
          /\ UNCHANGED <<active,generation,versions,current,previous,token,last>>
Next == (\E kind \in Kinds,include,allowed \in BOOLEAN : Query(kind,include,allowed))
        \/ (\E k \in Keys : Add(k) \/ Promote(k)) \/ Delete \/ Recreate \/ Reopen
        \/ UNCHANGED vars
TypeOK == /\ step \in 0..MaxSteps /\ active \in BOOLEAN /\ generation \in 1..2
          /\ versions \subseteq Keys /\ current \in Keys \cup {0}
          /\ previous \in Keys \cup {0}
          /\ token.exists \in BOOLEAN /\ token.pos \in 0..4
          /\ token.db \in 1..2 /\ token.region \in 1..2 /\ token.gen \in 1..2
          /\ token.include \in BOOLEAN /\ token.valid \in BOOLEAN
          /\ token.expired \in BOOLEAN /\ token.domain="versions-v1"
ReadAfterAuthorization == IF last.attempted THEN
    last.read => last.resolved /\ last.allowed ELSE TRUE
NoDeniedEmission == IF last.attempted THEN
    ~last.allowed \/ ~last.resolved => ~last.accepted /\ last.page={} /\ ~last.more ELSE TRUE
TokenScopeBound == IF last.attempted THEN
    last.accepted /\ last.usedToken =>
      last.signature /\ ~last.expired /\ last.domain="versions-v1"
      /\ last.db=last.tokenDB /\ last.region=last.tokenRegion /\ last.gen=last.tokenGen
      /\ last.include=last.tokenInclude ELSE TRUE
StrictForwardKeyset == IF last.attempted THEN
    last.accepted => \A k \in last.page : k>last.pos ELSE TRUE
FilterBeforePageLimit == IF last.attempted THEN
    last.accepted => last.page=FirstKey(last.qualified) ELSE TRUE
ContinuationPosition == IF last.attempted THEN
    last.more => last.page#{} /\ last.nextPosition=(CHOOSE k \in last.page : TRUE) ELSE TRUE
TerminalConverges == IF last.attempted THEN
    last.accepted => (last.more=(Cardinality(last.qualified)>1)) ELSE TRUE
ReadFramePreserved == IF last.attempted THEN last.before=last.after ELSE TRUE
=============================================================================
