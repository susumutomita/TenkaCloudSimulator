------------------------ MODULE EC2DetachedNetworkACLRules ------------------------
EXTENDS Naturals, FiniteSets, Sequences

CONSTANT None, RuleLimit
VARIABLE s

Directions == {"in", "out"}
Numbers == {10, 20, 30}
Terminal == 32767
Keys == Directions \X (Numbers \cup {Terminal})
Edits == {"create", "replace", "delete"}

Value(proto, verdict, cidr, ports) ==
    [protocol |-> proto, verdict |-> verdict, cidr |-> cidr, ports |-> ports]
AllowAll == Value("-1", "allow", "v4-a", None)
AllowAlias == Value("-1", "allow", "v4-a-hostbits", None)
AllowTCP == Value("6", "allow", "v4-a", <<0,65535>>)
DenyUDP == Value("17", "deny", "v4-b", <<53,53>>)
TerminalDeny == Value("-1", "deny", "0.0.0.0/0", None)
AllWithPorts == Value("-1", "allow", "v4-a", <<1,1>>)
TCPWithoutPorts == Value("6", "allow", "v4-a", None)
UDPReversedPorts == Value("17", "allow", "v4-a", <<54,53>>)
NonIPv4 == Value("6", "allow", "v6", <<1,1>>)
UnsupportedProtocol == Value("1", "allow", "v4-a", None)
StoredValues == {AllowAll, AllowTCP, DenyUDP}
InputValues == StoredValues \cup {AllowAlias,AllWithPorts,TCPWithoutPorts,
                                 UDPReversedPorts,NonIPv4,UnsupportedProtocol}
IPv4(v) == v.cidr \in {"v4-a","v4-b","v4-a-hostbits"}
PortShape(v) ==
    IF v.protocol = "-1" THEN v.ports = None
    ELSE IF v.protocol \in {"6","17"} THEN v.ports \in {<<0,65535>>,<<53,53>>}
    ELSE FALSE
Normalize(v) == IF v = AllowAlias THEN AllowAll ELSE v
Key(q) == <<q.direction,q.number>>
Blank == [k \in Keys |-> None]
Peer == [Blank EXCEPT ![<<"in",10>>] = AllowTCP]
ForeignOwned == [Blank EXCEPT ![<<"out",20>>] = DenyUDP]

Req(op, dir, number, val, witness) ==
    [op |-> op, direction |-> dir, number |-> number, value |-> val, witness |-> witness]
Edit(op, dir, n, v) == Req(op, dir, n, v, "ok")
Read == Req("describe","in",10,AllowAll,"ok")
Bad(w) == Req("create","in",10,AllowTCP,w)
BadState ==
    {"wrong-region","missing-row","foreign-row","wrong-parent","attached",
     "cf-claim","corrupt-generation"}
BadIAM == {"wrong-action","wrong-arn"}
BeforeWriteOnly == {"cf-declaration","wrong-carrier"}
WriteFailures == {"write-refused","different-readback","new-cf-claim","commit-refused","generation-limit"}

\* Finite synthetic request schedules; time, parser and grant outcomes are normalized witnesses.
NativePlans ==
  { <<Edit("create","in",10,AllowAll),Edit("create","out",10,AllowTCP),Read,Edit("replace","in",10,DenyUDP)>>,
    <<Edit("create","out",20,DenyUDP),Edit("delete","out",20,DenyUDP),Read,Edit("delete","out",20,DenyUDP)>>,
    <<Edit("create","in",20,AllowTCP),Edit("create","in",10,AllowAll),Edit("create","in",30,DenyUDP),Read>>,
    <<Edit("create","in",10,AllowAll),Edit("replace","in",10,AllowAlias),
      Edit("replace","in",10,AllowTCP),Read>>,
    <<Edit("create","in",10,AllowAll),Edit("create","in",10,DenyUDP),
      Edit("replace","out",10,AllowTCP),Read>>,
    <<Edit("create","in",10,AllowAll),Edit("delete","out",10,AllowAll),Read>>,
    <<Edit("create","in",10,AllowAll),
      Req("replace","in",10,AllowAll,"commit-refused"),Read>>,
    <<Edit("create","in",10,AllowAll),
      Req("replace","in",10,AllowAll,"generation-limit"),Read>>,
    <<Edit("create","in",10,AllowAll),Req("describe","in",10,AllowAll,"commit-refused")>>,
    <<Edit("create","in",Terminal,AllowAll)>>, <<Edit("delete","out",Terminal,AllowAll)>> }
   \cup {<<Bad(w)>> : w \in BadState \cup BadIAM \cup BeforeWriteOnly \cup WriteFailures}
   \cup {<<Req("describe","in",10,AllowAll,w)>> : w \in BadState \cup BadIAM}
   \cup {<<Edit("create","in",10,v)>> : v \in InputValues \ StoredValues}

