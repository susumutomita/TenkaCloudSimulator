---------------------- MODULE EC2InternetGatewayLifecycle ----------------------
CONSTANT Regions
VARIABLES row, proof, pending, notice
vars == <<row, proof, pending, notice>>
Writes == {"create", "delete"}
Operations == Writes \cup {"describe", "none"}
States == {"absent", "native", "native-dependent", "stack", "stack-attached"}
Selection == {"all", "exact", "missing"}
Modes == {"current", "stale"}
Good == [regional |-> TRUE, scoped |-> FALSE, allowed |-> TRUE,
         tagged |-> TRUE, tagGrant |-> TRUE, durable |-> TRUE, coherent |-> TRUE]
Witnesses == {Good, [Good EXCEPT !.tagged = FALSE, !.tagGrant = FALSE],
    [Good EXCEPT !.regional = FALSE], [Good EXCEPT !.scoped = TRUE],
    [Good EXCEPT !.allowed = FALSE], [Good EXCEPT !.tagGrant = FALSE],
    [Good EXCEPT !.durable = FALSE], [Good EXCEPT !.coherent = FALSE]}

NoProof == [op |-> "none", prior |-> "absent", post |-> "absent",
    attached |-> FALSE, f |-> Good, selector |-> "all"]
OneRegion == CHOOSE region \in Regions : TRUE
Await(op, region, expected) ==
    [op |-> op, region |-> region, expected |-> expected, confirmed |-> FALSE,
      mode |-> "current", readRegion |-> region, matches |-> FALSE]
NoPending == Await("none", OneRegion, "absent")
NoNotice == [shown |-> FALSE, receipt |-> NoPending]
Init ==
    /\ row \in [Regions -> States]
    /\ proof = NoProof
    /\ pending = NoPending
    /\ notice = NoNotice

Admitted(f) == f.regional /\ ~f.scoped /\ f.allowed
Record(op, prior, post, f, selector, attached) ==
    [op |-> op, prior |-> prior, post |-> post, attached |-> attached,
     f |-> f, selector |-> selector]

Allocate(region, f) ==
    /\ row[region] = "absent"
    /\ Admitted(f)
    /\ (~f.tagged \/ f.tagGrant)
    /\ f.durable /\ f.coherent
    /\ row' = [row EXCEPT ![region] = "native"]
    /\ proof' = Record("create", row[region], row'[region], f, "all", FALSE)
    /\ pending' = Await("create", region, "native")
    /\ notice' = NoNotice

Remove(region, f) ==
    /\ row[region] \in States \ {"absent"}
    /\ row[region] \notin {"stack", "stack-attached"}
    /\ row[region] # "native-dependent"
    /\ Admitted(f)
    /\ f.durable /\ f.coherent
    /\ row' = [row EXCEPT ![region] = "absent"]
    /\ proof' = Record("delete", row[region], row'[region], f, "all", FALSE)
    /\ pending' = Await("delete", region, "absent")
    /\ notice' = NoNotice

ReadAllowed(region, f, selector) ==
    /\ Admitted(f)
    /\ f.coherent
    /\ row[region] # "native-dependent"
    /\ (selector = "all" \/ (selector = "exact" /\ row[region] # "absent"))
ReadWitness(region, f, selector) ==
    Record("describe", row[region], row[region], f, selector, row[region] = "stack-attached")

Read(region, f, selector) ==
    /\ ReadAllowed(region, f, selector)
    /\ proof' = ReadWitness(region, f, selector)
    /\ UNCHANGED <<row, pending, notice>>

(* A mutation does not acknowledge readback. Only the reply of the live
   operation in the same UI scope/generation can verify installed state. *)
Confirm(region, f, mode, matches) ==
    /\ pending.op \in Writes
    /\ ~pending.confirmed
    /\ ReadAllowed(region, f, "all")
    /\ mode = "current"
    /\ region = pending.region
    /\ matches
    /\ row[region] = pending.expected
    /\ proof' = ReadWitness(region, f, "all")
    /\ pending' = [pending EXCEPT !.confirmed = TRUE, !.mode = mode,
                                     !.readRegion = region, !.matches = matches]
    /\ UNCHANGED <<row, notice>>

Publish ==
    /\ pending.op \in Writes
    /\ pending.confirmed
    /\ notice' = [shown |-> TRUE, receipt |-> pending]
    /\ UNCHANGED <<row, proof, pending>>

Invalidate ==
    /\ pending' = NoPending
    /\ notice' = NoNotice
    /\ UNCHANGED <<row, proof>>

Next ==
    (\E region \in Regions, f \in Witnesses, selector \in Selection:
        Allocate(region, f) \/ Remove(region, f) \/ Read(region, f, selector))
    \/ (\E r \in Regions, f \in Witnesses, mode \in Modes, matches \in BOOLEAN: Confirm(r, f, mode, matches))
    \/ Publish \/ Invalidate
    \/ UNCHANGED vars

ReceiptType ==
    [op: Operations, region: Regions, expected: States, confirmed: BOOLEAN,
     mode: Modes, readRegion: Regions, matches: BOOLEAN]
TypeOK ==
    /\ row \in [Regions -> States]
    /\ proof \in [op: Operations, prior: States, post: States,
                   attached: BOOLEAN, f: Witnesses, selector: Selection]
    /\ pending \in ReceiptType
    /\ notice \in [shown: BOOLEAN, receipt: ReceiptType]
Authorized == proof.op # "none" => proof.f.regional /\ ~proof.f.scoped /\ proof.f.allowed
TaggedCreation == proof.op = "create" => ~proof.f.tagged \/ proof.f.tagGrant
NativeOwnership ==
    /\ (proof.op = "create" => proof.prior = "absent" /\ proof.post = "native")
    /\ (proof.op = "delete" => proof.prior \notin {"stack", "stack-attached"} /\ proof.post = "absent")
DependentRowsPreserved == proof.op = "delete" => proof.prior # "native-dependent"
DurableMutation == proof.op \in Writes => proof.f.durable /\ proof.f.coherent
FaithfulRead == proof.op = "describe" =>
     /\ proof.f.coherent /\ proof.prior # "native-dependent" /\ proof.post = proof.prior
     /\ proof.attached = (proof.prior = "stack-attached")
     /\ (proof.selector = "all" \/ (proof.selector = "exact" /\ proof.prior # "absent"))
ExactConfirmation == pending.confirmed => pending.op \in Writes /\ pending.mode = "current" /\
    pending.region = pending.readRegion /\ pending.matches /\ row[pending.region] = pending.expected
ConfirmedNotice == notice.shown => notice.receipt.op \in Writes /\ notice.receipt.confirmed /\ notice.receipt = pending
=============================================================================
