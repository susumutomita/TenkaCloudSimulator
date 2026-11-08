--------------------------- MODULE DynamoDBLifecycle --------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Owners, Keys, MaxGeneration
VARIABLES tables, serial, items, pending, reqOwner, reqKey, reqGeneration,
          reqAction, reqCondition, reqAllow, reqDeny, reqSawExists,
          beforeTables, beforeItems, lastOp, lastOwner, lastKey,
          lastAllow, lastDeny, lastGeneration, lastCondition
vars == <<tables, serial, items, pending, reqOwner, reqKey, reqGeneration,
          reqAction, reqCondition, reqAllow, reqDeny, reqSawExists,
          beforeTables, beforeItems, lastOp, lastOwner, lastKey,
          lastAllow, lastDeny, lastGeneration, lastCondition>>
Empty == [key \in Keys |-> 0]
ConditionHolds(condition, exists) ==
 condition = "none" \/ (condition = "exists" /\ exists) \/
 (condition = "not_exists" /\ ~exists)
Init == /\ tables = [owner \in Owners |-> 0]
        /\ serial = tables /\ items = [owner \in Owners |-> Empty]
        /\ pending = FALSE /\ reqOwner \in Owners /\ reqKey \in Keys
        /\ reqGeneration = 0 /\ reqAction = "Put" /\ reqCondition = "none"
        /\ reqAllow = FALSE /\ reqDeny = FALSE /\ reqSawExists = FALSE
        /\ beforeTables = tables /\ beforeItems = items
        /\ lastOp = "Init" /\ lastOwner = reqOwner /\ lastKey = reqKey
        /\ lastAllow = FALSE /\ lastDeny = FALSE /\ lastGeneration = 0
        /\ lastCondition = "none"
Trace(owner,op,allow,deny,generation,condition,key) ==
 /\ beforeTables' = tables /\ beforeItems' = items
 /\ lastOwner' = owner /\ lastOp' = op /\ lastKey' = key
 /\ lastAllow' = allow /\ lastDeny' = deny
 /\ lastGeneration' = generation /\ lastCondition' = condition
Create(owner,allow,deny) ==
 /\ tables[owner] = 0 /\ serial[owner] < MaxGeneration
 /\ allow /\ ~deny
 /\ serial' = [serial EXCEPT ![owner] = @ + 1]
 /\ tables' = [tables EXCEPT ![owner] = serial[owner] + 1]
 /\ items' = [items EXCEPT ![owner] = Empty]
 /\ Trace(owner,"Create",allow,deny,0,"none",reqKey)
 /\ UNCHANGED <<pending,reqOwner,reqKey,reqGeneration,reqAction,reqCondition,
                reqAllow,reqDeny,reqSawExists>>