UI(op, fault) == [op |-> op, fault |-> fault]
BeforeUI == {"unsupported","not-native","invalid-rule","wrong-scope","stale-rule",
             "full-direction"}
AfterUI == {"false-ack","wrong-readback-scope","wrong-readback-row","retired-render"}
UICases ==
    {UI(o,"ok") : o \in Edits} \cup
    {UI("create",f) : f \in BeforeUI} \cup
    {UI("replace",f) : f \in AfterUI} \cup {UI("delete","wrong-readback-row")}

Initial(plan, ui) ==
    [plan |-> plan, ui |-> ui, step |-> 0, entries |-> Blank,
     peer |-> Peer, foreign |-> ForeignOwned, revision |-> 1, updates |-> 0,
     hasRuleState |-> FALSE, last |-> None, shown |-> None]

Init ==
    \/ \E plan \in NativePlans : s = Initial(plan, None)
    \/ \E ui \in UICases : s = Initial(<<>>, ui)

InDirection(rows,d) == {n \in Numbers : rows[<<d,n>>] # None}
NativeTarget(q) == q.witness \notin BadState
Authorized(q) == q.witness \notin BadIAM
SupportsPayload(q) == IPv4(q.value) /\ PortShape(q.value)
EditableKey(q) == q.number \in Numbers
IsChanged(q, slots) == q.op \in {"create","delete"} \/
                         (q.op = "replace" /\ slots[Key(q)] # Normalize(q.value))
CapacityOK(q, slots) == Cardinality(InDirection(slots,q.direction)) < RuleLimit
ReadyToCommit(q, changes) ==
    q.witness # "commit-refused" /\
    (~changes \/ q.witness \notin WriteFailures)
Accept(q, slots) ==
    /\ NativeTarget(q) /\ Authorized(q)
    /\ IF q.op = "describe" THEN ReadyToCommit(q, FALSE)
       ELSE /\ q.witness \notin BeforeWriteOnly /\ EditableKey(q)
            /\ (q.op = "delete" \/ SupportsPayload(q))
            /\ IF q.op = "create" THEN slots[Key(q)] = None /\ CapacityOK(q, slots)
               ELSE slots[Key(q)] # None
            /\ ReadyToCommit(q, IsChanged(q, slots))

StoreValue(q) == IF q.op = "delete" THEN None ELSE Normalize(q.value)
Apply(q, slots, success) ==
    IF ~success \/ q.op = "describe" THEN slots
    ELSE [slots EXCEPT ![Key(q)] = StoreValue(q)]

Row(d,n,v) == [dir |-> d, number |-> n, value |-> v]
DirectionView(slots,d) ==
    SelectSeq(<<Row(d,10,slots[<<d,10>>]),Row(d,20,slots[<<d,20>>]),Row(d,30,slots[<<d,30>>])>>,
              LAMBDA r : r.value # None)
    \o <<Row(d,Terminal,TerminalDeny)>>
View(slots) == [mode |-> "detached-config-only", attachments |-> {}, packetProof |-> FALSE,
                rows |-> DirectionView(slots,"in") \o DirectionView(slots,"out")]

Native ==
    /\ s.ui = None /\ s.step < Len(s.plan)
    /\ LET q == s.plan[s.step + 1]
           success == Accept(q,s.entries)
           bump == IF success /\ IsChanged(q,s.entries) THEN 1 ELSE 0
       IN s' = [s EXCEPT !.step = @ + 1,
                !.entries = Apply(q,s.entries,success),
                !.revision = @ + bump, !.updates = @ + bump,
                !.hasRuleState = @ \/ (bump = 1),
                !.last = [request |-> q, before |-> s.entries, priorRevision |-> s.revision,
                          priorUpdates |-> s.updates, success |-> success,
                          reply |-> IF success /\ q.op = "describe" THEN View(s.entries) ELSE None]]

CanSend(ui) == ui.fault \notin BeforeUI
CanShow(ui) == ui.fault \notin AfterUI
Console ==
    /\ s.ui # None /\ s.step = 0
    /\ LET dispatched == CanSend(s.ui)
       IN s' = [s EXCEPT !.step = 1, !.shown = [sent |-> dispatched,
                      success |-> dispatched /\ CanShow(s.ui)]]

