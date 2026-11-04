---------------------------- MODULE SQSLifecycle ----------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Scopes, NoHandle
HandleSet == Scopes \X (0..1) \X (0..1) \X (0..2)
VARIABLES queues, attributes, queueEpoch, messages, messageEpoch, visibleAt,
          expiresAt, deliveries, pending, now, allowed, kind, actor, previous,
          authorized, supplied, priorHandle, issued, eligible, inflight, outcome
stored == <<queues, attributes, queueEpoch, messages, messageEpoch,
            visibleAt, expiresAt, deliveries>>
vars == <<queues, attributes, queueEpoch, messages, messageEpoch, visibleAt,
          expiresAt, deliveries, pending, now, allowed, kind, actor, previous,
          authorized, supplied, priorHandle, issued, eligible, inflight, outcome>>
Current(s) == IF ~messages[s] \/ deliveries[s] = 0 THEN NoHandle
              ELSE <<s, queueEpoch[s], messageEpoch[s], deliveries[s]>>
ScopeView(g,s) == <<g[1][s],g[2][s],g[3][s],g[4][s],
                    g[5][s],g[6][s],g[7][s],g[8][s]>>
WriteKinds == {"Create", "CreateExisting", "DeleteQueue", "Send", "Receive",
               "DeleteMessage", "ChangeVisibility", "Expire"}
Record(s,k,result,h,old,new,wasEligible,wasInflight) ==
 /\ actor' = s /\ kind' = k /\ previous' = stored /\ authorized' = allowed
 /\ outcome' = result /\ supplied' = h /\ priorHandle' = old
 /\ issued' = new /\ eligible' = wasEligible /\ inflight' = wasInflight
Init ==
 /\ queues = [s \in Scopes |-> FALSE] /\ attributes = [s \in Scopes |-> 0]
 /\ queueEpoch = [s \in Scopes |-> 0] /\ messages = [s \in Scopes |-> FALSE]
 /\ messageEpoch = [s \in Scopes |-> 0] /\ visibleAt = [s \in Scopes |-> 0]
 /\ expiresAt = [s \in Scopes |-> 0] /\ deliveries = [s \in Scopes |-> 0]
 /\ pending = [s \in Scopes |-> FALSE] /\ now = 0 /\ allowed \in BOOLEAN
 /\ kind = "Init" /\ actor \in Scopes /\ previous = stored /\ authorized = TRUE
 /\ supplied = NoHandle /\ priorHandle = NoHandle /\ issued = NoHandle
 /\ eligible = TRUE /\ inflight = TRUE /\ outcome = "None"
Create(s,a) ==
 /\ allowed /\ ~queues[s] /\ queueEpoch[s] < 1
 /\ queues' = [queues EXCEPT ![s] = TRUE]
 /\ attributes' = [attributes EXCEPT ![s] = a]
 /\ queueEpoch' = [queueEpoch EXCEPT ![s] = @ + 1]
 /\ messageEpoch' = [messageEpoch EXCEPT ![s] = 0]
 /\ UNCHANGED <<messages,visibleAt,expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"Create","Success",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
CreateExisting(s,a) ==
 /\ allowed /\ queues[s] /\ attributes[s] = a
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"CreateExisting","Success",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
CreateMismatch(s,a) ==
 /\ allowed /\ queues[s] /\ attributes[s] # a
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"Error","QueueNameExists",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
BeginSend(s) ==
 /\ allowed /\ queues[s] /\ ~pending[s]
 /\ pending' = [pending EXCEPT ![s] = TRUE]
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,now,allowed>>
 /\ Record(s,"Read","CachedQueue",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
CommitSend(s,delay) ==
 /\ allowed /\ pending[s]
 /\ queues[s]
 /\ ~messages[s] /\ messageEpoch[s] < 1
 /\ messages' = [messages EXCEPT ![s] = TRUE]
 /\ messageEpoch' = [messageEpoch EXCEPT ![s] = @ + 1]
 /\ visibleAt' = [visibleAt EXCEPT ![s] = now + delay]
 /\ expiresAt' = [expiresAt EXCEPT ![s] = now + 2]
 /\ deliveries' = [deliveries EXCEPT ![s] = 0]
 /\ pending' = [pending EXCEPT ![s] = FALSE]
 /\ UNCHANGED <<queues,attributes,queueEpoch,now,allowed>>
 /\ Record(s,"Send","Success",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
RejectCachedSend(s) ==
 /\ allowed /\ pending[s] /\ ~queues[s]
 /\ pending' = [pending EXCEPT ![s] = FALSE]
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,now,allowed>>
 /\ Record(s,"Error","NonExistentQueue",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
DeleteQueue(s) ==
 /\ allowed /\ queues[s]
 /\ queues' = [queues EXCEPT ![s] = FALSE]
 /\ messages' = [messages EXCEPT ![s] = FALSE]
 /\ UNCHANGED <<attributes,queueEpoch,messageEpoch,visibleAt,
                 expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"DeleteQueue","Success",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
Receive(s,timeout) ==
 /\ allowed /\ queues[s] /\ messages[s] /\ deliveries[s] < 2
 /\ visibleAt[s] <= now
 /\ expiresAt[s] > now
 /\ deliveries' = [deliveries EXCEPT ![s] = @ + 1]
 /\ visibleAt' = [visibleAt EXCEPT ![s] = now + timeout]
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 expiresAt,pending,now,allowed>>
 /\ Record(s,"Receive","Success",NoHandle,Current(s),
           <<s,queueEpoch[s],messageEpoch[s],deliveries'[s]>>,
           visibleAt[s] <= now /\ expiresAt[s] > now,TRUE)
DeleteMessage(s,h) ==
 /\ allowed /\ queues[s] /\ messages[s] /\ expiresAt[s] > now
 /\ h = Current(s) /\ h # NoHandle
 /\ messages' = [messages EXCEPT ![s] = FALSE]
 /\ UNCHANGED <<queues,attributes,queueEpoch,messageEpoch,visibleAt,
                 expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"DeleteMessage","Success",h,Current(s),NoHandle,TRUE,TRUE)
ChangeVisibility(s,h,timeout) ==
 /\ allowed /\ queues[s] /\ messages[s] /\ expiresAt[s] > now
 /\ h = Current(s) /\ h # NoHandle
 /\ visibleAt[s] > now
 /\ visibleAt' = [visibleAt EXCEPT ![s] = now + timeout]
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"ChangeVisibility","Success",h,Current(s),NoHandle,TRUE,
           visibleAt[s] > now /\ expiresAt[s] > now)
Expire(s) ==
 /\ allowed /\ queues[s] /\ messages[s] /\ expiresAt[s] <= now
 /\ messages' = [messages EXCEPT ![s] = FALSE]
 /\ UNCHANGED <<queues,attributes,queueEpoch,messageEpoch,visibleAt,
                 expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"Expire","Success",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
StorageFailure(s) ==
 /\ allowed /\ (queues[s] \/ pending[s])
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"Error","InternalError",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
GetQueue(s) ==
 /\ allowed
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"Read",IF queues[s] THEN "QueueUrl" ELSE "NonExistentQueue",
           NoHandle,NoHandle,NoHandle,TRUE,TRUE)
