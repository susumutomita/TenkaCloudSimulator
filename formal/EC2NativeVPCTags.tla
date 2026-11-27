--------------------------- MODULE EC2NativeVPCTags ---------------------------
EXTENDS Naturals, FiniteSets

CONSTANT Absent, Omitted, TagLimit
VARIABLE s

\* One symbolic selected regional native VPC. Two editable cells stand for
\* distinct case-sensitive keys; other ordinary tags count without changing.
Region == "local-west"
Scope == <<Region, "selected-vpc">>
NeighborScope == <<Region, "neighbor-vpc">>
OtherRegion == <<"local-east", "selected-vpc">>
Scopes == {Scope, NeighborScope, OtherRegion}
Actions == {"CreateTags", "DeleteTags"}
Keys == {"Name", "Color"}
Reserved == "AWS:reserved"
Cells == Keys \cup {Reserved}
Values == {"", "blue", "green"}
CellsType == [Cells -> Values \cup {Absent, Omitted}]
Row(name, color, filler) ==
    [ tags |-> [k \in Cells |-> CASE k = "Name" -> name
                                  [] k = "Color" -> color
                                  [] OTHER -> Absent],
      filler |-> filler, owner |-> "ec2", cidr |-> "private-prefix",
      main |-> "local-main", rev |-> 2, dnsRev |-> 1,
      dnsSupport |-> TRUE, dnsHostnames |-> FALSE ]
