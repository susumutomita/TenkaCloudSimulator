-------------------------- MODULE EC2NativeSubnetTags --------------------------
EXTENDS Naturals

CONSTANT Absent, Omitted
VARIABLE s

\* Closed local fixture, one selected existing native subnet, not the entire
\* DescribeSubnets inventory. The VPC tag model owns shared limits/parsing.
Scope == [region |-> "west", id |-> "subnet-a", vpc |-> "vpc-a",
          cidr |-> "inside-prefix", az |-> "west-b", owner |-> "ec2"]
Target(region, kind, id) == <<region, kind, id>>
SubnetArn == Target(Scope.region,"subnet",Scope.id)
Actions == {"CreateTags","DeleteTags"}
TagKeys == {"Name","Purpose"}
Values == {"","before","after","independent"}
Tags(name) == [k \in TagKeys |-> IF k = "Name" THEN name ELSE "before"]
Saved(name) ==
    [subnet |-> [scope |-> Scope, tags |-> Tags(name), revision |-> 2,
                 publicIP |-> TRUE, route |-> "associated"],
     parent |-> "unchanged-parent", neighbor |-> "unchanged-sibling",
     routeRow |-> "unchanged-route"]
Initial == Saved("before")

\* Normalized target shape and ownership witnesses. Parent declaration/main
\* and DNS validation belong to the tag writer, not the native inventory.
RowFaults == {"missing-row","wrong-kind","stack-owned","child-claim",
              "child-cidr","outside-parent","foreign-zone","generation","invalid-tag-row"}
ParentFaults == {"missing-parent","foreign-parent","parent-claim","parent-prefix"}
WriteOnly == {"subnet-declaration","parent-declaration","parent-main","parent-dns","raw-document"}
TxFaults == {"cas","readback","commit","parent-side-effect","neighbor-side-effect","route-side-effect"}
Extras == {"parent-side-effect","neighbor-side-effect","route-side-effect"}
Query(action,key,value) ==
    [action |-> action, key |-> key, value |-> value, target |-> SubnetArn,
     grant |-> <<action,SubnetArn>>, proof |-> "valid", tx |-> "clean"]
Put(v) == Query("CreateTags","Name",v)
Del(v) == Query("DeleteTags","Name",v)
Plan(mode,row,q,capture,pre,post,other) ==
    [mode |-> mode, start |-> row, call |-> q, capture |-> capture,
     pre |-> pre, post |-> post, other |-> other]
Native(row,q) == Plan("query",row,q,"ok","ok","ok","none")
Read(q) == Plan("inventory",Initial,q,"ok","ok","ok","none")
Console(row,q,capture,pre,post,other) == Plan("console",row,q,capture,pre,post,other)
NativeCases ==
    { Native(Initial,Put("after")), Native(Initial,Put("before")),
      Native(Initial,Put("")), Native(Initial,Del(Omitted)),
      Native(Initial,Del("")), Native(Saved(""),Del("")),
      Native(Initial,Del("independent")), Native(Saved(Absent),Del(Omitted)),
      Native(Initial,[Put("after") EXCEPT !.target = Target("east","subnet",Scope.id),
                                          !.grant = <<"CreateTags",Target("east","subnet",Scope.id)>>]),
      Native(Initial,[Put("after") EXCEPT !.target = Target("west","subnet","subnet-b"),
                                          !.grant = <<"CreateTags",Target("west","subnet","subnet-b")>>]),
      Native(Initial,[Put("after") EXCEPT !.grant = <<"DeleteTags",SubnetArn>>]),
      Native(Initial,[Put("after") EXCEPT !.grant = <<"CreateTags",Target("west","vpc",Scope.vpc)>>]),
      Native(Initial,[Put("after") EXCEPT !.grant = <<"CreateTags",Target("east","subnet",Scope.id)>>]),
      Native(Initial,[Del("") EXCEPT !.tx = "commit"]) }
    \cup {Native(Initial,[Put("after") EXCEPT !.proof = p]) :
          p \in RowFaults \cup ParentFaults \cup WriteOnly}
    \cup {Native(Initial,[Put("after") EXCEPT !.tx = t]) : t \in TxFaults}
