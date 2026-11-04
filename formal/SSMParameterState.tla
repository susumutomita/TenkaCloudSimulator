------------------------ MODULE SSMParameterState ------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS RegionA, RegionB, NameA, NameB, ValueA, ValueB, MaxVersion, None
Regions == {RegionA, RegionB}
Names == {NameA, NameB}
Values == {ValueA, ValueB}
Keys == Regions \X Names
Entries == [value: Values, version: 1..MaxVersion]
Empty == [k \in Keys |-> None]
VARIABLES parameters, last
vars == <<parameters, last>>
Permitted(allow,deny) == allow /\ ~deny
Event(kind,target,allow,deny,overwrite,input,status,returned,reported) ==
 [kind |-> kind, target |-> target, allow |-> allow, deny |-> deny,
  overwrite |-> overwrite, input |-> input, status |-> status,
  returned |-> returned, reported |-> reported, before |-> parameters]
Init ==
 /\ parameters = Empty
 /\ last = [kind |-> "none", target |-> <<RegionA,NameA>>,
             allow |-> FALSE, deny |-> FALSE, overwrite |-> FALSE,
             input |-> None, status |-> "none", returned |-> None,
             reported |-> 0, before |-> Empty]
Put(r,n,v,overwrite,allow,deny) ==
 LET k == <<r,n>>
     old == parameters[k]
     accepted == Permitted(allow,deny) /\ (old = None \/ overwrite)
     nextVersion == IF old = None THEN 1 ELSE old.version + 1
 IN
 \* Finite exploration cutoff only, not a simulated AWS/runtime version limit.
 /\ (IF old = None THEN TRUE ELSE (~accepted \/ old.version < MaxVersion))
 /\ parameters' = IF accepted
      THEN [parameters EXCEPT ![k] = [value |-> v, version |-> nextVersion]]
      ELSE parameters
 /\ last' = Event("Put",k,allow,deny,overwrite,v,
      IF ~Permitted(allow,deny) THEN "Denied"
      ELSE IF old # None /\ ~overwrite THEN "AlreadyExists" ELSE "Success",
      None,IF accepted THEN nextVersion ELSE 0)
Delete(r,n,allow,deny) ==
 LET k == <<r,n>>
 IN /\ parameters' = IF Permitted(allow,deny)
                         THEN [parameters EXCEPT ![k] = None] ELSE parameters
    /\ last' = Event("Delete",k,allow,deny,FALSE,None,
         IF ~Permitted(allow,deny) THEN "Denied"
         ELSE IF parameters[k] = None THEN "NotFound" ELSE "Success",None,0)
Get(r,n,allow,deny) ==
 LET requested == <<r,n>>
     served == <<r,n>>
     result == IF Permitted(allow,deny) THEN parameters[served] ELSE None
 IN /\ UNCHANGED parameters
    /\ last' = Event("Get",requested,allow,deny,FALSE,None,
         IF ~Permitted(allow,deny) THEN "Denied"
         ELSE IF result = None THEN "NotFound" ELSE "Found",result,0)
Next == \E r \in Regions,n \in Names,allow \in BOOLEAN,deny \in BOOLEAN:
          Get(r,n,allow,deny) \/ Delete(r,n,allow,deny) \/
          (\E v \in Values,overwrite \in BOOLEAN:Put(r,n,v,overwrite,allow,deny))
TypeOK ==
 /\ parameters \in [Keys -> Entries \cup {None}]
 /\ last.before \in [Keys -> Entries \cup {None}]
 /\ last.kind \in {"none","Put","Delete","Get"}
 /\ last.target \in Keys
 /\ last.allow \in BOOLEAN /\ last.deny \in BOOLEAN
 /\ last.overwrite \in BOOLEAN /\ last.input \in Values \cup {None}
 /\ last.status \in {"none","Denied","AlreadyExists","Success","NotFound","Found"}
 /\ last.returned \in Entries \cup {None}
 /\ last.reported \in 0..MaxVersion
DeniedPreservesState ==
 (last.kind # "none" /\ (~last.allow \/ last.deny)) =>
    (parameters = last.before /\ last.returned = None /\ last.reported = 0)
OverwriteRefusal ==
 (last.kind = "Put" /\ last.allow /\ ~last.deny /\
  last.before[last.target] # None /\ ~last.overwrite) =>
    (parameters = last.before /\ last.status = "AlreadyExists" /\ last.reported = 0)
PutVersion ==
 (last.kind = "Put" /\ last.status = "Success") =>
    /\ parameters[last.target].value = last.input
    /\ parameters[last.target].version =
        IF last.before[last.target] = None THEN 1
        ELSE last.before[last.target].version + 1
    /\ last.reported = parameters[last.target].version
DeleteEffect ==
 (last.kind = "Delete" /\ last.status = "Success") => parameters[last.target] = None
OtherKeysUnchanged ==
 last.kind # "none" => \A k \in Keys \ {last.target}:parameters[k] = last.before[k]
ReadIsolation ==
 (last.kind = "Get" /\ last.allow /\ ~last.deny) =>
   /\ parameters = last.before
   /\ IF last.before[last.target] = None
       THEN last.status = "NotFound" /\ last.returned = None
       ELSE last.status = "Found" /\ last.returned = last.before[last.target]
=============================================================================