Base == Row("blue", "green", 0)
Peer == [Row("green", "", 0) EXCEPT !.main = "peer-main"]
Stack == [Row("blue", "", 0) EXCEPT !.owner = "cloudformation", !.main = "stack-main"]
TagCount(r) == r.filler + Cardinality({k \in Cells : r.tags[k] # Absent})
NetworkSide(r) == <<r.filler, r.owner, r.cidr, r.main, r.rev, r.dnsRev, r.dnsSupport, r.dnsHostnames>>

\* Parser, authorization, persisted VPC/CFN evidence and storage refusal are
\* normalized inputs. The matching region and grant tuple remain independent.
BadFormat == {"second-resource", "second-tag", "missing-key", "duplicate-member",
              "dry-run", "invalid-text"}
BadOwner == {"missing-row", "wrong-kind", "cfn-owner", "cfn-claim", "cfn-declared",
             "main-slot", "noncanonical-document", "invalid-generation"}
BadWrite == {"cas-mismatch", "write-refused", "reread-document", "reread-ownership"}
Storage == {"start-refused", "commit-refused"} \cup BadWrite
Query(action, key, value) ==
    [action |-> action, key |-> key, value |-> value, target |-> Scope,
     grant |-> <<action, Scope>>, shape |-> "single", witness |-> "canonical",
     tx |-> "ok"]
Put(key, value) == Query("CreateTags", key, value)
Del(key, value) == Query("DeleteTags", key, value)

Plan(mode, row, q, pre, read1, read2, concurrent) ==
    [mode |-> mode, start |-> row, q |-> q, pre |-> pre,
     read1 |-> read1, read2 |-> read2, concurrent |-> concurrent]
Native(row,q) == Plan("native",row,q,"ok","ok","ok","none")
Console(row,q,pre,read1,read2,concurrent) ==
    Plan("console",row,q,pre,read1,read2,concurrent)
NativePlans ==
    { Native(Base,Put("Name","")), Native(Base,Put("Name","blue")),
      Native(Base,Del("Name",Omitted)), Native(Base,Del("Name","")),
      Native(Base,Del("Name","green")), Native(Base,Del("Name","blue")),
      Native(Row("","green",0),Del("Name","")),
      Native(Row(Absent,"green",0),Del("Name",Omitted)),
      Native(Row(Absent,"green",0),Del("Name","")),
      Native(Row(Absent,"green",48),Put("Name","")),
      Native(Row(Absent,"green",49),Put("Name","")),
      Native(Row("blue","green",48),Put("Name","")),
      Native(Row("blue","green",48),Del("Color","green")),
      Native(Base,Put("Name",Omitted)), Native(Base,Put(Reserved,"blue")),
      Native(Base,Del(Reserved,Omitted)),
      Native(Base,[Put("Name","") EXCEPT !.target = OtherRegion, !.grant = <<"CreateTags",OtherRegion>>]),
      Native(Base,[Put("Name","") EXCEPT !.target = NeighborScope, !.grant = <<"CreateTags",NeighborScope>>]),
      Native(Base,[Put("Name","") EXCEPT !.grant = <<"DeleteTags",Scope>>]),
      Native(Base,[Put("Name","") EXCEPT !.grant = <<"CreateTags",OtherRegion>>]),
      Native(Base,[Put("Name","") EXCEPT !.grant = <<"CreateTags",NeighborScope>>]),
      Native(Base,[Put("Name","blue") EXCEPT !.tx = "commit-refused"]),
      Native(Base,[Del("Name","green") EXCEPT !.tx = "commit-refused"]),
      Native(Base,[Del("Name","green") EXCEPT !.tx = "reread-document"]) }
    \cup {Native(Base,[Put("Name","") EXCEPT !.shape = f]) : f \in BadFormat}
    \cup {Native(Base,[Put("Name","") EXCEPT !.witness = w]) : w \in BadOwner}
    \cup {Native(Base,[Put("Name","") EXCEPT !.tx = t]) : t \in Storage}

UIBlocks == {"not-authenticated", "unadvertised", "participant-scope", "not-native",
             "lease-busy", "different-capture"}
ReadFaults == {"other-region", "other-vpc", "other-key", "invalid-envelope",
               "unverified-owner"}
ConsolePlans ==
    { Console(Base,Put("Name",""),"ok","ok","ok","none"),
      Console(Base,Put("Color","blue"),"ok","ok","ok","none"),
      Console(Row("","green",0),Del("Name",Omitted),"ok","ok","ok","none"),
      Console(Base,Del("Name",Omitted),"ok","ok","ok","none"),
      Console(Row(Absent,"green",0),Del("Name",Omitted),"ok","ok","ok","none"),
      Console(Row(Absent,"green",49),Put("Name",""),"ok","ok","ok","none"),
      Console(Base,Put("Name","blue"),"ok","ok","ok","none"),
      Console(Base,Put(Reserved,""),"ok","ok","ok","none"),
      Console(Base,Put("Name",""),"stale-observation","ok","ok","none"),
      Console(Base,Del("Name",Omitted),"ok","ok","ok","tag-before"),
      Console(Base,Put("Name",""),"ok","ok","ok","tag-before"),
      Console(Base,Put("Name",""),"ok","ok","ok","dns-before"),
      Console(Base,Put("Name",""),"ok","ok","ok","tag-after"),
      Console(Base,[Put("Name","") EXCEPT !.tx = "commit-refused"],"ok","ok","ok","none"),
      Console(Base,[Put("Name","green") EXCEPT !.tx = "commit-refused"],"ok","ok","ok","tag-after") }
    \cup {Console(Base,Put("Name",""),b,"ok","ok","none") : b \in UIBlocks}
    \cup {Console(Base,Put("Name",""),"ok",f,"ok","none") : f \in ReadFaults}
    \cup {Console(Base,Put("Name",""),"ok","ok",f,"none") :
          f \in ReadFaults \cup {"stale-value","false-return","retired"}}

Init ==
    \E p \in NativePlans \cup ConsolePlans :
      s = [plan |-> p, phase |-> 0, row |-> p.start, peer |-> Peer,
           stack |-> Stack, sent |-> Absent, before |-> p.start,
           receipt |-> Absent, observed |-> Absent, shown |-> Absent]

SameRegion(q) == q.target[1] = Scope[1]
SameResource(q) == q.target[2] = Scope[2]
ExactGrant(q) == q.grant[1] = q.action /\ q.grant[2] = q.target
ValidShape(q) == q.shape = "single"
OwnsCanonical(q) == q.witness = "canonical"
LegalValue(q) == q.value \in Values \/ (q.action = "DeleteTags" /\ q.value = Omitted)
LegalKey(q) == q.key \in Keys
Admit(q) ==
    SameRegion(q) /\ SameResource(q) /\ ExactGrant(q) /\ ValidShape(q)
    /\ OwnsCanonical(q) /\ LegalKey(q) /\ LegalValue(q) /\ q.tx # "start-refused"
RemoveMatches(q, old) == old # Absent /\ (q.value = Omitted \/ q.value = old)
Propose(q, r) ==
    CASE q.action = "CreateTags" -> [r EXCEPT !.tags[q.key] = q.value]
      [] RemoveMatches(q, r.tags[q.key]) -> [r EXCEPT !.tags[q.key] = Absent]
      [] OTHER -> r
Fits(next) == TagCount(next) <= TagLimit
OneRowWrite(q) == q.tx \notin {"cas-mismatch", "write-refused"}
MatchesStored(q) == q.tx \notin {"reread-document", "reread-ownership"}
CommitReady(q, writes) ==
    q.tx # "commit-refused" /\ (~writes \/ (OneRowWrite(q) /\ MatchesStored(q)))
Execute(q, before) ==
    LET candidate == Propose(q,before)
        admitted == Admit(q) /\ Fits(candidate)
        changed == candidate # before
        ack == admitted /\ CommitReady(q,changed)
        writes == IF ack /\ changed THEN 1 ELSE 0
        saved == IF writes = 1 THEN candidate ELSE before
    IN [admitted |-> admitted, ack |-> ack, writes |-> writes, saved |-> saved]

\* Console evidence is about the selected key and live selection, not an
\* atomic claim across multiple requests. Intervening work may be visible at
\* the server or in the readback. An unconfirmed presentation is not rollback.
Snapshot(r, key, fault) ==
    [scope |-> CASE fault = "other-region" -> OtherRegion
                 [] fault = "other-vpc" -> NeighborScope [] OTHER -> Scope,
     key |-> IF fault = "other-key" THEN IF key = "Name" THEN "Color" ELSE "Name" ELSE key,
     tag |-> IF fault = "stale-value" THEN "blue" ELSE r.tags[key],
     native |-> fault # "unverified-owner", parsed |-> fault # "invalid-envelope",
     name |-> IF r.tags["Name"] = Absent THEN "" ELSE r.tags["Name"]]
ExactSnapshot(read, k) == read.scope = Scope /\ read.key = k /\ read.native /\ read.parsed
ObservedValue(p) == IF p.pre = "stale-observation" THEN "green" ELSE p.start.tags[p.q.key]
EditGate(p) == p.pre \notin UIBlocks
CanSubmit(p) ==
    LET k == p.q.key
        old == ObservedValue(p)
        fresh == Snapshot(p.start,k,p.read1)
    IN /\ EditGate(p) /\ ExactSnapshot(fresh,k) /\ fresh.tag = old /\ k \in Keys
       /\ IF p.q.action = "DeleteTags" THEN old # Absent
          ELSE /\ p.q.value \in Values /\ p.q.value # old
               /\ (old # Absent \/ TagCount(p.start) < TagLimit)
WireValue(p) == IF p.q.action = "DeleteTags" THEN ObservedValue(p) ELSE p.q.value
Wire(p) == [p.q EXCEPT !.value = WireValue(p)]
Prepare ==
    /\ s.phase = 0
    /\ s' = [s EXCEPT !.phase = 1, !.sent =
              IF s.plan.mode = "native" THEN s.plan.q
              ELSE IF CanSubmit(s.plan) THEN Wire(s.plan) ELSE Absent]

BeforeOther(p,r) ==
    CASE p.concurrent = "tag-before" -> [r EXCEPT !.tags[p.q.key] = "green"]
      [] p.concurrent = "dns-before" -> [r EXCEPT !.rev = @ + 1, !.dnsRev = @ + 1,
                                                        !.dnsHostnames = TRUE]
      [] OTHER -> r
AfterOther(p,r) ==
    IF p.concurrent = "tag-after" THEN [r EXCEPT !.tags[p.q.key] = "green"] ELSE r
Persist ==
    /\ s.phase = 1
    /\ LET before == IF s.sent = Absent THEN s.row ELSE BeforeOther(s.plan,s.row)
           r == IF s.sent = Absent THEN Absent ELSE Execute(s.sent,before)
       IN s' = [s EXCEPT !.phase = 2, !.before = before, !.receipt = r,
                         !.row = IF r = Absent THEN before ELSE r.saved]
Wanted(p) == IF p.q.action = "DeleteTags" THEN Absent ELSE p.q.value
CanPublish(p,r,read) ==
    r.ack /\ p.read2 # "false-return" /\ p.read2 # "retired"
    /\ ExactSnapshot(read,p.q.key) /\ read.tag = Wanted(p)
Finish ==
    /\ s.phase = 2 /\ s.plan.mode = "console"
    /\ LET p == s.plan
           actual == IF s.sent = Absent THEN s.row ELSE AfterOther(p,s.row)
           got == IF s.receipt = Absent THEN Absent ELSE Snapshot(actual,p.q.key,p.read2)
           publish == IF got = Absent THEN FALSE ELSE CanPublish(p,s.receipt,got)
       IN s' = [s EXCEPT !.phase = 3, !.row = actual, !.observed = got,
                         !.shown = IF publish THEN got ELSE Absent]
