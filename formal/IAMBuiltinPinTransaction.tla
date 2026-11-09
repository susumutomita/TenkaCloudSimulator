----------------------- MODULE IAMBuiltinPinTransaction -----------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Faults
VARIABLES op, requestedBuiltin, inlineDeny, fault, phase, admitted,
          data, work, observed, outcome
vars == <<op,requestedBuiltin,inlineDeny,fault,phase,admitted,data,work,observed,outcome>>
Ops == {"NativeRead","NativeAttach","NativeDetach","CFNCreate","CFNDelete",
        "MutateBuiltin","MutateCustomer"}
CFN == op \in {"CFNCreate","CFNDelete"}
NeedsPin == op \in {"NativeRead","NativeAttach","NativeDetach"} \/ (CFN /\ requestedBuiltin)
PinnedActions == {"ssm:DescribeAssociation",
                  "ssm:GetDeployablePatchSnapshotForInstance",
                  "ssm:GetDocument",
                  "ssm:DescribeDocument",
                  "ssm:GetManifest",
                  "ssm:GetParameter",
                  "ssm:GetParameters",
                  "ssm:ListAssociations",
                  "ssm:ListInstanceAssociations",
                  "ssm:PutInventory",
                  "ssm:PutComplianceItems",
                  "ssm:PutConfigurePackageResult",
                  "ssm:UpdateAssociationStatus",
                  "ssm:UpdateInstanceAssociationStatus",
                  "ssm:UpdateInstanceInformation",
                  "ssmmessages:CreateControlChannel",
                  "ssmmessages:CreateDataChannel",
                  "ssmmessages:OpenControlChannel",
                  "ssmmessages:OpenDataChannel",
                  "ec2messages:AcknowledgeMessage",
                  "ec2messages:DeleteMessage",
                  "ec2messages:FailMessage",
                  "ec2messages:GetEndpoint",
                  "ec2messages:GetMessages",
                  "ec2messages:SendReply"}
ProbeAction == "ssm:GetParameter"
ExtraAction == "ssm:SendCommand"
Pin == [present |-> TRUE, versions |-> {2}, default |-> 2,
        document |-> "Pin25", metadata |-> "Exact", namespace |-> "Builtin", generation |-> 1]
InitialEdge == IF op="NativeDetach" \/ (op="CFNDelete" /\ requestedBuiltin) THEN {"Builtin"} ELSE {}
InitialRole == op # "CFNCreate"
InitialInline == InitialRole /\ inlineDeny
Decision(allow,deny) == IF ProbeAction \in deny THEN "ExplicitDeny"
                       ELSE IF ProbeAction \in allow THEN "Allowed" ELSE "ImplicitDeny"
Before == [pin |-> IF fault="PriorPinCorruption" THEN [Pin EXCEPT !.document="Other"] ELSE Pin,
           customer |-> 1, role |-> InitialRole, roleGen |-> 1,
           edge |-> InitialEdge, edgeGen |-> IF InitialEdge={} THEN 0 ELSE 1,
           inline |-> InitialInline,
           allow |-> IF InitialEdge={} THEN {} ELSE PinnedActions,
           deny |-> IF InitialInline THEN {ProbeAction} ELSE {},
           decision |-> Decision(IF InitialEdge={} THEN {} ELSE PinnedActions,
                                IF InitialInline THEN {ProbeAction} ELSE {}),
           stack |-> op="CFNDelete", claim |-> op="CFNDelete"]
\* Requested state does not depend on the observed fingerprint or altered pin.
WantedEdge == CASE op="NativeAttach" -> {"Builtin"}
               [] op="NativeDetach" \/ op="CFNDelete" -> {}
               [] op="CFNCreate" -> IF requestedBuiltin THEN {"Builtin"} ELSE {}
               [] OTHER -> Before.edge
