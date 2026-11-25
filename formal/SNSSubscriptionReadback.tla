----------------------- MODULE SNSSubscriptionReadback -----------------------
EXTENDS FiniteSets

\* Bounded GetSubscriptionAttributes, public source tree
\* 8c7eaf4478317522aa0e71bebdede303c7c5c03e, internal/aws/sns.go
\* and sns_store.go. A validated ARN is represented by <<topic, subID>>;
\* parseOK abstracts syntax/account validation, not the shared Query parser.
\* grant is a captured decision set for sns:GetSubscriptionAttributes.
\* Access authentication and the successful policy-snapshot construction
\* precede this model. readOK is a storage-error witness, not a SQL model.
CONSTANTS A, B, C, East, West, Id1, Id2, Endpoint1, Endpoint2, Absent
Topics == {A, B, C}
Regions == {East, West}
IDs == {Id1, Id2}
ARNs == Topics \X IDs
TopicRows == Topics \X Regions
Protocols == {"https", "sqs"}
Endpoints == {Endpoint1, Endpoint2}
Phases == {"arn", "region", "iam", "lookup", "reply", "done"}
Codes == {"OK", "InvalidParameter", "NotFound", "AuthorizationError", "InternalError"}
Requests == [arn : ARNs, region : Regions, parseOK : BOOLEAN,
             grant : SUBSET Topics, readOK : BOOLEAN]
RowType == [arn : ARNs, topic : Topics, protocol : Protocols, endpoint : Endpoints]

VARIABLES req, store, phase, iamTopic, allowed, observation, code, result, readBefore
vars == <<req, store, phase, iamTopic, allowed, observation, code, result, readBefore>>

Parent(arn) == arn[1]
RegionOf(topic) == IF topic = C THEN West ELSE East
Neighbor(topic) == IF topic = A THEN B ELSE A
OtherRegion(region) == IF region = East THEN West ELSE East
OtherID(id) == IF id = Id1 THEN Id2 ELSE Id1
NormalTopic(topic) == <<topic, RegionOf(topic)>>
SubRow(arn, topic, protocol, endpoint) ==
  [arn |-> arn, topic |-> topic, protocol |-> protocol, endpoint |-> endpoint]
Db(topics, subs, history) == [topics |-> topics, subs |-> subs, history |-> history]

\* Normal retained, missing and historical-only cases; sibling ID is a real
\* peer resource, not a fallback. Three defensive seeds contain mismatched
\* storage input (missing topic, wrong parent, wrong stored region); these
\* are not claimed reachable through a healthy simulator's writes.
Seeds(arn) == LET t == Parent(arn)
                  p == Neighbor(t)
                  key == NormalTopic(t)
                  peerKey == NormalTopic(p)
                  row == IF arn[2] = Id1
                           THEN SubRow(arn, t, "https", Endpoint1)
                           ELSE SubRow(arn, t, "sqs", Endpoint2)
                  peer == SubRow(<<p, arn[2]>>, p, "sqs", Endpoint2)
                  sibling == SubRow(<<t, OtherID(arn[2])>>, t, "sqs", Endpoint2)
                  misowned == SubRow(arn, p, "sqs", Endpoint2)
              IN {Db({key}, {row}, {}),
                  Db({key, peerKey}, {row, peer}, {row}),
                  Db({key, peerKey}, {peer}, {}),
                  Db({key, peerKey}, {peer}, {row}),
                  Db({key}, {sibling}, {row}),
                  Db({peerKey}, {row, peer}, {row}),
                  Db({key, peerKey}, {misowned}, {}),
                  Db({<<t, OtherRegion(RegionOf(t))>>}, {row}, {})}

Init == /\ req \in Requests
        /\ store \in Seeds(req.arn)
        /\ phase = "arn"
        /\ iamTopic = Absent /\ allowed = FALSE
        /\ observation = Absent /\ code = Absent /\ result = Absent
        /\ readBefore = Absent

