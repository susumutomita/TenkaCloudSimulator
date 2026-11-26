--------------------------- MODULE S3MultipartPartReceipt ---------------------------
EXTENDS Naturals

\* One Console write followed by a scoped, terminal ListParts observation.
\* A separate SDK writer may replace/delete the committed part at any time.
\* Later changes do not invalidate an already correctly observed receipt.
VARIABLES stored, phase, scope, writes, ack, observed, notice
vars == <<stored, phase, scope, writes, ack, observed, notice>>
Value(num, size, tag) == [num |-> num, size |-> size, tag |-> tag]
Wanted == Value(1, 7, "a")
Other == Value(1, 7, "b")
Missing == Value(0, 0, "none")
Receipt(status, strong) == [status |-> status, strong |-> strong, part |-> Wanted]
Observation(v, bound, full, generation, seen) ==
  [v |-> v, bound |-> bound, full |-> full, generation |-> generation, seen |-> seen]
NoView == Observation(Missing, FALSE, FALSE, 0, FALSE)
Init ==
  /\ stored = Missing /\ phase = "init" /\ scope = 0 /\ writes = 0
  /\ ack = Receipt(0, FALSE) /\ observed = NoView /\ notice = FALSE

Put(status, strong) ==
  /\ phase = "init"
  /\ phase' = "await" /\ writes' = writes+1
  /\ ack' = Receipt(status, strong)
  /\ stored' = Wanted \* A malformed reply may still follow an actual commit.
  /\ UNCHANGED <<scope, observed, notice>>
Peer(v) ==
  /\ phase # "init" /\ stored # v
  /\ stored' = v
  /\ UNCHANGED <<phase, scope, writes, ack, observed, notice>>
Read(kind) ==
  LET value == CASE kind = "different-number" -> Value(2, 7, "a")
                 [] kind = "different-size" -> Value(1, 6, "a")
                 [] OTHER -> stored
  IN /\ phase = "await"
     /\ phase' = "read"
     /\ observed' = Observation(value, kind # "out-of-scope", kind # "partial", 0, TRUE)
     /\ UNCHANGED <<stored, scope, writes, ack, notice>>
Retire ==
  /\ scope = 0 /\ phase # "init"
  /\ scope' = 1 /\ notice' = FALSE
  /\ UNCHANGED <<stored, phase, writes, ack, observed>>

Allowed == /\ phase = "read"
           /\ ack.status = 200 /\ ack.strong
           /\ observed.bound /\ observed.full /\ observed.seen
           /\ observed.generation = scope
           /\ observed.v.num = ack.part.num
           /\ observed.v.size = ack.part.size
           /\ observed.v.tag = ack.part.tag
Publish ==
  /\ Allowed
  /\ phase' = "done" /\ notice' = TRUE
  /\ UNCHANGED <<stored, scope, writes, ack, observed>>
Refresh ==
  /\ phase \in {"await", "read", "done"}
  /\ phase' = "refresh" /\ notice' = FALSE
  /\ UNCHANGED <<stored, scope, writes, ack, observed>>
Halt ==
  /\ phase \in {"refresh", "done"} \/ scope = 1
  /\ UNCHANGED vars
Next ==
  \/ \E answer \in {200, 204}, strong \in BOOLEAN : Put(answer, strong)
  \/ \E v \in {Other, Missing} : Peer(v)
  \/ \E kind \in {"normal", "different-number", "different-size", "out-of-scope", "partial"} : Read(kind)
  \/ Retire \/ Publish \/ Refresh \/ Halt

TypeOK ==
  /\ stored \in {Wanted, Other, Missing}
  /\ phase \in {"init", "await", "read", "done", "refresh"}
  /\ scope \in 0..1 /\ writes \in 0..2 /\ notice \in BOOLEAN
  /\ ack \in [status:{0,200,204}, strong:BOOLEAN, part:{Wanted}]
  /\ observed \in [v:{Wanted,Other,Missing,Value(2,7,"a"),Value(1,6,"a")},
                   bound:BOOLEAN, full:BOOLEAN, generation:0..1, seen:BOOLEAN]
ConfirmedReadback ==
  notice => /\ ack.status = 200 /\ ack.strong
            /\ observed.seen /\ observed.bound /\ observed.full
            /\ observed.v = Wanted
CurrentViewNotice == notice => scope = observed.generation
NoMutationReplay == writes <= 1
=====================================================================================