WantedRole == IF op="CFNDelete" THEN FALSE ELSE IF op="CFNCreate" THEN TRUE ELSE Before.role
WantedInline == IF op="CFNDelete" THEN FALSE ELSE IF op="CFNCreate" THEN inlineDeny ELSE Before.inline
WantedAllow == IF "Builtin" \in WantedEdge THEN PinnedActions ELSE {}
WantedDeny == IF WantedInline THEN {ProbeAction} ELSE {}
Wanted == [Before EXCEPT !.role=WantedRole, !.edge=WantedEdge,
          !.edgeGen=(IF WantedEdge={} THEN 0 ELSE 1), !.inline=WantedInline,
          !.allow=WantedAllow, !.deny=WantedDeny, !.decision=Decision(WantedAllow,WantedDeny),
          !.customer=(IF op="MutateCustomer" THEN 2 ELSE @),
          !.stack=(IF CFN THEN op="CFNCreate" ELSE @),
          !.claim=(IF CFN THEN op="CFNCreate" ELSE @)]
ReadOnly == op # "MutateBuiltin"
Authorized == fault # "ActionDenied"
FreshRole == op \notin {"NativeAttach","NativeDetach","CFNDelete"} \/ fault # "StaleRole"
InitialPinValid == ~NeedsPin \/ Before.pin=Pin
Admission == /\ ReadOnly /\ Authorized /\ FreshRole /\ InitialPinValid
Init == /\ op \in Ops /\ requestedBuiltin \in BOOLEAN /\ inlineDeny \in BOOLEAN /\ fault \in Faults
        /\ (op="MutateBuiltin" => requestedBuiltin)
        /\ phase="Ready" /\ admitted=FALSE /\ data=Before /\ work=Before
        /\ observed=Before /\ outcome="Pending"
Admit == /\ phase="Ready" /\ Admission /\ admitted'=TRUE /\ phase'="Admitted"
         /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,data,work,observed,outcome>>
Reject == /\ phase="Ready" /\ ~Admission /\ phase'="Done" /\ outcome'="Refused"
          /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,admitted,data,work,observed>>
Write == /\ phase="Admitted" /\ work'=Wanted /\ phase'="Written"
         /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,admitted,data,observed,outcome>>
BadPin(p,f) == CASE f \in {"PinMissing","LatePinMissing"} -> [p EXCEPT !.present=FALSE]
               [] f \in {"VersionMissing","LateVersionMissing"} -> [p EXCEPT !.versions={}]
               [] f \in {"WrongVersion","LateWrongVersion"} -> [p EXCEPT !.default=1]
               [] f \in {"DocumentChanged","LateDocumentChanged"} -> [p EXCEPT !.document="Other"]
               [] f \in {"MetadataChanged","LateMetadataChanged"} -> [p EXCEPT !.metadata="Other"]
               [] f="ExtraVersion" -> [p EXCEPT !.versions={1,2}]
               [] f="NamespaceSwap" -> [p EXCEPT !.namespace="Customer"]
               [] f="PinGenerationChanged" -> [p EXCEPT !.generation=2]
               [] OTHER -> p
ImmediateFaults == {"PinMissing","VersionMissing","WrongVersion","DocumentChanged",
                    "MetadataChanged","ExtraVersion","NamespaceSwap","PinGenerationChanged"}
LateFaults == {"LatePinMissing","LateVersionMissing","LateWrongVersion","LateDocumentChanged","LateMetadataChanged"}
MutatesRole == op \in {"NativeAttach","NativeDetach","CFNCreate","CFNDelete"}
Corrupt == /\ phase="Written"
           /\ work'=CASE NeedsPin /\ MutatesRole /\ fault \in ImmediateFaults -> [work EXCEPT !.pin=BadPin(@,fault)]
                       [] op="CFNCreate" /\ ~requestedBuiltin /\ fault="BuiltinEdgeInjection" -> [work EXCEPT !.edge={"Builtin"}, !.edgeGen=1, !.allow=PinnedActions, !.decision=Decision(PinnedActions,WantedDeny)]
                       [] MutatesRole /\ fault="UnexpectedEdge" -> [work EXCEPT !.edge=@ \cup {"Customer"}]
                       [] MutatesRole /\ fault="WrongEdgeGeneration" /\ WantedEdge#{} -> [work EXCEPT !.edgeGen=2]
                       [] MutatesRole /\ fault="AggregateAdded" -> [work EXCEPT !.allow=@ \cup {ExtraAction}]
                       [] MutatesRole /\ fault="AggregateMissing" -> [work EXCEPT !.allow={}]
                       [] MutatesRole /\ fault="InlineChanged" -> [work EXCEPT !.inline=~@]
                       [] MutatesRole /\ fault="DenyIgnored" -> [work EXCEPT !.decision="Allowed"]
                       [] OTHER -> work
           /\ phase'="Observed"
           /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,admitted,data,observed,outcome>>
