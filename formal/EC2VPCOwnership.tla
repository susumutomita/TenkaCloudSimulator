--------------------------- MODULE EC2VPCOwnership ---------------------------
EXTENDS Naturals, FiniteSets
(* Finite store-level abstraction of the VPC rows shared by native
   CreateVpc/DeleteVpc and CloudFormation stacks, plus DescribeVpcs labels.
   Each action is one atomic transaction. Not SQL, HTTP/XML, IAM parsing,
   random ID generation, console rendering or a Go refinement proof. *)
CONSTANTS Regions, Stacks, Ids, Limit
Owners == {"ec2"} \cup Stacks
Absent == [region |-> "none", owner |-> "none"]
VARIABLES vpc, child, seen, last
vars == <<vpc, child, seen, last>>
Store == [vpc |-> vpc, child |-> child]
Used == Cardinality({i \in Ids : vpc[i] # Absent}) + Cardinality(child)
Event(kind, region, id, allowed) ==
    [kind |-> kind, region |-> region, id |-> id, allowed |-> allowed, before |-> Store]

Init == /\ vpc = [i \in Ids |-> Absent] /\ child = {} /\ seen = {}
        /\ last = Event("init", "none", "none", TRUE)

(* Native CreateVpc: authorization precedes any read or write; a refusal
   or a full store leaves every row unchanged. *)
NativeCreate(r, id, allow) ==
    /\ IF allow /\ vpc[id] = Absent /\ Used < Limit
          THEN vpc' = [vpc EXCEPT ![id] = [region |-> r, owner |-> "ec2"]]
          ELSE vpc' = vpc
    /\ last' = Event("nativeCreate", r, id, allow)
    /\ UNCHANGED <<child, seen>>

(* Native DeleteVpc: only an EC2-owned VPC in the request region, without
   dependents. CloudFormation-owned VPCs are refused. *)
NativeDelete(r, id, allow) ==
    /\ IF allow /\ vpc[id] # Absent /\ vpc[id].region = r /\ vpc[id].owner = "ec2" /\ ~\E c \in child : c.vpc = id
          THEN vpc' = [vpc EXCEPT ![id] = Absent]
          ELSE vpc' = vpc
    /\ last' = Event("nativeDelete", r, id, allow)
    /\ UNCHANGED <<child, seen>>

(* A stack transaction creates its own VPC; stack admission is modeled elsewhere. *)
StackCreate(s, r, id) ==
    /\ IF vpc[id] = Absent /\ Used < Limit
          THEN vpc' = [vpc EXCEPT ![id] = [region |-> r, owner |-> s]]
          ELSE vpc' = vpc
    /\ last' = Event("stackCreate", r, id, TRUE)
    /\ UNCHANGED <<child, seen>>

(* A stack subnet, route table, security group or attachment names a parent
   VPC by ID; the parent must belong to the same stack. *)
StackChild(s, id) ==
    /\ IF vpc[id] # Absent /\ vpc[id].owner = s /\ Used < Limit
          THEN child' = child \cup {[vpc |-> id, owner |-> s]}
          ELSE child' = child
    /\ last' = Event("stackChild", "none", id, TRUE)
    /\ UNCHANGED <<vpc, seen>>

(* DeleteStack removes the stack's children and VPCs in one transaction. *)
StackDelete(s) ==
    /\ child' = {c \in child : c.owner # s}
    /\ vpc' = [i \in Ids |-> IF vpc[i] # Absent /\ vpc[i].owner = s THEN Absent ELSE vpc[i]]
    /\ last' = Event("stackDelete", "none", "none", TRUE)
    /\ UNCHANGED seen

(* DescribeVpcs returns both owners of the request region, labeled by owner. *)
Describe(r, allow) ==
    /\ seen' = IF allow
                 THEN {[id |-> i, label |-> IF vpc[i].owner = "ec2" THEN "ec2" ELSE "cloudformation"] :
                         i \in {j \in Ids : vpc[j] # Absent /\ vpc[j].region = r}}
                 ELSE {}
    /\ last' = Event("describe", r, "none", allow)
    /\ UNCHANGED <<vpc, child>>

Next == \/ \E r \in Regions, id \in Ids, a \in BOOLEAN : NativeCreate(r, id, a) \/ NativeDelete(r, id, a)
        \/ \E r \in Regions, a \in BOOLEAN : Describe(r, a)
        \/ \E s \in Stacks, r \in Regions, id \in Ids : StackCreate(s, r, id)
        \/ \E s \in Stacks, id \in Ids : StackChild(s, id)
        \/ \E s \in Stacks : StackDelete(s)

TypeOK == /\ vpc \in [Ids -> ([region : Regions, owner : Owners] \cup {Absent})]
          /\ child \subseteq [vpc : Ids, owner : Stacks]
          /\ seen \subseteq [id : Ids, label : {"ec2", "cloudformation"}]

(* Every dependent row references an existing VPC of its own stack, so an
   EC2-owned VPC is never adopted and a deletion never orphans a child. *)
NoAdoption == \A c \in child : vpc[c.vpc] # Absent /\ vpc[c.vpc].owner = c.owner

FailureRollsBack == ~last.allowed => Store = last.before

NativeNeverDeletesStackVPC ==
    last.kind = "nativeDelete" =>
        \A i \in Ids : (last.before.vpc[i] # Absent /\ last.before.vpc[i].owner # "ec2") => vpc[i] = last.before.vpc[i]

NativeDeleteExact ==
    (last.kind = "nativeDelete" /\ vpc # last.before.vpc) =>
        /\ last.before.vpc[last.id].region = last.region
        /\ vpc = [last.before.vpc EXCEPT ![last.id] = Absent]

NativeCreateOwned ==
    (last.kind = "nativeCreate" /\ vpc # last.before.vpc) =>
        /\ last.before.vpc[last.id] = Absent
        /\ vpc = [last.before.vpc EXCEPT ![last.id] = [region |-> last.region, owner |-> "ec2"]]

StackDeleteSparesNative ==
    last.kind = "stackDelete" =>
        \A i \in Ids : (last.before.vpc[i] # Absent /\ last.before.vpc[i].owner = "ec2") => vpc[i] = last.before.vpc[i]

QuotaBound == Used <= Limit

DescribeExact ==
    last.kind = "describe" =>
        seen = IF last.allowed
                 THEN {[id |-> i, label |-> IF vpc[i].owner \in Stacks THEN "cloudformation" ELSE "ec2"] :
                         i \in {j \in Ids : vpc[j] # Absent /\ vpc[j].region = last.region}}
                 ELSE {}
=============================================================================