Capture(owner,key,action,condition,allow,deny) ==
 /\ ~pending /\ tables[owner] # 0
 /\ (action # "Drop" \/ condition = "none")
 /\ pending' = TRUE /\ reqOwner' = owner /\ reqKey' = key
 /\ reqGeneration' = tables[owner] /\ reqAction' = action
 /\ reqCondition' = condition /\ reqAllow' = allow /\ reqDeny' = deny
 /\ reqSawExists' = (items[owner][key] # 0)
 /\ Trace(owner,"Capture",allow,deny,tables[owner],condition,key)
 /\ UNCHANGED <<tables,serial,items>>
Commit ==
 /\ pending /\ reqAllow /\ ~reqDeny
 /\ tables[reqOwner] # 0 /\ tables[reqOwner] = reqGeneration
 /\ ConditionHolds(reqCondition,items[reqOwner][reqKey] # 0)
 /\ IF reqAction = "Drop"
       THEN /\ tables' = [tables EXCEPT ![reqOwner] = 0]
            /\ items' = [items EXCEPT ![reqOwner] = Empty]
       ELSE /\ tables' = tables
            /\ items' = [items EXCEPT ![reqOwner][reqKey] =
                          IF reqAction = "Put" THEN reqGeneration ELSE 0]
 /\ pending' = FALSE
 /\ Trace(reqOwner,"Commit",reqAllow,reqDeny,reqGeneration,reqCondition,reqKey)
 /\ UNCHANGED <<serial,reqOwner,reqKey,reqGeneration,reqAction,reqCondition,
                reqAllow,reqDeny,reqSawExists>>
Reject ==
 /\ pending
 /\ (~reqAllow \/ reqDeny \/ tables[reqOwner] = 0 \/
     tables[reqOwner] # reqGeneration \/
     ~ConditionHolds(reqCondition,items[reqOwner][reqKey] # 0))
 /\ pending' = FALSE
 /\ Trace(reqOwner,"Reject",reqAllow,reqDeny,reqGeneration,reqCondition,reqKey)
 /\ UNCHANGED <<tables,serial,items,reqOwner,reqKey,reqGeneration,reqAction,
                reqCondition,reqAllow,reqDeny,reqSawExists>>
OtherWrite(owner,key) ==
 /\ tables[owner] # 0
 /\ items' = [items EXCEPT ![owner][key] = tables[owner]]
 /\ Trace(owner,"OtherWrite",TRUE,FALSE,tables[owner],"none",key)
 /\ UNCHANGED <<tables,serial,pending,reqOwner,reqKey,reqGeneration,reqAction,
                reqCondition,reqAllow,reqDeny,reqSawExists>>
OtherDrop(owner) ==
 /\ tables[owner] # 0
 /\ tables' = [tables EXCEPT ![owner] = 0]
 /\ items' = [items EXCEPT ![owner] = Empty]
 /\ Trace(owner,"OtherDrop",TRUE,FALSE,tables[owner],"none",reqKey)
 /\ UNCHANGED <<serial,pending,reqOwner,reqKey,reqGeneration,reqAction,
                reqCondition,reqAllow,reqDeny,reqSawExists>>
SQLRollback ==
 /\ pending /\ pending' = FALSE
 /\ Trace(reqOwner,"Rollback",reqAllow,reqDeny,reqGeneration,reqCondition,reqKey)
 /\ UNCHANGED <<tables,serial,items,reqOwner,reqKey,reqGeneration,reqAction,
                reqCondition,reqAllow,reqDeny,reqSawExists>>
Restart ==
 /\ pending' = FALSE
 /\ Trace(reqOwner,"Restart",FALSE,FALSE,reqGeneration,reqCondition,reqKey)
 /\ UNCHANGED <<tables,serial,items,reqOwner,reqKey,reqGeneration,reqAction,
                reqCondition,reqAllow,reqDeny,reqSawExists>>
Next == \/ \E owner \in Owners,allow \in BOOLEAN,deny \in BOOLEAN : Create(owner,allow,deny)
        \/ \E owner \in Owners,key \in Keys,action \in {"Put","Delete","Drop"},
              condition \in {"none","exists","not_exists"},allow \in BOOLEAN,deny \in BOOLEAN :
              Capture(owner,key,action,condition,allow,deny)
        \/ Commit \/ Reject \/ SQLRollback \/ Restart
        \/ \E owner \in Owners,key \in Keys : OtherWrite(owner,key)
        \/ \E owner \in Owners : OtherDrop(owner)
Spec == Init /\ [][Next]_vars
TypeOK == /\ tables \in [Owners -> 0..MaxGeneration]
          /\ serial \in [Owners -> 0..MaxGeneration]
          /\ items \in [Owners -> [Keys -> 0..MaxGeneration]]
          /\ pending \in BOOLEAN
          /\ reqOwner \in Owners /\ reqKey \in Keys
          /\ reqGeneration \in 0..MaxGeneration
          /\ reqAction \in {"Put","Delete","Drop"}
          /\ reqCondition \in {"none","exists","not_exists"}
          /\ reqAllow \in BOOLEAN /\ reqDeny \in BOOLEAN /\ reqSawExists \in BOOLEAN
NoOrphans == \A owner \in Owners,key \in Keys : items[owner][key] # 0 => tables[owner] # 0
NoStaleItems == \A owner \in Owners,key \in Keys : items[owner][key] # 0 => items[owner][key] = tables[owner]
GenerationChecked == lastOp = "Commit" => lastGeneration # 0 /\ lastGeneration = beforeTables[lastOwner]
ConditionalAtomicity == lastOp = "Commit" => ConditionHolds(lastCondition,beforeItems[lastOwner][lastKey] # 0)
NoUnauthorizedMutation == (~lastAllow \/ lastDeny) => tables = beforeTables /\ items = beforeItems
NoForeignMutation == \A owner \in Owners \ {lastOwner} : tables[owner] = beforeTables[owner] /\ items[owner] = beforeItems[owner]
FailedUnchanged == lastOp \in {"Reject","Rollback","Restart"} => tables = beforeTables /\ items = beforeItems
=============================================================================
