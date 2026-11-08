-------------------------- MODULE ConsoleHandoff --------------------------
EXTENDS Naturals, FiniteSets
\* One fresh opaque code, one resulting cookie, one unrelated live cookie.
\* Two attempts include replay after success AND after failed redemption.
VARIABLES now, parent, generation, permission, issued, origin, available,
          handoffDeadline, browserDeadline, attempts, redemptions, active,
          sdk, loggedOut, accepted, witness, redeemAccepted, redeemWitness
vars == <<now,parent,generation,permission,issued,origin,available,
          handoffDeadline,browserDeadline,attempts,redemptions,active,
          sdk,loggedOut,accepted,witness,redeemAccepted,redeemWitness>>
Min(a,b) == IF a < b THEN a ELSE b
LiveParent == parent /\ generation = 1 /\ now < 2
EmptyWitness == [live |-> FALSE, time |-> 0, deadline |-> 0,
                  parent |-> FALSE, gen |-> 1, policy |-> FALSE,
                  parameter |-> "own", region |-> "east", action |-> "Get"]
Init == /\ now = 0 /\ parent = TRUE /\ generation = 1 /\ permission = TRUE
        /\ issued = FALSE /\ origin = "none" /\ available = FALSE
        /\ handoffDeadline = 0 /\ browserDeadline = 0
        /\ attempts = 0 /\ redemptions = 0 /\ active = {"peer"}
        /\ sdk = {"sts"} /\ loggedOut = FALSE
        /\ accepted = FALSE /\ witness = EmptyWitness
        /\ redeemAccepted = FALSE /\ redeemWitness = EmptyWitness
Issue(who) ==
 /\ ~issued /\ LiveParent /\ who = "participant" (* ParticipantOnly *)
 /\ issued' = TRUE /\ origin' = who /\ available' = TRUE
 /\ handoffDeadline' = Min(now + 1, 2) (* HandoffBound *)
 /\ UNCHANGED <<now,parent,generation,permission,browserDeadline,attempts,
                 redemptions,active,sdk,loggedOut,accepted,witness,redeemAccepted,redeemWitness>>
Redeem(ttl) ==
 /\ issued /\ attempts < 2
 /\ LET ok == available /\ now < handoffDeadline (* RedeemExpiry *)
              /\ LiveParent (* RedeemParent *)
    IN /\ redeemAccepted' = ok
       /\ redeemWitness' = [EmptyWitness EXCEPT !.live=available,
                  !.time=now, !.deadline=handoffDeadline, !.parent=parent, !.gen=generation]
       /\ available' = FALSE (* ConsumeEveryAttempt *)
       /\ attempts' = attempts + 1
       /\ redemptions' = redemptions + (IF ok THEN 1 ELSE 0)
       /\ active' = IF ok THEN active \cup {"self"} ELSE active
       /\ browserDeadline' = IF ok THEN Min(now + ttl, 2) ELSE browserDeadline (* BrowserBound *)
 /\ UNCHANGED <<now,parent,generation,permission,issued,origin,handoffDeadline,
                 sdk,loggedOut,accepted,witness>>
Use(p,r,a) ==
 /\ accepted' = ("self" \in active (* CookieLive *) /\ now < browserDeadline (* BrowserExpiry *)
                 /\ LiveParent (* UseParent *) /\ permission (* FreshPolicy *)
                 /\ p = "own" (* ExactParameter *) /\ r = "east" (* ExactRegion *)
                 /\ a \in {"Get","Put"} (* ExactAction *))
 /\ witness' = [live |-> "self" \in active, time |-> now, deadline |-> browserDeadline,
                 parent |-> parent, gen |-> generation, policy |-> permission,
                 parameter |-> p, region |-> r, action |-> a]
 /\ UNCHANGED <<now,parent,generation,permission,issued,origin,available,handoffDeadline,
                 browserDeadline,attempts,redemptions,active,sdk,loggedOut,redeemAccepted,redeemWitness>>
Logout ==
 /\ "self" \in active
 /\ active' = active \ {"self"} (* LocalRevocation *)
 /\ sdk' = sdk (* SDKFrame *) /\ loggedOut' = TRUE
 /\ UNCHANGED <<now,parent,generation,permission,issued,origin,available,handoffDeadline,
                 browserDeadline,attempts,redemptions,accepted,witness,redeemAccepted,redeemWitness>>
Environment ==
 /\ \/ /\ now < 3 /\ now' = now + 1 /\ UNCHANGED <<parent,generation,permission>>
       \/ /\ parent' = FALSE /\ UNCHANGED <<now,generation,permission>>
       \/ /\ generation' = 2 /\ UNCHANGED <<now,parent,permission>>
       \/ /\ permission' = ~permission /\ UNCHANGED <<now,parent,generation>>
 /\ UNCHANGED <<issued,origin,available,handoffDeadline,browserDeadline,attempts,
                 redemptions,active,sdk,loggedOut,accepted,witness,redeemAccepted,redeemWitness>>
Next == (\E who \in {"participant","operator"}: Issue(who)) \/ (\E ttl \in {1,3}: Redeem(ttl))
        \/ (\E p \in {"own","other"}, r \in {"east","west"}, a \in {"Get","Put","Delete"}: Use(p,r,a))
        \/ Logout \/ Environment \/ UNCHANGED vars
TypeOK == /\ now \in 0..3 /\ generation \in 1..2
          /\ parent \in BOOLEAN /\ permission \in BOOLEAN /\ issued \in BOOLEAN
          /\ origin \in {"none","participant","operator"} /\ available \in BOOLEAN
          /\ handoffDeadline \in 0..4 /\ browserDeadline \in 0..6
          /\ attempts \in 0..2 /\ redemptions \in 0..2
          /\ active \subseteq {"self","peer"} /\ sdk \subseteq {"sts","browser"}
          /\ loggedOut \in BOOLEAN /\ accepted \in BOOLEAN /\ redeemAccepted \in BOOLEAN
OperatorRefused == issued => origin = "participant"
ParentLifetimeBound == handoffDeadline <= 2 /\ browserDeadline <= 2
OneTimeRedemption == redemptions <= 1
AttemptConsumesCode == attempts > 0 => ~available
RedeemRequiresFreshParent == redeemAccepted =>
 /\ redeemWitness.live /\ redeemWitness.time < redeemWitness.deadline
 /\ redeemWitness.parent /\ redeemWitness.gen = 1 /\ redeemWitness.time < 2
\* Independent historical oracle reads observed prestate, not LiveParent or admission helpers.
UseRequiresFreshAuthority == accepted =>
 /\ witness.live /\ witness.time < witness.deadline /\ witness.time < 2
 /\ witness.parent /\ witness.gen = 1 /\ witness.policy
ExactDestination == accepted =>
 witness.parameter = "own" /\ witness.region = "east" /\ witness.action \in {"Get","Put"}
LogoutRejectsReplay == loggedOut => "self" \notin active
UnrelatedSessionsPreserved == "peer" \in active
SDKAuthorityPreserved == sdk = {"sts"}
=============================================================================
