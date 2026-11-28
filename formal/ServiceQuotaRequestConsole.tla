----------------------- MODULE ServiceQuotaRequestConsole ----------------------
EXTENDS Naturals, Sequences, FiniteSets

Home == [team |-> "A", account |-> "A1", region |-> "east", actor |-> "writer"]
Axes == {"team","account","region","principal","remount","hidden"}
Move(s,a) == CASE a="team" -> [s EXCEPT !.team="B"] [] a="account" -> [s EXCEPT !.account="A2"]
               [] a="region" -> [s EXCEPT !.region="west"] [] a="principal" -> [s EXCEPT !.actor="other"]
               [] OTHER -> s
Key(s) == <<s.team,s.account,s.region>>
Row(s,n,o) == [owner |-> Key(s), id |-> n, amount |-> 8, status |-> IF o="true" THEN "PENDING" ELSE "NOT_APPROVED"]
Owned(db,scope) == SelectSeq(db,LAMBDA row : row.owner=Key(scope))
Pending(db,scope) == \E r \in 1..Len(db): db[r].owner=Key(scope) /\ db[r].status="PENDING"
InitialRows == <<Row(Home,1,"false"),Row(Move(Home,"team"),2,"false")>>
Kinds == {"recover","ordinaryByQuota","general","filtered","resource","next","auto","inspect","capacity"}
Replies == {"ok","empty","denied","failed","malformed","wrongOwner"}
Ticket(scope,mount,k,seq) == [scope |-> scope, mount |-> mount, kind |-> k, seq |-> seq]
\* record denotes attribution to this submission. Viewing an existing ID in history or
\* independently opening its Get detail does not attribute it to the lost submission.
VARIABLE st,last
Note(k,arg) == [kind |-> k,arg |-> arg,before |-> st]
Init ==
 /\ st \in {[mode |-> m, scope |-> Home, mount |-> 0, visible |-> TRUE, db |-> InitialRows, sent |-> 0,
       phase |-> "idle", pending |-> <<>>, armed |-> FALSE, lookup |-> 0, record |-> 0, data |-> <<>>, more |-> FALSE, quota |-> TRUE,
       active |-> <<>>, stale |-> <<>>, reads |-> 0, separateUsed |-> FALSE, oldVisited |-> FALSE] : m \in {"flow","epochs"}}
 /\ last=Note("init","")
\* Only the fixed account-level quota is editable. Actor controls visibility, but two actors
\* with permission in the same team/account/region can see the same committed row.
Submit(o) ==
 /\ st.phase \in {"idle","confirmed","refused"} /\ st.pending= <<>> /\ st.active= <<>> /\ st.quota /\ st.sent<2
 /\ o \in {"true","false"} /\ (st.mode="epochs" => o="false")
 /\ st'=[st EXCEPT !.sent=@+1, !.pending= <<[scope |-> st.scope,mount |-> st.mount,o |-> o,server |-> "open",id |-> 0]>>,
                   !.phase="sending", !.record=0, !.armed=FALSE]
 /\ last'=Note("submit",o)
Settle(result) ==
 /\ Len(st.pending)=1 /\ st.pending[1].server="open" /\ result \in {"save","refuse"}
 /\ LET w == st.pending[1]
        saved == result="save" /\ ~Pending(st.db,w.scope)
        id == IF saved THEN Len(st.db)+1 ELSE 0
    IN st'=[st EXCEPT !.db=IF saved THEN Append(@,Row(w.scope,id,w.o)) ELSE @,
       !.pending[1].server=IF saved THEN "saved" ELSE "refused", !.pending[1].id=id]
 /\ last'=Note("settle",result)
Ack(k) ==
 /\ Len(st.pending)=1 /\ st.pending[1].server#"open"
 /\ k \in {"match","knownRefusal","forbidden","lost","badBody","wrongAmount","wrongStatus","wrongArn"}
 /\ LET w == st.pending[1]
        same == w.scope=st.scope /\ w.mount=st.mount
        success == k="match"
        known == k \in {"knownRefusal","forbidden"}
    IN /\ success => w.server="saved"
       /\ known => w.server="refused"
       /\ st'=IF ~same THEN [st EXCEPT !.pending= <<>>]
              ELSE [st EXCEPT !.pending= <<>>, !.phase=IF success THEN "confirmed" ELSE IF known THEN "refused" ELSE "unknown",
                      !.armed=FALSE, !.record=IF success THEN w.id ELSE 0,
                      !.lookup=IF k="forbidden" THEN 0 ELSE IF success THEN w.id ELSE @,
                      !.quota=IF k="forbidden" THEN FALSE ELSE @, !.more=IF k="forbidden" \/ success THEN FALSE ELSE @,
                      !.data=IF k="forbidden" \/ success THEN <<>> ELSE @]
 /\ last'=Note("ack",k)
