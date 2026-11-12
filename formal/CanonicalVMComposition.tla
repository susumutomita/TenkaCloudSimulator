-------------------- MODULE CanonicalVMComposition --------------------
EXTENDS Naturals, FiniteSets
\* Three disjoint initial scenarios avoid a Cartesian product of independent
\* network compilation, concrete launch, and asynchronous child ownership.
\* Identity equality is a finite predicate, not a proof of OS/SQL/crypto APIs.
VARIABLE s
Init == \E mode \in {"launch", "network", "creator", "configuration", "registry"}:
 s = [mode |-> mode, phase |-> "new", live |-> TRUE, sealed |-> FALSE,
       restarted |-> FALSE, pending |-> FALSE, durable |-> FALSE, preparedAck |-> FALSE,
       checked |-> FALSE, captured |-> FALSE, paused |-> FALSE,
       attached |-> FALSE, active |-> FALSE, qga |-> FALSE,
       published |-> FALSE, starts |-> 0, ownerJoin |-> FALSE,
       transportJoin |-> FALSE, creatorDone |-> FALSE, debt |-> FALSE,
       localDrained |-> FALSE, contained |-> FALSE, released |-> FALSE, configured |-> FALSE, advertised |-> FALSE,
       provider |-> FALSE, associated |-> FALSE, operation |-> FALSE, observed |-> FALSE, held |-> FALSE, mutated |-> FALSE, bad |-> {}]
Valid == s.live /\ ~s.sealed
Plan(pinned, sameStack) == /\ s.mode="launch" /\ s.phase="new"
 /\ pinned /\ sameStack /\ Valid
 /\ s'=[s EXCEPT !.phase="planned", !.checked=TRUE,
 !.bad=IF ~pinned \/ ~sameStack THEN @ \cup {"plan"} ELSE @]
PendingCommit(ack) == /\ s.mode="launch" /\ s.phase="planned" /\ ack
 /\ s'=[s EXCEPT !.phase="pending", !.pending=TRUE]
Reserve(intentWrite, intentReadback) == /\ s.mode="launch" /\ s.phase="pending"
 /\ Valid /\ intentWrite /\ intentReadback
 /\ s'=[s EXCEPT !.phase="reserved", !.durable=TRUE,
 !.bad=IF ~intentWrite \/ ~intentReadback \/ ~s.pending THEN @ \cup {"journal"} ELSE @]
Prepare(exact) == /\ s.mode="launch" /\ s.phase="reserved" /\ exact /\ Valid
 /\ s'=[s EXCEPT !.phase="prepared",
 !.bad=IF ~exact \/ ~s.durable \/ ~s.checked THEN @ \cup {"prepare"} ELSE @]
PreparedCommit(write, readback) == /\ s.mode="launch" /\ s.phase="prepared"
 /\ Valid /\ write /\ readback
 /\ s'=[s EXCEPT !.phase="prepared-committed", !.preparedAck=TRUE,
 !.bad=IF ~write \/ ~readback THEN @ \cup {"prepared-journal"} ELSE @]
Start == /\ s.mode="launch" /\ s.phase="prepared-committed" /\ Valid
 /\ s.starts=0 /\ s.durable /\ s.preparedAck
 /\ s'=[s EXCEPT !.phase="launched", !.starts=@+1, !.captured=TRUE, !.debt=TRUE,
 !.bad=IF ~s.durable \/ ~s.preparedAck \/ ~s.pending THEN @ \cup {"launch"} ELSE @]
Control(exact, stopped) == /\ s.mode="launch" /\ s.phase="launched"
 /\ exact /\ stopped /\ Valid
 /\ s'=[s EXCEPT !.phase="paused", !.paused=TRUE,
 !.bad=IF ~exact \/ ~stopped THEN @ \cup {"control"} ELSE @]
Attach(exact, ack) == /\ s.mode="launch" /\ s.phase="paused"
 /\ exact /\ ack /\ Valid
 /\ s'=[s EXCEPT !.phase="attached", !.attached=TRUE,
 !.bad=IF ~exact \/ ~ack \/ ~s.paused THEN @ \cup {"attach"} ELSE @]
Activate == /\ s.mode="launch" /\ s.phase="attached" /\ Valid
 /\ s.attached
 /\ s'=[s EXCEPT !.phase="active", !.active=TRUE,
 !.bad=IF ~s.attached \/ s.sealed \/ ~s.live THEN @ \cup {"activate"} ELSE @]
Agent(exact, ready) == /\ s.mode="launch" /\ s.phase="active"
 /\ exact /\ ready /\ Valid
 /\ s'=[s EXCEPT !.phase="ready", !.qga=TRUE,
 !.bad=IF ~exact \/ ~ready THEN @ \cup {"agent"} ELSE @]