Next == Native \/ Console

TypeOK ==
    /\ s.plan \in NativePlans \cup {<<>>} /\ s.ui \in UICases \cup {None}
    /\ s.step \in 0..4 /\ s.revision \in 1..5 /\ s.updates \in 0..4
    /\ s.entries \in [Keys -> InputValues \cup {None}]
    /\ s.hasRuleState \in BOOLEAN

ScopeFrame ==
    /\ s.peer = Peer /\ s.foreign = ForeignOwned
    /\ IF s.last = None THEN TRUE
       ELSE /\ s.last.request.witness \in (BadState \cup BadIAM) => ~s.last.success
            /\ s.last.request.op \in Edits /\ s.last.request.witness \in BeforeWriteOnly => ~s.last.success

OnlyEditablePersisted ==
    /\ \A d \in Directions : s.entries[<<d,Terminal>>] = None
    /\ \A d \in Directions : Cardinality(InDirection(s.entries,d)) <= RuleLimit
    /\ \A k \in Keys : s.entries[k] \in StoredValues \cup {None}

AtomicGeneration ==
    /\ s.revision = s.updates + 1
    /\ s.hasRuleState = (s.updates > 0)
    /\ IF s.last = None THEN TRUE
       ELSE LET l == s.last
                delta == IF l.before = s.entries THEN 0 ELSE 1
            IN /\ s.revision = l.priorRevision + delta
               /\ s.updates = l.priorUpdates + delta
               /\ ~l.success => delta = 0

\* Independent rule-set oracle, including idempotent equality after masking.
ExactNativeOperation ==
    IF s.last = None THEN TRUE
    ELSE LET l == s.last
             q == l.request
             k == <<q.direction,q.number>>
             old == l.before[k]
             v == IF q.value = AllowAlias THEN AllowAll ELSE q.value
             shape == q.number \in Numbers /\ (q.op = "delete" \/ v \in StoredValues)
             hasSlot == Cardinality({n \in Numbers : l.before[<<q.direction,n>>] # None}) < RuleLimit
             base == q.witness \notin (BadState \cup BadIAM \cup {"commit-refused"})
             chosen == IF q.op = "create" THEN old = None /\ hasSlot ELSE old # None
             changed == q.op # "replace" \/ old # v
             success == IF q.op = "describe" THEN base
                        ELSE base /\ shape /\ chosen /\ q.witness \notin BeforeWriteOnly /\
                             (q.witness \notin WriteFailures \/ ~changed)
             expected == IF success /\ q.op \in Edits
                         THEN [key \in Keys |-> IF key # k THEN l.before[key]
                                ELSE IF q.op = "delete" THEN None ELSE v]
                         ELSE l.before
         IN l.success = success /\ s.entries = expected

ExactReadProjection ==
    IF s.last = None THEN TRUE
    ELSE IF ~s.last.success \/ s.last.request.op # "describe" THEN s.last.reply = None
    ELSE IF s.last.reply = None THEN FALSE
    ELSE LET reply == s.last.reply
             rows == reply.rows
             editable == {rows[i] : i \in {j \in 1..Len(rows) : rows[j].number # Terminal}}
             requested == {Row(k[1],k[2],s.last.before[k]) :
                              k \in {t \in Keys : s.last.before[t] # None}}
             terminals == SelectSeq(rows,LAMBDA row : row.number = Terminal)
         IN /\ reply.mode = "detached-config-only" /\ reply.attachments = {} /\ ~reply.packetProof
            /\ editable = requested
            /\ Len(rows) = Cardinality(requested) + 2
            /\ terminals = <<Row("in",32767,TerminalDeny),Row("out",32767,TerminalDeny)>>
            /\ \A i,j \in 1..Len(rows) :
                 i < j => IF rows[i].dir = rows[j].dir THEN rows[i].number < rows[j].number
                          ELSE rows[i].dir = "in"

VerifiedSelectedConsole ==
    IF s.shown = None THEN TRUE
    ELSE /\ s.shown.sent = (s.ui.fault \in ({"ok"} \cup AfterUI))
         /\ s.shown.success = (s.ui.fault = "ok")
         /\ s.entries = Blank /\ s.revision = 1

==============================================================================