\* All recovery inputs are chosen anew from fixed mounted quota identity. Other reads can
\* replace displayed records but do not certify the outcome of the previous write.
Read(k) ==
 /\ st.phase="unknown" /\ st.active= <<>> /\ st.reads<4 /\ k \in Kinds /\ (k="next" => st.more)
 /\ (st.mode="epochs" => k="recover")
 /\ (st.reads>0 /\ st.mode="flow" => k \in {"recover","capacity","ordinaryByQuota","next"})
 /\ st'=[st EXCEPT !.reads=@+1, !.active= <<Ticket(st.scope,st.mount,k,st.reads+1)>>,
      !.lookup=IF k="capacity" THEN 0 ELSE @, !.armed=IF k \in {"recover","capacity"} THEN FALSE ELSE @, !.record=0, !.data= <<>>, !.more=FALSE]
 /\ last'=Note("read",k)
Selected(db,t) == Owned(db,t.scope)
DropData(s) == [s EXCEPT !.lookup=0, !.armed=FALSE, !.record=0, !.data= <<>>, !.more=FALSE, !.quota=FALSE]
Receive(outcome) ==
 /\ Len(st.active)=1 /\ outcome \in Replies
 /\ LET t == st.active[1]
        rows == Selected(st.db,t)
        valid == outcome \in {"ok","empty"}
        history == t.kind \in {"recover","ordinaryByQuota","general","filtered","next","auto","resource"}
        i == IF t.kind="next" THEN Len(rows)-1 ELSE Len(rows)
        observed == IF history /\ outcome="ok" THEN SubSeq(rows,i,i) ELSE <<>>
        recovered == t.kind="recover" /\ valid
    IN /\ outcome="ok" /\ history => (i>0 /\ t.kind#"resource")
       /\ t.kind="next" => Len(rows)>1
       /\ outcome="empty" /\ t.kind="recover" => Len(rows)=0
       /\ st'=IF ~valid THEN [DropData(st) EXCEPT !.active= <<>>]
              ELSE [st EXCEPT !.active= <<>>, !.data=observed, !.more=history /\ outcome="ok" /\ i>1, !.quota=IF t.kind="capacity" THEN TRUE ELSE @,
                                 !.armed=IF recovered THEN TRUE ELSE @]
 /\ last'=Note("receive",outcome)
Separate ==
 /\ st.phase="unknown" /\ st.armed /\ st.active= <<>>
 /\ st'=[st EXCEPT !.phase="idle", !.armed=FALSE, !.separateUsed=TRUE]
 /\ last'=Note("separate","")
\* Scope changes discard the local warning. Old reads or acknowledgements may still arrive.
\* The epochs lane permits one departure (including remounting the same symbols) and
\* one update for the replacement view; it deliberately avoids modeling an HTTP scheduler.
Switch(a) ==
 /\ st.mode="epochs" /\ st.mount=0 /\ a \in Axes /\ (Len(st.active)=1 \/ Len(st.pending)=1)
 /\ st'=[st EXCEPT !.mount=1, !.scope=Move(@,a), !.separateUsed=FALSE, !.visible=a#"hidden", !.phase=IF a="hidden" THEN "off" ELSE "idle", !.armed=FALSE, !.record=0,
      !.quota=a#"hidden", !.lookup=0, !.more=FALSE, !.data= <<>>, !.reads=0, !.stale=st.active, !.active= <<>>]
 /\ last'=Note("switch",a)
Late(k) ==
 /\ Len(st.stale)=1 /\ ~st.oldVisited /\ k \in {"ok","denied"}
 /\ LET t == st.stale[1]
        current == t.scope=st.scope /\ t.mount=st.mount
    IN st'=IF current THEN [st EXCEPT !.oldVisited=TRUE, !.armed=k="ok", !.data=IF k="denied" THEN <<>> ELSE Owned(st.db,t.scope)]
            ELSE [st EXCEPT !.oldVisited=TRUE]
 /\ last'=Note("late",k)
\* Blocked user clicks and automated work cannot start a native request during uncertainty.
TryAgain ==
 /\ st.phase \in {"sending","unknown"} /\ st.mode="flow"
 /\ st'=st /\ last'=Note("try","")
ReturnToView ==
 /\ ~st.visible /\ st.mount=1 /\ st.phase="off"
 /\ st'=[st EXCEPT !.visible=TRUE, !.mount=2, !.phase="idle", !.quota=TRUE]
 /\ last'=Note("return","")
RefreshRestoredQuota ==
 /\ st.phase="refused" /\ ~st.quota /\ st.active= <<>>
 /\ st'=[st EXCEPT !.quota=TRUE] /\ last'=Note("restore","")
Next == (\E o \in {"false","true"}: Submit(o)) \/ (\E r \in {"save","refuse"}:Settle(r))
 \/ (\E k \in {"match","knownRefusal","forbidden","lost","badBody","wrongAmount","wrongStatus","wrongArn"}:Ack(k))
 \/ (\E k \in Kinds: Read(k)) \/ (\E k \in Replies:Receive(k))
 \/ Separate \/ (\E a \in Axes:Switch(a)) \/ (\E k \in {"ok","denied"}:Late(k)) \/ TryAgain \/ RefreshRestoredQuota \/ ReturnToView

SingleIntent == last.kind#"submit" \/
 LET b==last.before IN st.sent=b.sent+1 /\ st.db=b.db /\ st.phase="sending" /\ st.pending[1].scope=b.scope
     /\ st.pending[1].mount=b.mount /\ b.phase \in {"idle","confirmed","refused"} /\ b.quota /\ b.pending= <<>>
NoAutomaticWrite == last.kind \in {"init","submit"} \/ st.sent=last.before.sent
NativeFrame == last.kind#"settle" => st.db=last.before.db
SavedExactlyOnce == last.kind#"settle" \/
 LET b==last.before
     w==b.pending[1]
     yes==last.arg="save" /\ ~Pending(b.db,w.scope)
 IN st.db=(IF yes THEN Append(b.db,Row(w.scope,Len(b.db)+1,w.o)) ELSE b.db) /\
    st.pending[1].id=(IF yes THEN Len(b.db)+1 ELSE 0) /\ st.pending[1].server=(IF yes THEN "saved" ELSE "refused")
Acknowledgement == last.kind#"ack" \/
 LET b==last.before
     w==b.pending[1]
 IN IF w.mount#b.mount \/ w.scope#b.scope THEN st=[b EXCEPT !.pending= <<>>]
    ELSE /\ (last.arg="match" => w.server="saved" /\ w.id>0) /\ (last.arg \in {"knownRefusal","forbidden"} => w.server="refused")
         /\ st.phase=(IF last.arg="match" THEN "confirmed" ELSE IF last.arg \in {"knownRefusal","forbidden"} THEN "refused" ELSE "unknown")
         /\ st.record=(IF last.arg="match" THEN w.id ELSE 0) /\ ~st.armed /\ st.pending= <<>>
         /\ (last.arg="forbidden" => ~st.quota /\ st.data= <<>> /\ ~st.more)
FreshRecovery == last.kind#"read" \/
 LET b==last.before IN /\ st.record=0 /\ st.data= <<>> /\ st.phase="unknown"
                      /\ st.armed=(b.armed /\ last.arg \notin {"recover","capacity"})
ObservedOnly == last.kind#"receive" \/
 LET b==last.before
     t==b.active[1]
     valid==last.arg \in {"ok","empty"}
     permit==valid /\ (t.kind="recover" \/ b.armed)
     rows==Owned(b.db,b.scope)
     position==IF t.kind="next" THEN Len(rows)-1 ELSE Len(rows)
     use==last.arg="ok" /\ t.kind \in {"recover","ordinaryByQuota","general","filtered","resource","auto","next"}
 IN /\ st.phase="unknown" /\ st.record=0 /\ st.armed=permit /\ st.active= <<>>
    /\ st.data=(IF use THEN <<rows[position]>> ELSE <<>>) /\ st.more=(use /\ position>1)
    /\ st.quota=(IF ~valid THEN FALSE ELSE IF t.kind="capacity" THEN TRUE ELSE b.quota)
SeparateIntent == last.kind#"separate" \/
 LET b==last.before IN b.phase="unknown" /\ b.armed /\ b.active= <<>>
                    /\ st=[b EXCEPT !.phase="idle", !.armed=FALSE, !.separateUsed=TRUE]
RetiredRead == last.kind#"late" \/ st=[last.before EXCEPT !.oldVisited=TRUE]
ScopeCleared == last.kind#"switch" \/ (st.phase=(IF last.arg="hidden" THEN "off" ELSE "idle") /\ ~st.armed /\ st.record=0 /\ st.data= <<>> /\ st.mount=1 /\ st.scope=Move(last.before.scope,last.arg))
VisibleMount ==
 /\ (~st.visible => (st.phase="off" /\ ~st.quota /\ ~st.armed /\ st.data= <<>> /\ st.record=0 /\ st.lookup=0))
 /\ (last.kind="switch" => st.visible=(last.arg#"hidden"))
 /\ (last.kind="return" => st=[last.before EXCEPT !.visible=TRUE, !.mount=2, !.phase="idle", !.quota=TRUE])
VisibleLookup ==
 LET b==last.before
 IN CASE last.kind="ack" ->
        st.lookup=(IF b.pending[1].scope#b.scope \/ b.pending[1].mount#b.mount THEN b.lookup
          ELSE IF last.arg="forbidden" THEN 0 ELSE IF last.arg="match" THEN b.pending[1].id ELSE b.lookup)
    [] last.kind="read" -> st.lookup=(IF last.arg="capacity" THEN 0 ELSE b.lookup)
    [] last.kind="receive" -> st.lookup=(IF last.arg \in {"ok","empty"} THEN b.lookup ELSE 0)
    [] last.kind="switch" -> st.lookup=0
    [] OTHER -> st.lookup=b.lookup
FreshCapacity == last.kind#"restore" \/ (last.before.phase="refused" /\ ~last.before.quota /\ st=[last.before EXCEPT !.quota=TRUE])
BlockedRetry == last.kind#"try" \/ st=last.before
===============================================================================
