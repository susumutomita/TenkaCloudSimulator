----------------------- MODULE CFNNativeIAMTransaction -----------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Faults
VARIABLES op, mode, requested, fault, phase, admitted, data, work, fingerprint, outcome
vars == <<op,mode,requested,fault,phase,admitted,data,work,fingerprint,outcome>>
Modes == {"Profile", "ZeroTrust", "Legacy", "Filtered"}
Native == mode \in {"Profile", "ZeroTrust"}
Policies == {"Read", "InjectedAllow"}
Empty == [legacy |-> (mode="Legacy"), role |-> FALSE, profile |-> FALSE, inline |-> {}, aggregate |-> {}, managed |-> FALSE,
          members |-> {}, roleGen |-> 1, profileGen |-> 1, metadata |-> "Requested",
          owner |-> "Self", stack |-> FALSE, claim |-> FALSE, peer |-> "Untouched"]
Existing == [Empty EXCEPT !.role=TRUE, !.profile=(mode="Profile"),
            !.inline=requested, !.aggregate=requested,
            !.members=(IF mode="Profile" THEN {1} ELSE {}), !.stack=TRUE, !.claim=TRUE]
Before == IF op="Create" THEN Empty ELSE Existing
Wanted == CASE op="Create" -> Existing
            [] op="Delete" -> Empty
            [] OTHER -> [Existing EXCEPT !.inline={}, !.aggregate={}]
ActionAllowed == fault # "ActionDenied"
PassAllowed == mode # "Profile" \/ op # "Create" \/ fault # "PassDenied"
CanonicalOwner == fault # "CanonicalConflict"
RoleGeneration == fault # "StaleRole"
ProfileGeneration == mode # "Profile" \/ fault # "StaleProfile"
NoExternalDependency == op # "Delete" \/ fault # "ExternalDependency"
Supported == fault # "UnsupportedTrust"
Admission == /\ Native /\ ActionAllowed /\ PassAllowed /\ CanonicalOwner
             /\ RoleGeneration /\ ProfileGeneration /\ NoExternalDependency /\ Supported
Init == /\ op \in {"Create","Delete","ClearPolicy"}
        /\ mode \in Modes /\ requested \in {{},{"Read"}} /\ fault \in Faults
        /\ (mode="ZeroTrust" => requested={})
        /\ (op="ClearPolicy" => requested={"Read"} /\ mode="Profile")
        /\ phase="Ready" /\ admitted=FALSE /\ data=Before /\ work=Before
        /\ fingerprint=Before /\ outcome="Pending"
Admit == /\ phase="Ready" /\ Admission
         /\ admitted'=TRUE /\ phase'="Admitted"
         /\ UNCHANGED <<op,mode,requested,fault,data,work,fingerprint,outcome>>
Reject == /\ phase="Ready" /\ ~Admission /\ phase'="Done" /\ outcome'="Refused"
          /\ UNCHANGED <<op,mode,requested,fault,admitted,data,work,fingerprint>>
Write == /\ phase="Admitted" /\ work'=Wanted /\ phase'="Written"
         /\ UNCHANGED <<op,mode,requested,fault,admitted,data,fingerprint,outcome>>
\* SQL trigger/ignored-write interference, BEFORE recording an observed hash.
Corrupt == /\ phase="Written"
           /\ work'=CASE fault="InlineInjection" /\ op#"Delete" ->
                            [work EXCEPT !.inline=@ \cup {"InjectedAllow"}, !.aggregate=@ \cup {"InjectedAllow"}]
                       [] fault="AggregateInjection" /\ op#"Delete" -> [work EXCEPT !.aggregate={"InjectedAllow"}]
                       [] fault="MembershipDrift" /\ mode="Profile" /\ op#"Delete" -> [work EXCEPT !.members={1,2}]
                       [] fault="MetadataDrift" /\ op#"Delete" -> [work EXCEPT !.metadata="Changed"]
                       [] fault="GenerationDrift" /\ op#"Delete" -> [work EXCEPT !.roleGen=2, !.profileGen=2]
                       [] fault="OwnerDrift" -> [work EXCEPT !.owner="Peer"]
                       [] fault="ManagedInjection" /\ op#"Delete" -> [work EXCEPT !.managed=TRUE, !.aggregate={"InjectedAllow"}]
                       [] OTHER -> work
           /\ phase'="Observed"
           /\ UNCHANGED <<op,mode,requested,fault,admitted,data,fingerprint,outcome>>
