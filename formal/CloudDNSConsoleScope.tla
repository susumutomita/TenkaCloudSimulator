---- MODULE CloudDNSConsoleScope ----
EXTENDS Naturals

VARIABLES scope, pending, discovered, ticket, reads, shown, lastContinue
vars == <<scope, pending, discovered, ticket, reads, shown, lastContinue>>

Principals == {"owner", "other"}
Epochs == 0..2
Scopes == [epoch : Epochs, principal : Principals]
Discovery == [epoch : Epochs, principal : Principals, enabled : BOOLEAN]

Admit(principal, trusted, handoff) ==
    trusted /\ principal = "owner" /\ ~handoff
ReadAllowed(principal, trusted, handoff) ==
    trusted /\ principal = "owner" /\ ~handoff
CapabilityAuthorized ==
    \A t \in BOOLEAN, h \in BOOLEAN :
        Admit(scope.principal, t, h) => ReadAllowed(scope.principal, t, h)

SameEpoch(value) == value.epoch = scope.epoch
Visible ==
    discovered.enabled /\ SameEpoch(discovered)
    /\ discovered.principal = scope.principal

Init ==
    /\ \E p \in Principals :
        /\ scope = [epoch |-> 0, principal |-> p]
        /\ discovered = [epoch |-> 0, principal |-> p,
                         enabled |-> Admit(p, TRUE, FALSE)]
    /\ pending = {}
    /\ ticket = 0 /\ reads = {} /\ shown = 0 /\ lastContinue = 0

Switch ==
    /\ scope.epoch < 2
    /\ \E p \in Principals \ {scope.principal} :
        scope' = [epoch |-> scope.epoch + 1, principal |-> p]
    /\ pending' = pending \cup {scope'}
    /\ shown' = 0 /\ lastContinue' = 0 /\ ticket' = 0 /\ reads' = {}
    /\ UNCHANGED discovered

Reply ==
    \E p \in pending :
        /\ pending' = pending \ {p}
        /\ discovered' =
            IF p.epoch < discovered.epoch THEN discovered
            ELSE [epoch |-> p.epoch, principal |-> p.principal,
                  enabled |-> Admit(p.principal, TRUE, FALSE)]
        /\ UNCHANGED <<scope, ticket, reads, shown, lastContinue>>

PublishActive(id) == id = ticket
ContinueActive(id) == id = ticket
NewRead ==
    /\ Visible /\ ticket < 2
    /\ ticket' = ticket + 1
    /\ reads' = reads \cup {ticket'}
    /\ shown' = 0 /\ lastContinue' = 0
    /\ UNCHANGED <<scope, pending, discovered>>

ReadReply ==
    \E id \in reads :
        /\ reads' = reads \ {id}
        /\ shown' = IF PublishActive(id) THEN id ELSE shown
        /\ UNCHANGED <<scope, pending, discovered, ticket, lastContinue>>

ContinueRead ==
    \E id \in reads :
        /\ ContinueActive(id)
        /\ lastContinue' = id
        /\ UNCHANGED <<scope, pending, discovered, ticket, reads, shown>>

Next == Switch \/ Reply \/ NewRead \/ ReadReply \/ ContinueRead
Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ scope \in Scopes /\ pending \subseteq Scopes
    /\ discovered \in Discovery
    /\ ticket \in 0..2 /\ reads \subseteq 1..2
    /\ shown \in 0..2 /\ lastContinue \in 0..2
OnlyCurrentDiscovery ==
    Visible => (discovered.epoch = scope.epoch /\ discovered.principal = scope.principal)
OnlyCurrentReadVisible == shown = 0 \/ shown = ticket
NoStaleContinuation == lastContinue = 0 \/ lastContinue = ticket
====
