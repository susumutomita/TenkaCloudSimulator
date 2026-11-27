----------------------- MODULE EC2NativeNetworkACLAssociation -----------------------
EXTENDS Naturals, FiniteSets, Sequences

CONSTANT None
VARIABLE s

\* Finite saved IPv4 configuration; no packet engine. Ticket numbers are
\* symbolic identities bound to the same region and subnet, not wire IDs.
Region == "local-west"
Vpc == "native-vpc"
Subnet == "selected-subnet"
DefaultId == "system-default"
AclA == "custom-a"
AclB == "custom-b"
Foreign == "other-vpc-acl"
Custom == {AclA,AclB}
DefaultRow == [region |-> Region, vpc |-> Vpc, acl |-> DefaultId, profile |-> "ipv4-default-v1"]
Ticket(g) == <<Region,Subnet,g>>
PeerTicket == <<Region,"peer-subnet",1>>
Bind(id,g) ==
    [region |-> Region, vpc |-> Vpc, subnet |-> Subnet, acl |-> id,
     current |-> Ticket(g), prev |-> IF g=1 THEN None ELSE Ticket(g-1), gen |-> g]
DefaultRules == {<<d,100,"all-ipv4","allow">> : d \in {"in","out"}}
                \cup {<<d,32767,"all-ipv4","deny">> : d \in {"in","out"}}
World(kind,sub,custom) ==
    [vpc |-> kind, hasSubnet |-> sub, default |-> IF kind="marked" THEN DefaultRow ELSE None,
     binding |-> IF kind="marked" /\ sub THEN Bind(DefaultId,1) ELSE None,
     custom |-> custom, ruleEdited |-> {}, peers |-> <<"peer-vpc",PeerTicket>>,
     routes |-> "protected-unrelated-route-rows", cfn |-> "unmodified-stack"]
Empty == World("missing",FALSE,{})
Native == World("marked",TRUE,Custom)
Attached == [Native EXCEPT !.binding = Bind(AclA,2)]
Legacy == World("legacy",TRUE,Custom)
Stack == World("stack",TRUE,{})

ACLGrant(region,id) == <<region,"network-acl",id,"ReplaceNetworkAclAssociation">>
SubnetGrant(region,id) == <<region,"subnet",id,"ReplaceNetworkAclAssociation">>
Req(op,acl,ticket) ==
    [op |-> op, acl |-> acl, ticket |-> ticket, region |-> Region,
     aclGrant |-> ACLGrant(Region,acl), subnetGrant |-> SubnetGrant(Region,Subnet),
     actor |-> "operator", proof |-> "exact", commit |-> "ok"]
Request(acl,ticket) == Req("replace",acl,ticket)
LC(op) == Req(op,None,None)
ReadSubnet == LC("describe-subnet")
ReadACL(acl) == Req("describe-acl",acl,None)
Delete(acl) == Req("delete-acl",acl,None)
Rules(acl) == Req("edit-rules",acl,None)
BadGraph == {"broken-default","incomplete-cohort","outside-cohort-reference",
             "cfn-declaration","cfn-claim","unverified-current"}
StorageFault == {"row-count","exact-row","post-cohort","extra-write","commit"}
Plan(db,steps,ui) == [initial |-> db, steps |-> steps, ui |-> ui]
N(db,steps) == Plan(db,steps,None)

