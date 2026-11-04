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
ExpectedPermit(ready,tableState,itemState,s,t,k,allow,deny) ==
 ready /\ tableState[s] /\ Valid(t) /\ allow /\ ~deny /\ ExpectedSize(k)<=MaxItemBytes /\
 (itemState[s][ExpectedKey(t)] # 0 \/ Count(itemState[s])<MaxItems)
VARIABLES initialized, startupValid, tables, rows, stats, beforeTables, beforeRows, beforeStats,
 lastOp, lastScope, lastToken, lastKind, lastAllow, lastDeny, lastAccepted,
 lastReady, lastKey, lastBytes
vars == <<initialized,startupValid,tables,rows,stats,beforeTables,beforeRows,beforeStats,
 lastOp,lastScope,lastToken,lastKind,lastAllow,lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
Init == /\ initialized=FALSE /\ startupValid \in BOOLEAN
 /\ tables=[s \in Scopes |-> FALSE] /\ rows=[s \in Scopes |-> Empty]
 /\ stats=[s \in Scopes |-> 0] /\ beforeTables=tables /\ beforeRows=rows /\ beforeStats=stats
 /\ lastOp="Init" /\ lastScope \in Scopes /\ lastToken="nInteger" /\ lastKind="small"
 /\ lastAllow=FALSE /\ lastDeny=FALSE /\ lastAccepted=FALSE
 /\ lastReady=FALSE /\ lastKey="N1" /\ lastBytes=0
Trace(s,op,t,k,allow,deny,accepted,key,bytes) ==
 /\ beforeTables'=tables /\ beforeRows'=rows /\ beforeStats'=stats
 /\ lastOp'=op /\ lastScope'=s /\ lastToken'=t /\ lastKind'=k
 /\ lastAllow'=allow /\ lastDeny'=deny /\ lastAccepted'=accepted
 /\ lastReady'=initialized /\ lastKey'=key /\ lastBytes'=bytes
Initialize == /\ ~initialized /\ startupValid /\ initialized'=TRUE
 /\ lastOp'="Initialize" /\ beforeTables'=tables /\ beforeRows'=rows /\ beforeStats'=stats
 /\ UNCHANGED <<startupValid,tables,rows,stats,lastScope,lastToken,lastKind,lastAllow,
 lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
RefuseStartup == /\ ~initialized /\ ~startupValid /\ initialized'=FALSE
 /\ lastOp'="StartupRefused" /\ beforeTables'=tables /\ beforeRows'=rows /\ beforeStats'=stats
 /\ UNCHANGED <<startupValid,tables,rows,stats,lastScope,lastToken,lastKind,lastAllow,
 lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
Create(s,allow,deny) ==
 LET permit == initialized /\ allow /\ ~deny /\ ~tables[s] /\ TableCount(tables)<MaxTables
 IN /\ tables'=IF permit THEN [tables EXCEPT ![s]=TRUE] ELSE tables
 /\ UNCHANGED <<initialized,startupValid,rows,stats>>
 /\ Trace(s,"Create","nInteger","small",allow,deny,permit,"N1",0)
Put(s,t,k,allow,deny) ==
 LET key == Key(t)
     bytes == Size(k)
     permit == initialized /\ tables[s] /\ Valid(t) /\ allow /\ ~deny /\ bytes<=MaxItemBytes /\
               (rows[s][key] # 0 \/ Count(rows[s])<MaxItems)
     replacement == [rows EXCEPT ![s][key]=bytes]
 IN /\ rows'=IF permit THEN replacement ELSE rows
    /\ stats'=IF permit THEN [stats EXCEPT ![s]=Sum(replacement[s])] ELSE stats
    /\ UNCHANGED <<initialized,startupValid,tables>>
    /\ Trace(s,"Put",t,k,allow,deny,permit,key,bytes)
Delete(s,t,allow,deny) ==
 LET key == Key(t)
     permit == initialized /\ tables[s] /\ Valid(t) /\ allow /\ ~deny
     replacement == [rows EXCEPT ![s][key]=0]
 IN /\ rows'=IF permit THEN replacement ELSE rows
    /\ stats'=IF permit THEN [stats EXCEPT ![s]=Sum(replacement[s])] ELSE stats
    /\ UNCHANGED <<initialized,startupValid,tables>>
    /\ Trace(s,"Delete",t,"small",allow,deny,permit,key,0)
Drop(s,allow,deny) ==
 LET permit == initialized /\ tables[s] /\ allow /\ ~deny
 IN /\ tables'=IF permit THEN [tables EXCEPT ![s]=FALSE] ELSE tables
 /\ rows'=IF permit THEN [rows EXCEPT ![s]=Empty] ELSE rows
 /\ stats'=IF permit THEN [stats EXCEPT ![s]=0] ELSE stats
 /\ UNCHANGED <<initialized,startupValid>>
 /\ Trace(s,"Drop","nInteger","small",allow,deny,permit,"N1",0)
Restart == /\ initialized'=FALSE /\ lastOp'="Restart"
 /\ beforeTables'=tables /\ beforeRows'=rows /\ beforeStats'=stats
 /\ UNCHANGED <<startupValid,tables,rows,stats,lastScope,lastToken,lastKind,lastAllow,
 lastDeny,lastAccepted,lastReady,lastKey,lastBytes>>
Next == Initialize \/ RefuseStartup \/ Restart \/
 (\E s \in Scopes,allow \in BOOLEAN,deny \in BOOLEAN : Create(s,allow,deny) \/ Drop(s,allow,deny)) \/
 (\E s \in Scopes,t \in Tokens,allow \in BOOLEAN,deny \in BOOLEAN : Delete(s,t,allow,deny)) \/
 (\E s \in Scopes,t \in Tokens,k \in Kinds,allow \in BOOLEAN,deny \in BOOLEAN : Put(s,t,k,allow,deny))
Spec == Init /\ [][Next]_vars
TypeOK == /\ initialized \in BOOLEAN /\ startupValid \in BOOLEAN
 /\ tables \in [Scopes -> BOOLEAN] /\ beforeTables \in [Scopes -> BOOLEAN]
 /\ rows \in [Scopes -> [Keys -> 0..8]] /\ stats \in [Scopes -> 0..40]
 /\ beforeRows \in [Scopes -> [Keys -> 0..8]] /\ beforeStats \in [Scopes -> 0..40]
 /\ lastOp \in {"Init","Initialize","StartupRefused","Create","Put","Delete","Drop","Restart"}
 /\ lastScope \in Scopes /\ lastToken \in Tokens /\ lastKind \in Kinds
 /\ lastAllow \in BOOLEAN /\ lastDeny \in BOOLEAN /\ lastAccepted \in BOOLEAN
 /\ lastReady \in BOOLEAN /\ lastKey \in Keys /\ lastBytes \in 0..8
InitializedBeforeMutation == lastOp \in Mutations /\ lastAccepted => lastReady
DeniedFrame == lastOp \in Mutations /\ (~lastAllow \/ lastDeny) => tables=beforeTables /\ rows=beforeRows /\ stats=beforeStats
ValidKeysOnly == lastOp \in {"Put","Delete"} /\ lastAccepted => Valid(lastToken)
CanonicalKeys == lastOp \in {"Put","Delete"} /\ lastAccepted => lastKey=ExpectedKey(lastToken)
SemanticBytes == lastOp="Put" /\ lastAccepted => lastBytes=ExpectedSize(lastKind)
CommittedBytes == lastOp="Put" /\ lastAccepted => rows[lastScope][lastKey]=lastBytes
CreateCommitted == lastOp="Create" /\ lastAccepted => tables[lastScope]
AcknowledgedRemoval == lastAccepted =>
 /\ (lastOp="Delete" => rows[lastScope][lastKey]=0)
 /\ (lastOp="Drop" => ~tables[lastScope] /\ rows[lastScope]=Empty /\ stats[lastScope]=0)
ActiveTableRequired == lastOp \in {"Put","Delete","Drop"} /\ lastAccepted => beforeTables[lastScope]
TableBudget == TableCount(tables)<=MaxTables
ItemBudget == \A s \in Scopes,k \in Keys : rows[s][k]<=MaxItemBytes
ItemCountBudget == \A s \in Scopes : Count(rows[s])<=MaxItems
StatisticsExact == \A s \in Scopes : stats[s]=Sum(rows[s])
PeerFrame == \A s \in Scopes \ {lastScope} : tables[s]=beforeTables[s] /\ rows[s]=beforeRows[s] /\ stats[s]=beforeStats[s]
NoOrphans == \A s \in Scopes : ~tables[s] => rows[s]=Empty /\ stats[s]=0
RefusalFrame == lastOp \in Mutations /\ ~lastAccepted => tables=beforeTables /\ rows=beforeRows /\ stats=beforeStats
AdmissionExact == lastOp="Put" => lastAccepted=ExpectedPermit(lastReady,beforeTables,beforeRows,lastScope,lastToken,lastKind,lastAllow,lastDeny)
TableAdmissionExact == lastOp="Create" => lastAccepted=(lastReady /\ lastAllow /\ ~lastDeny /\ ~beforeTables[lastScope] /\ TableCount(beforeTables)<MaxTables)
RemovalAdmissionExact == lastOp \in {"Delete","Drop"} => lastAccepted=(lastReady /\ beforeTables[lastScope] /\ lastAllow /\ ~lastDeny /\ (lastOp="Delete" => Valid(lastToken)))
StartupAdmission == initialized => startupValid
StartupRefusedFrame == lastOp="StartupRefused" => ~initialized /\ tables=beforeTables /\ rows=beforeRows /\ stats=beforeStats
=============================================================================
