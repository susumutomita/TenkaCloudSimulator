------------------------- MODULE ServiceQuotaRequests -------------------------
EXTENDS Naturals, Sequences, FiniteSets

\* AE, AW and BE denote two isolated databases and two regional quota scopes.
Scopes == {"AE","AW","BE"}
Team(s) == IF s = "BE" THEN "B" ELSE "A"
Status(option) == IF option = "false" THEN "NOT_APPROVED" ELSE "PENDING"
FilterSeq(rows, s) == SelectSeq(rows, LAMBDA x : x.scope = s)
TeamRows(rows, s) == SelectSeq(rows, LAMBDA x : Team(x.scope) = Team(s))
HasPending(rows, s) == \E i \in 1..Len(rows) : rows[i].scope = s /\ rows[i].status = "PENDING"
Record(rows, s, option, day, who, want) ==
 LET n == Len(TeamRows(rows,s))+1
 IN [scope |-> s, seq |-> n, id |-> <<Team(s),n>>, requester |-> who, day |-> day,
     desired |-> want, status |-> Status(option), hasCase |-> FALSE]
\* Desired 11 and 13 denote 5.5 and 6.5 in the half-unit encoding; the fixed profile bound
\* in this model is 2. The simulator's actual profile bound is 5.
HistoryFixture == <<"AE","AE","AW","BE","AE">>
SeedScopes(lane) == CASE lane="history" -> HistoryFixture [] lane="full" -> <<"AW","AW","AW">>
                       [] OTHER -> <<"AW","BE">>
SeedDay(lane,i) == IF lane = "history" /\ i = 1 THEN 0 ELSE 1
SeedOption(s,lane) == IF s = "BE" \/ (lane = "history" /\ s = "AW") THEN "true" ELSE "false"
NoResult == [code |-> "none", rows |-> <<>>, cursor |-> {}]
Result(code, rows, cursor) == [code |-> code, rows |-> rows, cursor |-> cursor]
VARIABLE st,last
Note(event,mode,arg) == [event |-> event, mode |-> mode, arg |-> arg, before |-> st]
Init ==
 /\ st \in {[lane |-> l, db |-> <<>>, phase |-> "seed", tries |-> 0, scope |-> "AE",
       option |-> "false", actor |-> "applicant", desired |-> 11, waiting |-> FALSE, staged |-> <<>>, applied |-> 2, used |-> 1, probes |-> 0,
       day |-> 91, epoch |-> 0, reads |-> 0, added |-> FALSE, out |-> NoResult] :
         l \in {"requests","full","history"}}
 /\ last = Note("init","","")
Bootstrap ==
 /\ st.phase = "seed"
 /\ LET path == SeedScopes(st.lane)
        i == Len(st.db)+1
    IN /\ i <= Len(path)
       /\ LET s == path[i]
          IN st' = [st EXCEPT !.db = Append(@,Record(st.db,s,SeedOption(s,st.lane),SeedDay(st.lane,i),"earlier",IF i % 2 = 0 THEN 13 ELSE 11)),
                             !.phase = IF i=Len(path) THEN "ready" ELSE "seed"]
 /\ last' = Note("seed","","")

Grants == {"wildcard","none","sameQuota","otherQuota","explicitDeny"}
\* The wildcard grant permits named and account-wide resources; explicitDeny includes a competing allow.
Target(op) == IF op \in {"request","byQuota"} THEN "quota" ELSE "*"
Allowed(op,grant) == grant="wildcard" \/ (grant="sameQuota" /\ Target(op)="quota")
Guards == {"ok","denied","notHigher","unsupported","wrongLevel","tooLarge","sameQuota","otherQuota","explicitDeny"}
RequestGuard(g) == IF g \in {"sameQuota","otherQuota","explicitDeny"}
                   THEN IF Allowed("request",g) THEN "ok" ELSE "denied" ELSE g
Admit(st0,s,g) ==
 CASE g = "denied" -> "denied"
   [] g # "ok" -> "invalid"
   [] HasPending(st0.db,s) -> "pending"
   [] Len(TeamRows(st0.db,s)) >= 3 -> "full"
   [] OTHER -> "stage"
