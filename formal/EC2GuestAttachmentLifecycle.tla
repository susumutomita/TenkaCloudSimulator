-------------------- MODULE EC2GuestAttachmentLifecycle --------------------
EXTENDS Naturals, FiniteSets
VARIABLE s
Init == s = [stage |-> "idle", db |-> "absent", known |-> TRUE,
 match |-> TRUE, profile |-> TRUE, prepAck |-> FALSE, attachedAck |-> FALSE,
 owner |-> FALSE, registered |-> FALSE, revoked |-> FALSE, io |-> FALSE,
 constructorDone |-> FALSE, transport |-> FALSE, transportOK |-> FALSE, published |-> FALSE,
 capturedOwner |-> FALSE, sealed |-> FALSE, sealPhase |-> "none", sealAck |-> FALSE,
 ownerDrain |-> FALSE, transportDrain |-> FALSE, canceled |-> FALSE,
 lease |-> "held", dispatched |-> FALSE, restarted |-> FALSE,
 mutation |-> FALSE, backendObserved |-> FALSE, bad |-> {}, binding |-> 1, deadline |-> 1, lifetime |-> 2]
Reserve(m) == /\ s.stage="idle" /\ s.known
 /\ s'=[s EXCEPT !.stage="reserved", !.match=m]
Prepare(write,ack) == /\ s.stage="reserved" /\ s.known /\ s.match
 /\ s.db="absent" /\ s.lease="held" /\ s.profile /\ (ack => write)
 /\ s'=[s EXCEPT !.db=IF write THEN "preparing" ELSE "absent",
 !.prepAck=ack, !.stage=IF ack THEN "prepared" ELSE "abandoned",
 !.sealed=@ \/ ~ack,
 !.bad=IF ~s.match \/ s.lease#"held" \/ ~s.profile THEN @ \cup {"prepare"} ELSE @]
BeginOwner == /\ s.stage="prepared" /\ s.known /\ s.prepAck /\ ~s.sealed /\ ~s.canceled
 /\ s'=[s EXCEPT !.stage="constructing",
 !.bad=IF ~s.prepAck \/ s.db="absent" THEN @ \cup {"consume"} ELSE @]
RegisterOwner == /\ s.stage="constructing" /\ s.known
 /\ s'=[s EXCEPT !.stage="registered", !.owner=TRUE, !.registered=TRUE]
StartTransport == /\ s.stage="registered" /\ s.known /\ s.registered
 /\ ~s.sealed /\ ~s.revoked /\ ~s.canceled
 /\ s'=[s EXCEPT !.stage="attaching", !.io=TRUE,
 !.bad=IF ~s.registered \/ ~s.owner \/ s.sealed \/ s.revoked \/ ~s.known THEN @ \cup {"io"} ELSE @]
FinishTransport(transfer,ok) == /\ s.stage="attaching" /\ s.known /\ (ok => transfer)
 /\ s'=[s EXCEPT !.stage="finished", !.constructorDone=TRUE, !.transport=transfer, !.transportOK=ok]
DeferredConstructorRevoke == /\ s.stage="registered" /\ s.known /\ (s.sealed \/ s.canceled) /\ ~s.revoked
 /\ s'=[s EXCEPT !.revoked=TRUE]
AbortConstruction == /\ s.stage="registered" /\ s.known /\ s.revoked /\ (s.sealed \/ s.canceled)
 /\ s'=[s EXCEPT !.stage="abandoned", !.constructorDone=TRUE]
FailConstructor == /\ s.stage="constructing" /\ s.known
 /\ s'=[s EXCEPT !.stage="failed", !.constructorDone=TRUE]
CommitAttached(write,ack) == /\ s.stage="finished" /\ s.known /\ s.transportOK
 /\ s.db="preparing" /\ ~s.sealed /\ (ack => write)
 /\ s'=[s EXCEPT !.db=IF write THEN "attached" ELSE @,
 !.attachedAck=ack, !.stage=IF ack THEN "committed" ELSE "failed"]
Publish == /\ s.stage="committed" /\ s.known /\ ~s.sealed /\ ~s.canceled
 /\ s.prepAck /\ s.attachedAck /\ s.transportOK
 /\ s'=[s EXCEPT !.stage="done", !.published=TRUE,
 !.bad=IF ~s.known \/ s.sealed \/ s.canceled \/ ~s.prepAck \/ ~s.attachedAck \/ ~s.transportOK THEN @ \cup {"publish"} ELSE @]
Cancel == /\ ~s.canceled /\ s'=[s EXCEPT !.canceled=TRUE]
SealStart == /\ s.stage#"idle" /\ s.known /\ s.sealPhase="none"
 /\ s'=[s EXCEPT !.sealed=TRUE, !.capturedOwner=s.owner, !.sealPhase="flagged"]
