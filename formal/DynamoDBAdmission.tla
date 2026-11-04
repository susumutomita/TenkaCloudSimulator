-------------------------- MODULE DynamoDBAdmission --------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Scopes, MaxTables, MaxItems, MaxItemBytes
Tokens == {"nInteger","nExponent","nOther","sOne","bOne","fraction","precision","range"}
Kinds == {"small","utf8","binary","escaped","nested","over"}
Keys == {"N1","N2","S1","B1","invalid"}
Mutations == {"Create","Put","Delete","Drop"}
ExpectedKey(t) == CASE t \in {"nInteger","nExponent"} -> "N1"
 [] t="nOther" -> "N2" [] t="sOne" -> "S1" [] t="bOne" -> "B1"
 [] OTHER -> "invalid"
Valid(t) == t \notin {"fraction","precision","range"}
Key(t) == ExpectedKey(t)
ExpectedSize(k) == CASE k="small" -> 2 [] k \in {"binary","escaped"} -> 3
 [] k \in {"utf8","nested"} -> 4 [] OTHER -> 5
Size(k) == ExpectedSize(k)
WireSize(k) == IF k \in {"binary","escaped"} THEN 8 ELSE ExpectedSize(k)
Empty == [k \in Keys |-> 0]
Count(values) == Cardinality({k \in Keys : values[k] # 0})
TableCount(value) == Cardinality({s \in Scopes : value[s]})
Sum(values) == values["N1"]+values["N2"]+values["S1"]+values["B1"]+values["invalid"]
ExpectedPermit(ready,tableReady,itemPresent,itemCount,t,k,allow,deny) ==
 ready /\ tableReady /\ Valid(t) /\ allow /\ ~deny /\ ExpectedSize(k)<=MaxItemBytes /\
 (itemPresent \/ itemCount<MaxItems)
VARIABLES initialized, startupValid, tables, rows, stats,
 priorTableReady, priorExpectedPresent, priorItemCount, priorTableCount, allFrame, peerFrame,
 lastOp, lastScope, lastToken, lastKind, lastAllow, lastDeny, lastAccepted,
 lastReady, lastKey, lastBytes
vars == <<initialized,startupValid,tables,rows,stats,
 priorTableReady,priorExpectedPresent,priorItemCount,priorTableCount,allFrame,peerFrame,
 lastOp,lastScope,lastToken,lastKind,lastAllow,lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
Init == /\ initialized=FALSE /\ startupValid \in BOOLEAN
 /\ tables=[s \in Scopes |-> FALSE] /\ rows=[s \in Scopes |-> Empty]
 /\ stats=[s \in Scopes |-> 0]
 /\ lastOp="Init" /\ lastScope \in Scopes /\ lastToken="nInteger" /\ lastKind="small"
 /\ lastAllow=FALSE /\ lastDeny=FALSE /\ lastAccepted=FALSE
 /\ lastReady=FALSE /\ lastKey="N1" /\ lastBytes=0
 /\ priorTableReady=tables[lastScope]
 /\ priorExpectedPresent=(rows[lastScope][ExpectedKey(lastToken)]#0)
 /\ priorItemCount=Count(rows[lastScope]) /\ priorTableCount=TableCount(tables)
 /\ allFrame=(tables=[s \in Scopes |-> FALSE] /\ rows=[s \in Scopes |-> Empty] /\ stats=[s \in Scopes |-> 0])
 /\ peerFrame=(\A peer \in Scopes \ {lastScope} : ~tables[peer] /\ rows[peer]=Empty /\ stats[peer]=0)
\* Observations are exact functions of actual PRE/POST data, never data guards.
\* Expected-key presence is captured with ExpectedKey, not mutated Key or permit.
Prior(s,t) ==
 /\ priorTableReady'=tables[s]
 /\ priorExpectedPresent'=(rows[s][ExpectedKey(t)]#0)
 /\ priorItemCount'=Count(rows[s]) /\ priorTableCount'=TableCount(tables)
Observe(s) ==
 /\ allFrame'=(tables'=tables /\ rows'=rows /\ stats'=stats)
 /\ peerFrame'=(\A peer \in Scopes \ {s} : tables'[peer]=tables[peer] /\ rows'[peer]=rows[peer] /\ stats'[peer]=stats[peer])
Trace(s,op,t,k,allow,deny,accepted,key,bytes) ==
 /\ lastOp'=op /\ lastScope'=s /\ lastToken'=t /\ lastKind'=k
 /\ lastAllow'=allow /\ lastDeny'=deny /\ lastAccepted'=accepted
 /\ lastReady'=initialized /\ lastKey'=key /\ lastBytes'=bytes
 /\ Prior(s,t) /\ Observe(s)
Initialize == /\ ~initialized /\ startupValid /\ initialized'=TRUE
 /\ lastOp'="Initialize"
 /\ UNCHANGED <<startupValid,tables,rows,stats,lastScope,lastToken,lastKind,lastAllow,
 lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
 /\ Prior(lastScope,lastToken) /\ Observe(lastScope)
RefuseStartup == /\ ~initialized /\ ~startupValid /\ initialized'=FALSE
 /\ lastOp'="StartupRefused"
 /\ UNCHANGED <<startupValid,tables,rows,stats,lastScope,lastToken,lastKind,lastAllow,
 lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
 /\ Prior(lastScope,lastToken) /\ Observe(lastScope)
Create(s,allow,deny) ==
 LET permit == initialized /\ allow /\ ~deny /\ ~tables[s] /\ TableCount(tables)<MaxTables
 IN /\ tables'=(IF permit THEN [tables EXCEPT ![s]=TRUE] ELSE tables)
 /\ UNCHANGED <<initialized,startupValid,rows,stats>>
 /\ Trace(s,"Create","nInteger","small",allow,deny,permit,"N1",0)
Put(s,t,k,allow,deny) ==
 LET key == Key(t)
     bytes == Size(k)
     permit == initialized /\ tables[s] /\ Valid(t) /\ allow /\ ~deny /\ bytes<=MaxItemBytes /\
               (rows[s][key] # 0 \/ Count(rows[s])<MaxItems)
     replacement == [rows EXCEPT ![s][key]=bytes]
 IN /\ rows'=(IF permit THEN replacement ELSE rows)
    /\ stats'=(IF permit THEN [stats EXCEPT ![s]=Sum(replacement[s])] ELSE stats)
    /\ UNCHANGED <<initialized,startupValid,tables>>
    /\ Trace(s,"Put",t,k,allow,deny,permit,key,bytes)
Delete(s,t,allow,deny) ==
 LET key == Key(t)
     permit == initialized /\ tables[s] /\ Valid(t) /\ allow /\ ~deny
     replacement == [rows EXCEPT ![s][key]=0]
 IN /\ rows'=(IF permit THEN replacement ELSE rows)
    /\ stats'=(IF permit THEN [stats EXCEPT ![s]=Sum(replacement[s])] ELSE stats)
    /\ UNCHANGED <<initialized,startupValid,tables>>
    /\ Trace(s,"Delete",t,"small",allow,deny,permit,key,0)
Drop(s,allow,deny) ==
 LET permit == initialized /\ tables[s] /\ allow /\ ~deny
 IN /\ tables'=(IF permit THEN [tables EXCEPT ![s]=FALSE] ELSE tables)
 /\ rows'=(IF permit THEN [rows EXCEPT ![s]=Empty] ELSE rows)
 /\ stats'=(IF permit THEN [stats EXCEPT ![s]=0] ELSE stats)
 /\ UNCHANGED <<initialized,startupValid>>
 /\ Trace(s,"Drop","nInteger","small",allow,deny,permit,"N1",0)
Restart == /\ initialized'=FALSE /\ lastOp'="Restart"
 /\ UNCHANGED <<startupValid,tables,rows,stats,lastScope,lastToken,lastKind,lastAllow,
 lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
 /\ Prior(lastScope,lastToken) /\ Observe(lastScope)
Next == Initialize \/ RefuseStartup \/ Restart \/
 (\E s \in Scopes,allow \in BOOLEAN,deny \in BOOLEAN : Create(s,allow,deny) \/ Drop(s,allow,deny)) \/
 (\E s \in Scopes,t \in Tokens,allow \in BOOLEAN,deny \in BOOLEAN : Delete(s,t,allow,deny)) \/
 (\E s \in Scopes,t \in Tokens,k \in Kinds,allow \in BOOLEAN,deny \in BOOLEAN : Put(s,t,k,allow,deny))
Spec == Init /\ [][Next]_vars
TypeOK == /\ initialized \in BOOLEAN /\ startupValid \in BOOLEAN
 /\ tables \in [Scopes -> BOOLEAN]
 /\ rows \in [Scopes -> [Keys -> 0..8]] /\ stats \in [Scopes -> 0..40]
 /\ priorTableReady \in BOOLEAN /\ priorExpectedPresent \in BOOLEAN
 /\ priorItemCount \in 0..Cardinality(Keys) /\ priorTableCount \in 0..Cardinality(Scopes)
 /\ allFrame \in BOOLEAN /\ peerFrame \in BOOLEAN
 /\ lastOp \in {"Init","Initialize","StartupRefused","Create","Put","Delete","Drop","Restart"}
 /\ lastScope \in Scopes /\ lastToken \in Tokens /\ lastKind \in Kinds
 /\ lastAllow \in BOOLEAN /\ lastDeny \in BOOLEAN /\ lastAccepted \in BOOLEAN
 /\ lastReady \in BOOLEAN /\ lastKey \in Keys /\ lastBytes \in 0..8
InitializedBeforeMutation == lastOp \in Mutations /\ lastAccepted => lastReady
DeniedFrame == lastOp \in Mutations /\ (~lastAllow \/ lastDeny) => allFrame
ValidKeysOnly == lastOp \in {"Put","Delete"} /\ lastAccepted => Valid(lastToken)
CanonicalKeys == lastOp \in {"Put","Delete"} /\ lastAccepted => lastKey=ExpectedKey(lastToken)
SemanticBytes == lastOp="Put" /\ lastAccepted => lastBytes=ExpectedSize(lastKind)
CommittedBytes == lastOp="Put" /\ lastAccepted => rows[lastScope][lastKey]=lastBytes
CreateCommitted == lastOp="Create" /\ lastAccepted => tables[lastScope]
AcknowledgedRemoval == lastAccepted =>
 /\ (lastOp="Delete" => rows[lastScope][lastKey]=0)
 /\ (lastOp="Drop" => ~tables[lastScope] /\ rows[lastScope]=Empty /\ stats[lastScope]=0)
ActiveTableRequired == lastOp \in {"Put","Delete","Drop"} /\ lastAccepted => priorTableReady
TableBudget == TableCount(tables)<=MaxTables
ItemBudget == \A s \in Scopes,k \in Keys : rows[s][k]<=MaxItemBytes
ItemCountBudget == \A s \in Scopes : Count(rows[s])<=MaxItems
StatisticsExact == \A s \in Scopes : stats[s]=Sum(rows[s])
PeerFrame == peerFrame
NoOrphans == \A s \in Scopes : ~tables[s] => rows[s]=Empty /\ stats[s]=0
RefusalFrame == lastOp \in Mutations /\ ~lastAccepted => allFrame
AdmissionExact == lastOp="Put" => lastAccepted=ExpectedPermit(lastReady,priorTableReady,priorExpectedPresent,priorItemCount,lastToken,lastKind,lastAllow,lastDeny)
TableAdmissionExact == lastOp="Create" => lastAccepted=(lastReady /\ lastAllow /\ ~lastDeny /\ ~priorTableReady /\ priorTableCount<MaxTables)
RemovalAdmissionExact == lastOp \in {"Delete","Drop"} => lastAccepted=(lastReady /\ priorTableReady /\ lastAllow /\ ~lastDeny /\ (lastOp="Delete" => Valid(lastToken)))
StartupAdmission == initialized => startupValid
StartupRefusedFrame == lastOp="StartupRefused" => ~initialized /\ allFrame
=============================================================================