NativePlans ==
    { N(Empty,<<LC("create-vpc"),ReadACL(DefaultId),LC("create-subnet"),ReadSubnet,
                       LC("delete-vpc"),LC("delete-subnet"),LC("delete-vpc"),ReadACL(DefaultId)>>),
      N(Empty,<<[LC("create-vpc") EXCEPT !.proof = "default-collision"],ReadACL(DefaultId)>>),
      N(Empty,<<[LC("create-vpc") EXCEPT !.commit = "commit"],ReadACL(DefaultId)>>),
      N(World("marked",FALSE,{}),<<[LC("create-subnet") EXCEPT !.commit = "row-count"],ReadSubnet>>),
      N(World("legacy",FALSE,Custom),<<LC("create-subnet"),ReadSubnet,Request(AclA,Ticket(1))>>),
      N(Legacy,<<ReadSubnet,Request(AclA,Ticket(1)),ReadACL(AclA)>>),
      N(Stack,<<ReadSubnet,Request(AclA,Ticket(1)),LC("create-subnet"),ReadACL(DefaultId)>>),
      N(World("stack",FALSE,{}),<<LC("create-subnet"),ReadSubnet>>),
      N(Native,<<Request(AclA,Ticket(1)),ReadSubnet,Request(AclA,Ticket(1)),
                  Request(AclB,Ticket(1)),Request(AclA,Ticket(2)),Request(DefaultId,Ticket(2)),
                  Request(DefaultId,Ticket(1)),Request(AclA,Ticket(1)),ReadSubnet>>),
      N(Native,<<Delete(DefaultId),Rules(DefaultId),Request(AclA,Ticket(1)),ReadACL(AclA),
                  Delete(AclA),Rules(AclA),Request(DefaultId,Ticket(2)),Rules(AclA),Delete(AclA),ReadACL(AclA)>>),
      N(Native,<<Delete(AclB),Request(AclB,Ticket(1)),Request(Foreign,Ticket(1)),ReadSubnet>>),
      N(Native,<<[LC("delete-subnet") EXCEPT !.proof = "retained-reference"],ReadSubnet>>),
      N(Native,<<[LC("delete-subnet") EXCEPT !.commit = "commit"],ReadSubnet>>),
      N(Native,<<[Request(AclA,Ticket(1)) EXCEPT !.region = "other-region",
           !.aclGrant = ACLGrant("other-region",AclA), !.subnetGrant=SubnetGrant("other-region",Subnet)]>>),
      N(Native,<<[Request(AclA,Ticket(1)) EXCEPT !.aclGrant = ACLGrant(Region,AclB)]>>),
      N(Native,<<[Request(AclA,Ticket(1)) EXCEPT !.subnetGrant = SubnetGrant(Region,"other-subnet")]>>),
      N(Native,<<[Request(AclA,Ticket(1)) EXCEPT !.aclGrant = None]>>),
      N(Native,<<[Request(AclA,PeerTicket) EXCEPT !.actor = "ordinary"]>>),
      N(Native,<<[Request(AclA,Ticket(3)) EXCEPT !.actor = "ordinary"]>>),
      N(Native,<<[Request(AclA,Ticket(1)) EXCEPT !.subnetGrant = None, !.actor = "ordinary"]>>),
      N(Native,<<Request(AclA,Ticket(1)),
                   [Request(AclB,Ticket(1)) EXCEPT !.actor = "ordinary"],
                   [Request(AclA,Ticket(1)) EXCEPT !.commit = "commit"]>>),
      N(Native,<<[Request(DefaultId,Ticket(1)) EXCEPT !.commit = "commit"]>>) }
    \cup { N(Native,<<[Request(AclA,Ticket(1)) EXCEPT !.proof=p],ReadSubnet>>) :
           p \in BadGraph \cup {"new-id-unavailable"}}
    \cup { N(Native,<<[ReadSubnet EXCEPT !.proof=p]>>) : p \in BadGraph }
    \cup { N(Native,<<[Request(AclA,Ticket(1)) EXCEPT !.commit=f],ReadSubnet>>) : f \in StorageFault }

UI(choice,gate,pre,post,receipt,other,tx) ==
    [choice |-> choice, gate |-> gate, pre |-> pre, post |-> post,
     receipt |-> receipt, other |-> other, tx |-> tx]
UIBlock == {"no-profile","scoped-handoff","non-native-parent","lease-busy","retired-before",
            "no-default-in-catalog","stale-catalog-row"}
BeforeBad == {"empty","other-vpc","other-subnet","other-acl","stale-association","duplicate-row"}
AfterBad == BeforeBad \cup {"retired-after"}
UIPlans ==
    { Plan(Native,<<>>,UI(AclA,"ok","ok","ok","ok","none","ok")),
      Plan(Native,<<>>,UI(DefaultId,"ok","ok","ok","ok","none","ok")),
      Plan(Native,<<>>,UI(Foreign,"ok","ok","ok","ok","none","ok")),
      Plan(Attached,<<>>,UI(DefaultId,"ok","ok","ok","ok","none","ok")),
      Plan(Legacy,<<>>,UI(AclA,"ok","ok","ok","ok","none","ok")),
      Plan(Native,<<>>,UI(AclA,"ok","ok","ok","old","none","ok")),
      Plan(Native,<<>>,UI(AclA,"ok","ok","ok","different","none","ok")),
      Plan(Native,<<>>,UI(AclA,"ok","ok","ok","invalid","none","ok")),
      Plan(Native,<<>>,UI(AclA,"ok","ok","ok","ok","after","ok")),
      Plan(Native,<<>>,UI(AclA,"ok","ok","ok","ok","same-after","commit")) }
    \cup { Plan(Native,<<>>,UI(AclA,f,"ok","ok","ok","none","ok")) : f \in UIBlock }
    \cup { Plan(Native,<<>>,UI(AclA,"ok",f,"ok","ok","none","ok")) : f \in BeforeBad }
    \cup { Plan(Native,<<>>,UI(AclA,"ok","ok",f,"ok","none","ok")) : f \in AfterBad }
