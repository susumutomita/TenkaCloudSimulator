------------------- MODULE EC2RouteTableInventoryPages --------------------
EXTENDS Integers
CONSTANT Last
VARIABLES inventory, origin, assembled, proof, afterWrite, visible
vars == <<inventory, origin, assembled, proof, afterWrite, visible>>
Revisions == 0..2
MaxRows == 100

Good == [allowed |-> TRUE, actor |-> TRUE, graph |-> TRUE,
         idCombined |-> FALSE, validSize |-> TRUE, filter |-> "all",
         seal |-> TRUE, region |-> TRUE, sameActor |-> TRUE,
         sameSelector |-> TRUE, sameSize |-> TRUE, sameOrder |-> TRUE,
         live |-> TRUE, fits |-> TRUE, rows |-> MaxRows]
Facts == {Good, [Good EXCEPT !.filter = "subnet"], [Good EXCEPT !.rows = 0],
          [Good EXCEPT !.rows = 1], [Good EXCEPT !.rows = MaxRows + 1],
          [Good EXCEPT !.allowed = FALSE], [Good EXCEPT !.actor = FALSE],
          [Good EXCEPT !.graph = FALSE], [Good EXCEPT !.idCombined = TRUE],
          [Good EXCEPT !.validSize = FALSE], [Good EXCEPT !.seal = FALSE],
          [Good EXCEPT !.region = FALSE], [Good EXCEPT !.sameActor = FALSE],
          [Good EXCEPT !.sameSelector = FALSE], [Good EXCEPT !.sameSize = FALSE],
          [Good EXCEPT !.sameOrder = FALSE], [Good EXCEPT !.live = FALSE],
          [Good EXCEPT !.fits = FALSE]}

Witness(op, f, read, anchored, used, terminal) ==
    [op |-> op, facts |-> f, read |-> read, anchored |-> anchored,
     prefix |-> used, terminal |-> terminal]
NoProof == Witness("none",Good,0,0,0,FALSE)
Shown(show, done, live, mut, matches, sampled, ack) ==
    [show |-> show, complete |-> done, live |-> live, mutation |-> mut,
     matches |-> matches, sampled |-> sampled, required |-> ack]
NoView == Shown(FALSE,FALSE,TRUE,FALSE,FALSE,0,-1)
Init ==
    /\ inventory = 0 /\ origin = 0 /\ assembled = 0
    /\ proof = NoProof /\ afterWrite = -1 /\ visible = NoView

Authorized(f) == f.allowed /\ f.actor /\ ~f.idCombined /\ f.validSize
SafeWire(f, used, terminal) ==
    /\ f.graph /\ f.fits /\ f.rows <= MaxRows
    /\ (used = 0 \/ f.rows > 0)
    /\ (terminal \/ f.rows = MaxRows)
SafeAssembly(f) == f.sameOrder /\ f.live
CursorBound(f) == f.seal /\ f.region /\ f.sameActor /\ f.sameSelector /\ f.sameSize

First(f, terminal) ==
    /\ assembled = 0
    /\ Authorized(f) /\ SafeWire(f,0,terminal) /\ SafeAssembly(f)
    /\ origin' = inventory /\ assembled' = 1
    /\ proof' = Witness("first",f,inventory,inventory,0,terminal)
    /\ UNCHANGED <<inventory, afterWrite, visible>>

Continue(f, terminal) ==
    /\ assembled > 0 /\ assembled < Last /\ ~proof.terminal
    /\ Authorized(f) /\ SafeWire(f,assembled,terminal) /\ SafeAssembly(f)
    /\ CursorBound(f)
    /\ inventory = origin
    /\ (assembled < Last - 1 \/ terminal)
    /\ assembled' = assembled + 1
    /\ proof' = Witness("next",f,inventory,origin,assembled,terminal)
    /\ UNCHANGED <<inventory, origin, afterWrite, visible>>

(* Keep an independent inventory change distinct from a successful write
   in this Console. Only the latter requires a readback before confirmation. *)
OtherWrite ==
    /\ inventory < 2 /\ inventory' = inventory + 1
    /\ UNCHANGED <<origin, assembled, proof, afterWrite, visible>>

StartRead(ack) ==
    /\ origin' = inventory'
    /\ assembled' = 0 /\ proof' = NoProof
    /\ afterWrite' = ack /\ visible' = NoView
AcknowledgedWrite ==
    /\ inventory < 2 /\ inventory' = inventory + 1
    /\ StartRead(inventory')
Refresh ==
    /\ UNCHANGED inventory
    /\ StartRead(-1)

Publish(matches) ==
    /\ assembled > 0 /\ proof.terminal
    /\ (afterWrite = -1 \/ (matches /\ proof.read >= afterWrite))
    /\ visible' = Shown(TRUE,proof.terminal,proof.facts.live,afterWrite >= 0,matches,proof.read,afterWrite)
    /\ UNCHANGED <<inventory, origin, assembled, proof, afterWrite>>

Next ==
    (\E f \in Facts, terminal \in BOOLEAN: First(f,terminal) \/ Continue(f,terminal))
    \/ (\E matches \in BOOLEAN: Publish(matches))
    \/ OtherWrite \/ AcknowledgedWrite \/ Refresh \/ UNCHANGED vars

TypeOK ==
    /\ inventory \in Revisions /\ origin \in Revisions /\ assembled \in 0..Last
    /\ proof \in [op: {"none","first","next"},facts: Facts,read: Revisions,
                   anchored: Revisions,prefix: 0..Last,terminal: BOOLEAN]
    /\ afterWrite \in {-1} \cup Revisions
    /\ visible \in [show: BOOLEAN,complete: BOOLEAN,live: BOOLEAN,mutation: BOOLEAN,
                     matches: BOOLEAN,sampled: Revisions,required: {-1} \cup Revisions]
PageAuthorized == proof.op # "none" =>
    proof.facts.allowed /\ proof.facts.actor /\ ~proof.facts.idCombined /\ proof.facts.validSize
PageBounds == proof.op # "none" =>
    /\ proof.facts.graph /\ proof.facts.fits /\ proof.facts.rows <= MaxRows
    /\ (proof.prefix = 0 \/ proof.facts.rows > 0)
    /\ (proof.terminal \/ proof.facts.rows = MaxRows)
ScopedContinuation == proof.op = "next" =>
    /\ proof.facts.seal /\ proof.facts.region /\ proof.facts.sameActor
    /\ proof.facts.sameSelector /\ proof.facts.sameSize /\ proof.read = proof.anchored
CurrentAssembly == proof.op # "none" => proof.facts.sameOrder /\ proof.facts.live
CompletePublication == visible.show => visible.live /\ visible.complete
ExactWriteReadback == visible.show /\ visible.mutation =>
    visible.matches /\ visible.sampled >= visible.required
=============================================================================
