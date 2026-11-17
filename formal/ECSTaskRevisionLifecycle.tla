--------------------- MODULE ECSTaskRevisionLifecycle -----------------------
EXTENDS Naturals, Sequences

CONSTANT MaxRevision
VARIABLES images, active, claim, planned, last

State == [images |-> images, active |-> active, claim |-> claim]
Event(kind, target, allowed, retain) ==
    [kind |-> kind, before |-> State, target |-> target, allowed |-> allowed, retain |-> retain]

Init == /\ images = <<>> /\ active = {} /\ claim = 0 /\ planned = 0
        /\ last = Event("init", 0, TRUE, FALSE)

Plan == /\ planned' = Len(images) + 1
        /\ UNCHANGED <<images, active, claim, last>>

Register(stack, target, image, grant, laterOK) ==
    /\ target > 0
    /\ LET fresh == target = Len(images) + 1
           capacity == Len(images) < MaxRevision
           eligible == ~stack \/ claim = 0
       IN IF fresh /\ capacity /\ eligible /\ grant /\ laterOK
          THEN /\ images' = Append(images, image)
               /\ active' = active \cup {target}
               /\ claim' = IF stack THEN target ELSE claim
          ELSE UNCHANGED <<images, active, claim>>
    /\ last' = Event(IF stack THEN "stack" ELSE "register", target, grant /\ laterOK, FALSE)
    /\ UNCHANGED planned

Deregister(revision) ==
    /\ revision \in active
    /\ active' = active \ {revision}
    /\ last' = Event("retire", revision, TRUE, FALSE)
    /\ UNCHANGED <<images, claim, planned>>

DeleteStack(retain) ==
    /\ claim > 0
    /\ active' = IF retain THEN active ELSE active \ {claim}
    /\ claim' = 0
    /\ last' = Event("cleanup", claim, TRUE, retain)
    /\ UNCHANGED <<images, planned>>

Next == \/ Plan
        \/ \E image \in {1,2}, grant \in BOOLEAN :
               Register(FALSE, Len(images) + 1, image, grant, TRUE)
        \/ \E image \in {1,2}, grant, laterOK \in BOOLEAN :
               Register(TRUE, planned, image, grant, laterOK)
        \/ \E revision \in active : Deregister(revision)
        \/ \E retain \in BOOLEAN : DeleteStack(retain)

TypeOK == /\ Len(images) <= MaxRevision /\ active \subseteq 1..MaxRevision
          /\ claim \in 0..MaxRevision /\ planned \in 0..(MaxRevision + 1)

CommitRequiresAdmission ==
    (last.kind \in {"stack","register"} /\
        (~last.allowed \/ last.target # Len(last.before.images) + 1 \/
         Len(last.before.images) >= MaxRevision \/ (last.kind = "stack" /\ last.before.claim # 0)))
    => State = last.before

HistoryImmutable == /\ Len(images) >= Len(last.before.images)
                    /\ \A i \in 1..Len(last.before.images) : images[i] = last.before.images[i]

RevisionIdentity ==
    (last.kind \in {"stack","register"} /\ Len(images) > Len(last.before.images)) =>
       /\ last.target = Len(images)
       /\ active = last.before.active \cup {last.target}
       /\ claim = IF last.kind = "stack" THEN last.target ELSE last.before.claim

CleanupExact ==
    /\ last.kind = "cleanup" =>
        /\ active = IF last.retain THEN last.before.active ELSE last.before.active \ {last.before.claim}
        /\ images = last.before.images /\ claim = 0
    /\ last.kind = "retire" =>
        /\ active = last.before.active \ {last.target}
        /\ images = last.before.images /\ claim = last.before.claim

=============================================================================
