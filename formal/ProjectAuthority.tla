------------------------- MODULE ProjectAuthority -------------------------
EXTENDS Naturals
CONSTANTS ProjectA, ProjectB, SubjectA, SubjectB, None
Projects == {ProjectA,ProjectB}
Subjects == {SubjectA,SubjectB}
OwnerMaps == [Projects -> Subjects]
Candidates == [Projects -> Subjects \cup {None}]
EmptyOwners == [p \in Projects |-> None]
Permissions == {"storage.buckets.create","storage.buckets.list",
 "storage.buckets.get","storage.buckets.delete","storage.objects.create",
 "storage.objects.list","storage.objects.get","storage.objects.delete"}
VARIABLES configured, persisted, ready, bucketProject, bucketValue, last
vars == <<configured,persisted,ready,bucketProject,bucketValue,last>>
BoundOwner(project,actor) ==
 IF project \in Projects THEN
  persisted[project] # None /\ persisted[project] = configured[project] /\
  actor = persisted[project]
 ELSE FALSE
Event(kind,actor,claimed,actual,permission,candidate,status,returned) ==
 [kind |-> kind, actor |-> actor, claimed |-> claimed, actual |-> actual,
  permission |-> permission, candidate |-> candidate, status |-> status,
  returned |-> returned, oldConfigured |-> configured,
  oldPersisted |-> persisted, oldReady |-> ready,
  oldBucketProject |-> bucketProject, oldBucketValue |-> bucketValue]
Init ==
 /\ configured \in OwnerMaps
 /\ persisted = EmptyOwners /\ ready = FALSE
 \* A legacy unbound bucket must not be silently assigned to a new factory.
 /\ bucketProject \in Projects \cup {None}
 /\ bucketValue \in IF bucketProject = None THEN {None} ELSE {None,0}
 /\ last = [kind |-> "None",actor |-> SubjectA,claimed |-> ProjectA,
     actual |-> None,permission |-> "",candidate |-> EmptyOwners,
     status |-> "None",returned |-> None,oldConfigured |-> configured,
     oldPersisted |-> EmptyOwners,oldReady |-> FALSE,
     oldBucketProject |-> bucketProject,oldBucketValue |-> bucketValue]
Construct(candidate) ==
 LET unbound == IF bucketProject = None THEN FALSE
                ELSE persisted[bucketProject] = None
     valid == candidate \in OwnerMaps
     accepted == valid /\ ~unbound /\
                 (persisted = EmptyOwners \/ candidate = persisted)
 IN
 \* First valid factory defines the fixed trusted host configuration. Reopen
 \* can propose changed/removed owners, which must be refused atomically.
 /\ (~ready => candidate = configured)
 /\ persisted' = IF accepted THEN candidate ELSE persisted
 /\ ready' = IF accepted THEN TRUE ELSE ready
 /\ UNCHANGED <<configured,bucketProject,bucketValue>>
 /\ last' = Event("Construct",SubjectA,ProjectA,None,"",candidate,
                  IF accepted THEN "Accepted" ELSE "Refused",None)
CreateBucket(project,actor) ==
 LET authorized == ready /\ BoundOwner(project,actor) /\
       (bucketProject = None \/ BoundOwner(bucketProject,actor))
     accepted == authorized /\ bucketProject = None
 IN /\ bucketProject' = IF accepted THEN project ELSE bucketProject
    /\ UNCHANGED <<configured,persisted,ready,bucketValue>>
    /\ last' = Event("CreateBucket",actor,project,project,
       "storage.buckets.create",configured,
       IF ~authorized THEN "Denied" ELSE IF accepted THEN "Allowed"
       ELSE "Conflict",None)
Operation(actor,claimed,permission,input) ==
 LET actual == bucketProject
     accepted == ready /\ permission \in Permissions /\ BoundOwner(actual,actor)
 IN /\ bucketValue' =
       IF accepted /\ permission = "storage.objects.create" THEN input
       ELSE IF accepted /\ permission = "storage.objects.delete" THEN None
       ELSE bucketValue
    /\ UNCHANGED <<configured,persisted,ready,bucketProject>>
    /\ last' = Event("Operation",actor,claimed,actual,permission,configured,
        IF accepted THEN "Allowed" ELSE "Denied",
        IF accepted /\ permission = "storage.objects.get" THEN bucketValue ELSE None)
DeleteBucket(actor) ==
 LET authorized == ready /\ BoundOwner(bucketProject,actor)
     accepted == authorized /\ bucketValue = None
 IN /\ bucketProject' = IF accepted THEN None ELSE bucketProject
    /\ UNCHANGED <<configured,persisted,ready,bucketValue>>
    /\ last' = Event("DeleteBucket",actor,ProjectA,bucketProject,
         "storage.buckets.delete",configured,
         IF ~authorized THEN "Denied" ELSE IF accepted THEN "Allowed"
         ELSE "Nonempty",None)
Next == (\E candidate \in Candidates: Construct(candidate)) \/
 (\E actor \in Subjects:
    DeleteBucket(actor) \/
    (\E project \in Projects: CreateBucket(project,actor)) \/
    (\E claimed \in Projects,permission \in Permissions \cup {"unsupported"},
        input \in {0,1}: Operation(actor,claimed,permission,input)))
TypeOK ==
 /\ configured \in OwnerMaps /\ persisted \in Candidates /\ ready \in BOOLEAN
 /\ bucketProject \in Projects \cup {None} /\ bucketValue \in {None,0,1}
FixedHostConfiguration == configured = last.oldConfigured
OwnerBindingFrozen ==
 \A p \in Projects: last.oldPersisted[p] # None => persisted[p] = last.oldPersisted[p]
ConstructorRefusalFrame ==
 (last.kind = "Construct" /\ last.status = "Refused") =>
 /\ persisted = last.oldPersisted /\ ready = last.oldReady
 /\ bucketProject = last.oldBucketProject /\ bucketValue = last.oldBucketValue
NoUnboundAdoption ==
 (last.kind = "Construct" /\
  IF last.oldBucketProject = None THEN FALSE
  ELSE last.oldPersisted[last.oldBucketProject] = None) =>
 /\ last.status = "Refused" /\ persisted = last.oldPersisted
 /\ ready = last.oldReady
InitializationBindsConfigured ==
 (last.kind = "Construct" /\ last.status = "Accepted") =>
   persisted = configured /\ ready
DeniedFrame == last.status = "Denied" =>
 /\ persisted = last.oldPersisted /\ bucketProject = last.oldBucketProject
 /\ bucketValue = last.oldBucketValue /\ last.returned = None
FinitePermissions ==
 last.status = "Allowed" => last.permission \in Permissions
ActorUsesPersistedProject ==
 (last.kind = "Operation" /\ last.status = "Allowed") =>
   last.actual = last.oldBucketProject
OnlyActualOwnerAuthorized == last.status = "Allowed" =>
 /\ last.oldReady /\ last.actual \in Projects
 /\ last.actor = last.oldPersisted[last.actual]
 /\ last.oldPersisted[last.actual] = last.oldConfigured[last.actual]
=============================================================================
