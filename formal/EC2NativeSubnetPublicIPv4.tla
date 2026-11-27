--------------------------- MODULE EC2NativeSubnetPublicIPv4 ---------------------------
EXTENDS Naturals

\* One exact owned subnet and an immutable neighbor/CloudFormation frame.
\* Two initial Boolean values, one set request, and its scoped readback.
VARIABLES prior, stored, revision, frame, phase, receipt, generation, ack, notice, tries
Vars == <<prior, stored, revision, frame, phase, receipt, generation, ack, notice, tries>>

Cases == {"valid", "no-grant", "handoff", "wrong-region", "missing-subnet",
          "stack-owned", "claim", "declaration", "invalid-value", "fault", "drift"}
ObservationKinds == {"complete", "wrong-value", "wrong-target", "partial", "denied"}
Frame == {"sibling-subnet", "stack-subnet", "parent-vpc", "route-state"}
EmptyReceipt == [case |-> "none", value |-> FALSE, ok |-> FALSE]
EmptyAck == [kind |-> "none", generation |-> 0, value |-> FALSE]

Init ==
  /\ prior \in BOOLEAN
  /\ stored = prior /\ revision = 0 /\ frame = Frame
  /\ phase = "initial" /\ receipt = EmptyReceipt
  /\ generation = 0 /\ ack = EmptyAck /\ notice = FALSE /\ tries = 0

Authorized(c) == c # "no-grant" /\ c # "handoff"
Owned(c) ==
  /\ c # "wrong-region" /\ c # "missing-subnet" /\ c # "stack-owned"
  /\ c # "claim" /\ c # "declaration"
Input(c) == c # "invalid-value"
Durable(c) == c # "fault" /\ c # "drift"

Save(c, value) ==
  LET accept == Authorized(c) /\ Owned(c) /\ Input(c) /\ Durable(c)
  IN /\ phase = "initial"
     /\ receipt' = [case |-> c, value |-> value, ok |-> accept]
     /\ stored' = IF accept THEN value ELSE stored
     /\ revision' = IF accept /\ value # stored THEN revision+1 ELSE revision
     /\ phase' = IF accept THEN "await-read" ELSE "done"
     /\ tries' = tries+1
     /\ UNCHANGED <<prior, frame, generation, ack, notice>>

Read(kind) ==
  /\ phase = "await-read"
  /\ ack' = [kind |-> kind, generation |-> 0,
               value |-> IF kind = "wrong-value" THEN ~stored ELSE stored]
  /\ phase' = "read"
  /\ UNCHANGED <<prior, stored, revision, frame, receipt, generation, notice, tries>>

Publish ==
  /\ phase = "read" /\ receipt.ok
  /\ ack.kind \notin {"denied", "partial"} /\ ack.kind # "wrong-target"
  /\ ack.value = receipt.value /\ ack.generation = generation
  /\ notice' = TRUE /\ phase' = "done"
  /\ UNCHANGED <<prior, stored, revision, frame, receipt, generation, ack, tries>>

Retire ==
  /\ generation = 0 /\ phase # "initial"
  /\ generation' = 1 /\ notice' = FALSE
  /\ UNCHANGED <<prior, stored, revision, frame, phase, receipt, ack, tries>>

Refresh ==
  /\ phase \in {"await-read", "read", "done"}
  /\ phase' = "refreshed" /\ notice' = FALSE
  /\ UNCHANGED <<prior, stored, revision, frame, receipt, generation, ack, tries>>

Finish ==
  /\ (phase \in {"done", "refreshed"} \/ generation = 1)
  /\ UNCHANGED Vars

Next ==
  \/ \E c \in Cases, b \in BOOLEAN : Save(c,b)
  \/ \E kind \in ObservationKinds : Read(kind)
  \/ Publish \/ Retire \/ Refresh \/ Finish

TypeOK ==
  /\ prior \in BOOLEAN /\ stored \in BOOLEAN /\ revision \in 0..2
  /\ frame \subseteq Frame /\ generation \in 0..1 /\ notice \in BOOLEAN /\ tries \in 0..2
  /\ phase \in {"initial", "await-read", "read", "done", "refreshed"}
  /\ receipt \in [case:Cases \cup {"none"}, value:BOOLEAN, ok:BOOLEAN]
  /\ ack \in [kind:ObservationKinds \cup {"none"}, generation:0..1, value:BOOLEAN]

ExactAdmission == receipt.ok = (receipt.case = "valid")
RefusalNoEffect == ~receipt.ok => stored = prior /\ revision = 0
ExactPreference ==
  receipt.ok => stored = receipt.value /\ revision = (IF receipt.value = prior THEN 0 ELSE 1)
SameFrame == frame = Frame
CurrentConfirmedNotice ==
  notice => receipt.ok /\ ack.kind = "complete" /\ ack.value = receipt.value
            /\ ack.generation = generation
NoReplay == tries <= 1
=======================================================================================