InventoryCases ==
    {Read([Put("after") EXCEPT !.proof = proof]) :
     proof \in {"valid","child-cidr","outside-parent","foreign-zone","parent-claim",
                 "parent-declaration","parent-main","parent-dns","raw-document"}}

UIBlock == {"scope-not-announced","scoped-handoff","no-lease","other-capture"}
ScopeFaults == {"other-region","other-subnet","other-parent","same-id-other-cidr",
                "same-id-other-zone","unverified-owner","duplicate-selection"}
ConsoleCases ==
    { Console(Initial,Put("after"),"ok","ok","ok","none"),
      Console(Saved(""),Del(Omitted),"ok","ok","ok","none"),
      Console(Initial,Del(Omitted),"ok","ok","ok","before"),
      Console(Initial,Put("after"),"ok","ok","ok","after"),
      Console(Initial,Put("after"),"stale-tag","ok","ok","none"),
      Console(Initial,Put("before"),"ok","ok","ok","none"),
      Console(Saved(Absent),Del(Omitted),"ok","ok","ok","none"),
      Console(Initial,[Put("independent") EXCEPT !.tx = "commit"],"ok","ok","ok","after") }
    \cup {Console(Initial,Put("after"),f,"ok","ok","none") : f \in UIBlock}
    \cup {Console(Initial,Put("after"),"ok",f,"ok","none") : f \in ScopeFaults}
    \cup {Console(Initial,Put("after"),"ok","ok",f,"none") :
          f \in ScopeFaults \cup {"false-return","retired"}}

Plans == NativeCases \cup InventoryCases \cup ConsoleCases
Init ==
    \E p \in Plans :
        s = [plan |-> p, step |-> 0, data |-> p.start, prior |-> p.start,
             sent |-> Absent, receipt |-> Absent, observed |-> Absent, shown |-> Absent]

RightTarget(q) == q.target = SubnetArn
RightGrant(q) == q.grant = <<q.action,q.target>>
OwnSubnet(q) == q.proof \notin RowFaults
OwnParent(q) == q.proof \notin ParentFaults
ValidateTagRecord(q) == q.proof \notin WriteOnly
NativeAdmission(q) == RightTarget(q) /\ RightGrant(q) /\
    OwnSubnet(q) /\ OwnParent(q) /\ ValidateTagRecord(q)
NextTag(q,old) ==
    CASE q.action = "CreateTags" -> q.value
      [] q.value = Omitted \/ q.value = old -> Absent
      [] OTHER -> old
Staged(q,before,tag) ==
    LET expected == [before EXCEPT !.subnet.tags[q.key] = tag]
    IN CASE q.tx = "parent-side-effect" -> [expected EXCEPT !.parent = "changed-parent"]
         [] q.tx = "neighbor-side-effect" -> [expected EXCEPT !.neighbor = "changed-sibling"]
         [] q.tx = "route-side-effect" -> [expected EXCEPT !.routeRow = "changed-route"]
         [] OTHER -> expected
Affected(q) == IF q.tx = "cas" THEN 0 ELSE 1
AllWrites(q) == IF q.tx \in Extras THEN 2 ELSE 1
RightReadback(q) == q.tx # "readback"
Transaction(q,before) ==
    LET tag == NextTag(q,before.subnet.tags[q.key])
        changed == tag # before.subnet.tags[q.key]
        admitted == NativeAdmission(q)
        ok == admitted /\ q.tx # "commit" /\
              (~changed \/ (Affected(q) = 1 /\ AllWrites(q) = 1 /\ RightReadback(q)))
        saved == IF ok /\ changed THEN Staged(q,before,tag) ELSE before
    IN [admitted |-> admitted, ack |-> ok, count |-> IF ok /\ changed THEN 1 ELSE 0,
        saved |-> saved]