\* Capturing an observed state is NOT evidence that the requested state was made.
Capture == /\ phase="Observed" /\ fingerprint'=work /\ phase'="Captured"
           /\ UNCHANGED <<op,mode,requested,fault,admitted,data,work,outcome>>
Event == /\ phase="Captured"
         /\ work'=IF fault="EventResurrection" /\ op="Delete"
                   THEN [work EXCEPT !.role=TRUE, !.stack=TRUE, !.claim=TRUE]
                   ELSE work
         /\ phase'="Final"
         /\ UNCHANGED <<op,mode,requested,fault,admitted,data,fingerprint,outcome>>
RequestedPolicies == work.inline=Wanted.inline /\ work.aggregate=Wanted.aggregate /\ work.managed=Wanted.managed
RequestedMembership == work.members=Wanted.members
RequestedMetadata == work.metadata=Wanted.metadata
CapturedIdentity == work.roleGen=Wanted.roleGen /\ work.profileGen=Wanted.profileGen /\ work.owner="Self"
FinalPhysicalACK == work.role=Wanted.role /\ work.profile=Wanted.profile
                   /\ work.stack=Wanted.stack /\ work.claim=Wanted.claim
Accept == /\ RequestedPolicies /\ RequestedMembership /\ RequestedMetadata
          /\ CapturedIdentity /\ FinalPhysicalACK
Commit == /\ phase="Final" /\ Accept /\ data'=work /\ outcome'="Success" /\ phase'="Done"
          /\ UNCHANGED <<op,mode,requested,fault,admitted,work,fingerprint>>
Rollback == /\ phase="Final" /\ ~Accept /\ data'=Before /\ outcome'="Refused" /\ phase'="Done"
            /\ UNCHANGED <<op,mode,requested,fault,admitted,work,fingerprint>>
Next == Admit \/ Reject \/ Write \/ Corrupt \/ Capture \/ Event \/ Commit \/ Rollback
FrameType == [legacy:BOOLEAN, role:BOOLEAN, profile:BOOLEAN, inline:SUBSET Policies, aggregate:SUBSET Policies, managed:BOOLEAN,
              members:SUBSET {1,2}, roleGen:1..2, profileGen:1..2,
              metadata:{"Requested","Changed"}, owner:{"Self","Peer"},
              stack:BOOLEAN, claim:BOOLEAN, peer:{"Untouched","Changed"}]
TypeOK == /\ op \in {"Create","Delete","ClearPolicy"} /\ mode \in Modes
          /\ requested \in {{},{"Read"}} /\ fault \in Faults /\ admitted \in BOOLEAN
          /\ phase \in {"Ready","Admitted","Written","Observed","Captured","Final","Done"}
          /\ data \in FrameType /\ work \in FrameType /\ fingerprint \in FrameType
          /\ outcome \in {"Pending","Refused","Success"}
AdmissionSound == admitted => (Native /\ ActionAllowed /\ PassAllowed /\ CanonicalOwner
                 /\ RoleGeneration /\ ProfileGeneration /\ NoExternalDependency /\ Supported)
RequestedStateACK == outcome="Success" => data=Wanted
ZeroPolicyImplicitDeny == outcome="Success" /\ Wanted.inline={} => data.aggregate={}
SingleRole == outcome="Success" => Cardinality(data.members)<=1
FinalCleanupACK == outcome="Success" /\ op="Delete" => ~data.role /\ ~data.profile /\ ~data.stack /\ ~data.claim
FailureAtomic == outcome="Refused" => data=Before
PeerFrame == data.peer=Before.peer
PendingFrame == outcome="Pending" => data=Before
Spec == Init /\ [][Next]_vars
=============================================================================