RevokeCaptured == /\ s.known /\ s.sealPhase="flagged"
 /\ s'=[s EXCEPT !.revoked=@ \/ s.capturedOwner, !.sealPhase="revoked"]
SealCommit(write,ack) == /\ s.known /\ s.sealPhase="revoked" /\ s.db#"absent" /\ (ack => write)
 /\ s'=[s EXCEPT !.db=IF ~write \/ @="drained" THEN @ ELSE "sealed",
 !.sealAck=ack, !.sealPhase="done",
 !.bad=IF ack /\ (~s.sealed \/ (s.capturedOwner /\ ~s.revoked)) THEN @ \cup {"seal"} ELSE @]
OwnerJoins == /\ s.owner /\ s.revoked /\ ~s.ownerDrain
 /\ s'=[s EXCEPT !.ownerDrain=TRUE]
TransportJoins == /\ s.transport /\ s.revoked /\ ~s.transportDrain
 /\ s'=[s EXCEPT !.transportDrain=TRUE]
Drain == /\ s.known /\ s.db="sealed" /\ s.sealed /\ s.owner
 /\ s.constructorDone
 /\ s.ownerDrain /\ (~s.transport \/ s.transportDrain)
 /\ s'=[s EXCEPT !.db="drained",
 !.bad=IF ~s.constructorDone \/ ~s.ownerDrain \/ (s.transport /\ ~s.transportDrain) \/ ~s.owner \/ ~s.sealed THEN @ \cup {"drain"} ELSE @]
Advance(coordinated,exact) == /\ s.lease="held" /\ exact
 /\ (s.db \in {"absent","drained"} \/
       (coordinated /\ s.known /\ s.db="sealed" /\ s.sealed /\ s.sealAck))
 /\ s'=[s EXCEPT !.lease="pending",
 !.bad=IF ~exact \/ (s.db \notin {"absent","drained"} /\
       (~coordinated \/ ~s.known \/ s.db#"sealed" \/ ~s.sealed \/ ~s.sealAck)) THEN @ \cup {"advance"} ELSE @]
Dispatch == /\ s.lease="pending" /\ ~s.dispatched
 /\ s'=[s EXCEPT !.dispatched=TRUE]
BackendObserve == /\ s.dispatched /\ s.lease="pending"
 /\ s'=[s EXCEPT !.lease="contained", !.backendObserved=TRUE]
ProfileMutation(same,delete) == /\ ~s.mutation
 /\ (~same \/ s.db \in {"absent","drained"})
 /\ s'=[s EXCEPT !.mutation=TRUE, !.profile=IF same /\ delete THEN FALSE ELSE @,
 !.bad=IF same /\ s.db \notin {"absent","drained"} THEN @ \cup {"profile"} ELSE @]
Restart == /\ ~s.restarted
 /\ s'=[s EXCEPT !.known=FALSE, !.restarted=TRUE]
Next == (\E m \in BOOLEAN: Reserve(m))
 \/ (\E w,a \in BOOLEAN: Prepare(w,a) \/ CommitAttached(w,a) \/ SealCommit(w,a))
 \/ BeginOwner \/ RegisterOwner \/ StartTransport \/ DeferredConstructorRevoke \/ AbortConstruction \/ FailConstructor
 \/ (\E transfer,ok \in BOOLEAN: FinishTransport(transfer,ok))
 \/ Publish \/ Cancel \/ SealStart \/ RevokeCaptured \/ OwnerJoins \/ TransportJoins \/ Drain
 \/ (\E c,e \in BOOLEAN: Advance(c,e)) \/ Dispatch \/ BackendObserve
 \/ (\E same,delete \in BOOLEAN: ProfileMutation(same,delete)) \/ Restart
TypeOK == /\ s.db \in {"absent","preparing","attached","sealed","drained"}
 /\ s.lease \in {"held","pending","contained"}
 /\ s.bad \subseteq {"prepare","consume","io","publish","seal","drain","advance","profile"}
PreparationExact == "prepare" \notin s.bad
DurableBeforeConsumption == "consume" \notin s.bad
RegisteredBeforeIO == "io" \notin s.bad
PublicationAdmission == "publish" \notin s.bad
SealBeforeAck == "seal" \notin s.bad
DurableSealAck == s.sealAck => s.db \in {"sealed","drained"}
BackendIndependent == s.lease="contained" => s.backendObserved
IndependentDrain == "drain" \notin s.bad
AdvanceGuard == "advance" \notin s.bad
ProfileGuard == "profile" \notin s.bad
ImmutableAssociation == s.binding=1 /\ s.deadline=1 /\ s.lifetime=2
RetainedScope == s.published => s.prepAck /\ s.attachedAck
NoReconstruction == s.restarted => ~s.known
Spec == Init /\ [][Next]_s
=============================================================================