Describe(data,key,fault) ==
    LET shape == data.subnet.scope
        reported == CASE fault = "other-region" -> [shape EXCEPT !.region = "east"]
                      [] fault = "other-subnet" -> [shape EXCEPT !.id = "subnet-b"]
                      [] fault = "other-parent" -> [shape EXCEPT !.vpc = "vpc-b"]
                      [] fault = "same-id-other-cidr" -> [shape EXCEPT !.cidr = "other-prefix"]
                      [] fault = "same-id-other-zone" -> [shape EXCEPT !.az = "west-c"]
                      [] fault = "unverified-owner" -> [shape EXCEPT !.owner = "cloudformation"]
                      [] OTHER -> shape
    IN [scope |-> reported, unambiguous |-> fault # "duplicate-selection", key |-> key,
        value |-> data.subnet.tags[key], tags |-> data.subnet.tags, publicIP |-> data.subnet.publicIP]
TypedInventory(p,data) ==
    IF OwnSubnet(p.call) /\ OwnParent(p.call) THEN Describe(data,p.call.key,"ok") ELSE Absent
Selected(read) == read.unambiguous /\ read.scope = Scope
PriorValue(p) == IF p.capture = "stale-tag" THEN "independent" ELSE p.start.subnet.tags[p.call.key]
CanSend(p) ==
    LET pre == Describe(p.start,p.call.key,p.pre)
    IN p.capture \notin UIBlock /\ Selected(pre) /\ pre.value = PriorValue(p)
       /\ IF p.call.action = "DeleteTags" THEN pre.value # Absent ELSE pre.value # p.call.value
WireForConsole(p) ==
    [p.call EXCEPT !.value = IF p.call.action = "DeleteTags" THEN PriorValue(p) ELSE @]
Prepare ==
    /\ s.step = 0
    /\ LET p == s.plan
       IN s' = [s EXCEPT !.step = 1, !.sent =
                    CASE p.mode = "query" -> p.call
                      [] p.mode = "console" /\ CanSend(p) -> WireForConsole(p)
                      [] OTHER -> Absent]
BeforeAlternate(p,data) ==
    IF p.other = "before" THEN [data EXCEPT !.subnet.tags[p.call.key] = "independent"] ELSE data
AfterAlternate(p,data) ==
    IF p.other = "after" THEN [data EXCEPT !.subnet.tags[p.call.key] = "independent"] ELSE data
DoNative ==
    /\ s.step = 1 /\ s.plan.mode # "inventory"
    /\ LET prior == IF s.sent = Absent THEN s.data ELSE BeforeAlternate(s.plan,s.data)
           r == IF s.sent = Absent THEN Absent ELSE Transaction(s.sent,prior)
       IN s' = [s EXCEPT !.step = 2, !.prior = prior, !.receipt = r,
                        !.data = IF r = Absent THEN prior ELSE r.saved]
DoRead ==
    /\ s.step = 1 /\ s.plan.mode = "inventory"
    /\ s' = [s EXCEPT !.step = 2, !.observed = TypedInventory(s.plan,s.data)]
Wanted(p) == IF p.call.action = "DeleteTags" THEN Absent ELSE p.call.value
MayShow(p,receipt,seen) ==
    receipt.ack /\ p.post # "false-return" /\ p.post # "retired"
    /\ Selected(seen) /\ seen.key = p.call.key /\ seen.value = Wanted(p)
Confirm ==
    /\ s.step = 2 /\ s.plan.mode = "console"
    /\ LET p == s.plan
           live == IF s.sent = Absent THEN s.data ELSE AfterAlternate(p,s.data)
           reply == IF s.sent = Absent THEN Absent ELSE Describe(live,p.call.key,p.post)
           confirmed == IF reply = Absent THEN FALSE ELSE MayShow(p,s.receipt,reply)
       IN s' = [s EXCEPT !.step = 3, !.data = live, !.observed = reply,
                         !.shown = IF confirmed THEN reply ELSE Absent]
Next == Prepare \/ DoNative \/ DoRead \/ Confirm

