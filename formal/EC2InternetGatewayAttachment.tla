-------------------- MODULE EC2InternetGatewayAttachment --------------------
CONSTANTS Gateways, VPCs
VARIABLES links, record, pending, notice
vars == <<links, record, pending, notice>>
None == "none"
Actions == {"attach", "detach"}
Faults == {"ok", "region", "scoped", "gatewayGrant", "vpcGrant",
           "stackGateway", "stackVPC", "claim", "declaration", "unconfirmed",
           "dependent", "dryRun"}
ReadModes == {"current", "stale", "otherScope", "mismatch", "partial", None}
Gateway == CHOOSE g \in Gateways: TRUE
VPC == CHOOSE v \in VPCs: TRUE
NoProof == [action |-> None, g |-> Gateway, v |-> VPC, prior |-> None,
            occupied |-> FALSE, fault |-> "ok"]
Await(g, expected) == [g |-> g, expected |-> expected,
    phase |-> "written", exactMode |-> None, regionalMode |-> None]
NoPending == [Await(Gateway, None) EXCEPT !.phase = "idle"]

Init ==
    /\ links = [g \in Gateways |-> None]
    /\ record = NoProof
    /\ pending = NoPending
    /\ notice = FALSE

BothIAM(f) == f \notin {"gatewayGrant", "vpcGrant"}
NativeEndpoints(f) == f \notin {"stackGateway", "stackVPC", "claim", "declaration"}
AuthorizedScope(f) == f \notin {"region", "scoped"}
RealWrite(f) == f # "dryRun"
Durable(f) == f # "unconfirmed"
VPCAvailable(v) == \A g \in Gateways: links[g] # v

Receipt(action, g, v, fault) ==
    [action |-> action, g |-> g, v |-> v, prior |-> links[g],
     occupied |-> ~VPCAvailable(v), fault |-> fault]
Run(f) ==
    /\ AuthorizedScope(f) /\ BothIAM(f) /\ NativeEndpoints(f)
    /\ RealWrite(f) /\ Durable(f)

Attach(g, v, f) ==
    /\ Run(f)
    /\ links[g] = None
    /\ VPCAvailable(v)
    /\ links' = [links EXCEPT ![g] = v]
    /\ record' = Receipt("attach", g, v, f)
    /\ pending' = Await(g, v)
    /\ notice' = FALSE

Detach(g, v, f) ==
    /\ Run(f)
    /\ links[g] = v
    /\ f # "dependent"
    /\ links' = [links EXCEPT ![g] = None]
    /\ record' = Receipt("detach", g, v, f)
    /\ pending' = Await(g, None)
    /\ notice' = FALSE

ConfirmExact(mode) ==
    /\ pending.phase = "written"
    /\ mode = "current"
    /\ links[pending.g] = pending.expected
    /\ pending' = [pending EXCEPT !.phase = "exact", !.exactMode = mode]
    /\ UNCHANGED <<links, record, notice>>

ConfirmRegional(mode) ==
    /\ pending.phase = "exact"
    /\ mode = "current"
    /\ links[pending.g] = pending.expected
    /\ pending' = [pending EXCEPT !.phase = "regional", !.regionalMode = mode]
    /\ UNCHANGED <<links, record, notice>>

Publish ==
    /\ pending.phase = "regional"
    /\ notice' = TRUE
    /\ UNCHANGED <<links, record, pending>>

Retire ==
    /\ pending' = NoPending
    /\ notice' = FALSE
    /\ UNCHANGED <<links, record>>

Next ==
    (\E g \in Gateways, v \in VPCs, f \in Faults: Attach(g, v, f) \/ Detach(g, v, f))
    \/ (\E mode \in ReadModes \ {None}: ConfirmExact(mode) \/ ConfirmRegional(mode))
    \/ Publish \/ Retire \/ UNCHANGED vars

TypeOK ==
    /\ links \in [Gateways -> VPCs \cup {None}]
    /\ record \in [action: Actions \cup {None}, g: Gateways, v: VPCs,
                  prior: VPCs \cup {None}, occupied: BOOLEAN, fault: Faults]
    /\ pending \in [g: Gateways, expected: VPCs \cup {None},
                   phase: {"idle", "written", "exact", "regional"},
                   exactMode: ReadModes, regionalMode: ReadModes]
    /\ notice \in BOOLEAN
OneGatewayPerVPC == \A a,b \in Gateways: (a # b /\ links[a] # None) => links[a] # links[b]
ExactMutation ==
    /\ (record.action = "attach" => record.prior = None /\ ~record.occupied /\ links[record.g] = record.v)
    /\ (record.action = "detach" => record.prior = record.v /\ record.fault # "dependent" /\ links[record.g] = None)
Authority == record.action \in Actions =>
    /\ record.fault \notin {"region", "scoped", "gatewayGrant", "vpcGrant"}
    /\ record.fault \notin {"stackGateway", "stackVPC", "claim", "declaration"}
Committed == record.action \in Actions => record.fault \notin {"unconfirmed", "dryRun"}
ReadbackFence ==
    /\ pending.phase \in {"exact", "regional"} =>
        pending.exactMode = "current" /\ links[pending.g] = pending.expected
    /\ pending.phase = "regional" =>
        pending.regionalMode = "current" /\ links[pending.g] = pending.expected
ConfirmedNotice == notice => pending.phase = "regional" /\ record.action \in Actions
=============================================================================
