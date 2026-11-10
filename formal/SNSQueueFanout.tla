----------------------------- MODULE SNSQueueFanout -----------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Queues, MaxCount, MaxBytes
VARIABLES targets, liveQueues, permitted, topicLive, oldCount, oldBytes,
          payload, commitOK, audit, delivered, done, accepted
vars == <<targets,liveQueues,permitted,topicLive,oldCount,oldBytes,
          payload,commitOK,audit,delivered,done,accepted>>
TopicPresent == topicLive
DestinationsPresent == targets \subseteq liveQueues
DestinationsAuthorized == targets \subseteq permitted
CountFits == oldCount + Cardinality(targets) <= MaxCount
BytesFit == oldBytes + payload * Cardinality(targets) <= MaxBytes
CanCommit == commitOK
Allowed == TopicPresent /\ DestinationsPresent /\ DestinationsAuthorized
           /\ CountFits /\ BytesFit /\ CanCommit
Init == /\ targets \in SUBSET Queues /\ liveQueues \in SUBSET Queues
        /\ permitted \in SUBSET Queues /\ topicLive \in BOOLEAN
        /\ oldCount \in 0..MaxCount /\ oldBytes \in 0..MaxBytes
        /\ payload \in 1..2 /\ commitOK \in BOOLEAN
        /\ audit = FALSE /\ delivered = {} /\ done = FALSE /\ accepted = FALSE
Publish == /\ ~done
           /\ accepted' = Allowed
           /\ audit' = Allowed
           /\ delivered' = IF Allowed THEN targets ELSE {}
           /\ done' = TRUE
           /\ UNCHANGED <<targets,liveQueues,permitted,topicLive,
                          oldCount,oldBytes,payload,commitOK>>
Next == Publish \/ UNCHANGED vars
TypeOK == /\ delivered \subseteq Queues /\ audit \in BOOLEAN
          /\ done \in BOOLEAN /\ accepted \in BOOLEAN
Authorized == accepted => targets \subseteq permitted
ParentPresent == accepted => topicLive
DestinationsExist == accepted => targets \subseteq liveQueues
CountBounded == oldCount + Cardinality(delivered) <= MaxCount
BytesBounded == oldBytes + payload * Cardinality(delivered) <= MaxBytes
DurableAck == accepted => commitOK
AuditExact == done => audit = accepted
RecipientsExact == accepted => delivered = targets
RefusalUnchanged == (done /\ ~accepted) => (~audit /\ delivered = {})
=============================================================================