\* A step marking itself as part of the read captures its input store. This
\* permits an independent frame check instead of only assuming UNCHANGED.
ReadStep == readBefore' = store

ParseARN == /\ phase = "arn"
            /\ IF req.parseOK
                  THEN /\ phase' = "region" /\ code' = Absent
                  ELSE /\ phase' = "done" /\ code' = "InvalidParameter"
            /\ ReadStep
            /\ UNCHANGED <<req, store, iamTopic, allowed, observation, result>>

SelectRegion == /\ phase = "region"
                /\ IF req.region = RegionOf(Parent(req.arn))
                      THEN /\ phase' = "iam" /\ code' = Absent
                      ELSE /\ phase' = "done" /\ code' = "NotFound"
                /\ ReadStep
                /\ UNCHANGED <<req, store, iamTopic, allowed, observation, result>>

CheckAuthority == /\ phase = "iam"
                  /\ LET subject == Parent(req.arn)
                         grant == subject \in req.grant
                     IN /\ iamTopic' = subject /\ allowed' = grant
                        /\ IF grant
                              THEN /\ phase' = "lookup" /\ code' = Absent
                              ELSE /\ phase' = "done" /\ code' = "AuthorizationError"
                  /\ ReadStep
                  /\ UNCHANGED <<req, store, observation, result>>

\* Logical shape of one SQLite SELECT, not two separately observable reads.
SQLRows(db, arn, topic, region) ==
  {r \in db.subs : /\ r.arn = arn
                  /\ r.topic = topic
                  /\ <<r.topic, region>> \in db.topics}
Pick(rows) == IF rows = {} THEN Absent ELSE CHOOSE r \in rows : TRUE

Lookup == /\ phase = "lookup"
          /\ observation' = [snapshot |-> store, arn |-> req.arn, topic |-> iamTopic,
                              region |-> req.region,
                              row |-> IF req.readOK
                                        THEN Pick(SQLRows(store, req.arn, iamTopic, req.region))
                                        ELSE Absent]
          /\ phase' = "reply"
          /\ ReadStep
          /\ UNCHANGED <<req, store, iamTopic, allowed, code, result>>

WireAttributes(row) ==
  [SubscriptionArn |-> row.arn, TopicArn |-> row.topic, Owner |-> "000000000000",
   Protocol |-> row.protocol, Endpoint |-> row.endpoint,
   PendingConfirmation |-> "false", RawMessageDelivery |-> "false",
   ConfirmationWasAuthenticated |-> "false"]

Reply == /\ phase = "reply"
         /\ LET row == observation.row
            IN /\ code' = IF ~req.readOK THEN "InternalError"
                          ELSE IF row = Absent THEN "NotFound" ELSE "OK"
               /\ result' = IF row = Absent THEN Absent ELSE WireAttributes(row)
         /\ phase' = "done"
         /\ ReadStep
         /\ UNCHANGED <<req, store, iamTopic, allowed, observation>>

\* Environment actions can interleave before or after the SELECT. There are
\* no new rows, revived IDs, writes inside the read, or fairness assumptions.
\* An unsubscribe leaves deliveries for the old ID; topic deletion removes
\* its observed topic/retained/history rows together.
OtherUnsubscribe(r) == /\ r \in store.subs
                       /\ store' = [store EXCEPT !.subs = @ \ {r}]
                       /\ readBefore' = Absent
                       /\ UNCHANGED <<req, phase, iamTopic, allowed, observation, code, result>>