TypeOK ==
    /\ s.plan \in Plans /\ s.step \in 0..3
    /\ s.data.subnet.tags \in [TagKeys -> Values \cup {Absent}]
    /\ s.data.subnet.publicIP \in BOOLEAN /\ s.data.subnet.revision \in 1..3
    /\ s.sent = Absent \/ (s.sent.action \in Actions /\ s.sent.key \in TagKeys)
SubnetAndParentFences ==
    IF s.receipt = Absent THEN TRUE
    ELSE LET q == s.sent
         IN s.receipt.admitted =
             (q.target = <<"west","subnet","subnet-a">> /\
              q.grant = <<q.action,<<"west","subnet","subnet-a">>>> /\ q.proof = "valid")
TagWriteOracle(q,old) ==
    IF q.action = "DeleteTags" THEN
        IF q.value \in {old,Omitted} THEN Absent ELSE old
    ELSE q.value
SingleDocumentCommit ==
    IF s.receipt = Absent THEN TRUE
    ELSE LET q == s.sent
             old == s.prior.subnet.tags[q.key]
             tag == TagWriteOracle(q,old)
             delta == IF tag = old THEN 0 ELSE 1
             validCommit == s.receipt.admitted /\ q.tx # "commit" /\ (delta = 0 \/ q.tx = "clean")
             expected == IF validCommit THEN [s.prior EXCEPT !.subnet.tags[q.key] = tag] ELSE s.prior
         IN /\ s.receipt.ack = validCommit
            /\ s.receipt.count = (IF validCommit THEN delta ELSE 0)
            /\ s.receipt.saved.subnet.tags = expected.subnet.tags
UnchangedNetworkFrame ==
    /\ s.data.parent = s.plan.start.parent /\ s.data.neighbor = s.plan.start.neighbor
    /\ s.data.routeRow = s.plan.start.routeRow
    /\ s.data.subnet.scope = s.plan.start.subnet.scope
    /\ s.data.subnet.revision = s.plan.start.subnet.revision
    /\ s.data.subnet.publicIP = s.plan.start.subnet.publicIP
    /\ s.data.subnet.route = s.plan.start.subnet.route
ExactNativeTagProjection ==
    IF s.plan.mode # "inventory" \/ s.step < 2 THEN TRUE
    ELSE LET p == s.plan
         IN /\ s.data = p.start
            /\ IF p.call.proof \in RowFaults \cup ParentFaults THEN s.observed = Absent
               ELSE IF s.observed = Absent THEN FALSE
               ELSE /\ s.observed.scope = p.start.subnet.scope
                    /\ s.observed.tags = p.start.subnet.tags
                    /\ s.observed.publicIP = p.start.subnet.publicIP
ExactSelectedSubnetRequest ==
    IF s.plan.mode # "console" \/ s.step = 0 THEN TRUE
    ELSE LET p == s.plan
             old == p.start.subnet.tags[p.call.key]
             change == IF p.call.action = "DeleteTags" THEN old # Absent ELSE p.call.value # old
             expected == IF p.capture = "ok" /\ p.pre = "ok" /\ change
                         THEN [p.call EXCEPT !.value = IF p.call.action = "DeleteTags" THEN old ELSE @]
                         ELSE Absent
         IN s.sent = expected
ExactSelectedSubnetConfirmation ==
    IF s.step # 3 \/ s.receipt = Absent THEN s.shown = Absent
    ELSE LET q == s.plan.call
             ok == s.plan.post = "ok" /\ s.receipt.ack /\ s.data.subnet.tags[q.key] = Wanted(s.plan)
         IN IF ~ok THEN s.shown = Absent
            ELSE IF s.shown = Absent THEN FALSE
            ELSE /\ s.shown.scope = Scope /\ s.shown.unambiguous /\ s.shown.key = q.key
                 /\ s.shown.value = Wanted(s.plan) /\ s.shown.tags = s.data.subnet.tags
NoUnconfirmedUndo ==
    IF s.step # 3 \/ s.receipt = Absent THEN TRUE
    ELSE LET committed == s.receipt.saved
         IN IF s.plan.other = "after"
            THEN s.data = [committed EXCEPT !.subnet.tags[s.sent.key] = "independent"]
            ELSE s.data = committed
==============================================================================
