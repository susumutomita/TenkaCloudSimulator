---------------- MODULE PersistentTransportLifecycle ----------------
EXTENDS Naturals, FiniteSets, Sequences
\* One already admitted child; two one-shot producers, no packet semantics.
Requests == {1,2} \* 1 probe, 2 passive service
VARIABLES closed, pclosing, expired, queueClosed, queue, stage, selected,
 writer, reader, servicePump, sendPump, receivePump, dial,
 readCall, readStarted, dialStarted, childDone, streamClose, serviceClose,
 done, slot, effects, enteredEver, badAdmission, badPublish, successes
vars == <<closed,pclosing,expired,queueClosed,queue,stage,selected,
 writer,reader,servicePump,sendPump,receivePump,dial,readCall,readStarted,
 dialStarted,childDone,streamClose,serviceClose,done,slot,effects,enteredEver,
 badAdmission,badPublish,successes>>
Init == /\ closed=FALSE /\ pclosing=FALSE /\ expired=FALSE
 /\ queueClosed=FALSE /\ queue = <<>> /\ stage=[i \in Requests |-> 0]
 /\ selected=0 /\ writer=TRUE /\ reader=TRUE /\ servicePump=TRUE
 /\ sendPump=TRUE /\ receivePump=TRUE /\ dial=FALSE /\ readCall=FALSE
 /\ readStarted=FALSE /\ dialStarted=FALSE /\ childDone=FALSE
 /\ streamClose=0 /\ serviceClose=0 /\ done=FALSE /\ slot=TRUE
 /\ enteredEver={} /\ effects={} /\ badAdmission=FALSE /\ badPublish=FALSE /\ successes={}
