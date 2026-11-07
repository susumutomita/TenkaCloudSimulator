----------------------------- MODULE SNSAdmission -----------------------------
EXTENDS Naturals
Ops == {"CreateTopic","Subscribe","Publish","Unsubscribe","Read","Unknown"}
Writes == {"CreateTopic","Subscribe","Publish","Unsubscribe"}
Requests == [op : Ops, single : BOOLEAN, attrs : BOOLEAN, value : BOOLEAN,
             scope : BOOLEAN, region : BOOLEAN, fifo : BOOLEAN,
             authorized : BOOLEAN, initialized : BOOLEAN]
VARIABLES request, phase, success, mutated
vars == <<request,phase,success,mutated>>
Single(r) == r.single
Attributes(r) == r.attrs
Value(r) == r.value
Scope(r) == r.scope
Region(r) == r.region
FIFO(r) == r.op # "Publish" \/ ~r.fifo
Authorized(r) == r.authorized
Initialized(r) == r.initialized
Supported(r) == r.op # "Unknown"
Accepted(r) == Single(r) /\ Attributes(r) /\ Value(r) /\ Scope(r) /\ Region(r)
               /\ FIFO(r) /\ Authorized(r) /\ Initialized(r) /\ Supported(r)
Expected(r) == r.single /\ r.attrs /\ r.value /\ r.scope /\ r.region
               /\ (r.op # "Publish" \/ ~r.fifo) /\ r.authorized
               /\ r.initialized /\ r.op # "Unknown"
Init == /\ request \in Requests /\ phase = 0 /\ success = FALSE /\ mutated = FALSE
Dispatch == LET accepted == Accepted(request) IN
 /\ phase = 0 /\ phase' = 1 /\ success' = accepted
 /\ mutated' = (accepted /\ request.op \in Writes)
 /\ UNCHANGED request
Next == Dispatch \/ (phase = 1 /\ UNCHANGED vars)
TypeOK == request \in Requests /\ phase \in 0..1 /\ success \in BOOLEAN /\ mutated \in BOOLEAN
NoUnauthorizedEffect == success => request.authorized
NoFIFOPublish == success => request.op # "Publish" \/ ~request.fifo
NoRegionMismatch == success => request.region
NoSuccessOnRefusal == success => Expected(request)
NoRejectedMutation == ~Expected(request) => ~mutated
=============================================================================
