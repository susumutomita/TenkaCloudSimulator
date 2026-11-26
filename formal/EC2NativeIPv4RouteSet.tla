--------------------- MODULE EC2NativeIPv4RouteSet ---------------------
EXTENDS FiniteSets, Naturals

CONSTANTS TableA, TableB
Tables == {TableA, TableB}
Legal == {"default", "external", "narrow"}
Inputs == Legal \cup {"hostAlias", "localRange", "serviceRange"}
Ops == {"add", "remove"}
Faults == {"ok", "policy", "owner", "target", "dryRun", "unconfirmed"}
Reads == {"exact", "dropsSibling", "wrongTable", "incomplete"}
Capacity == 2

VARIABLES state, epoch, phase, proof, readback, shown
vars == <<state, epoch, phase, proof, readback, shown>>

Canonical(d) == IF d = "hostAlias" THEN "external" ELSE d
Routes(table, config) == {d \in Inputs : <<table,d>> \in config}
Other(table) == IF table = TableA THEN TableB ELSE TableA
Allowed(f) == f \notin {"policy", "owner", "target"}
Durable(f) == f \notin {"dryRun", "unconfirmed"}
Admissible(d) == d \in Legal
CapacityAvailable(t, previous) == Cardinality(Routes(t, previous)) < Capacity
CanWrite(op,t,d,r) ==
    /\ Admissible(Canonical(d))
    /\ IF op = "add" THEN Canonical(d) \notin Routes(t,r) /\ CapacityAvailable(t,r)
                    ELSE d = Canonical(d) /\ d \in Routes(t,r)

Expected(op,t,d,r) ==
    IF op = "add" THEN r \cup {<<t,Canonical(d)>>} ELSE r \ {<<t,d>>}
Write(op,t,d,r) == Expected(op,t,d,r)
BlankProof == [ok |-> FALSE, op |-> "add", t |-> TableA, d |-> "default",
               f |-> "ok", before |-> {}, after |-> {}, issued |-> 0]
BlankRead == [t |-> TableA, routes |-> {}, complete |-> FALSE, at |-> 0]
Init == /\ state = {} /\ epoch = 0 /\ phase = "idle"
        /\ proof = BlankProof /\ readback = BlankRead /\ shown = FALSE

Apply(op,t,d,f) ==
    /\ phase \in {"idle", "unconfirmed", "shown"}
    /\ LET ok == Allowed(f) /\ Durable(f) /\ CanWrite(op,t,d,state)
           next == IF ok THEN Write(op,t,d,state) ELSE state
       IN /\ state' = next /\ phase' = IF ok THEN "await" ELSE "idle"
          /\ proof' = [ok |-> ok, op |-> op, t |-> t, d |-> d, f |-> f,
                        before |-> state, after |-> next, issued |-> epoch]
    /\ readback' = BlankRead /\ shown' = FALSE /\ UNCHANGED epoch

ReadbackOK(p,r,e) ==
    /\ p.ok /\ p.issued = e /\ r.at = e /\ r.complete /\ r.t = p.t
    /\ r.routes = Routes(p.t,p.after)
Read(kind) ==
    /\ phase = "await"
    /\ LET current == Routes(proof.t,state)
           seen == [t |-> IF kind = "wrongTable" THEN Other(proof.t) ELSE proof.t,
                    routes |-> IF kind = "dropsSibling"
                               THEN current \ (Legal \ {Canonical(proof.d)}) ELSE current,
                    complete |-> kind # "incomplete", at |-> epoch]
       IN /\ readback' = seen
          /\ phase' = IF ReadbackOK(proof,seen,epoch) THEN "confirmed" ELSE "unconfirmed"
    /\ UNCHANGED <<state,epoch,proof,shown>>

Show == /\ phase = "confirmed" /\ proof.issued = epoch
        /\ shown' = TRUE /\ phase' = "shown"
        /\ UNCHANGED <<state,epoch,proof,readback>>
Navigate == /\ epoch = 0 /\ epoch' = 1 /\ shown' = FALSE
            /\ phase' = IF phase = "await" THEN "await" ELSE "idle"
            /\ UNCHANGED <<state,proof,readback>>
Next == \/ \E op \in Ops, t \in Tables, d \in Inputs, f \in Faults: Apply(op,t,d,f)
        \/ \E kind \in Reads: Read(kind)
        \/ Show \/ Navigate

TypeOK == /\ state \subseteq Tables \X Inputs /\ epoch \in 0..1
          /\ phase \in {"idle", "await", "unconfirmed", "confirmed", "shown"} /\ shown \in BOOLEAN
OwnedCanonical == state \subseteq Tables \X Legal
PerTableLimit == \A t \in Tables : Cardinality(Routes(t,state)) <= Capacity
AtomicClass ==
    /\ ~proof.ok => proof.before = proof.after
    /\ proof.ok =>
           /\ proof.f = "ok"
           /\ Canonical(proof.d) \in Legal
           /\ IF proof.op = "add"
              THEN Canonical(proof.d) \notin Routes(proof.t,proof.before)
              ELSE proof.d = Canonical(proof.d) /\ proof.d \in Routes(proof.t,proof.before)
ExactRouteEffect == proof.ok => proof.after = Expected(proof.op,proof.t,proof.d,proof.before)
CompleteReadback ==
    phase \in {"confirmed","shown"} =>
        /\ proof.ok /\ proof.issued = epoch /\ readback.at = epoch
        /\ readback.complete /\ readback.t = proof.t /\ readback.routes = Routes(proof.t,proof.after)
ConfirmedNotice == shown => phase = "shown" /\ proof.issued = epoch /\ readback.routes = Routes(proof.t,proof.after)
=========================================================================