Capture == /\ phase="Observed" /\ observed'=work /\ phase'="Captured"
           /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,admitted,data,work,outcome>>
Event == /\ phase="Captured"
         /\ work'=CASE CFN /\ NeedsPin /\ fault \in LateFaults -> [work EXCEPT !.pin=BadPin(@,fault)]
                     [] op="CFNDelete" /\ fault="LateResurrection" -> [work EXCEPT !.role=TRUE, !.stack=TRUE]
                     [] OTHER -> work
         /\ phase'="Final"
         /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,admitted,data,observed,outcome>>
\* This guard intentionally does NOT depend on live role/remaining attachments.
FinalPin == ~NeedsPin \/ (work.pin=Pin /\ work.pin=Before.pin)
RequestedEdges == work.edge=Wanted.edge /\ work.edgeGen=Wanted.edgeGen
ExactAggregate == work.allow=WantedAllow /\ work.deny=WantedDeny /\ work.decision=Decision(WantedAllow,WantedDeny)
RoleFrame == work.roleGen=Before.roleGen /\ work.inline=WantedInline
FinalRecords == work.role=WantedRole /\ work.stack=Wanted.stack /\ work.claim=Wanted.claim
Accept == /\ FinalPin /\ RequestedEdges /\ ExactAggregate /\ RoleFrame /\ FinalRecords
Commit == /\ phase="Final" /\ Accept /\ data'=work /\ outcome'="Success" /\ phase'="Done"
          /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,admitted,work,observed>>
Rollback == /\ phase="Final" /\ ~Accept /\ data'=Before /\ outcome'="Refused" /\ phase'="Done"
            /\ UNCHANGED <<op,requestedBuiltin,inlineDeny,fault,admitted,work,observed>>
Next == Admit \/ Reject \/ Write \/ Corrupt \/ Capture \/ Event \/ Commit \/ Rollback
PinType == [present:BOOLEAN, versions:SUBSET {1,2}, default:1..2, document:{"Pin25","Other"},
            metadata:{"Exact","Other"}, namespace:{"Builtin","Customer"}, generation:1..2]
FrameType == [pin:PinType, customer:1..2, role:BOOLEAN, roleGen:1..2,
              edge:SUBSET {"Builtin","Customer"}, edgeGen:0..2, inline:BOOLEAN,
              allow:SUBSET (PinnedActions \cup {ExtraAction}), deny:SUBSET {ProbeAction}, decision:{"Allowed","ExplicitDeny","ImplicitDeny"},
              stack:BOOLEAN, claim:BOOLEAN]
TypeOK == /\ op \in Ops /\ requestedBuiltin \in BOOLEAN /\ inlineDeny \in BOOLEAN /\ fault \in Faults
          /\ phase \in {"Ready","Admitted","Written","Observed","Captured","Final","Done"}
          /\ admitted \in BOOLEAN /\ data \in FrameType /\ work \in FrameType /\ observed \in FrameType
          /\ outcome \in {"Pending","Success","Refused"}
AdmissionSound == admitted => (op#"MutateBuiltin" /\ Authorized /\ FreshRole /\ InitialPinValid)
PinPreserved == outcome="Success" /\ NeedsPin => data.pin=Pin /\ data.pin=Before.pin
RequestedAttachmentACK == outcome="Success" => data.edge=WantedEdge /\ data.edgeGen=Wanted.edgeGen
AggregateExact == outcome="Success" => data.allow=WantedAllow /\ data.deny=WantedDeny /\ data.decision=Decision(WantedAllow,WantedDeny)
RetiredRoleFinalACK == outcome="Success" /\ op="CFNDelete" => ~data.role /\ ~data.stack /\ ~data.claim /\ data.edge={}
CustomerFrame == outcome="Success" => data.customer=(IF op="MutateCustomer" THEN 2 ELSE Before.customer)
RequestedStateACK == outcome="Success" => data=Wanted
FailureAtomic == outcome="Refused" => data=Before
PendingFrame == outcome="Pending" => data=Before
NormalRequestCompletes == phase="Done" /\ fault="Normal" /\ op#"MutateBuiltin" => outcome="Success"
Spec == Init /\ [][Next]_vars
=============================================================================
