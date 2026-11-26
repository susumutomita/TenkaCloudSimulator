----------------------------- MODULE EC2NativeVPCChildren -----------------------------
(* One existing native VPC, one child per kind and one custom ACL client token
   in each of two abstract deployment/region scopes. Legacy parents never
   enter native storage; no associations, packet paths or guest handoffs. *)
VARIABLES parents, children, receipts, visible, last

Scopes == {"team-a-east", "team-b-west"}
Kinds == {"subnet", "sg", "acl"}
TokenStates == {"unused", "live", "retired"}
InScope(s) == {p \in children: p[1] = s}
Event(op,s,k,granted,isNative) ==
  [op |-> op, scope |-> s, kind |-> k, granted |-> granted, isNative |-> isNative,
   before |-> children, oldParents |-> parents, beforeToken |-> receipts[s]]

Init ==
  /\ parents = Scopes /\ children = {}
  /\ receipts = [s \in Scopes |-> "unused"] /\ visible = {}
  /\ last = Event("none","team-a-east","subnet",TRUE,TRUE)

Create(s,k,parentGrant,exactGrant,isNative) ==
  LET admitted == parentGrant /\ exactGrant /\ isNative /\ s \in parents
        /\ (k # "acl" \/ receipts[s] = "unused")
  IN /\ children' = IF admitted THEN children \cup {<<s,k>>} ELSE children
     /\ receipts' = IF admitted /\ k = "acl"
                    THEN [receipts EXCEPT ![s] = "live"] ELSE receipts
     /\ last' = Event("create",s,k,parentGrant /\ exactGrant,isNative)
     /\ visible' = {} /\ UNCHANGED parents

DeleteChild(s,k,exactGrant) ==
  /\ children' = IF exactGrant THEN children \ {<<s,k>>} ELSE children
  /\ receipts' = IF exactGrant /\ k = "acl" /\ receipts[s] = "live"
                 THEN [receipts EXCEPT ![s] = "retired"] ELSE receipts
  /\ last' = Event("delete",s,k,exactGrant,TRUE)
  /\ visible' = {} /\ UNCHANGED parents

DeleteParent(s,grant) ==
  /\ parents' = IF grant /\ InScope(s) = {} THEN parents \ {s} ELSE parents
  /\ last' = Event("parent",s,"subnet",grant,TRUE)
  /\ visible' = {} /\ UNCHANGED <<children,receipts>>

Read(s,grant) ==
  /\ visible' = IF grant THEN InScope(s) ELSE {}
  /\ last' = Event("read",s,"subnet",grant,TRUE)
  /\ UNCHANGED <<children,parents,receipts>>

Next ==
  \/ \E s \in Scopes, k \in Kinds, p \in BOOLEAN, g \in BOOLEAN, n \in BOOLEAN: Create(s,k,p,g,n)
  \/ \E s \in Scopes, k \in Kinds, g \in BOOLEAN: DeleteChild(s,k,g)
  \/ \E s \in Scopes, g \in BOOLEAN: DeleteParent(s,g)
  \/ \E s \in Scopes, g \in BOOLEAN: Read(s,g)

TypeOK == /\ parents \subseteq Scopes /\ children \subseteq (Scopes \X Kinds)
          /\ receipts \in [Scopes -> TokenStates] /\ visible \subseteq (Scopes \X Kinds)
GrantAtomic ==
  ~last.granted => children = last.before /\ parents = last.oldParents
RejectOtherOwner ==
  last.op = "create" /\ ~last.isNative => children = last.before
ParentDependency == \A c \in children: c[1] \in parents
SiblingFrame ==
  children \ {<<last.scope,last.kind>>} = last.before \ {<<last.scope,last.kind>>}
ReadScoped ==
  last.op = "read" => visible = IF last.granted THEN InScope(last.scope) ELSE {}
ReplayNoRecreate ==
  last.op = "create" /\ last.kind = "acl" /\ last.beforeToken # "unused"
  => children = last.before
ReceiptTracksLiveACL ==
  \A s \in Scopes: (receipts[s] = "live") <=> (<<s,"acl">> \in children)
========================================================================================
