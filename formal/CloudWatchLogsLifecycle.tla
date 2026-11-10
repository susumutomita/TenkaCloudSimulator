---------------------- MODULE CloudWatchLogsLifecycle ----------------------
EXTENDS Naturals, FiniteSets
CONSTANT Teams
VARIABLES groups, streams, events, generation, tokens, receipts
vars == <<groups, streams, events, generation, tokens, receipts>>
Init == /\ groups = {} /\ streams = {} /\ events = {}
        /\ generation = [t \in Teams |-> 0]
        /\ tokens = {} /\ receipts = {}
CreateGroup(t) == /\ t \notin groups /\ generation[t] < 2
                  /\ groups' = groups \cup {t}
                  /\ generation' = [generation EXCEPT ![t] = @ + 1]
                  /\ UNCHANGED <<streams, events, tokens, receipts>>
CreateStream(t) == /\ t \in groups /\ streams' = streams \cup {t}
                   /\ UNCHANGED <<groups, events, generation, tokens, receipts>>
Put(t, allowed, valid) ==
    /\ t \in streams
    /\ allowed /\ valid
    /\ events' = events \cup {[team |-> t, gen |-> generation[t],
                               authorized |-> allowed, accepted |-> valid]}
    /\ UNCHANGED <<groups, streams, generation, tokens, receipts>>
DeleteGroup(t) == /\ t \in groups
                  /\ groups' = groups \ {t}
                  /\ streams' = streams \ {t}
                  /\ events' = {e \in events : e.team # t}
                  /\ UNCHANGED <<generation, tokens, receipts>>
\* A stream deletion advances the abstract incarnation immediately. Recreate
\* makes that incarnation readable; native code instead assigns it on create.
DeleteStream(t) == /\ t \in streams /\ generation[t] < 2
                   /\ streams' = streams \ {t}
                   /\ events' = {record \in events : record.team # t}
                   /\ generation' = [generation EXCEPT ![t] = @ + 1]
                   /\ UNCHANGED <<groups, tokens, receipts>>
IssueToken(t) == /\ t \in streams
                 /\ tokens' = tokens \cup {[team |-> t, gen |-> generation[t]]}
                 /\ UNCHANGED <<groups, streams, events, generation, receipts>>
ReadToken(t, token) ==
    /\ t \in streams
    /\ token.team = t /\ token.gen = generation[t]
    /\ receipts' = receipts \cup {[requestTeam |-> t, tokenTeam |-> token.team,
                                  activeGen |-> generation[t], tokenGen |-> token.gen]}
    /\ UNCHANGED <<groups, streams, events, generation, tokens>>
Next == (\E t \in Teams : CreateGroup(t) \/ CreateStream(t) \/ DeleteGroup(t) \/ DeleteStream(t)
          \/ IssueToken(t) \/ (\E a,v \in BOOLEAN : Put(t,a,v))
          \/ (\E token \in tokens : ReadToken(t,token))) \/ UNCHANGED vars
TypeOK == /\ groups \subseteq Teams /\ streams \subseteq Teams
          /\ generation \in [Teams -> 0..2]
          /\ events \subseteq [team: Teams, gen: 1..2, authorized: BOOLEAN, accepted: BOOLEAN]
          /\ tokens \subseteq [team: Teams, gen: 1..2]
          /\ receipts \subseteq [requestTeam: Teams, tokenTeam: Teams, activeGen: 1..2, tokenGen: 1..2]
NoOrphans == /\ streams \subseteq groups
             /\ \A e \in events : e.team \in streams /\ e.gen = generation[e.team]
OnlyAcceptedWrites == \A e \in events : e.authorized /\ e.accepted
TokenAuthority == \A r \in receipts : r.requestTeam = r.tokenTeam /\ r.activeGen = r.tokenGen
=============================================================================
