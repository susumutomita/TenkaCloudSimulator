------------------------- MODULE GCPStorageState -------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS EnvironmentA, EnvironmentB, ProjectA, ProjectB, KeyA, KeyB, None,
          MaxGeneration, MaxObjects, MaxBytes
Environments == {EnvironmentA, EnvironmentB}
Projects == {ProjectA, ProjectB}
Keys == {KeyA, KeyB}
Slots == Environments \X Keys
Entries == [generation: 1..MaxGeneration, size: {1}]
EmptyOwners == [e \in Environments |-> None]
EmptyObjects == [k \in Slots |-> None]
EmptySequence == [e \in Environments |-> 0]
VARIABLES owners, objects, sequence, last
vars == <<owners, objects, sequence, last>>
Count(e) == Cardinality({k \in Keys: objects[<<e,k>>] # None})
Bytes(e) == Count(e) \* All modeled payloads cost one abstract budget unit.
CanAccess(owner,actor,grant) == grant /\ owner = actor
Matches(old,match) == match = None \/
  (IF old = None THEN match = 0 ELSE match = old.generation)
Event(kind,e,k,actor,grant,match,status,returned) ==
 [kind |-> kind, environment |-> e, key |-> k, actor |-> actor,
  grant |-> grant, match |-> match, status |-> status, returned |-> returned,
  oldOwners |-> owners, oldObjects |-> objects, oldSequence |-> sequence]
Init ==
 /\ owners = EmptyOwners /\ objects = EmptyObjects /\ sequence = EmptySequence
 /\ last = [kind |-> "None", environment |-> EnvironmentA, key |-> KeyA,
            actor |-> ProjectA, grant |-> FALSE, match |-> None,
            status |-> "None", returned |-> None, oldOwners |-> EmptyOwners,
            oldObjects |-> EmptyObjects, oldSequence |-> EmptySequence]
CreateBucket(e,actor,grant) ==
 LET accepted == grant /\ owners[e] = None
     status == IF ~grant \/ (owners[e] # None /\ owners[e] # actor)
               THEN "Denied" ELSE IF accepted THEN "Success" ELSE "Conflict"
 IN /\ owners' = IF accepted THEN [owners EXCEPT ![e] = actor] ELSE owners
    /\ UNCHANGED <<objects,sequence>>
    /\ last' = Event("CreateBucket",e,KeyA,actor,grant,None,status,None)
DeleteBucket(e,actor,grant) ==
 LET accepted == CanAccess(owners[e],actor,grant) /\ Count(e) = 0
     status == IF ~CanAccess(owners[e],actor,grant) THEN "Denied"
               ELSE IF Count(e) # 0 THEN "Nonempty" ELSE "Success"
 IN /\ owners' = IF accepted THEN [owners EXCEPT ![e] = None] ELSE owners
    /\ UNCHANGED <<objects,sequence>>
    /\ last' = Event("DeleteBucket",e,KeyA,actor,grant,None,status,None)
Put(e,k,actor,grant,match) ==
 LET slot == <<e,k>>
     old == objects[slot]
     authorized == CanAccess(owners[e],actor,grant)
     fits == (old # None \/ Count(e) < MaxObjects) /\
             (old # None \/ Bytes(e) < MaxBytes)
     accepted == authorized /\ Matches(old,match) /\ fits
     status == IF ~authorized THEN "Denied"
               ELSE IF ~Matches(old,match) THEN "Precondition"
               ELSE IF ~fits THEN "Budget" ELSE "Success"
 IN
 \* Exploration cutoff only, not a claim that the API has a two-generation cap.
 /\ (~accepted \/ sequence[e] < MaxGeneration)
 /\ objects' = IF accepted THEN [objects EXCEPT
       ![slot] = [generation |-> sequence[e]+1, size |-> 1]] ELSE objects
 /\ sequence' = IF accepted THEN [sequence EXCEPT ![e] = @+1] ELSE sequence
 /\ UNCHANGED owners
 /\ last' = Event("Put",e,k,actor,grant,match,status,None)
DeleteObject(e,k,actor,grant,match) ==
 LET slot == <<e,k>>
     old == objects[slot]
     authorized == CanAccess(owners[e],actor,grant)
     accepted == authorized /\ old # None /\ Matches(old,match)
     status == IF ~authorized THEN "Denied" ELSE IF old = None THEN "Missing"
               ELSE IF ~Matches(old,match) THEN "Precondition" ELSE "Success"
 IN /\ objects' = IF accepted THEN [objects EXCEPT ![slot] = None] ELSE objects
    /\ UNCHANGED <<owners,sequence>>
    /\ last' = Event("DeleteObject",e,k,actor,grant,match,status,None)
Get(e,k,actor,grant) ==
 LET slot == <<e,k>>
     authorized == CanAccess(owners[e],actor,grant)
     returned == IF authorized THEN objects[slot] ELSE None
 IN /\ UNCHANGED <<owners,objects,sequence>>
    /\ last' = Event("Get",e,k,actor,grant,None,
          IF ~authorized THEN "Denied" ELSE IF returned = None
          THEN "Missing" ELSE "Found",returned)
Next == \E e \in Environments, actor \in Projects, grant \in BOOLEAN:
  CreateBucket(e,actor,grant) \/ DeleteBucket(e,actor,grant) \/
  (\E k \in Keys: Get(e,k,actor,grant) \/
    (\E match \in {None} \cup 0..MaxGeneration:
      Put(e,k,actor,grant,match) \/ DeleteObject(e,k,actor,grant,match)))
TypeOK ==
 /\ owners \in [Environments -> Projects \cup {None}]
 /\ objects \in [Slots -> Entries \cup {None}]
 /\ sequence \in [Environments -> 0..MaxGeneration]
DeniedFrame == last.status = "Denied" =>
 /\ owners = last.oldOwners /\ objects = last.oldObjects
 /\ sequence = last.oldSequence /\ last.returned = None
NoOrphans == \A e \in Environments:
 owners[e] = None => \A k \in Keys: objects[<<e,k>>] = None
NonemptyDeleteRefused ==
 (last.kind = "DeleteBucket" /\ last.status = "Nonempty") =>
    owners = last.oldOwners /\ objects = last.oldObjects
GenerationRefusal == last.status = "Precondition" =>
 /\ objects = last.oldObjects /\ sequence = last.oldSequence
GenerationFreshness == last.kind # "None" =>
 /\ \A e \in Environments: sequence[e] >= last.oldSequence[e]
 /\ ((last.kind = "Put" /\ last.status = "Success") =>
    /\ sequence[last.environment] = last.oldSequence[last.environment]+1
    /\ objects[<<last.environment,last.key>>].generation =
         last.oldSequence[last.environment]+1)
Budget == \A e \in Environments: Count(e) <= MaxObjects /\ Bytes(e) <= MaxBytes
OtherEnvironmentUnchanged == last.kind # "None" =>
 \A e \in Environments \ {last.environment}:
 /\ owners[e] = last.oldOwners[e] /\ sequence[e] = last.oldSequence[e]
 /\ \A k \in Keys: objects[<<e,k>>] = last.oldObjects[<<e,k>>]
OtherProjectUnchanged ==
 (last.kind # "None" /\ last.oldOwners[last.environment] # None /\
  last.oldOwners[last.environment] # last.actor) =>
    owners = last.oldOwners /\ objects = last.oldObjects /\ sequence = last.oldSequence
ReadIsolation == last.kind = "Get" =>
 /\ objects = last.oldObjects
 /\ last.returned = IF CanAccess(last.oldOwners[last.environment],last.actor,last.grant)
                    THEN last.oldObjects[<<last.environment,last.key>>] ELSE None
=============================================================================
