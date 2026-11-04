----------------------------- MODULE AzureBlobState -----------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Scopes, Keys, Owners, Actors, MaxWrites, MaxRows, MaxBytes
AccountOwners == [s \in Scopes |-> IF s=(CHOOSE x \in Scopes : TRUE) THEN (CHOOSE a \in Actors : TRUE) ELSE (CHOOSE a \in Actors : a#(CHOOSE b \in Actors : TRUE))]
Cells == Scopes \X Keys
Empty == [live |-> FALSE,value |-> 0,tag |-> 0]
Value == [live:BOOLEAN,value:0..2,tag:0..MaxWrites]
VARIABLES containers, blobs, serial, beforeContainers, beforeBlobs, beforeSerial,
 actor, trusted, scope, key, operation, mode, expected, result, response
vars == <<containers,blobs,serial,beforeContainers,beforeBlobs,beforeSerial,
 actor,trusted,scope,key,operation,mode,expected,result,response>>
Allowed(s,a,t) == t /\ a=Owners[s]
Rows(b) == Cardinality({p \in Cells : b[p].live})
RECURSIVE Weight(_,_)
Weight(b,ps) == IF ps={} THEN 0 ELSE
 LET p == CHOOSE x \in ps : TRUE
 IN (IF b[p].live THEN b[p].value ELSE 0)+Weight(b,ps\{p})
Fits(b) == Rows(b)<=MaxRows /\ Weight(b,Cells)<=MaxBytes
Condition(b,p,m,e) ==
 /\ (m#"Match" \/ (b[p].live /\ b[p].tag=e))
 /\ (m#"Exists" \/ b[p].live)
 /\ (m#"Absent" \/ ~b[p].live)
Capture(s,k,a,t,op,m,e) ==
 /\ beforeContainers'=containers /\ beforeBlobs'=blobs /\ beforeSerial'=serial
 /\ scope'=s /\ key'=(IF op \in {"Create","DeleteContainer"} THEN (CHOOSE x \in Keys : TRUE) ELSE k) /\ actor'=a /\ trusted'=t
 /\ operation'=op /\ mode'=m /\ expected'=(IF m="Match" THEN e ELSE 0)
Preserve == UNCHANGED <<containers,blobs,serial>>
Init ==
 /\ containers={} /\ blobs=[p \in Cells |-> Empty] /\ serial=0
 /\ beforeContainers=containers /\ beforeBlobs=blobs /\ beforeSerial=serial
 /\ scope=(CHOOSE s \in Scopes : TRUE) /\ key=(CHOOSE k \in Keys : TRUE)
 /\ actor=(CHOOSE a \in Actors : TRUE) /\ trusted=FALSE
 /\ operation="Init" /\ mode="None" /\ expected=0 /\ result="Initial" /\ response=0
Create(s,k,a,t,ok) ==
 /\ Capture(s,k,a,t,"Create","None",0) /\ response'=0
 /\ IF ~Allowed(s,a,t) THEN /\ result'="Denied" /\ Preserve
    ELSE IF s \in containers THEN /\ result'="Exists" /\ Preserve
    ELSE IF ~ok THEN /\ result'="StorageError" /\ Preserve
    ELSE /\ result'="Accepted" /\ containers'=containers\cup{s}
         /\ UNCHANGED <<blobs,serial>>
Put(s,k,a,t,m,e,v,ok) ==
 /\ serial<MaxWrites /\ Capture(s,k,a,t,"Put",m,e) /\ response'=0
 /\ LET p == <<s,k>>
        candidate == [blobs EXCEPT ![p]=[live |-> TRUE,value |-> v,tag |-> serial+1]]
    IN IF ~Allowed(s,a,t) THEN /\ result'="Denied" /\ Preserve
       ELSE IF s \notin containers THEN /\ result'="Missing" /\ Preserve
       ELSE IF ~Condition(blobs,p,m,e) THEN /\ result'="ConditionFailed" /\ Preserve
       ELSE IF ~Fits(candidate) THEN /\ result'="QuotaRejected" /\ Preserve
       ELSE IF ~ok THEN /\ result'="StorageError" /\ Preserve
       ELSE /\ result'="Accepted" /\ blobs'=candidate /\ serial'=serial+1
            /\ UNCHANGED containers
Get(s,k,a,t) ==
 /\ Capture(s,k,a,t,"Get","None",0) /\ Preserve
 /\ IF ~Allowed(s,a,t) THEN /\ result'="Denied" /\ response'=0
    ELSE IF s \notin containers \/ ~blobs[<<s,k>>].live
         THEN /\ result'="Missing" /\ response'=0
    ELSE /\ result'="Read" /\ response'=blobs[<<s,k>>].value
DeleteBlob(s,k,a,t,ok) ==
 /\ Capture(s,k,a,t,"DeleteBlob","None",0) /\ response'=0
 /\ IF ~Allowed(s,a,t) THEN /\ result'="Denied" /\ Preserve
    ELSE IF s \notin containers \/ ~blobs[<<s,k>>].live THEN /\ result'="Missing" /\ Preserve
    ELSE IF ~ok THEN /\ result'="StorageError" /\ Preserve
    ELSE /\ result'="Accepted" /\ blobs'=[blobs EXCEPT ![<<s,k>>]=Empty]
         /\ UNCHANGED <<containers,serial>>
DeleteContainer(s,k,a,t,ok) ==
 /\ Capture(s,k,a,t,"DeleteContainer","None",0) /\ response'=0
 /\ IF ~Allowed(s,a,t) THEN /\ result'="Denied" /\ Preserve
    ELSE IF s \notin containers THEN /\ result'="Missing" /\ Preserve
    ELSE IF ~ok THEN /\ result'="StorageError" /\ Preserve
    ELSE /\ result'="Accepted" /\ containers'=containers\{s}
         /\ blobs'=[p \in Cells |-> IF p[1]=s THEN Empty ELSE blobs[p]]
         /\ UNCHANGED serial
\* Deduplicate identical successors, not state assignments or request bounds.
\* Non-Match requests already record expected=0; a failure flag matters only
\* when the storage branch is reached. Container operations ignore key.
CanFailPut(s,k,a,t,m,e,v) ==
 /\ serial<MaxWrites /\ Allowed(s,a,t) /\ s \in containers
 /\ Condition(blobs,<<s,k>>,m,e)
 /\ Fits([blobs EXCEPT ![<<s,k>>]=[live |-> TRUE,value |-> v,tag |-> serial+1]])
Next ==
 (\E s \in Scopes,a \in Actors,t \in BOOLEAN :
   Create(s,(CHOOSE x \in Keys : TRUE),a,t,TRUE)
   \/ (Allowed(s,a,t) /\ s \notin containers /\ Create(s,(CHOOSE x \in Keys : TRUE),a,t,FALSE))
   \/ DeleteContainer(s,(CHOOSE x \in Keys : TRUE),a,t,TRUE)
   \/ (Allowed(s,a,t) /\ s \in containers /\ DeleteContainer(s,(CHOOSE x \in Keys : TRUE),a,t,FALSE))
   \/ (\E k \in Keys : Get(s,k,a,t) \/ DeleteBlob(s,k,a,t,TRUE)
       \/ (Allowed(s,a,t) /\ s \in containers /\ blobs[<<s,k>>].live /\ DeleteBlob(s,k,a,t,FALSE))
       \/ (\E m \in {"None","Match","Exists","Absent"} :
          \E e \in (IF m="Match" THEN 1..MaxWrites ELSE {0}),v \in 1..2 :
             Put(s,k,a,t,m,e,v,TRUE)
             \/ (CanFailPut(s,k,a,t,m,e,v) /\ Put(s,k,a,t,m,e,v,FALSE)))))
 \/ UNCHANGED vars
TypeOK ==
 /\ containers \subseteq Scopes /\ beforeContainers \subseteq Scopes
 /\ blobs \in [Cells -> Value] /\ beforeBlobs \in [Cells -> Value]
 /\ serial \in 0..MaxWrites /\ beforeSerial \in 0..MaxWrites
 /\ actor \in Actors /\ trusted \in BOOLEAN /\ scope \in Scopes /\ key \in Keys
 /\ operation \in {"Init","Create","Put","Get","DeleteBlob","DeleteContainer"}
 /\ mode \in {"None","Match","Exists","Absent"} /\ expected \in 0..MaxWrites
 /\ result \in {"Initial","Denied","Missing","Exists","ConditionFailed","QuotaRejected","StorageError","Accepted","Read"}
 /\ response \in 0..2
NoUnauthorizedMutation == operation#"Init" /\ (~trusted \/ actor#Owners[scope]) =>
 containers=beforeContainers /\ blobs=beforeBlobs /\ serial=beforeSerial /\ response=0
PeerStatePreserved == operation#"Init" =>
 /\ containers\{scope}=beforeContainers\{scope}
 /\ \A p \in Cells : p[1]#scope => blobs[p]=beforeBlobs[p]
NoOrphans == \A p \in Cells : blobs[p].live => p[1] \in containers
FailureAtomic == result \in {"StorageError","ConditionFailed","QuotaRejected","Missing","Exists"} =>
 containers=beforeContainers /\ blobs=beforeBlobs /\ serial=beforeSerial
ReadExact == result="Read" => response=beforeBlobs[<<scope,key>>].value
NoDeniedRead == result#"Read" => response=0
ConditionEnforced == operation="Put" /\ result="Accepted" =>
 /\ (mode#"Match" \/ (beforeBlobs[<<scope,key>>].live /\ beforeBlobs[<<scope,key>>].tag=expected))
 /\ (mode#"Exists" \/ beforeBlobs[<<scope,key>>].live)
 /\ (mode#"Absent" \/ ~beforeBlobs[<<scope,key>>].live)
FreshWriteIdentity == operation="Put" /\ result="Accepted" =>
 blobs[<<scope,key>>].tag=beforeSerial+1 /\ serial=beforeSerial+1
BudgetBounded == Rows(blobs)<=MaxRows /\ Weight(blobs,Cells)<=MaxBytes
=============================================================================
