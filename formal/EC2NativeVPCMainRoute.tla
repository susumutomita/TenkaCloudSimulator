---------------------------- MODULE EC2NativeVPCMainRoute ----------------------------
EXTENDS Naturals

MaxStatic == 2
Epochs == 0..2
Kinds == {"none", "claim", "declaration", "foreign-reference"}
Reads == {"none", "refused", "empty", "main", "explicit", "implicit", "unavailable"}
Proofs == {"effective-complete", "effective-denied", "effective-partial", "effective-stale"}
Events == {"idle", "create", "delete", "static", "read"} \cup Proofs
Empty == [vpc |-> 0, main |-> 0, localMain |-> FALSE, custom |-> FALSE,
          localCustom |-> FALSE, subnet |-> FALSE, explicit |-> FALSE, static |-> 0,
          legacy |-> "unaltered", stack |-> "unaltered"]
VARIABLES net, epoch, retained, result, event, allowed, ack, finalWitness
vars == <<net, epoch, retained, result, event, allowed, ack, finalWitness>>

Init == /\ net = Empty /\ epoch = 0 /\ retained = "none" /\ result = "none"
        /\ event = "idle" /\ allowed = FALSE /\ ack = FALSE /\ finalWitness = FALSE

Create(valid) ==
  /\ net.vpc = 0 /\ epoch < 2
  /\ net' = IF valid THEN [Empty EXCEPT !.vpc = epoch + 1, !.main = epoch + 1, !.localMain = TRUE] ELSE net
  /\ epoch' = IF valid THEN epoch + 1 ELSE epoch
  /\ event' = "create" /\ allowed' = valid /\ ack' = valid
  /\ result' = "none" /\ finalWitness' = FALSE
  /\ UNCHANGED retained

Mark(kind) ==
  /\ net.vpc # 0
  /\ retained' = kind /\ result' = "none" /\ event' = "idle"
  /\ UNCHANGED <<net, epoch, allowed, ack, finalWitness>>

SetUp(kind) ==
  /\ net.vpc # 0 /\ retained = "none"
  /\ net' = CASE kind = "custom" -> [net EXCEPT !.custom = TRUE, !.localCustom = TRUE]
              [] kind = "subnet" -> [net EXCEPT !.subnet = TRUE]
              [] OTHER -> [net EXCEPT !.explicit = net.custom /\ net.subnet]
  /\ result' = "none" /\ event' = "idle"
  /\ UNCHANGED <<epoch, retained, allowed, ack, finalWitness>>

Remove(kind) ==
  /\ net.vpc # 0 /\ retained = "none"
  /\ net' = CASE kind = "explicit" -> [net EXCEPT !.explicit = FALSE]
              [] kind = "subnet" -> IF net.explicit THEN net ELSE [net EXCEPT !.subnet = FALSE]
              [] OTHER -> IF net.explicit THEN net ELSE [net EXCEPT !.custom = FALSE, !.localCustom = FALSE]
  /\ result' = "none" /\ event' = "idle"
  /\ UNCHANGED <<epoch, retained, allowed, ack, finalWitness>>

AddStatic ==
  /\ net.vpc # 0 /\ retained = "none"
  /\ allowed' = (net.static < MaxStatic)
  /\ ack' = (net.static < MaxStatic)
  /\ net' = IF ack' THEN [net EXCEPT !.static = @ + 1] ELSE net
  /\ event' = "static" /\ result' = "none"
  /\ UNCHANGED <<epoch, retained, finalWitness>>

RemoveStatic ==
  /\ net.static > 0
  /\ net' = [net EXCEPT !.static = @ - 1]
  /\ event' = "idle" /\ result' = "none"
  /\ UNCHANGED <<epoch, retained, allowed, ack, finalWitness>>

DeleteParent(late) ==
  /\ net.vpc # 0
  /\ allowed' = (retained = "none" /\ ~net.custom /\ ~net.subnet /\ net.static = 0 /\ ~late)
  /\ ack' = allowed'
  /\ net' = IF ack' THEN Empty ELSE net
  /\ finalWitness' = late
  /\ result' = "none" /\ event' = "delete"
  /\ UNCHANGED <<epoch, retained>>

DeleteMainDirect ==
  /\ net.main # 0
  /\ net' = net
  /\ result' = "none" /\ event' = "idle"
  /\ UNCHANGED <<epoch, retained, allowed, ack, finalWitness>>

Read(filtered) ==
  /\ result' = IF retained # "none" THEN "refused"
               ELSE IF filtered THEN IF net.explicit THEN "explicit" ELSE "empty"
               ELSE IF net.main # 0 THEN "main" ELSE "empty"
  /\ event' = "read" /\ UNCHANGED <<net, epoch, retained, allowed, ack, finalWitness>>

\* Separately acquired, same-view native subnet inventory and full route
\* list. This is a UI observation, not a wire association or a mutation.
ReadEffective(proof) ==
  /\ net.main # 0
  /\ result' = IF proof = "effective-complete" /\ retained = "none"
               THEN IF net.subnet /\ ~net.explicit THEN "implicit" ELSE "empty"
               ELSE "unavailable"
  /\ event' = proof /\ UNCHANGED <<net, epoch, retained, allowed, ack, finalWitness>>

Next == (\E valid \in BOOLEAN: Create(valid)) \/ (\E k \in Kinds: Mark(k)) \/
        (\E k \in {"custom", "subnet", "explicit"}: SetUp(k) \/ Remove(k)) \/ AddStatic \/ RemoveStatic \/
        (\E late \in BOOLEAN: DeleteParent(late)) \/ DeleteMainDirect \/ (\E filtered \in BOOLEAN: Read(filtered)) \/
        (\E proof \in Proofs: ReadEffective(proof))
Spec == Init /\ [][Next]_vars

TypeOK ==
  /\ net.vpc \in Epochs /\ net.main \in Epochs /\ net.static \in 0..(MaxStatic+1)
  /\ net.localMain \in BOOLEAN /\ net.localCustom \in BOOLEAN
  /\ net.custom \in BOOLEAN /\ net.subnet \in BOOLEAN /\ net.explicit \in BOOLEAN
  /\ epoch \in Epochs /\ retained \in Kinds /\ result \in Reads /\ event \in Events
  /\ allowed \in BOOLEAN /\ ack \in BOOLEAN /\ finalWitness \in BOOLEAN
AtomicPairedSlot == net.vpc = net.main /\ (net.vpc # 0 => net.vpc = epoch)
OwnedLocalOnly == net.localMain = (net.main # 0) /\ net.localCustom = net.custom
NoImplicitWire == result = "explicit" => net.explicit
CountersExcludeLocal == net.static <= MaxStatic /\ (event = "static" => ack = allowed)
RetainedFencesDelete == (event = "delete" /\ ack) => (retained = "none" /\ ~finalWitness /\ net.vpc = 0)
ReadInvalidRefuses == event = "read" /\ retained # "none" => result = "refused"
UnrelatedOwnershipUnchanged == net.legacy = "unaltered" /\ net.stack = "unaltered"
EffectiveNeedsFreshInventories ==
  (event \in Proofs /\ (event # "effective-complete" \/ retained # "none")) => result = "unavailable"
EffectiveOnlyUnassociated ==
  (event = "effective-complete" /\ retained = "none") =>
    (result = IF net.subnet /\ ~net.explicit THEN "implicit" ELSE "empty")
=============================================================================