Next == Prepare \/ Persist \/ Finish

TypeOK ==
    /\ s.plan \in NativePlans \cup ConsolePlans /\ s.phase \in 0..3
    /\ s.row.tags \in CellsType /\ s.row.filler \in 0..50
    /\ s.row.rev \in 2..4 /\ s.row.dnsRev \in 1..3
    /\ s.row.dnsSupport \in BOOLEAN /\ s.row.dnsHostnames \in BOOLEAN
    /\ s.peer.tags \in CellsType /\ s.stack.tags \in CellsType
    /\ IF s.sent = Absent THEN TRUE ELSE
        /\ s.sent.action \in Actions /\ s.sent.key \in Cells /\ s.sent.target \in Scopes
        /\ s.sent.value \in Values \cup {Omitted}
    /\ IF s.receipt = Absent THEN TRUE ELSE
        /\ s.receipt.admitted \in BOOLEAN /\ s.receipt.ack \in BOOLEAN
        /\ s.receipt.writes \in 0..1 /\ s.receipt.saved.tags \in CellsType

ExactNativeAdmission ==
    IF s.receipt = Absent THEN TRUE
    ELSE LET q == s.sent
             full == q.action = "CreateTags" /\ s.before.tags[q.key] = Absent
                     /\ TagCount(s.before) = TagLimit
         IN s.receipt.admitted =
            (q.target = Scope /\ q.grant = <<q.action,Scope>>
             /\ q.shape \notin BadFormat /\ q.witness \notin BadOwner
             /\ q.key # Reserved /\ (q.action = "DeleteTags" \/ q.value # Omitted)
             /\ q.tx # "start-refused" /\ ~full)
ExpectedWriteCell(q,old) ==
    IF q.action = "CreateTags" THEN q.value
    ELSE IF q.value \in {old,Omitted} THEN Absent ELSE old
ExactTagEffect ==
    /\ s.row.tags[Reserved] = Absent /\ TagCount(s.row) <= TagLimit
    /\ \A key \in Keys : s.row.tags[key] \in Values \cup {Absent}
    /\ IF s.receipt = Absent \/ ~s.receipt.ack THEN TRUE
       ELSE LET q == s.sent
                old == s.before.tags[q.key]
                expected == ExpectedWriteCell(q,old)
            IN /\ s.receipt.saved.tags[q.key] = expected
               /\ s.receipt.writes = (IF old = expected THEN 0 ELSE 1)

AtomicTagCommit ==
    IF s.receipt = Absent THEN TRUE
    ELSE LET q == s.sent
             changed == s.before.tags[q.key] # ExpectedWriteCell(q,s.before.tags[q.key])
             ok == s.receipt.admitted /\ q.tx # "commit-refused"
                   /\ (q.tx \notin BadWrite \/ ~changed)
         IN /\ s.receipt.ack = ok
            /\ ~ok => (s.receipt.saved = s.before /\ s.receipt.writes = 0)

NonTagFrame ==
    /\ s.peer = Peer /\ s.stack = Stack
    /\ IF s.receipt = Absent THEN TRUE
       ELSE /\ NetworkSide(s.receipt.saved) = NetworkSide(s.before)
            /\ \A k \in Cells \ {s.sent.key} : s.receipt.saved.tags[k] = s.before.tags[k]

ExactConsoleRequest ==
    IF s.plan.mode # "console" \/ s.phase = 0 THEN TRUE
    ELSE LET p == s.plan
             old == p.start.tags[p.q.key]
             can == p.pre = "ok" /\ p.read1 = "ok" /\ p.q.key # Reserved
                    /\ IF p.q.action = "DeleteTags" THEN old # Absent
                       ELSE p.q.value # old /\ (old # Absent \/ TagCount(p.start) < TagLimit)
         IN IF ~can THEN s.sent = Absent
            ELSE IF s.sent = Absent THEN FALSE
            ELSE /\ s.sent.target = Scope /\ s.sent.action = p.q.action
                 /\ s.sent.key = p.q.key
                 /\ s.sent.value = (IF p.q.action = "DeleteTags" THEN old ELSE p.q.value)

VerifiedConsoleOutcome ==
    IF s.phase # 3 THEN s.shown = Absent
    ELSE IF s.receipt = Absent THEN s.shown = Absent
    ELSE LET p == s.plan
             k == p.q.key
             ok == s.receipt.ack /\ p.read2 = "ok"
                   /\ s.row.tags[k] = (IF p.q.action = "DeleteTags" THEN Absent ELSE p.q.value)
             name == IF s.row.tags["Name"] = Absent THEN "" ELSE s.row.tags["Name"]
         IN IF ~ok THEN s.shown = Absent
            ELSE IF s.shown = Absent THEN FALSE
            ELSE /\ s.shown.scope = Scope /\ s.shown.key = k /\ s.shown.tag = s.row.tags[k]
                 /\ s.shown.name = name /\ s.shown.native /\ s.shown.parsed

NoConfirmationRollback ==
    IF s.phase # 3 \/ s.receipt = Absent THEN TRUE
    ELSE IF s.plan.concurrent = "tag-after"
         THEN s.row = [s.receipt.saved EXCEPT !.tags[s.plan.q.key] = "green"]
         ELSE s.row = s.receipt.saved
=============================================================================