Denied(s) ==
 /\ ~allowed
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,pending,now,allowed>>
 /\ Record(s,"Error","AccessDenied",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
Tick ==
 /\ now < 2 /\ now' = now + 1
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,pending,allowed>>
 /\ Record(actor,"Clock","TimeAdvanced",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
ChangeAuthority ==
 /\ allowed' \in BOOLEAN
 /\ UNCHANGED <<queues,attributes,queueEpoch,messages,messageEpoch,
                 visibleAt,expiresAt,deliveries,pending,now>>
 /\ Record(actor,"Context","AuthorityChanged",NoHandle,NoHandle,NoHandle,TRUE,TRUE)
Next == Tick \/ ChangeAuthority \/ (\E s \in Scopes:
 DeleteQueue(s) \/ BeginSend(s) \/ RejectCachedSend(s) \/ Expire(s)
 \/ StorageFailure(s) \/ GetQueue(s) \/ Denied(s)
 \/ (\E a \in 0..1: Create(s,a) \/ CreateExisting(s,a) \/ CreateMismatch(s,a))
 \/ (\E delay \in 0..1: CommitSend(s,delay))
 \/ (\E timeout \in 0..1: Receive(s,timeout))
 \/ (\E h \in HandleSet: DeleteMessage(s,h)
        \/ (\E timeout \in 0..1: ChangeVisibility(s,h,timeout))))
TypeOK ==
 /\ queues \in [Scopes -> BOOLEAN] /\ attributes \in [Scopes -> 0..1]
 /\ queueEpoch \in [Scopes -> 0..1] /\ messages \in [Scopes -> BOOLEAN]
 /\ messageEpoch \in [Scopes -> 0..1] /\ visibleAt \in [Scopes -> 0..3]
 /\ expiresAt \in [Scopes -> 0..4] /\ deliveries \in [Scopes -> 0..2]
 /\ pending \in [Scopes -> BOOLEAN] /\ now \in 0..2
 /\ allowed \in BOOLEAN /\ authorized \in BOOLEAN /\ actor \in Scopes
 /\ supplied \in HandleSet \cup {NoHandle} /\ priorHandle \in HandleSet \cup {NoHandle}
 /\ issued \in HandleSet \cup {NoHandle} /\ eligible \in BOOLEAN /\ inflight \in BOOLEAN
NoOrphans == \A s \in Scopes: messages[s] => queues[s]
AuthorizedMutations == kind \in WriteKinds => authorized
ScopedMutation == kind \in WriteKinds =>
 \A s \in Scopes \ {actor}: ScopeView(stored,s) = ScopeView(previous,s)
FailedMutationAtomic == kind = "Error" => stored = previous
IdempotentCreateUnchanged == kind = "CreateExisting" => stored = previous
ReceiveOnlyEligible == kind = "Receive" => eligible
FreshReceipt == IF kind = "Receive" THEN issued # NoHandle /\ issued # priorHandle
               /\ issued[4] > 0 ELSE TRUE
ReceiptMutationCurrent == kind \in {"DeleteMessage","ChangeVisibility"} =>
 supplied # NoHandle /\ supplied = priorHandle
ChangeOnlyInflight == kind = "ChangeVisibility" => inflight
=============================================================================