Enqueue(i) == /\ ~closed /\ ~queueClosed /\ stage[i]=0 /\ Len(queue)<1
 /\ (IF i=1 THEN sendPump ELSE TRUE)
 /\ queue'=Append(queue,i) /\ stage'=[stage EXCEPT ![i]=1]
 /\ badAdmission'=(badAdmission \/ closed \/ queueClosed \/ (i=1 /\ ~sendPump))
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badPublish,successes>>
Select == /\ writer /\ ~closed /\ selected=0 /\ Len(queue)>0
 /\ (LET i==Head(queue)
          reject==i=1 /\ pclosing
       IN
    /\ queue'=Tail(queue)
    /\ stage'=[stage EXCEPT ![i]=IF reject THEN 4 ELSE 2]
    /\ selected'=(IF reject THEN 0 ELSE i)
    /\ badAdmission'=(badAdmission \/ (~reject /\ i=1 /\ pclosing)))
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badPublish,successes>>
Enter == /\ selected # 0 /\ stage[selected]=2 /\ ~closed /\ ~(selected=1 /\ expired)
 /\ stage'=[stage EXCEPT ![selected]=3] /\ effects'=effects \cup {selected} /\ enteredEver'=enteredEver \cup {selected}
 /\ badAdmission'=(badAdmission \/ closed \/ (selected=1 /\ expired))
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,badPublish,successes>>
Reject == /\ selected # 0 /\ stage[selected]=2 /\ (closed \/ (selected=1 /\ expired))
 /\ stage'=[stage EXCEPT ![selected]=4] /\ selected'=0
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
Return == /\ selected # 0 /\ stage[selected]=3
 /\ stage'=[stage EXCEPT ![selected]=4] /\ selected'=0
 /\ (LET accepted == ~closed /\ ~(selected=1 /\ expired) IN
    /\ successes'=(IF accepted THEN successes \cup {selected} ELSE successes)
    /\ badPublish'=(badPublish \/ (accepted /\ (closed \/ (selected=1 /\ expired)))))
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badAdmission>>
CloseProbe == /\ ~pclosing /\ pclosing'=TRUE
 /\ UNCHANGED <<closed,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
CloseWhole == /\ ~closed /\ closed'=TRUE /\ pclosing'=TRUE
 /\ UNCHANGED <<expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
Expire == /\ ~expired /\ expired'=TRUE /\ pclosing'=TRUE
 \* Only already-entered uncertain output causes shared failure here.
 /\ closed'=(closed \/ (selected=1 /\ stage[selected]=3))
 /\ UNCHANGED <<queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
WriterExit == /\ closed /\ writer /\ selected=0
 /\ writer'=FALSE /\ queueClosed'=TRUE /\ queue' = <<>>
 /\ stage'=[i \in Requests |-> IF stage[i]=1 THEN 4 ELSE stage[i]]
 /\ UNCHANGED <<closed,pclosing,expired,selected,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,
 childDone,streamClose,serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
ReadBegin == /\ ~pclosing /\ ~readStarted /\ readStarted'=TRUE /\ readCall'=TRUE
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,dialStarted,childDone,streamClose,serviceClose,
 done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
ReadReturn == /\ readCall /\ readCall'=FALSE
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readStarted,dialStarted,childDone,streamClose,
 serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
DialBegin == /\ ~pclosing /\ ~dialStarted /\ dialStarted'=TRUE /\ dial'=TRUE
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,readCall,readStarted,childDone,streamClose,serviceClose,
 done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
DialReturn == /\ dial /\ dial'=FALSE
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,readCall,readStarted,dialStarted,childDone,streamClose,
 serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
JoinProbePumps == /\ pclosing /\ stage[1] \in {0,4} /\ ~readCall
 /\ sendPump'=FALSE /\ receivePump'=FALSE
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,dial,readCall,readStarted,dialStarted,childDone,streamClose,serviceClose,
 done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
JoinChild == /\ pclosing /\ ~sendPump /\ ~receivePump /\ ~dial /\ ~childDone
 /\ childDone'=TRUE
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,streamClose,
 serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
JoinBasePumps == /\ closed /\ stage[2] \in {0,4}
 /\ reader'=FALSE /\ servicePump'=FALSE
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,
 sendPump,receivePump,dial,readCall,readStarted,dialStarted,childDone,streamClose,
 serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
CloseStream(result) == /\ closed /\ streamClose=0 /\ streamClose'=result
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,childDone,
 serviceClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
CloseService(result) == /\ closed /\ serviceClose=0 /\ serviceClose'=result
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,childDone,
 streamClose,done,slot,effects,enteredEver,badAdmission,badPublish,successes>>
Finish == /\ closed /\ ~writer /\ ~reader /\ ~servicePump /\ childDone
 /\ streamClose # 0 /\ serviceClose # 0 /\ ~done
 /\ done'=TRUE /\ slot'=~(streamClose=1 /\ serviceClose=1)
 /\ UNCHANGED <<closed,pclosing,expired,queueClosed,queue,stage,selected,writer,reader,
 servicePump,sendPump,receivePump,dial,readCall,readStarted,dialStarted,childDone,
 streamClose,serviceClose,effects,enteredEver,badAdmission,badPublish,successes>>
Next == (\E i \in Requests: Enqueue(i)) \/ Select \/ Enter \/ Reject \/ Return
 \/ CloseProbe \/ CloseWhole \/ Expire \/ WriterExit \/ ReadBegin \/ ReadReturn
 \/ DialBegin \/ DialReturn \/ JoinProbePumps \/ JoinChild \/ JoinBasePumps
 \/ (\E result \in {1,2}: CloseStream(result) \/ CloseService(result)) \/ Finish
TypeOK == /\ stage \in [Requests -> 0..4] /\ selected \in 0..2
 /\ Len(queue)<=1 /\ streamClose \in 0..2 /\ serviceClose \in 0..2
 /\ {closed,pclosing,expired,queueClosed,writer,reader,servicePump,sendPump,
 receivePump,dial,readCall,readStarted,dialStarted,childDone,done,slot,
 badAdmission,badPublish} \subseteq BOOLEAN
 /\ effects \subseteq Requests /\ enteredEver \subseteq Requests /\ successes \subseteq Requests
 /\ queue \in Seq(Requests)
QueueSealed == /\ (queueClosed => Len(queue)=0 /\ ~writer)
 /\ (~writer => queueClosed /\ Len(queue)=0)
QueueOwnership == \A i \in Requests: (stage[i]=1) <=> i \in {queue[j]: j \in 1..Len(queue)}
SelectedDebt == selected # 0 => writer /\ stage[selected] \in {2,3}
ProbePumpDebt == ~sendPump => stage[1] \in {0,4}
ReadJoin == ~receivePump => ~readCall
ChildJoin == childDone => ~sendPump /\ ~receivePump /\ ~dial
CompletionJoin == done => ~writer /\ ~reader /\ ~servicePump /\ childDone /\ streamClose # 0 /\ serviceClose # 0
CapacityRelease == ~slot => done /\ streamClose=1 /\ serviceClose=1
NoInvalidAdmission == ~badAdmission
NoLateSuccess == ~badPublish
EffectRetained == effects = enteredEver
=============================================================================
