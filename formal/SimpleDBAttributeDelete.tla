----------------------- MODULE SimpleDBAttributeDelete -----------------------
EXTENDS FiniteSets
Names == {"a", "b"}
Values == {"red", "blue"}
Pairs == Names \X Values
Modes == {"item", "name", "value"}
Conditions == {"any", "absent", "equal"}
VARIABLES before, rows, hasItem, mode, name, value, condition, expectedName,
          expectedValue, allowed, phase
vars == <<before,rows,hasItem,mode,name,value,condition,expectedName,expectedValue,allowed,phase>>
At(r,n) == {v \in Values : <<n,v>> \in r}
Admitted == allowed /\
   CASE condition = "any" -> TRUE
     [] condition = "absent" -> At(before,expectedName) = {}
     [] OTHER -> At(before,expectedName) = {expectedValue}
ToRemove == CASE mode = "item" -> Pairs
              [] mode = "name" -> {name} \X Values
              [] OTHER -> {<<name,value>>}
Init ==
 /\ before \in SUBSET Pairs /\ rows = before /\ hasItem = (before # {})
 /\ mode \in Modes /\ name \in Names /\ value \in Values
 /\ condition \in Conditions /\ expectedName \in Names /\ expectedValue \in Values
 /\ allowed \in BOOLEAN /\ phase = "New"
Finish ==
 /\ phase = "New"
 /\ phase' = IF Admitted THEN "Ack" ELSE "No"
 /\ rows' = IF Admitted THEN before \ ToRemove ELSE before
 /\ hasItem' = (rows' # {})
 /\ UNCHANGED <<before,mode,name,value,condition,expectedName,expectedValue,allowed>>
Next == Finish \/ (phase # "New" /\ UNCHANGED vars)
NoPhantomItem == hasItem = (rows # {})
NoUnacknowledgedChange == phase # "Ack" => rows = before
OnlyEligibleMayAck == phase = "Ack" =>
  /\ allowed
  /\ (condition = "absent" => At(before,expectedName) = {})
  /\ (condition = "equal" => At(before,expectedName) = {expectedValue})
ExactSurvivors == phase = "Ack" =>
  \A n \in Names, v \in Values :
    (<<n,v>> \in rows) <=> (<<n,v>> \in before /\
      (mode # "item" /\ (n # name \/ (mode = "value" /\ v # value))))
=============================================================================