OtherDeleteTopic(t) == /\ \E region \in Regions : <<t, region>> \in store.topics
                       /\ store' = Db({k \in store.topics : k[1] # t},
                                      {r \in store.subs : r.topic # t},
                                      {r \in store.history : r.topic # t})
                       /\ readBefore' = Absent
                       /\ UNCHANGED <<req, phase, iamTopic, allowed, observation, code, result>>

Next == \/ ParseARN \/ SelectRegion \/ CheckAuthority \/ Lookup \/ Reply
        \/ (\E row \in store.subs : OtherUnsubscribe(row))
        \/ (\E topic \in Topics : OtherDeleteTopic(topic))
        \/ (phase = "done" /\ UNCHANGED vars)

DbType(db) == /\ db.topics \subseteq TopicRows
              /\ db.subs \subseteq RowType /\ db.history \subseteq RowType
TypeOK == /\ req \in Requests /\ DbType(store) /\ phase \in Phases
          /\ iamTopic \in Topics \cup {Absent} /\ allowed \in BOOLEAN
          /\ code \in Codes \cup {Absent}
          /\ IF observation = Absent THEN TRUE
                ELSE /\ DbType(observation.snapshot)
                     /\ observation.row \in RowType \cup {Absent}
          /\ IF readBefore = Absent THEN TRUE ELSE DbType(readBefore)

ReadDoesNotChangeStore == readBefore = Absent \/ store = readBefore
AuthorizationTargetsParent ==
  IF iamTopic = Absent THEN ~allowed
  ELSE /\ req.parseOK
       /\ req.region = RegionOf(Parent(req.arn))
       /\ iamTopic = Parent(req.arn)
       /\ allowed = (Parent(req.arn) \in req.grant)

LookupRequiresAuthorization ==
  IF observation = Absent THEN TRUE
  ELSE /\ req.parseOK /\ req.region = RegionOf(Parent(req.arn))
       /\ allowed /\ iamTopic = Parent(req.arn) /\ Parent(req.arn) \in req.grant
       /\ observation.arn = req.arn /\ observation.topic = Parent(req.arn)
       /\ observation.region = req.region

NoAuditResurrection ==
  IF observation = Absent THEN TRUE
  ELSE IF observation.row = Absent THEN TRUE
  ELSE observation.row \in observation.snapshot.subs

\* State the contract from the expected ARN and region, not the SQL's
\* effective parameters or chosen row. Check positive and negative results.
ExpectedRows(snapshot) ==
  LET parent == req.arn[1]
  IN IF <<parent, req.region>> \notin snapshot.topics THEN {}
     ELSE {r \in snapshot.subs : r.arn = req.arn /\ r.topic = parent}

RetainedJoinIsExact ==
  IF observation = Absent THEN TRUE
  ELSE LET expected == ExpectedRows(observation.snapshot)
       IN IF ~req.readOK \/ expected = {} THEN observation.row = Absent
          ELSE /\ Cardinality(expected) = 1 /\ observation.row \in expected

TypedShortCircuit ==
  IF phase # "done" THEN TRUE
  ELSE IF ~req.parseOK THEN /\ code = "InvalidParameter" /\ iamTopic = Absent /\ observation = Absent
  ELSE IF req.region # RegionOf(Parent(req.arn))
    THEN /\ code = "NotFound" /\ iamTopic = Absent /\ observation = Absent
  ELSE IF Parent(req.arn) \notin req.grant THEN /\ code = "AuthorizationError" /\ observation = Absent
  ELSE observation # Absent

\* Check the exact wire map against the captured row, not the current store.
\* A response may follow an unsubscribe or delete after its successful read.
ReplyMatchesSnapshot ==
  IF phase # "done" \/ observation = Absent THEN TRUE
  ELSE LET expected == ExpectedRows(observation.snapshot)
       IN IF ~req.readOK THEN code = "InternalError"
          ELSE IF expected = {} THEN code = "NotFound"
          ELSE LET r == CHOOSE hit \in expected : TRUE
               IN /\ code = "OK"
                  /\ result = [SubscriptionArn |-> req.arn, TopicArn |-> req.arn[1],
                                Owner |-> "000000000000", Protocol |-> r.protocol,
                                Endpoint |-> r.endpoint, PendingConfirmation |-> "false",
                                RawMessageDelivery |-> "false", ConfirmationWasAuthenticated |-> "false"]

OnlySuccessReturnsAttributes == (code = "OK") = (result # Absent)
=============================================================================
