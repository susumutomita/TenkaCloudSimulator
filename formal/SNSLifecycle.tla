----------------------------- MODULE SNSLifecycle -----------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Topics, Subs, Messages, None, MaxMessages, MaxBytes, MaxDeliveries
VARIABLES live, subOwner, msgOwner, sizes, deliveries, last
vars == <<live,subOwner,msgOwner,sizes,deliveries,last>>
EmptySubs == [s \in Subs |-> None]
EmptyMsgs == [m \in Messages |-> None]
EmptySizes == [m \in Messages |-> 0]
Frame == [topics |-> live, subs |-> subOwner, msgs |-> msgOwner,
          bytes |-> sizes, rows |-> deliveries]
Notice(kind,t,s,m,ok) == [kind |-> kind, topic |-> t, sub |-> s, msg |-> m,
                         ok |-> ok, before |-> Frame]
ParentExists(t) == t \in live
Committed(writeOK) == writeOK
Stored == {m \in Messages : msgOwner[m] # None}
RECURSIVE ByteSum(_)
ByteSum(ms) == IF ms = {} THEN 0 ELSE
              LET m == CHOOSE k \in ms : TRUE IN sizes[m] + ByteSum(ms \ {m})
CountFits == Cardinality(Stored) < MaxMessages
BytesFit(n) == ByteSum(Stored) + n <= MaxBytes
DeliveryFits(t) == Cardinality(deliveries) + Cardinality({s \in Subs : subOwner[s] = t}) <= MaxDeliveries
ClearSubs(t) == [s \in Subs |-> IF subOwner[s] = t THEN None ELSE subOwner[s]]
ClearMsgs(t) == [m \in Messages |-> IF msgOwner[m] = t THEN None ELSE msgOwner[m]]
ClearSizes(t) == [m \in Messages |-> IF msgOwner[m] = t THEN 0 ELSE sizes[m]]
ClearDeliveries(t) == {d \in deliveries : msgOwner[d[1]] # t /\ subOwner[d[2]] # t}
Init == /\ live = {} /\ subOwner = EmptySubs /\ msgOwner = EmptyMsgs
        /\ sizes = EmptySizes /\ deliveries = {}
        /\ last = [kind |-> "none", topic |-> None, sub |-> None, msg |-> None,
                    ok |-> FALSE, before |-> [topics |-> {}, subs |-> EmptySubs,
                        msgs |-> EmptyMsgs, bytes |-> EmptySizes, rows |-> {}]]
Create(t,w) == LET ok == t \in live \/ Committed(w) IN
 /\ live' = IF ok /\ w THEN live \cup {t} ELSE live
 /\ last' = Notice("create",t,None,None,ok)
 /\ UNCHANGED <<subOwner,msgOwner,sizes,deliveries>>
Subscribe(t,s,w) == LET ok == ParentExists(t) /\ subOwner[s] = None /\ Committed(w) IN
 /\ subOwner' = IF ok /\ w THEN [subOwner EXCEPT ![s] = t] ELSE subOwner
 /\ last' = Notice("subscribe",t,s,None,ok)
 /\ UNCHANGED <<live,msgOwner,sizes,deliveries>>
Publish(t,m,n,w) == LET ok == ParentExists(t) /\ msgOwner[m] = None /\
                            CountFits /\ BytesFit(n) /\ DeliveryFits(t) /\ Committed(w) IN
 /\ msgOwner' = IF ok /\ w THEN [msgOwner EXCEPT ![m] = t] ELSE msgOwner
 /\ sizes' = IF ok /\ w THEN [sizes EXCEPT ![m] = n] ELSE sizes
 /\ deliveries' = IF ok /\ w THEN deliveries \cup {<<m,s>> : s \in {k \in Subs : subOwner[k] = t}} ELSE deliveries
 /\ last' = Notice("publish",t,None,m,ok)
 /\ UNCHANGED <<live,subOwner>>
Unsubscribe(s) == /\ subOwner' = [subOwner EXCEPT ![s] = None]
 /\ last' = Notice("unsubscribe",None,s,None,subOwner[s] # None)
 /\ UNCHANGED <<live,msgOwner,sizes,deliveries>>
Delete(t,w) == LET ok == ParentExists(t) /\ Committed(w)
                  remove == ok /\ w IN
 /\ live' = IF remove THEN live \ {t} ELSE live
 /\ subOwner' = IF remove THEN ClearSubs(t) ELSE subOwner
 /\ msgOwner' = IF remove THEN ClearMsgs(t) ELSE msgOwner
 /\ sizes' = IF remove THEN ClearSizes(t) ELSE sizes
 /\ deliveries' = IF remove THEN ClearDeliveries(t) ELSE deliveries
 /\ last' = Notice("delete",t,None,None,ok)
Next == (\E t \in Topics,w \in BOOLEAN : Create(t,w))
 \/ (\E t \in Topics,s \in Subs,w \in BOOLEAN : Subscribe(t,s,w))
 \/ (\E t \in Topics,m \in Messages,n \in 1..3,w \in BOOLEAN : Publish(t,m,n,w))
 \/ (\E s \in Subs : Unsubscribe(s))
 \/ (\E t \in Topics,w \in BOOLEAN : Delete(t,w))
 \/ UNCHANGED vars
TypeOK == /\ live \subseteq Topics /\ subOwner \in [Subs -> Topics \cup {None}]
 /\ msgOwner \in [Messages -> Topics \cup {None}] /\ sizes \in [Messages -> 0..3]
 /\ deliveries \subseteq Messages \X Subs /\ last.ok \in BOOLEAN
NoOrphans == /\ \A s \in Subs : subOwner[s] # None => subOwner[s] \in live
             /\ \A m \in Messages : msgOwner[m] # None => msgOwner[m] \in live
MessageCountBounded == Cardinality(Stored) <= MaxMessages
ByteBudgetBounded == ByteSum(Stored) <= MaxBytes
DeliveryBudgetBounded == Cardinality(deliveries) <= MaxDeliveries
NoDanglingDeliveredMessage == \A d \in deliveries : msgOwner[d[1]] # None
DurableAck == ~last.ok \/ CASE
 last.kind = "create" -> last.topic \in live
 [] last.kind = "subscribe" -> subOwner[last.sub] = last.topic /\ last.topic \in live
 [] last.kind = "publish" -> msgOwner[last.msg] = last.topic /\ last.topic \in live
 [] last.kind = "delete" -> last.topic \notin live
 [] OTHER -> TRUE
PublishRecipientsExact == ~last.ok \/ last.kind # "publish" \/
 {d \in deliveries : d[1] = last.msg} = {<<last.msg,s>> : s \in {k \in Subs : subOwner[k] = last.topic}}
DeleteAckEmpty == ~last.ok \/ last.kind # "delete" \/
 /\ \A s \in Subs : subOwner[s] # last.topic
 /\ \A m \in Messages : msgOwner[m] # last.topic
 /\ \A d \in deliveries : last.before.msgs[d[1]] # last.topic /\ last.before.subs[d[2]] # last.topic
PeerFrame == ~last.ok \/ last.kind # "delete" \/
 /\ \A t \in Topics \ {last.topic} : (t \in live) = (t \in last.before.topics)
 /\ \A s \in Subs : last.before.subs[s] # last.topic => subOwner[s] = last.before.subs[s]
 /\ \A m \in Messages : last.before.msgs[m] # last.topic =>
       msgOwner[m] = last.before.msgs[m] /\ sizes[m] = last.before.bytes[m]
 /\ {d \in deliveries : last.before.msgs[d[1]] # last.topic /\ last.before.subs[d[2]] # last.topic}
    = {d \in last.before.rows : last.before.msgs[d[1]] # last.topic /\ last.before.subs[d[2]] # last.topic}
=============================================================================