SubmitAt(s,o,g,want) ==
 /\ st.lane # "history" /\ st.phase = "ready" /\ ~st.waiting /\ st.tries < 2
 /\ want \in {11,13} /\ s \in Scopes /\ o \in {"true","false","omitted"} /\ g \in Guards
 /\ st.tries > 0 => (g = "ok" /\ o # "omitted")
 /\ LET verdict == Admit(st,s,RequestGuard(g))
        rec == Record(st.db,s,o,st.day,"applicant",want)
    IN st' = [st EXCEPT !.tries = @+1, !.scope = s, !.option = o, !.actor = "applicant", !.desired=want,
      !.phase = IF verdict="stage" THEN "staged" ELSE "ready",
      !.staged = IF verdict="stage" THEN <<rec>> ELSE <<>>,
      !.out = IF verdict="stage" THEN NoResult ELSE Result(verdict,<<>>,{})]
 /\ last' = Note("submit",g,<<s,o>>)
Submit(s,o,g) == SubmitAt(s,o,g,11)
\* The second authorized caller can arrive while the first row is physically
\* staged. Its database admission reuses the ordinary guards after publication or rollback.
QueueContender ==
 /\ st.lane="requests" /\ st.phase="staged" /\ st.tries=1 /\ ~st.waiting /\ st.option#"false"
 /\ st'=[st EXCEPT !.waiting=TRUE] /\ last'=Note("queue","","")
ResumeContender ==
 /\ st.waiting /\ st.phase="ready" /\ st.tries=1
 /\ LET verdict == Admit(st,st.scope,"ok")
        rec == Record(st.db,st.scope,"true",st.day,"secondCaller",13)
    IN st'=[st EXCEPT !.tries=2, !.waiting=FALSE, !.actor="secondCaller", !.desired=13, !.option="true",
           !.phase=IF verdict="stage" THEN "staged" ELSE "ready",
           !.staged=IF verdict="stage" THEN <<rec>> ELSE <<>>,
           !.out=IF verdict="stage" THEN NoResult ELSE Result(verdict,<<>>,{})]
 /\ last'=Note("resume","ok",<<st.scope,"true">>)
PeekWhileStaged ==
 /\ st.phase="staged"
 /\ st'=[st EXCEPT !.out=Result("read",FilterSeq(st.db,st.scope),{})]
 /\ last'=Note("peek","","")
Outcomes == {"confirmed","lostReply","fault","wrongReadback","cancel","deadline","commitError"}
Published(m) == m \in {"confirmed","lostReply"}
Finish(m) ==
 /\ st.phase="staged" /\ m \in Outcomes
 /\ LET r == st.staged[1]
    IN st' = [st EXCEPT !.phase="ready", !.db = IF Published(m) THEN Append(@,r) ELSE @,
                       !.staged= <<>>, !.out=IF m="confirmed" THEN Result("ok",<<r>>,{})
                                            ELSE Result(m,<<>>,{})]
 /\ last' = Note("finish",m,"")
Probe(k) ==
 /\ st.lane="requests" /\ st.phase="ready" /\ st.tries=2 /\ st.probes<4
 /\ k \in {"create","delete","get"}
 /\ LET take == k="create" /\ st.used<st.applied
        drop == k="delete" /\ st.used>0
    IN st'=[st EXCEPT !.probes=@+1, !.used=IF take THEN @+1 ELSE IF drop THEN @-1 ELSE @,
        !.out=Result(IF k="get" \/ take \/ drop THEN "ok" ELSE "refused",
                     IF k="get" THEN <<st.applied>> ELSE <<>>,{})]
 /\ last'=Note("profile",k,"")

Modes == {"general","byQuota"}
Filters == {"default","account","allLevels","b2bi","pending","terminal","approved","resource","otherService"}
Q(scope, actor, op, filter) == [scope |-> scope, actor |-> actor, op |-> op, filter |-> filter]
Cap(st0,q,before) == [epoch |-> st0.epoch, scope |-> q.scope, actor |-> q.actor, op |-> q.op,
                        filter |-> q.filter, before |-> before]
