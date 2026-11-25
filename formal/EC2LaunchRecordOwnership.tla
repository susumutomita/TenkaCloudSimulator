----------------------- MODULE EC2LaunchRecordOwnership -----------------------
EXTENDS Naturals, FiniteSets
(* Finite store-level abstraction of native CreateVpc/CreateSubnet/RunInstances
   and their delete actions beside CloudFormation stack networks and instance
   claims. Each action is one atomic transaction. Not SQL or its foreign key,
   HTTP/XML, IAM parsing, CIDR arithmetic, random ID generation, console
   rendering, guest execution or a Go refinement proof. *)
CONSTANTS Stacks, Vpcs, Subnets, Instances, Limit
Owners == {"ec2"} \cup Stacks
NoSubnet == [vpc |-> "none", owner |-> "none"]
NoRecord == [subnet |-> "none", request |-> "none"]
(* A launch request carries no client token ("none") or the one modeled
   token. A repeated token never writes: equal parameters return the first
   record and different parameters are refused. *)
Requests == {"none", "token"}
VARIABLES vpc, subnet, record, claim, last
vars == <<vpc, subnet, record, claim, last>>
Store == [vpc |-> vpc, subnet |-> subnet, record |-> record, claim |-> claim]
Records == {i \in Instances : record[i] # NoRecord}
Tokened == {i \in Records : record[i].request # "none"}
Event(kind, id, allowed) == [kind |-> kind, id |-> id, allowed |-> allowed, before |-> Store]

Init == /\ vpc = [v \in Vpcs |-> "none"] /\ subnet = [n \in Subnets |-> NoSubnet]
        /\ record = [i \in Instances |-> NoRecord] /\ claim = {}
        /\ last = Event("init", "none", TRUE)

(* Native CreateVpc and CreateSubnet: authorization precedes any read or
   write. A subnet needs an EC2-owned VPC. *)
NativeVpc(v, allow) ==
    /\ vpc' = IF allow /\ vpc[v] = "none" THEN [vpc EXCEPT ![v] = "ec2"] ELSE vpc
    /\ last' = Event("nativeVpc", v, allow)
    /\ UNCHANGED <<subnet, record, claim>>

NativeSubnet(n, v, allow) ==
    /\ subnet' = IF allow /\ subnet[n] = NoSubnet /\ vpc[v] = "ec2"
                   THEN [subnet EXCEPT ![n] = [vpc |-> v, owner |-> "ec2"]] ELSE subnet
    /\ last' = Event("nativeSubnet", n, allow)
    /\ UNCHANGED <<vpc, record, claim>>

(* Native DeleteVpc and DeleteSubnet: only EC2-owned rows without dependents. *)
NativeDeleteVpc(v, allow) ==
    /\ vpc' = IF allow /\ vpc[v] = "ec2" /\ ~\E n \in Subnets : subnet[n].vpc = v
                THEN [vpc EXCEPT ![v] = "none"] ELSE vpc
    /\ last' = Event("nativeDeleteVpc", v, allow)
    /\ UNCHANGED <<subnet, record, claim>>

NativeDeleteSubnet(n, allow) ==
    /\ subnet' = IF allow /\ subnet[n].owner = "ec2" /\ ~\E i \in Instances : record[i].subnet = n
                   THEN [subnet EXCEPT ![n] = NoSubnet] ELSE subnet
    /\ last' = Event("nativeDeleteSubnet", n, allow)
    /\ UNCHANGED <<vpc, record, claim>>

(* RunInstances saves one launch record in an EC2-owned subnet. A repeated
   token returns the record it first created when the parameters match and is
   refused otherwise; neither case writes. *)
Run(i, n, q, allow) ==
    /\ record' = IF /\ allow /\ (q = "none" \/ Tokened = {})
                    /\ subnet[n].owner = "ec2" /\ record[i] = NoRecord
                    /\ i \notin {c.id : c \in claim}
                    /\ Cardinality(Records) < Limit
                   THEN [record EXCEPT ![i] = [subnet |-> n, request |-> q]] ELSE record
    /\ last' = Event("run", i, allow)
    /\ UNCHANGED <<vpc, subnet, claim>>

(* TerminateInstances removes exactly one EC2-owned record. A stack's
   instance is refused and stays claimed. *)
Terminate(i, allow) ==
    /\ record' = IF allow /\ record[i] # NoRecord THEN [record EXCEPT ![i] = NoRecord] ELSE record
    /\ last' = Event("terminate", i, allow)
    /\ claim' = claim /\ UNCHANGED <<vpc, subnet>>

(* Stack transactions own their VPC, subnet and instance; every parent must
   belong to the same stack. Stack admission is modeled elsewhere. *)
StackVpc(s, v) ==
    /\ vpc' = IF vpc[v] = "none" THEN [vpc EXCEPT ![v] = s] ELSE vpc
    /\ last' = Event("stackVpc", v, TRUE)
    /\ UNCHANGED <<subnet, record, claim>>

StackSubnet(s, n, v) ==
    /\ subnet' = IF subnet[n] = NoSubnet /\ vpc[v] = s
                   THEN [subnet EXCEPT ![n] = [vpc |-> v, owner |-> s]] ELSE subnet
    /\ last' = Event("stackSubnet", n, TRUE)
    /\ UNCHANGED <<vpc, record, claim>>

StackInstance(s, i, n) ==
    /\ claim' = IF subnet[n].owner = s /\ record[i] = NoRecord /\ ~\E c \in claim : c.id = i
                  THEN claim \cup {[id |-> i, owner |-> s, subnet |-> n]} ELSE claim
    /\ last' = Event("stackInstance", i, TRUE)
    /\ UNCHANGED <<vpc, subnet, record>>

(* DeleteStack removes the stack's instances, subnets and VPCs together. *)
StackDelete(s) ==
    /\ claim' = {c \in claim : c.owner # s}
    /\ subnet' = [n \in Subnets |-> IF subnet[n].owner = s THEN NoSubnet ELSE subnet[n]]
    /\ vpc' = [v \in Vpcs |-> IF vpc[v] = s THEN "none" ELSE vpc[v]]
    /\ last' = Event("stackDelete", "none", TRUE)
    /\ UNCHANGED record

Next == \/ \E v \in Vpcs, a \in BOOLEAN : NativeVpc(v, a) \/ NativeDeleteVpc(v, a)
        \/ \E n \in Subnets, v \in Vpcs, a \in BOOLEAN : NativeSubnet(n, v, a)
        \/ \E n \in Subnets, a \in BOOLEAN : NativeDeleteSubnet(n, a)
        \/ \E i \in Instances, n \in Subnets, q \in Requests, a \in BOOLEAN : Run(i, n, q, a)
        \/ \E i \in Instances, a \in BOOLEAN : Terminate(i, a)
        \/ \E s \in Stacks, v \in Vpcs : StackVpc(s, v)
        \/ \E s \in Stacks, n \in Subnets, v \in Vpcs : StackSubnet(s, n, v)
        \/ \E s \in Stacks, i \in Instances, n \in Subnets : StackInstance(s, i, n)
        \/ \E s \in Stacks : StackDelete(s)

TypeOK == /\ vpc \in [Vpcs -> Owners \cup {"none"}]
          /\ subnet \in [Subnets -> ([vpc : Vpcs, owner : Owners] \cup {NoSubnet})]
          /\ record \in [Instances -> ([subnet : Subnets, request : Requests] \cup {NoRecord})]
          /\ claim \subseteq [id : Instances, owner : Stacks, subnet : Subnets]

(* Every child references an existing parent of its own owner: nothing is
   adopted across owners and no deletion orphans a child. *)
NoAdoption ==
    /\ \A n \in Subnets : subnet[n] # NoSubnet => vpc[subnet[n].vpc] = subnet[n].owner
    /\ \A i \in Records : subnet[record[i].subnet].owner = "ec2"
    /\ \A c \in claim : subnet[c.subnet].owner = c.owner

FailureRollsBack == ~last.allowed => Store = last.before

(* One instance ID is a launch record or a stack's instance, never both. *)
IdentityDisjoint == \A c \in claim : record[c.id] = NoRecord

NativeNeverChangesStackState ==
    last.kind \in {"nativeVpc", "nativeSubnet", "nativeDeleteVpc", "nativeDeleteSubnet", "run", "terminate"} =>
        /\ claim = last.before.claim
        /\ \A v \in Vpcs : last.before.vpc[v] \in Stacks => vpc[v] = last.before.vpc[v]
        /\ \A n \in Subnets : last.before.subnet[n].owner \in Stacks => subnet[n] = last.before.subnet[n]

RunExact ==
    (last.kind = "run" /\ record # last.before.record) =>
        /\ last.before.record[last.id] = NoRecord
        /\ \A i \in Instances \ {last.id} : record[i] = last.before.record[i]
        /\ Cardinality(Records) = Cardinality({i \in Instances : last.before.record[i] # NoRecord}) + 1

TerminateExact ==
    (last.kind = "terminate" /\ record # last.before.record) =>
        /\ last.before.record[last.id] # NoRecord
        /\ record = [last.before.record EXCEPT ![last.id] = NoRecord]

(* The client token names at most one launch record. *)
TokenUnique == Cardinality(Tokened) <= 1

StackDeleteSparesNative ==
    last.kind = "stackDelete" =>
        /\ record = last.before.record
        /\ \A v \in Vpcs : last.before.vpc[v] = "ec2" => vpc[v] = "ec2"
        /\ \A n \in Subnets : last.before.subnet[n].owner = "ec2" => subnet[n] = last.before.subnet[n]

QuotaBound == Cardinality(Records) <= Limit
=============================================================================
