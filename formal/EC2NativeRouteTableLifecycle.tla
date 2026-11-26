----------------------------- MODULE EC2NativeRouteTableLifecycle -----------------------------
(* One mutating region (east) and one fixed peer fixture (west).
   One symbolic native slot, two candidate parents, one CFN row in each region.
   No route or association writes, network effects, quotas or parser modeling. *)
VARIABLES parents, tables, receipts, legacy, visible, last

Regions == {"east","west"}
Native == {"native-a","native-b"}
Owners == Native \cup {"cfn"}
Tags == {"plain","named"}
None == [owner |-> "none", tag |-> "plain", token |-> FALSE]
Receipt(phase,p,n) == [phase |-> phase, owner |-> p, tag |-> n]
Unused == Receipt("unused","none","plain")
Row(p,n,token) == [owner |-> p, tag |-> n, token |-> token]
Here(r) == IF tables[r]=None THEN {} ELSE {<<r,tables[r].owner>>}
Cf(r) == IF r \in legacy THEN {<<r,"cfn">>} ELSE {}
Projection(r) == Here(r) \cup Cf(r)
Exists(r,p) == IF p="cfn" THEN r \in legacy ELSE <<r,p>> \in parents
Intent(a,p,n) == a.owner = p /\ a.tag = n
Event(op,r,p,n,token,grant,answer) ==
  [op |-> op, region |-> r, owner |-> p, tag |-> n, token |-> token,
   grant |-> grant, answer |-> answer, oldTable |-> tables[r], oldReceipt |-> receipts[r]]

Init ==
  /\ parents = Regions \X Native /\ legacy = Regions
  /\ tables = [r \in Regions |-> IF r="east" THEN None ELSE Row("native-b","named",TRUE)]
  /\ receipts = [r \in Regions |-> IF r="east" THEN Unused ELSE Receipt("live","native-b","named")]
  /\ visible = {}
  /\ last = Event("init","east","native-a","plain",FALSE,TRUE,"none")

Create(r,p,n,token,parentGrant,exactGrant) ==
  LET admitted == parentGrant /\ exactGrant /\ p \in Native /\ Exists(r,p)
      a == receipts[r]
      fresh == admitted /\ tables[r]=None /\ (~token \/ a.phase="unused")
      replay == admitted /\ token /\ a.phase="live" /\ Intent(a,p,n) /\ tables[r] # None
  IN /\ tables' = IF fresh THEN [tables EXCEPT ![r] = Row(p,n,token)] ELSE tables
     /\ receipts' = IF fresh /\ token THEN [receipts EXCEPT ![r] = Receipt("live",p,n)] ELSE receipts
     /\ last' = Event("create",r,p,n,token,parentGrant /\ exactGrant,
                     IF fresh THEN "created" ELSE IF replay THEN "replay" ELSE "denied")
     /\ visible' = {} /\ UNCHANGED <<parents,legacy>>

Delete(r,isNative,exactGrant) ==
  LET allowed == exactGrant /\ isNative
      existed == IF isNative THEN tables[r] # None ELSE r \in legacy
      target == IF isNative THEN tables[r].owner ELSE "cfn"
      a == receipts[r]
  IN /\ tables' = IF allowed /\ existed /\ isNative THEN [tables EXCEPT ![r] = None] ELSE tables
     /\ legacy' = IF allowed /\ existed /\ ~isNative THEN legacy \ {r} ELSE legacy
     /\ receipts' = IF allowed /\ existed /\ isNative /\ a.phase="live"
                    THEN [receipts EXCEPT ![r] = Receipt("retired",a.owner,a.tag)] ELSE receipts
     /\ last' = Event("delete",r,target,"plain",FALSE,exactGrant,IF allowed /\ existed THEN "deleted" ELSE "denied")
     /\ visible' = {} /\ UNCHANGED parents

DeleteParent(r,p,grant) ==
  /\ parents' = IF grant /\ tables[r].owner # p THEN parents \ {<<r,p>>} ELSE parents
  /\ last' = Event("parent",r,p,"plain",FALSE,grant,"none")
  /\ visible' = {} /\ UNCHANGED <<tables,receipts,legacy>>

Read(r,grant) ==
  /\ visible' = IF grant THEN Projection(r) ELSE {}
  /\ last' = Event("read",r,"none","plain",FALSE,grant,IF grant THEN "read" ELSE "denied")
  /\ UNCHANGED <<parents,tables,receipts,legacy>>

Next ==
  \/ \E p \in Owners, n \in Tags, t \in BOOLEAN, a \in BOOLEAN, g \in BOOLEAN: Create("east",p,n,t,a,g)
  \/ \E n \in BOOLEAN, g \in BOOLEAN: Delete("east",n,g)
  \/ \E p \in Native, g \in BOOLEAN: DeleteParent("east",p,g)
  \/ \E r \in Regions, g \in BOOLEAN: Read(r,g)

TypeOK ==
  /\ parents \subseteq (Regions \X Native) /\ legacy \subseteq Regions
  /\ tables \in [Regions -> [owner:Owners \cup {"none"}, tag:Tags, token:BOOLEAN]]
  /\ receipts \in [Regions -> [phase:{"unused","live","retired"}, owner:Owners \cup {"none"}, tag:Tags]]
  /\ visible \subseteq (Regions \X Owners)
GrantAtomic == last.op \in {"create","delete"} /\ ~last.grant =>
  tables[last.region] = last.oldTable /\ receipts[last.region] = last.oldReceipt /\ last.answer="denied"
NativeParentOnly == \A r \in Regions: tables[r] # None => tables[r].owner \in Native
ParentDependency == \A r \in Regions: tables[r] # None => <<r,tables[r].owner>> \in parents
LegacyPreserved == legacy = Regions
LiveReceiptMatches == \A r \in Regions:
  LET a == receipts[r] IN
  /\ (a.phase="live") <=> tables[r].token
  /\ a.phase="live" => Intent(tables[r],a.owner,a.tag)
RetiredNotRecreated == last.op="create" /\ last.token /\ last.oldReceipt.phase="retired" =>
  tables[last.region]=last.oldTable /\ receipts[last.region]=last.oldReceipt /\ last.answer="denied"
ReplayBindsIntent == last.op="create" /\ last.answer="replay" =>
  last.oldReceipt.phase="live" /\ Intent(last.oldReceipt,last.owner,last.tag)
  /\ tables[last.region]=last.oldTable
ReadScoped == last.op="read" => visible = IF last.grant THEN Projection(last.region) ELSE {}
========================================================================================
