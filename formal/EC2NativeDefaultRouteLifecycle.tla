------------------ MODULE EC2NativeDefaultRouteLifecycle ------------------
EXTENDS Naturals

CONSTANTS TableA, TableB, GatewayA, GatewayB
Tables == {TableA, TableB}
Gateways == {GatewayA, GatewayB}
None == "none"
DefaultGateway(t) == IF t = TableA THEN GatewayA ELSE GatewayB
Operations == {"add", "remove"}
Faults == {"ok", "wrongScope", "noGrant", "foreignTable", "foreignGateway",
           "claim", "declaration", "dryRun", "quota", "unconfirmed"}
ReadKinds == {"exact", "differentTable", "wrongTarget", "truncated"}

VARIABLES routes, tables, attachments, epoch, phase, proof, witness, notice
vars == <<routes, tables, attachments, epoch, phase, proof, witness, notice>>

EmptyRoutes == [t \in Tables |-> None]
EmptyProof == [used |-> FALSE, ok |-> FALSE, t |-> TableA, g |-> GatewayA, op |-> "add",
               f |-> "ok", table |-> TRUE, link |-> TRUE, at |-> 0,
               before |-> EmptyRoutes, after |-> EmptyRoutes]
EmptyWitness == [t |-> TableA, result |-> None, at |-> 0, complete |-> FALSE]
Init == /\ routes = EmptyRoutes /\ tables = Tables /\ attachments = Tables
        /\ epoch = 0 /\ phase = "idle" /\ proof = EmptyProof
        /\ witness = EmptyWitness /\ notice = FALSE

Authorization(f) == f \notin {"wrongScope", "noGrant"}
Native(f) == f \notin {"foreignTable", "foreignGateway", "claim", "declaration"}
Persistent(f) == f \notin {"dryRun", "quota", "unconfirmed"}
ValidDestination(op, t, g) ==
    /\ t \in tables
    /\ IF op = "add" THEN t \in attachments /\ g = DefaultGateway(t) /\ routes[t] = None
                      ELSE routes[t] # None
Expected(before, op, t, g) == [before EXCEPT ![t] = IF op = "add" THEN g ELSE None]

Apply(op, t, g, f) ==
    /\ phase \in {"idle", "unconfirmed", "shown"}
    /\ LET ok == Authorization(f) /\ Native(f) /\ Persistent(f) /\ ValidDestination(op, t, g)
           next == IF ok THEN Expected(routes, op, t, g) ELSE routes
       IN /\ proof' = [used |-> TRUE, ok |-> ok, t |-> t, g |-> g, op |-> op, f |-> f,
                       table |-> t \in tables, link |-> t \in attachments, at |-> epoch,
                       before |-> routes, after |-> next]
          /\ routes' = next
          /\ phase' = IF ok THEN "await" ELSE "idle"
    /\ witness' = EmptyWitness /\ notice' = FALSE
    /\ UNCHANGED <<tables, attachments, epoch>>

OtherTable(t) == IF t = TableA THEN TableB ELSE TableA
OtherGateway(t) == DefaultGateway(OtherTable(t))
Readback(p, w, e) ==
    /\ p.ok /\ p.at = e /\ w.at = e /\ w.complete /\ w.t = p.t
    /\ w.result = p.after[p.t]

Read(k) ==
    /\ phase = "await"
    /\ LET w == [t |-> IF k = "differentTable" THEN OtherTable(proof.t) ELSE proof.t,
                 result |-> IF k = "wrongTarget" THEN OtherGateway(proof.t) ELSE routes[proof.t],
                 at |-> epoch, complete |-> k # "truncated"]
       IN /\ witness' = w
          /\ phase' = IF Readback(proof, w, epoch) THEN "confirmed" ELSE "unconfirmed"
    /\ UNCHANGED <<routes, tables, attachments, epoch, proof, notice>>

Show ==
    /\ phase = "confirmed"
    /\ epoch = proof.at
    /\ phase' = "shown" /\ notice' = TRUE
    /\ UNCHANGED <<routes, tables, attachments, epoch, proof, witness>>

Navigate ==
    /\ epoch = 0
    /\ epoch' = 1 /\ notice' = FALSE
    /\ phase' = IF phase = "await" THEN "await" ELSE "idle"
    /\ UNCHANGED <<routes, tables, attachments, proof, witness>>

InterveningWrite ==
    /\ phase = "await"
    /\ proof.t \in tables /\ proof.t \in attachments
    /\ routes' = [routes EXCEPT ![proof.t] = IF @ = None THEN DefaultGateway(proof.t) ELSE None]
    /\ UNCHANGED <<tables, attachments, epoch, phase, proof, witness, notice>>

Cleanup(t, op) ==
    /\ phase = "idle" /\ routes[t] = None
    /\ tables' = IF op = "table" THEN tables \ {t} ELSE tables
    /\ attachments' = IF op = "link" THEN attachments \ {t} ELSE attachments
    /\ UNCHANGED <<routes, epoch, phase, proof, witness, notice>>

Next ==
    \/ \E op \in Operations, t \in Tables, g \in Gateways, f \in Faults : Apply(op, t, g, f)
    \/ \E k \in ReadKinds : Read(k)
    \/ \E t \in Tables, op \in {"table", "link"} : Cleanup(t, op)
    \/ Show \/ Navigate \/ InterveningWrite

TypeOK ==
    /\ routes \in [Tables -> Gateways \cup {None}] /\ tables \subseteq Tables
    /\ attachments \subseteq Tables /\ epoch \in 0..1
    /\ phase \in {"idle", "await", "confirmed", "unconfirmed", "shown"}
    /\ notice \in BOOLEAN

OwnedGraph ==
    \A t \in Tables : routes[t] # None => t \in tables /\ t \in attachments /\ routes[t] = DefaultGateway(t)

GrantAndScope ==
    proof.ok => proof.f \notin {"wrongScope", "noGrant", "foreignTable", "foreignGateway", "claim", "declaration"}

AtomicEffect ==
    /\ proof.used /\ ~proof.ok => proof.before = proof.after
    /\ proof.ok => /\ proof.f \notin {"dryRun", "quota", "unconfirmed"}
                   /\ proof.after = Expected(proof.before, proof.op, proof.t, proof.g)
                   /\ proof.table
                   /\ IF proof.op = "add" THEN proof.link /\ proof.before[proof.t] = None
                                         ELSE proof.before[proof.t] # None

ExactReadback ==
    phase \in {"confirmed", "shown"} =>
        /\ proof.ok /\ proof.at = epoch /\ witness.at = epoch /\ witness.complete
        /\ witness.t = proof.t /\ witness.result = proof.after[proof.t]

VerifiedNotice ==
    notice => /\ phase = "shown" /\ proof.ok /\ proof.at = epoch
              /\ witness.t = proof.t /\ witness.complete /\ witness.result = proof.after[proof.t]
=============================================================================