Publish(sameGeneration) == /\ s.mode="launch" /\ s.phase="ready"
 /\ s.pending /\ s.qga /\ sameGeneration /\ Valid
 /\ s'=[s EXCEPT !.phase="published", !.published=TRUE,
 !.bad=IF ~s.qga \/ ~sameGeneration \/ ~s.pending \/ s.sealed \/ ~s.live
 THEN @ \cup {"publication"} ELSE @]
\* Entry revocation is before the cleanup snapshot; pending work may still
\* return its exact child and must retain it, not erase cleanup debt.
Seal == /\ ~s.sealed /\ s'=[s EXCEPT !.sealed=TRUE]
Restart == /\ ~s.restarted
 /\ s'=[s EXCEPT !.restarted=TRUE, !.live=FALSE]
OwnerJoin == /\ s.captured /\ s.sealed /\ ~s.ownerJoin
 /\ s'=[s EXCEPT !.ownerJoin=TRUE]
TransportJoin == /\ s.sealed /\ ~s.transportJoin
 /\ s'=[s EXCEPT !.transportJoin=TRUE]
Drain == /\ s.sealed /\ s.captured /\ s.ownerJoin /\ s.transportJoin
 /\ (s.mode#"creator" \/ s.creatorDone) /\ ~s.localDrained
 /\ s'=[s EXCEPT !.localDrained=TRUE, !.debt=FALSE,
 !.bad=IF ~s.ownerJoin \/ ~s.transportJoin \/ (s.mode="creator" /\ ~s.creatorDone)
 THEN @ \cup {"drain"} ELSE @]
Contain(exact, observed) == /\ s.mode="launch" /\ s.localDrained
 /\ s.live /\ exact /\ observed /\ ~s.contained
 /\ s'=[s EXCEPT !.contained=TRUE,
 !.bad=IF ~exact \/ ~observed THEN @ \cup {"contain"} ELSE @]
Release == /\ s.mode="launch" /\ s.contained /\ s.live /\ ~s.released
 /\ s'=[s EXCEPT !.released=TRUE]
NetworkCompile(same, valid) == /\ s.mode="network" /\ s.phase="new"
 /\ same /\ valid
 /\ s'=[s EXCEPT !.phase="network", !.checked=TRUE,
 !.bad=IF ~same \/ ~valid THEN @ \cup {"network"} ELSE @]
NetworkLease == /\ s.mode="network" /\ s.phase="network" /\ Valid
 /\ ~s.held /\ ~s.mutated
 /\ s'=[s EXCEPT !.held=TRUE]
NetworkMutation(same) == /\ s.mode="network" /\ s.phase="network"
 /\ ~s.mutated /\ (~same \/ ~s.held)
 /\ s'=[s EXCEPT !.mutated=TRUE,
 !.bad=IF same /\ s.held THEN @ \cup {"mutation"} ELSE @]
Delete(authorized, same) == /\ s.mode="network" /\ s.phase="network"
 /\ authorized /\ same /\ ~s.sealed
 /\ s'=[s EXCEPT !.sealed=TRUE, !.phase="deleting",
 !.bad=IF ~authorized \/ ~same THEN @ \cup {"delete"} ELSE @]
\* BeginSupervisorBoundLaunch duplicates an explicitly allowed endpoint before
\* admission; it does not adopt/restart children or qualify a VM socket.
BeginCreator(validFD, budget) == /\ s.mode="creator" /\ s.phase="new"
 /\ Valid /\ validFD /\ budget
 /\ s'=[s EXCEPT !.phase="creating", !.debt=TRUE,
 !.bad=IF ~validFD \/ ~budget THEN @ \cup {"creator"} ELSE @]
ReturnChild == /\ s.mode="creator" /\ s.phase="creating"
 /\ s'=[s EXCEPT !.phase="returned", !.captured=TRUE, !.creatorDone=TRUE, !.starts=1]
PublishChild == /\ s.mode="creator" /\ s.phase="returned" /\ Valid
 /\ s'=[s EXCEPT !.phase="published", !.published=TRUE,
 !.bad=IF s.sealed \/ ~s.live THEN @ \cup {"publication"} ELSE @]
\* Configuration publishes support, never physical readiness. Platform and file
\* validators are abstract predicates; their parser implementation is not proved.
Configure(platform, strictFile, pin, explicit) ==
 /\ s.mode="configuration" /\ s.phase="new" /\ Valid
 /\ platform /\ strictFile /\ pin /\ explicit
 /\ s'=[s EXCEPT !.phase="configured", !.configured=TRUE,
     !.bad=IF ~platform \/ ~strictFile \/ ~pin \/ ~explicit THEN @ \cup {"configuration"} ELSE @]
Advertise == /\ s.mode="configuration" /\ s.phase="configured"
 /\ s.configured /\ ~s.advertised
 /\ s'=[s EXCEPT !.advertised=TRUE]