Reverse(seq) == [i \in 1..Len(seq) |-> seq[Len(seq)+1-i]]
Eligible(r,q,day,before) ==
 /\ r.scope=q.scope /\ r.seq<before
 /\ (q.op="byQuota" \/ (r.day<=day /\ r.day+90>=day))
 /\ q.filter \notin {"resource","approved","otherService"}
 /\ (q.filter # "pending" \/ r.status="PENDING")
 /\ (q.filter # "terminal" \/ r.status="NOT_APPROVED")
PageResult(st0,q,size,supplied,token,auth) ==
 LET cut == IF supplied THEN token.before ELSE 10
     hay == SelectSeq(Reverse(st0.db), LAMBDA x : Eligible(x,q,st0.day,cut))
     n == IF Len(hay)>size THEN size ELSE Len(hay)
     page == SubSeq(hay,1,n)
 IN CASE ~auth -> Result("denied",<<>>,{})
      [] q.op="byQuota" /\ q.filter="otherService" -> Result("notFound",<<>>,{})
      [] supplied /\ token # Cap(st0,q,cut) -> Result("token",<<>>,{})
      [] OTHER -> Result("ok",page,IF n<Len(hay) THEN {Cap(st0,q,page[n].seq)} ELSE {})
First(s,op,f,size,grant) ==
 /\ st.phase="ready" /\ st.reads<2
 /\ st.lane="history" \/ (st.lane="requests" /\ st.tries>0 /\ st.reads=0 /\ s=st.scope /\ op="byQuota" /\ f="default" /\ size=2 /\ grant="wildcard")
 /\ s \in Scopes /\ op \in Modes /\ f \in Filters /\ size \in {1,2} /\ grant \in Grants
 /\ grant \notin {"wildcard","none"} => (s="AE" /\ f="default" /\ size=1)
 /\ st.reads>0 => (s="AE" /\ f="default" /\ size=1 /\ grant="wildcard")
 /\ LET q == Q(s,"reader",op,f)
    IN st'=[st EXCEPT !.reads=@+1, !.out=PageResult(st,q,size,FALSE,{},Allowed(op,grant))]
 /\ last'=Note("first",<<size,grant>>,Q(s,"reader",op,f))
ChangedQuery(t,d) ==
 Q(IF d="team" THEN "BE" ELSE IF d="region" THEN "AW" ELSE t.scope,
   IF d="actor" THEN "other" ELSE t.actor,
   IF d="op" THEN IF t.op="general" THEN "byQuota" ELSE "general" ELSE t.op,
   IF d="filter" THEN IF t.filter="default" THEN "account" ELSE "default" ELSE t.filter)
Continue(d,size,grant) ==
 /\ st.lane="history" /\ st.phase="ready" /\ st.reads=1 /\ st.out.cursor # {}
 /\ d \in {"same","actor","region","team","op","filter","malformed"} /\ size \in {1,2} /\ grant \in Grants
 /\ grant \notin {"wildcard","none"} => (d="same" /\ size=1)
 /\ LET t == CHOOSE token \in st.out.cursor : TRUE
        token == IF d="malformed" THEN [t EXCEPT !.before = 0] ELSE t
        q == ChangedQuery(t,d)
        auth == Allowed(q.op,grant)
        ans == IF token.before=0 /\ auth THEN Result("token",<<>>,{})
               ELSE PageResult(st,q,size,TRUE,token,auth)
    IN st'=[st EXCEPT !.reads=@+1, !.out=ans]
 /\ last'=Note("continue",<<size,grant,d>>,st.out.cursor)
ReadOne(s,id,grant) ==
 /\ st.phase="ready" /\ st.lane="history" /\ st.reads<2
 /\ s \in Scopes /\ id \in {<<"A",1>>,<<"A",2>>,<<"A",3>>,<<"A",5>>,<<"B",1>>,<<"B",4>>} /\ grant \in Grants
 /\ grant \notin {"wildcard","none"} => (s="AE" /\ id= <<"A",1>>)
 /\ LET r == SelectSeq(st.db,LAMBDA row : row.scope=s /\ row.id=id)
    auth == Allowed("get",grant)
    IN st'=[st EXCEPT !.reads=@+1, !.out= IF ~auth THEN Result("denied",<<>>,{})
                  ELSE IF r= <<>> THEN Result("notFound",<<>>,{}) ELSE Result("ok",r,{})]
 /\ last'=Note("get",grant,<<s,id>>)
Advance ==
 /\ st.phase="ready" /\ st.lane="history" /\ st.day=91 /\ st.reads=1 /\ st.out.cursor # {}
 /\ st'=[st EXCEPT !.day=92] /\ last'=Note("advance","","")
RollClockBack ==
 /\ st.phase="ready" /\ st.lane="history" /\ st.day=91 /\ st.reads=1 /\ st.added /\ st.out.cursor # {}
 /\ st'=[st EXCEPT !.day=90] /\ last'=Note("clockBack","","")
InsertAfterPage ==
 /\ st.phase="ready" /\ st.lane="history" /\ st.reads=1 /\ st.out.cursor # {} /\ ~st.added
 /\ st'=[st EXCEPT !.added=TRUE, !.db=Append(@,Record(st.db,"AE","true",st.day,"later",13))]
 /\ last'=Note("later","","")
Reopen ==
 /\ st.phase="ready" /\ st.epoch=0
 /\ (st.lane="history" /\ st.reads=1 /\ st.out.cursor # {}) \/ (st.lane#"history" /\ st.tries>0)
 /\ st'=[st EXCEPT !.epoch=1] /\ last'=Note("reopen","","")
Next ==
 \/ SubmitAt("AE","false","ok",13) \/ QueueContender \/ ResumeContender \/ PeekWhileStaged \/ Bootstrap \/ (\E s \in Scopes,o \in {"true","false","omitted"},g \in Guards : Submit(s,o,g))
 \/ (\E m \in Outcomes: Finish(m)) \/ (\E k \in {"create","delete","get"} : Probe(k))
 \/ (\E s \in Scopes,op \in Modes,f \in Filters,z \in {1,2},a \in Grants : First(s,op,f,z,a))
 \/ (\E d \in {"same","actor","region","team","op","filter","malformed"},z \in {1,2},a \in Grants : Continue(d,z,a))
 \/ (\E s \in Scopes,id \in {<<"A",1>>,<<"A",2>>,<<"A",3>>,<<"A",5>>,<<"B",1>>,<<"B",4>>},a \in Grants : ReadOne(s,id,a))
 \/ Advance \/ RollClockBack \/ InsertAfterPage \/ Reopen

TypeOK == st.applied=2 /\ st.used \in 0..2 /\ st.tries \in 0..2 /\ st.reads \in 0..2
Admissions == last.event \notin {"submit","resume"} \/
 LET old == last.before
     s == last.arg[1]
     verdict == IF last.mode \in {"denied","otherQuota","explicitDeny"} THEN "denied" ELSE IF last.mode \notin {"ok","sameQuota"} THEN "invalid"
              ELSE IF HasPending(old.db,s) THEN "pending" ELSE IF Len(TeamRows(old.db,s))>=3 THEN "full" ELSE "stage"
 IN /\ st.db=old.db /\ st.used=old.used /\ st.applied=old.applied
    /\ IF verdict="stage" THEN st.phase="staged" /\ st.out=NoResult /\ Len(st.staged)=1
                          ELSE st.phase="ready" /\ st.staged= <<>> /\ st.out=Result(verdict,<<>>,{})
StoredFields == last.event # "finish" \/ ~Published(last.mode) \/
 LET old == last.before
     r == st.db[Len(st.db)]
 IN /\ r.scope=old.scope /\ r.requester=old.actor /\ r.day=old.day /\ r.desired=old.desired /\ ~r.hasCase
    /\ r.status=(IF old.option="false" THEN "NOT_APPROVED" ELSE "PENDING")
    /\ r.seq=Len(TeamRows(old.db,old.scope))+1 /\ r.id= <<Team(old.scope),r.seq>>
CommitFrame == last.event # "finish" \/
 LET old == last.before
 IN /\ st.phase="ready" /\ st.staged= <<>> /\ st.applied=old.applied /\ st.used=old.used
    /\ st.db=(IF last.mode \in {"confirmed","lostReply"} THEN Append(old.db,old.staged[1]) ELSE old.db)
    /\ st.out=(IF last.mode="confirmed" THEN Result("ok",old.staged,{}) ELSE Result(last.mode,<<>> ,{}))
Capacity == last.event # "profile" \/
 LET o == last.before
     take == last.mode="create" /\ o.used<2
     drop == last.mode="delete" /\ o.used>0
 IN /\ st.used=(IF take THEN o.used+1 ELSE IF drop THEN o.used-1 ELSE o.used) /\ st.db=o.db
    /\ st.out=Result(IF last.mode="get" \/ take \/ drop THEN "ok" ELSE "refused",
                     IF last.mode="get" THEN <<2>> ELSE <<>>,{})
Visibility == last.event \notin {"first","continue","get","reopen","advance","clockBack"} \/
              (st.db=last.before.db /\ st.used=last.before.used /\ st.applied=last.before.applied)
Lookup == last.event # "get" \/
 LET rows == SelectSeq(last.before.db,LAMBDA x : x.scope=last.arg[1] /\ x.id=last.arg[2])
 IN st.out = (IF last.mode#"wildcard" THEN Result("denied",<<>>,{}) ELSE IF rows= <<>> THEN Result("notFound",<<>>,{})
               ELSE Result("ok",rows,{}))
PageScope == last.event \notin {"first","continue"} \/
 LET a == last.arg
     continued == last.event="continue"
     t == IF continued THEN CHOOSE x \in a : TRUE ELSE {}
     q == IF continued THEN ChangedQuery(t,last.mode[3]) ELSE a
     auth == last.mode[2]="wildcard" \/ (last.mode[2]="sameQuota" /\ q.op="byQuota")
     prior == last.before
     bad == continued /\ (last.mode[3]="malformed" \/ t.epoch#prior.epoch \/ t.scope#q.scope
                        \/ t.actor#q.actor \/ t.op#q.op \/ t.filter#q.filter)
 IN IF ~auth THEN st.out=Result("denied",<<>>,{})
    ELSE IF q.op="byQuota" /\ q.filter="otherService" THEN st.out=Result("notFound",<<>>,{})
    ELSE IF bad THEN st.out=Result("token",<<>>,{})
    ELSE LET before == IF continued THEN t.before ELSE 10
             matching == SelectSeq(Reverse(prior.db),LAMBDA r :
                  r.scope=q.scope /\ r.seq<before /\ (q.op="byQuota" \/ (r.day<=prior.day /\ r.day+90>=prior.day))
                  /\ q.filter \notin {"resource","otherService","approved"}
                  /\ (q.filter#"pending" \/ r.status="PENDING") /\ (q.filter#"terminal" \/ r.status="NOT_APPROVED"))
             size == last.mode[1]
             n == IF Len(matching)>size THEN size ELSE Len(matching)
             page == SubSeq(matching,1,n)
         IN st.out=Result("ok",page,IF Len(matching)>n THEN {[epoch |-> prior.epoch, scope |-> q.scope, actor |-> q.actor, op |-> q.op,
                             filter |-> q.filter, before |-> page[n].seq]} ELSE {})
PrivateStage == last.event # "peek" \/
 (st.db=last.before.db /\ st.staged=last.before.staged /\ st.out=Result("read",FilterSeq(last.before.db,last.before.scope),{}))
ContenderFrame == last.event # "queue" \/ st=[last.before EXCEPT !.waiting=TRUE]
RestartFrame == last.event # "reopen" \/ st=[last.before EXCEPT !.epoch=1]
=============================================================================
