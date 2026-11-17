-------------------- MODULE VpcOriginReferenceLifecycle ---------------------
EXTENDS Naturals

CONSTANT MaxEpoch
VARIABLES lb, epoch, origin, claim, last

State == [lb |-> lb, epoch |-> epoch, origin |-> origin, claim |-> claim]
Event(kind, expected, allowed) == [kind |-> kind, before |-> State, expected |-> expected, allowed |-> allowed]

Init == /\ lb = "none" /\ epoch = 0 /\ origin = 0 /\ claim = 0
        /\ last = Event("init", 0, TRUE)

CreateLB(scheme) ==
    /\ lb = "none" /\ lb' = scheme
    /\ last' = Event("createLB", 0, TRUE)
    /\ UNCHANGED <<epoch, origin, claim>>

DeleteLB == /\ lb # "none" /\ origin = 0
            /\ lb' = "none" /\ last' = Event("deleteLB", 0, TRUE)
            /\ UNCHANGED <<epoch, origin, claim>>

Save(stack, expected, grant, laterOK) ==
    /\ LET exact == expected = origin
           free == ~stack \/ (claim = 0 /\ origin = 0)
       IN IF exact /\ free /\ grant /\ laterOK /\ lb = "private" /\ epoch < MaxEpoch
          THEN /\ epoch' = epoch + 1 /\ origin' = epoch + 1
               /\ claim' = IF stack THEN origin' ELSE claim
          ELSE UNCHANGED <<epoch, origin, claim>>
    /\ last' = Event(IF stack THEN "stackSave" ELSE "save", expected, grant /\ laterOK)
    /\ UNCHANGED lb

Remove(stack, expected, grant) ==
    /\ (~stack \/ claim > 0)
    /\ LET tag == IF stack THEN claim ELSE expected
       IN /\ IF grant /\ tag = origin /\ origin > 0
             THEN /\ origin' = 0 /\ claim' = IF stack THEN 0 ELSE claim
             ELSE /\ origin' = origin
                  /\ claim' = IF stack /\ grant /\ origin = 0 THEN 0 ELSE claim
          /\ last' = Event(IF stack THEN "stackDelete" ELSE "delete", tag, grant)
    /\ UNCHANGED <<lb, epoch>>

Next == \/ \E scheme \in {"private","public"} : CreateLB(scheme)
        \/ DeleteLB
        \/ \E expected \in 0..MaxEpoch, grant \in BOOLEAN : Save(FALSE, expected, grant, TRUE)
        \/ \E grant, laterOK \in BOOLEAN : Save(TRUE, 0, grant, laterOK)
        \/ \E expected \in 1..MaxEpoch, grant \in BOOLEAN : Remove(FALSE, expected, grant)
        \/ \E grant \in BOOLEAN : Remove(TRUE, 0, grant)

PrivateALBRequired == origin = 0 \/ lb = "private"

RejectStaleMutation ==
    (last.kind \in {"save","stackSave","delete","stackDelete"} /\
     last.expected # last.before.origin /\ last.before.origin > 0) => State = last.before

FailureRollsBack == ~last.allowed => State = last.before

GenerationAdvances ==
    (last.kind \in {"save","stackSave"} /\ origin # last.before.origin) =>
        /\ epoch = last.before.epoch + 1 /\ origin = epoch
        /\ claim = IF last.kind = "stackSave" THEN origin ELSE last.before.claim

DeleteExact ==
    (last.kind \in {"delete","stackDelete"} /\ last.before.origin > 0 /\ origin = 0) =>
        /\ last.expected = last.before.origin /\ epoch = last.before.epoch
        /\ claim = IF last.kind = "stackDelete" THEN 0 ELSE last.before.claim

TypeOK == /\ lb \in {"none","private","public"} /\ epoch \in 0..MaxEpoch
          /\ origin \in 0..epoch /\ claim \in 0..epoch

=============================================================================
