----------------------------- MODULE EC2DetachedNetworkConsole -----------------------------
(* Finite UI composition; no IAM enforcement, HTTP parser, or network effect.
   Epoch summarizes a change of VPC, identity, region, or view incarnation. *)
VARIABLES epoch, gate, pending, leases, shown, last

Panes == {"subnet","securityGroup","networkAcl"}
Gates == {"native","cfn","handoff","unadvertised","unauthenticated"}
Stages == {"read","write","ack"}
Row(p,e,g,s) == [pane |-> p, epoch |-> e, gate |-> g, stage |-> s]
Report(r,verified,acked,saved) ==
  [pane |-> r.pane, epoch |-> r.epoch, verified |-> verified, acked |-> acked, saved |-> saved]
Without(s,p) == {row \in s: row.pane # p}
Event(op,p) == [op |-> op,pane |-> p,epoch |-> epoch,before |-> leases]

Init ==
  /\ epoch = 0 /\ gate = "native" /\ pending = {} /\ leases = {} /\ shown = {}
  /\ last = Event("init","subnet")

Start(p,s) ==
  /\ gate = "native"
  /\ \A r \in pending: r.pane # p
  /\ pending' = pending \cup {Row(p,epoch,gate,s)}
  /\ leases' = IF s = "write" THEN leases \cup {p} ELSE leases
  /\ shown' = Without(shown,p)
  /\ last' = Event("start",p) /\ UNCHANGED <<epoch,gate>>

Navigate(g) ==
  /\ epoch = 0
  /\ epoch' = IF leases = {} THEN 1 ELSE epoch
  /\ gate' = IF epoch' = epoch THEN gate ELSE g
  /\ shown' = IF epoch' = epoch THEN shown ELSE {}
  /\ last' = Event("navigate","subnet") /\ UNCHANGED <<pending,leases>>

AcceptNativeReceipt(r) ==
  /\ r.stage = "write"
  /\ pending' = Without(pending,r.pane) \cup {Row(r.pane,r.epoch,r.gate,"ack")}
  /\ last' = Event("ack",r.pane)
  /\ UNCHANGED <<epoch,gate,leases,shown>>

Complete(r,ok) ==
  /\ pending' = Without(pending,r.pane)
  /\ leases' = leases \ {r.pane}
  /\ shown' = IF r.epoch = epoch /\ ok /\ r.stage # "write"
     THEN Without(shown,r.pane) \cup {Report(r,ok,r.stage = "ack",r.stage # "read")}
     ELSE shown
  /\ last' = Event("complete",r.pane) /\ UNCHANGED <<epoch,gate>>

(* A bounded view which does not admit native reads or writes may rest. *)
Idle == /\ epoch = 1 /\ gate # "native" /\ pending = {}
        /\ UNCHANGED <<epoch,gate,pending,leases,shown,last>>

Next ==
  \/ \E p \in Panes,s \in {"read","write"}: Start(p,s)
  \/ \E g \in Gates: Navigate(g)
  \/ \E r \in pending: AcceptNativeReceipt(r)
  \/ \E r \in pending,ok \in BOOLEAN: Complete(r,ok)
  \/ Idle

TypeOK == /\ epoch \in {0,1} /\ gate \in Gates /\ leases \subseteq Panes
  /\ pending \subseteq [pane:Panes,epoch:{0,1},gate:Gates,stage:Stages]
  /\ shown \subseteq [pane:Panes,epoch:{0,1},verified:BOOLEAN,acked:BOOLEAN,saved:BOOLEAN]
AdmittedNativeOnly == \A r \in pending: r.gate = "native"
OnlyCurrentScope == \A s \in shown: s.epoch = epoch
VerifiedWriteOnly == \A s \in shown: s.saved => s.verified /\ s.acked
ScopeChangeFenced == last.op = "navigate" /\ last.before # {} => epoch = last.epoch
SiblingLeasePreserved == last.op = "complete" =>
  leases \ {last.pane} = last.before \ {last.pane}
LeaseUntilReadback == leases = {r.pane: r \in {w \in pending: w.stage # "read"}}
============================================================================================
