-------------------------- MODULE ConsoleHandoff --------------------------
EXTENDS Naturals, FiniteSets
\* One fresh opaque code, one resulting cookie, one unrelated live cookie.
\* Two attempts include replay after success AND after failed redemption.
\* A handoff names exactly one target kind: a Parameter Store parameter (Get/Put)
\* or an EC2 instance (Describe and start of its terminal session). The terminal
\* start additionally requires an answering guest relay.
VARIABLES kind, relay, now, parent, generation, permission, issued, origin, available,
          handoffDeadline, browserDeadline, attempts, redemptions, active,
          sdk, loggedOut, accepted, witness, redeemAccepted, redeemWitness
aux == <<kind,relay>>
vars == <<kind,relay,now,parent,generation,permission,issued,origin,available,
          handoffDeadline,browserDeadline,attempts,redemptions,active,
          sdk,loggedOut,accepted,witness,redeemAccepted,redeemWitness>>
Min(a,b) == IF a < b THEN a ELSE b
LiveParent == parent /\ generation = 1 /\ now < 2
EmptyWitness == [kind |-> "none", relay |-> FALSE, live |-> FALSE, time |-> 0, deadline |-> 0,
                  parent |-> FALSE, gen |-> 1, policy |-> FALSE,
                  parameter |-> "own", region |-> "east", action |-> "Get"]
Init == /\ kind = "none" /\ relay = FALSE /\ now = 0 /\ parent = TRUE /\ generation = 1 /\ permission = TRUE
        /\ issued = FALSE /\ origin = "none" /\ available = FALSE
        /\ handoffDeadline = 0 /\ browserDeadline = 0
        /\ attempts = 0 /\ redemptions = 0 /\ active = {"peer"}
        /\ sdk = {"sts"} /\ loggedOut = FALSE
        /\ accepted = FALSE /\ witness = EmptyWitness
        /\ redeemAccepted = FALSE /\ redeemWitness = EmptyWitness
Issue(who,k) ==
 /\ ~issued /\ LiveParent /\ who = "participant" (* ParticipantOnly *)
 /\ kind' = k /\ UNCHANGED relay
 /\ issued' = TRUE /\ origin' = who /\ available' = TRUE
 /\ handoffDeadline' = Min(now + 1, 2) (* HandoffBound *)
 /\ UNCHANGED <<now,parent,generation,permission,browserDeadline,attempts,
                 redemptions,active,sdk,loggedOut,accepted,witness,redeemAccepted,redeemWitness>>
Redeem(ttl) ==
 /\ issued /\ attempts < 2
 /\ LET ok == available /\ now < handoffDeadline (* RedeemExpiry *)
              /\ LiveParent (* RedeemParent *)
    IN /\ redeemAccepted' = ok
       /\ redeemWitness' = IF ~ok THEN EmptyWitness ELSE [EmptyWitness EXCEPT !.live=available,
                  !.time=now, !.deadline=handoffDeadline, !.parent=parent, !.gen=generation]
       /\ available' = FALSE (* ConsumeEveryAttempt *)
       /\ attempts' = attempts + 1
       /\ redemptions' = redemptions + (IF ok THEN 1 ELSE 0)
       /\ active' = IF ok THEN active \cup {"self"} ELSE active
       /\ browserDeadline' = IF ok THEN Min(now + ttl, 2) ELSE browserDeadline (* BrowserBound *)
 /\ UNCHANGED <<aux,now,parent,generation,permission,issued,origin,handoffDeadline,
                 sdk,loggedOut,accepted,witness>>
Use(p,r,a) ==
 /\ LET ok == ("self" \in active (* CookieLive *) /\ now < browserDeadline (* BrowserExpiry *)
                 /\ LiveParent (* UseParent *) /\ permission (* FreshPolicy *)
                 /\ p = "own" (* ExactParameter *) /\ r = "east" (* ExactRegion *)
                 /\ ((kind = "parameter" /\ a \in {"Get","Put"})
                     \/ (kind = "instance" /\ a \in {"Describe","Terminal"})) (* ExactAction *)
                 /\ (a # "Terminal" \/ relay) (* RelayReady *))
    IN /\ accepted' = ok
       /\ witness' = IF ~ok THEN EmptyWitness ELSE [kind |-> kind, relay |-> relay, live |-> "self" \in active, time |-> now, deadline |-> browserDeadline,
                 parent |-> parent, gen |-> generation, policy |-> permission,
                 parameter |-> p, region |-> r, action |-> a]
 /\ UNCHANGED <<aux,now,parent,generation,permission,issued,origin,available,handoffDeadline,
                 browserDeadline,attempts,redemptions,active,sdk,loggedOut,redeemAccepted,redeemWitness>>
Logout ==
 /\ "self" \in active
 /\ active' = active \ {"self"} (* LocalRevocation *)
 /\ sdk' = sdk (* SDKFrame *) /\ loggedOut' = TRUE
 /\ UNCHANGED <<aux,now,parent,generation,permission,issued,origin,available,handoffDeadline,
                 browserDeadline,attempts,redemptions,accepted,witness,redeemAccepted,redeemWitness>>
Environment ==
 /\ \/ /\ now < 3 /\ now' = now + 1 /\ UNCHANGED <<parent,generation,permission,relay>>
       \/ /\ parent' = FALSE /\ UNCHANGED <<now,generation,permission,relay>>
       \/ /\ generation' = 2 /\ UNCHANGED <<now,parent,permission,relay>>
       \/ /\ permission' = ~permission /\ UNCHANGED <<now,parent,generation,relay>>
       \/ /\ relay' = ~relay /\ UNCHANGED <<now,parent,generation,permission>>
 /\ UNCHANGED <<kind,issued,origin,available,handoffDeadline,browserDeadline,attempts,
                 redemptions,active,sdk,loggedOut,accepted,witness,redeemAccepted,redeemWitness>>
Next == (\E who \in {"participant","operator"}, k \in {"parameter","instance"}: Issue(who,k)) \/ (\E ttl \in {1,3}: Redeem(ttl))
        \/ (\E p \in {"own","other"}, r \in {"east","west"}, a \in {"Get","Put","Delete","Describe","Terminal"}: Use(p,r,a))
        \/ Logout \/ Environment \/ UNCHANGED vars
TypeOK == /\ kind \in {"none","parameter","instance"} /\ relay \in BOOLEAN /\ now \in 0..3 /\ generation \in 1..2
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
 /\ witness.parameter = "own" /\ witness.region = "east"
 /\ \/ witness.kind = "parameter" /\ witness.action \in {"Get","Put"}
    \/ witness.kind = "instance" /\ witness.action \in {"Describe","Terminal"}
\* A terminal start is accepted only while the guest relay answers.
TerminalRequiresRelay == (accepted /\ witness.action = "Terminal") => witness.relay
LogoutRejectsReplay == loggedOut => "self" \notin active
UnrelatedSessionsPreserved == "peer" \in active
SDKAuthorityPreserved == sdk = {"sts"}
=============================================================================
