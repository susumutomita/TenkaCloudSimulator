------------------------- MODULE EC2NativeSubnetLifecycle -------------------------
EXTENDS FiniteSets, Naturals
(* Finite control-plane abstraction. No HTTP parser, packet path, AZ allocation,
   SQLite races, credential implementation, or proof of the Go implementation. *)
VARIABLES rows, last, seen
vars == <<rows,last,seen>>
Regions == {"east","west"}
Ids == {"a","b","c"}
Vpcs == {"nativeEast","nativeWest","stackEast"}
Blocks == {"one","two","outside"}
Parent == [nativeEast |-> [owner |-> "ec2", region |-> "east"],
           nativeWest |-> [owner |-> "ec2", region |-> "west"],
           stackEast |-> [owner |-> "cfn", region |-> "east"]]
Saved(id,v,r,b,owner) == [id |-> id, vpc |-> v, region |-> r, block |-> b, owner |-> owner]
Legacy == Saved("stackSubnet","stackEast","east","one","cfn")
Limit == 3
Event(op,r,i,vpcOK,subnetOK,dep) ==
    [op |-> op,r |-> r,id |-> i,vpcOK |-> vpcOK,subnetOK |-> subnetOK,dep |-> dep,before |-> rows]
Init == /\ rows = {Legacy} /\ seen = [status |-> "idle", items |-> {}]
        /\ last = Event("none","east","a",TRUE,TRUE,FALSE)

Create(r,i,v,b,vpcOK,subnetOK) ==
  /\ rows' = IF vpcOK /\ subnetOK /\ Parent[v].owner = "ec2" /\ Parent[v].region = r
              /\ b # "outside" /\ Cardinality(rows) < Limit
              /\ ~(\E s \in rows: s.id = i \/ (s.vpc = v /\ s.block = b))
             THEN rows \cup {Saved(i,v,r,b,"ec2")}
             ELSE rows
  /\ last' = Event("create",r,i,vpcOK,subnetOK,FALSE)
  /\ seen' = [status |-> "idle", items |-> {}]

Delete(r,i,subnetOK,dep) ==
  /\ rows' = IF subnetOK /\ ~dep
             THEN {s \in rows: s.id # i \/ s.owner # "ec2" \/ s.region # r}
             ELSE rows
  /\ last' = Event("delete",r,i,TRUE,subnetOK,dep)
  /\ seen' = [status |-> "idle", items |-> {}]

Read(r,subnetOK) ==
  /\ seen' = IF subnetOK THEN [status |-> "ready", items |-> {s \in rows: s.region = r}]
             ELSE [status |-> "denied", items |-> {}]
  /\ last' = Event("read",r,"a",TRUE,subnetOK,FALSE)
  /\ UNCHANGED rows

Next == \/ \E r \in Regions,i \in Ids,v \in Vpcs,b \in Blocks,a \in BOOLEAN,w \in BOOLEAN: Create(r,i,v,b,a,w)
        \/ \E r \in Regions,i \in (Ids \cup {"stackSubnet"}),w \in BOOLEAN,dep \in BOOLEAN: Delete(r,i,w,dep)
        \/ \E r \in Regions,w \in BOOLEAN: Read(r,w)

StackUnchanged == {s \in rows: s.owner = "cfn"} = {Legacy}
AuthorizedWrites == (last.op \in {"create","delete"} /\ (~last.vpcOK \/ ~last.subnetOK)) => rows = last.before
NativeParent == \A s \in rows: s.owner = "ec2" =>
  Parent[s.vpc].owner = "ec2" /\ s.region = Parent[s.vpc].region /\ s.block # "outside"
Nonoverlap == \A s,t \in rows: s # t => s.id # t.id /\ (s.vpc # t.vpc \/ s.block # t.block)
Bounded == Cardinality(rows) <= Limit
ExactDelete == last.op = "delete" =>
  rows = IF last.subnetOK /\ ~last.dep THEN
    {s \in last.before: s.id # last.id \/ s.owner # "ec2" \/ s.region # last.r}
    ELSE last.before
ExactRead == last.op = "read" =>
  seen = IF last.subnetOK THEN [status |-> "ready", items |-> {s \in rows: s.region = last.r}]
         ELSE [status |-> "denied", items |-> {}]
==================================================================================
