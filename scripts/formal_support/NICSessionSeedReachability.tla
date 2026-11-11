------------------- MODULE NICSessionSeedReachability -------------------
EXTENDS NICSessionBookkeeping, Sequences
VARIABLE chosen
Plans == << <<>>, <<"open-web">>, <<"open-web", "open-health">>,
            <<"open-web", "receive">>, <<"open-web", "send">>,
            <<"open-web", "send", "revoke-session", "watch-session">>,
            <<"open-web", "revoke-session", "watch-session", "drain-session">>,
            <<"open-web", "read-start", "revoke-nic", "watch-nic", "watch-session">> >>
Targets == <<Empty, One, Two, Queued, Writing, ClosingWrite, Retired, ClosingRead>>
ReachInit ==
  /\ chosen \in 1..8
  /\ d = Empty /\ depth = 0
  /\ last = [before |-> d, event |-> "init", k |-> 0, ok |-> TRUE, size |-> 0, success |-> FALSE]
ReachNext ==
  /\ depth < Len(Plans[chosen])
  /\ LET command == Plans[chosen][depth+1] IN
       CASE command = "open-web" -> Open("web")
         [] command = "open-health" -> Open("health")
         [] command = "receive" -> Receive(1, TRUE, 1)
         [] command = "send" -> Send(1, TRUE, 1)
         [] command = "revoke-session" -> RevokeSession(1)
         [] command = "watch-session" -> WatchSession(1)
         [] command = "drain-session" -> DrainSession(1)
         [] command = "read-start" -> ReadStart(1)
         [] command = "revoke-nic" -> RevokeNIC
         [] command = "watch-nic" -> WatchNIC
  /\ UNCHANGED chosen
SeedTargetReached == depth = Len(Plans[chosen]) => d = Targets[chosen]
PrefixHasNext == depth < Len(Plans[chosen]) => ENABLED ReachNext
=============================================================================