\* Provider registration retains typed source identity. Host association selects
\* only that saved source/service, never supplied addresses. Read remains allowed
\* after seal to observe an already retained operation; Start does not.
ProviderRegister(typed, ready, services) == /\ s.mode="registry" /\ ~s.provider
 /\ typed /\ ready /\ services /\ Valid
 /\ s'=[s EXCEPT !.provider=TRUE,
 !.bad=IF ~typed \/ ~ready \/ ~services THEN @ \cup {"provider"} ELSE @]
Associate(exactOwner, exactBinding, exactService) == /\ s.mode="registry"
 /\ s.provider /\ ~s.associated /\ s.live
 /\ exactOwner /\ exactBinding /\ exactService
 /\ s'=[s EXCEPT !.associated=TRUE,
 !.bad=IF ~exactOwner \/ ~exactBinding \/ ~exactService THEN @ \cup {"association"} ELSE @]
RepeatAssociation(sameKey, sameService) == /\ s.mode="registry" /\ s.associated
 /\ sameKey /\ sameService /\ s.phase="new"
 /\ s'=[s EXCEPT !.phase="repeated",
 !.bad=IF ~sameKey \/ ~sameService THEN @ \cup {"rebind"} ELSE @]
StartOperation(operator, savedBinding) == /\ s.mode="registry" /\ s.associated
 /\ ~s.operation /\ operator /\ savedBinding /\ Valid
 /\ s'=[s EXCEPT !.operation=TRUE,
 !.bad=IF ~operator \/ ~savedBinding \/ s.sealed \/ ~s.live THEN @ \cup {"operation"} ELSE @]
ReadOperation(operator, savedBinding) == /\ s.mode="registry" /\ s.operation
 /\ ~s.observed /\ operator /\ savedBinding
 /\ s'=[s EXCEPT !.observed=TRUE,
 !.bad=IF ~operator \/ ~savedBinding THEN @ \cup {"operation"} ELSE @]
Next == (\E t,r,v \in BOOLEAN: ProviderRegister(t,r,v) \/ Associate(t,r,v)) \/ (\E o,b \in BOOLEAN: RepeatAssociation(o,b) \/ StartOperation(o,b) \/ ReadOperation(o,b)) \/ (\E p,f,h,e \in BOOLEAN: Configure(p,f,h,e)) \/ Advertise \/ (\E x,y \in BOOLEAN: Plan(x,y) \/ Reserve(x,y) \/ PreparedCommit(x,y) \/ Control(x,y)
 \/ Attach(x,y) \/ Agent(x,y) \/ Contain(x,y) \/ NetworkCompile(x,y)
 \/ Delete(x,y) \/ BeginCreator(x,y))
 \/ (\E x \in BOOLEAN: PendingCommit(x) \/ Prepare(x) \/ Publish(x) \/ NetworkMutation(x))
 \/ Start \/ Activate \/ Seal \/ Restart \/ OwnerJoin \/ TransportJoin
 \/ Drain \/ Release \/ NetworkLease \/ ReturnChild \/ PublishChild
TypeOK == /\ s.mode \in {"launch","network","creator","configuration","registry"} /\ s.starts \in 0..1
 /\ s.bad \subseteq {"plan","journal","prepared-journal","prepare","launch","control","attach",
 "activate","agent","publication","drain","contain","network","mutation","delete","creator","configuration","provider","association","rebind","operation"}
PinnedPlan == "plan" \notin s.bad
JournalBeforeLaunch == "journal" \notin s.bad /\ "prepared-journal" \notin s.bad /\ "launch" \notin s.bad
PreparedIdentity == "prepare" \notin s.bad
PausedExactControl == "control" \notin s.bad
ExactAttachment == "attach" \notin s.bad
AttachBeforeActivation == "activate" \notin s.bad
PrivateAgentReadiness == "agent" \notin s.bad
PublicationAdmission == "publication" \notin s.bad
IndependentCleanup == "drain" \notin s.bad
ObservedContainment == "contain" \notin s.bad
ReleaseAfterContainment == s.released => s.contained
NetworkOwnership == "network" \notin s.bad
FrozenNetwork == "mutation" \notin s.bad
AuthorizedDelete == "delete" \notin s.bad
CreatorAdmission == "creator" \notin s.bad
NoReconstruction == s.restarted => ~s.live
UncertainDebtRetained == (s.captured /\ ~s.localDrained) => s.debt
ConfigurationAdmission == "configuration" \notin s.bad
ConfiguredIsNotReady == s.mode="configuration" => ~s.published /\ ~s.qga /\ s.starts=0
TypedProvider == "provider" \notin s.bad
SavedAssociation == "association" \notin s.bad
NoRebinding == "rebind" \notin s.bad
SavedOperationAuthority == "operation" \notin s.bad
Spec == Init /\ [][Next]_s
=============================================================================
