---- MODULE SakuraServerReadEpoch ----
EXTENDS Naturals

VARIABLES context, revision, target, pending, result
vars == <<context, revision, target, pending, result>>

Targets == {"list", "one", "two"}
Envelopes == [enabled : BOOLEAN, valid : BOOLEAN, handoff : BOOLEAN]
Context == [sequence : 1..2, config : Envelopes]
Marker == [context : 1..2, revision : 0..2, target : Targets]
NoResult == [context |-> 0, revision |-> 0, target |-> "list", actual |-> "list"]

VisibleCapability(config) ==
    config.enabled /\ config.valid /\ ~config.handoff
Mounted == VisibleCapability(context.config)
BoundContext(r) == r.context = context.sequence
BoundRevision(r) == r.revision = revision
ProjectionOK(r, actual) == r.target = actual

Visible ==
    Mounted /\ result /= NoResult
    /\ BoundContext(result) /\ BoundRevision(result) /\ result.target = target

CurrentRequest(num, selection) ==
    [context |-> context.sequence, revision |-> num, target |-> selection]

Init ==
    /\ \E config \in Envelopes : context = [sequence |-> 1, config |-> config]
    /\ revision = 0 /\ target = "list" /\ result = NoResult
    /\ pending = IF Mounted THEN {CurrentRequest(0, "list")} ELSE {}

NewContext ==
    /\ context.sequence < 2
    /\ \E config \in Envelopes : context' = [sequence |-> 2, config |-> config]
    /\ revision' = 0 /\ target' = "list"
    /\ pending' = IF VisibleCapability(context'.config) THEN
         pending \cup {[context |-> context'.sequence, revision |-> 0, target |-> "list"]}
       ELSE pending
    /\ UNCHANGED result

SelectOrRefresh ==
    /\ Mounted /\ revision < 2
    /\ \E next \in Targets :
        /\ next = target \/ next = "list" \/ (target = "list" /\ Visible)
        /\ target' = next
    /\ revision' = revision + 1
    /\ pending' = pending \cup {CurrentRequest(revision', target')}
    /\ UNCHANGED <<context, result>>

Reply ==
    \E r \in pending, got \in Targets :
        /\ pending' = pending \ {r}
        /\ result' = IF ProjectionOK(r, got) THEN [context |-> r.context, revision |-> r.revision,
                                               target |-> r.target, actual |-> got] ELSE result
        /\ UNCHANGED <<context, revision, target>>

Next == NewContext \/ SelectOrRefresh \/ Reply
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ context \in Context /\ revision \in 0..2 /\ target \in Targets
    /\ pending \subseteq Marker
    /\ result \in [context : 1..2, revision : 0..2, target : Targets, actual : Targets] \cup {NoResult}
NoUnadvertisedMount ==
    Mounted => (context.config.enabled /\ context.config.valid /\ ~context.config.handoff)
OnlyCurrentResult ==
    Visible => (result.context = context.sequence /\ result.revision = revision /\ result.target = target)
NoDifferentServer == Visible => result.actual = target
====