Plans == NativePlans \cup UIPlans
Init ==
    \E p \in Plans :
      s = [plan |-> p, step |-> 0, data |-> p.initial, last |-> None,
           uiSent |-> None, receipt |-> None, shown |-> None]

MissingReceipt(q) == IF q.actor="ordinary" THEN "denied" ELSE "missing"
KnownTicket(q,db) ==
    db.binding # None /\ q.region=Region /\
      (q.ticket=db.binding.current \/ (db.binding.prev # None /\ q.ticket=db.binding.prev))
CanACL(q) == q.aclGrant=ACLGrant(q.region,q.acl)
CanSubnet(q) == q.subnetGrant=SubnetGrant(q.region,Subnet)
SameVpcDestination(q,db) == q.acl=DefaultId \/ q.acl \in db.custom
CohortOK(q,db) == db.default = DefaultRow /\ q.proof \notin BadGraph
Approved(q,db) ==
    IF ~CanACL(q) THEN "denied"
    ELSE IF ~KnownTicket(q,db) THEN MissingReceipt(q)
    ELSE IF ~CanSubnet(q) THEN "denied"
    ELSE IF ~CohortOK(q,db) THEN "refused"
    ELSE IF q.ticket # db.binding.current THEN
        IF db.binding.acl=q.acl THEN "repeat" ELSE MissingReceipt(q)
    ELSE IF db.binding.acl=q.acl THEN "repeat"
    ELSE IF SameVpcDestination(q,db) /\ q.proof \notin {"new-id-unavailable"}
         THEN "update" ELSE "refused"
Receipt(status,id,row) == [status |-> status, id |-> id, row |-> row]
Stay(status,db) == Receipt(status,None,db)
Save(status,id,row) == Receipt(status,id,row)
CommitOne(q) == q.commit \notin StorageFault
Replace(q,db) ==
    LET outcome == Approved(q,db)
    IN CASE outcome = "update" /\ CommitOne(q) ->
                LET next == Bind(q.acl,db.binding.gen+1)
                IN Save("ok",next.current,[db EXCEPT !.binding=next])
          [] outcome = "repeat" /\ q.commit # "commit" -> Save("ok",db.binding.current,db)
          [] outcome \in {"repeat","update"} -> Stay("refused",db)
          [] OTHER -> Stay(outcome,db)

Link(db) ==
    IF db.binding=None THEN None
    ELSE [vpc |-> Vpc, subnet |-> Subnet, acl |-> db.binding.acl, id |-> db.binding.current]
Mode(db,id) ==
    IF id=DefaultId THEN "default-config-only"
    ELSE IF db.binding # None /\ db.binding.acl=id THEN "attached-config-only"
    ELSE "detached-config-only"
ACLProjection(db,id) ==
    IF (id=DefaultId /\ db.default=DefaultRow) \/ id \in db.custom THEN
        [mode |-> Mode(db,id), acl |-> id, link |-> IF db.binding # None /\ db.binding.acl=id THEN Link(db) ELSE None,
         rules |-> IF id=DefaultId THEN DefaultRules ELSE None]
    ELSE None
Observe(q,db) ==
    IF q.proof \in BadGraph \/ q.commit="commit" THEN Stay("refused",db)
    ELSE LET id == IF q.op="describe-subnet"
                   THEN IF db.binding=None THEN None ELSE db.binding.acl
                   ELSE q.acl
             projected == ACLProjection(db,id)
         IN IF q.op="describe-acl" /\ projected=None THEN Stay("missing",db)
            ELSE Receipt("read",projected,db)
Detached(db,id) == id \in db.custom /\ (db.binding=None \/ db.binding.acl#id)
CanLifecycle(q,db) ==
    CASE q.op="create-vpc" -> db.vpc="missing"
      [] q.op="create-subnet" -> db.vpc \in {"marked","legacy"} /\ ~db.hasSubnet
      [] q.op="delete-subnet" -> db.vpc \in {"marked","legacy"} /\ db.hasSubnet
      [] q.op="delete-vpc" -> db.vpc \in {"marked","legacy"} /\ ~db.hasSubnet /\ db.custom={}
      [] OTHER -> Detached(db,q.acl)
Life(q,db) ==
    IF q.proof # "exact" \/ ~CommitOne(q) \/ ~CanLifecycle(q,db) THEN Stay("refused",db)
    ELSE LET next ==
        CASE q.op="create-vpc" -> World("marked",FALSE,{})
          [] q.op="create-subnet" -> [db EXCEPT !.hasSubnet=TRUE,
                      !.binding=IF db.default=DefaultRow THEN Bind(DefaultId,1) ELSE None]
          [] q.op="delete-subnet" -> [db EXCEPT !.hasSubnet=FALSE, !.binding=None]
          [] q.op="delete-vpc" -> Empty
          [] q.op="delete-acl" -> [db EXCEPT !.custom=@ \ {q.acl}, !.ruleEdited=@ \ {q.acl}]
          [] OTHER -> [db EXCEPT !.ruleEdited=@ \cup {q.acl}]
         IN Save("ok",None,next)
Execute(q,db) ==
    CASE q.op="replace" -> Replace(q,db)
      [] q.op \in {"describe-acl","describe-subnet"} -> Observe(q,db)
      [] OTHER -> Life(q,db)
DoNative ==
    /\ s.plan.ui=None /\ s.step < Len(s.plan.steps)
    /\ LET q == s.plan.steps[s.step+1]
           reply == Execute(q,s.data)
       IN s' = [s EXCEPT !.step=@+1, !.data=reply.row,
                      !.last=[q |-> q, before |-> s.data, result |-> reply]]

UIRead(db,fault) ==
    LET link == Link(db)
    IN IF link=None \/ fault \in {"empty","duplicate-row"} THEN None
       ELSE CASE fault="other-vpc" -> [link EXCEPT !.vpc="wrong-vpc"]
             [] fault="other-subnet" -> [link EXCEPT !.subnet="peer-subnet"]
             [] fault="other-acl" -> [link EXCEPT !.acl=AclB]
             [] fault="stale-association" -> [link EXCEPT !.id=PeerTicket]
             [] OTHER -> link
UIBegin ==
    /\ s.plan.ui # None /\ s.step=0
    /\ LET ui == s.plan.ui
           saved == Link(s.data)
           fresh == UIRead(s.data,ui.pre)
           sent == IF ui.gate \notin UIBlock /\ saved#None /\ fresh=saved /\
                         ui.choice \in (s.data.custom \cup {DefaultId}) \ {saved.acl}
                   THEN [Request(ui.choice,fresh.id) EXCEPT !.commit=ui.tx] ELSE None
       IN s'=[s EXCEPT !.step=1, !.uiSent=sent]
UIReplace ==
    /\ s.plan.ui # None /\ s.step=1
    /\ LET reply == IF s.uiSent=None THEN None ELSE Replace(s.uiSent,s.data)
       IN s'=[s EXCEPT !.step=2, !.receipt=reply, !.data=IF reply=None THEN @ ELSE reply.row]
AfterOther(ui,db) ==
    IF ui.other \in {"after","same-after"} THEN
      LET dest == IF ui.other="after" THEN AclB ELSE ui.choice
          reply == Replace(Request(dest,db.binding.current),db)
      IN reply.row
    ELSE db
Reported(ui,reply,old) ==
    IF reply=None \/ reply.status#"ok" \/ ui.receipt="invalid" THEN None
    ELSE CASE ui.receipt="old" -> old [] ui.receipt="different" -> PeerTicket [] OTHER -> reply.id
UICheck(ui,ack,read,old) ==
    ack # None /\ ack # old /\ ui.post#"retired-after" /\ read # None /\
    read = [vpc |-> Vpc, subnet |-> Subnet, acl |-> ui.choice, id |-> ack]
UIEnd ==
    /\ s.plan.ui # None /\ s.step=2
    /\ LET ui == s.plan.ui
           old == IF s.plan.initial.binding=None THEN None ELSE s.plan.initial.binding.current
           ack == Reported(ui,s.receipt,old)
           live == IF s.uiSent=None THEN s.data ELSE AfterOther(ui,s.data)
           read == UIRead(live,ui.post)
           confirmed == UICheck(ui,ack,read,old)
       IN s'=[s EXCEPT !.step=3, !.data=live, !.shown=IF confirmed THEN read ELSE None]
Next == DoNative \/ UIBegin \/ UIReplace \/ UIEnd

TypeOK ==
    /\ s.plan \in Plans /\ s.step \in 0..10
    /\ s.data.vpc \in {"missing","marked","legacy","stack"}
    /\ s.data.hasSubnet \in BOOLEAN /\ s.data.custom \subseteq Custom
    /\ IF s.data.binding=None THEN TRUE ELSE s.data.binding.gen \in 1..3
ExactSavedDefault ==
    /\ s.data.default=(IF s.data.vpc="marked" THEN DefaultRow ELSE None)
    /\ ((s.data.binding # None) = (s.data.vpc="marked" /\ s.data.hasSubnet))
    /\ IF s.data.binding=None THEN TRUE
       ELSE /\ s.data.binding=Bind(s.data.binding.acl,s.data.binding.gen)
            /\ s.data.binding.acl \in {DefaultId} \cup s.data.custom
    /\ IF s.last=None THEN TRUE
       ELSE LET l == s.last
                begin == l.q.op="create-vpc" /\ l.before.vpc="missing"
                         /\ l.q.proof="exact" /\ l.q.commit="ok"
                end == l.q.op="delete-vpc" /\ l.before.vpc="marked"
                       /\ ~l.before.hasSubnet /\ l.before.custom={} /\ l.q.proof="exact" /\ l.q.commit="ok"
                createChild == l.q.op="create-subnet" /\ l.before.vpc \in {"marked","legacy"} /\
                               ~l.before.hasSubnet /\ l.q.proof="exact" /\ l.q.commit="ok"
                retireChild == l.q.op="delete-subnet" /\ l.before.vpc \in {"marked","legacy"} /\
                               l.before.hasSubnet /\ l.q.proof="exact" /\ l.q.commit="ok"
            IN /\ s.data.default = (IF begin THEN DefaultRow ELSE IF end THEN None ELSE l.before.default)
               /\ IF l.q.op="create-subnet" THEN
                      /\ (l.result.status="ok")=createChild
                      /\ s.data.hasSubnet=(l.before.hasSubnet \/ createChild)
                      /\ s.data.binding = (IF createChild /\ l.before.vpc="marked"
                                           THEN Bind(DefaultId,1) ELSE l.before.binding)
                  ELSE IF l.q.op="delete-subnet" THEN
                      /\ (l.result.status="ok")=retireChild
                      /\ IF retireChild THEN ~s.data.hasSubnet /\ s.data.binding=None ELSE s.data=l.before
                  ELSE TRUE
ReferenceReplacement(q,b) ==
    LET candidate == b.binding
        targetOK == q.aclGrant=ACLGrant(q.region,q.acl)
        inScope == q.region=Region /\ candidate#None
        found == IF inScope THEN q.ticket \in {candidate.current,candidate.prev} /\ q.ticket#None ELSE FALSE
        hidden == IF q.actor="operator" THEN "missing" ELSE "denied"
    IN IF ~targetOK THEN Stay("denied",b)
       ELSE IF ~found THEN Stay(hidden,b)
       ELSE IF q.subnetGrant#SubnetGrant(Region,Subnet) THEN Stay("denied",b)
       ELSE IF q.proof \in BadGraph \/ b.default#DefaultRow THEN Stay("refused",b)
       ELSE IF q.ticket=candidate.prev /\ q.acl#candidate.acl THEN Stay(hidden,b)
       ELSE IF q.acl=candidate.acl THEN
                IF q.commit="commit" THEN Stay("refused",b) ELSE Receipt("ok",candidate.current,b)
       ELSE IF q.acl \notin ({DefaultId} \cup b.custom) \/ q.proof#"exact" \/ q.commit#"ok"
            THEN Stay("refused",b)
       ELSE LET oldGen == candidate.gen
                new == [candidate EXCEPT !.prev=candidate.current, !.gen=oldGen+1,
                                          !.acl=q.acl, !.current=Ticket(oldGen+1)]
            IN Receipt("ok",Ticket(oldGen+1),[b EXCEPT !.binding=new])
ExactAssociationReceipt ==
    IF s.last=None \/ s.last.q.op#"replace" THEN TRUE
    ELSE s.last.result=ReferenceReplacement(s.last.q,s.last.before)
DetachedRuleAndDeleteFence ==
    IF s.last=None \/ s.last.q.op \notin {"delete-acl","edit-rules"} THEN TRUE
    ELSE LET l == s.last
             id == l.q.acl
             eligible == id \in l.before.custom /\
                  (l.before.binding=None \/ l.before.binding.acl#id) /\ l.q.proof="exact" /\ l.q.commit="ok"
         IN /\ (l.result.status="ok")=eligible
            /\ IF ~eligible THEN l.result.row=l.before
               ELSE IF l.q.op="delete-acl" THEN s.data.custom=l.before.custom \ {id}
                    ELSE s.data.ruleEdited=l.before.ruleEdited \cup {id}
UnchangedNetworkFrame ==
    /\ s.data.peers=s.plan.initial.peers /\ s.data.routes=s.plan.initial.routes /\ s.data.cfn=s.plan.initial.cfn
    /\ IF s.last=None \/ s.last.q.op#"replace" THEN TRUE
       ELSE [s.data EXCEPT !.binding=None]=[s.last.before EXCEPT !.binding=None]
ExactStoredACLProjection ==
    IF s.last=None \/ s.last.q.op \notin {"describe-acl","describe-subnet"} THEN TRUE
    ELSE LET l == s.last
             b == l.before.binding
             id == IF l.q.op="describe-subnet" THEN IF b=None THEN None ELSE b.acl ELSE l.q.acl
             expected == id \in l.before.custom \/ (id=DefaultId /\ l.before.default=DefaultRow)
             item == l.result.id
         IN /\ s.data=l.before
            /\ IF l.q.proof#"exact" \/ l.q.commit="commit" THEN l.result.status="refused" /\ item=None
               ELSE /\ l.result.status=(IF ~expected /\ l.q.op="describe-acl" THEN "missing" ELSE "read")
                    /\ IF ~expected THEN item=None
                       ELSE IF item=None THEN FALSE ELSE
                         /\ item.acl=id
                         /\ item.mode=(IF id=DefaultId THEN "default-config-only"
                                      ELSE IF b#None /\ b.acl=id THEN "attached-config-only" ELSE "detached-config-only")
                         /\ item.rules=(IF id=DefaultId THEN
                               {<<"in",100,"all-ipv4","allow">>,<<"out",100,"all-ipv4","allow">>,
                                <<"in",32767,"all-ipv4","deny">>,<<"out",32767,"all-ipv4","deny">>}
                                       ELSE None)
                         /\ item.link=(IF b=None \/ b.acl#id THEN None
                                       ELSE [vpc |-> Vpc, subnet |-> Subnet, acl |-> id, id |-> b.current])
ExactSelectedUI ==
    IF s.plan.ui=None \/ s.step=0 THEN TRUE
    ELSE LET u == s.plan.ui
             b == s.plan.initial.binding
             expected == u.gate="ok" /\ u.pre="ok" /\ b#None /\
                        u.choice \in ({DefaultId} \cup s.plan.initial.custom) /\ u.choice#b.acl
             q == IF b=None THEN None ELSE [Request(u.choice,b.current) EXCEPT !.commit=u.tx]
             ok == expected /\ u.tx="ok" /\ u.receipt="ok" /\ u.post="ok" /\ u.other="none"
         IN /\ s.uiSent=(IF expected THEN q ELSE None)
            /\ IF s.step=2 /\ expected THEN s.receipt=ReferenceReplacement(q,s.plan.initial) ELSE TRUE
            /\ IF s.step=3 /\ ok THEN
                  s.shown=[vpc |-> Vpc, subnet |-> Subnet, acl |-> u.choice, id |-> Ticket(b.gen+1)]
               ELSE s.shown=None
ReadbackDoesNotUndo ==
    IF s.plan.ui=None \/ s.step#3 \/ s.receipt=None THEN TRUE
    ELSE LET u == s.plan.ui
             saved == s.receipt.row
             expected == IF u.other="after" THEN [saved EXCEPT !.binding=Bind(AclB,3)]
                         ELSE IF u.other="same-after" THEN [saved EXCEPT !.binding=Bind(u.choice,2)]
                         ELSE saved
         IN s.data=expected
==============================================================================
