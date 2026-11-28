----------------------- MODULE SupportCaseLifecycle -----------------------
EXTENDS Naturals, Sequences
VARIABLES db, minute, last
vars == <<db, minute, last>>
Times == {0,1,59,60,61}
\* One request can reach exactly one independent refusal or fault scenario.
Outcomes == {"ok","denied","foreign","cancel","fault","late invalid"}
Authorized(g) == g \notin {"denied","foreign"}
Commit(g) == g \notin {"cancel","fault","late invalid"}
EmptyView == [visible |-> FALSE, messages |-> 0, file |-> 0]
Mark(op,g,expected,want,ok,answer,reference) ==
 [op |-> op, outcome |-> g, expect |-> expected, want |-> want, ok |-> ok,
  answer |-> answer, reference |-> reference]
Written(op,g,expected,want,ok) == Mark(op,g,expected,want,ok,EmptyView,EmptyView)
Peer == [status |-> "opened", initial |-> 7, staging |-> 8, attachment |-> 9]
Init ==
 /\ db = [status |-> "absent", messages |-> <<>>, expires |-> 0, staged |-> 0, peer |-> Peer]
 /\ minute = 0
 /\ last = Written("start","ok",db,TRUE,TRUE)

\* A positive ordinal denotes an individual synthetic file identity. A saved
\* message contains independent copies of the selected set's ordered files.
Files(n) == [i \in 1..n |-> i]
Body(kind,n) == [text |-> kind, files |-> Files(n)]
Live == db.expires > 0 /\ minute < db.expires
Clean == IF db.expires > 0 /\ minute >= db.expires THEN [db EXCEPT !.staged = 0] ELSE db

Stage(mode,batch,g) ==
 LET new == mode = "new"
     eligible == IF new THEN db.expires = 0 ELSE Live
     valid == eligible /\ db.staged + batch <= 3
     success == Authorized(g) /\ valid /\ Commit(g)
     written == [db EXCEPT !.staged = @ + batch, !.expires = IF new THEN minute + 60 ELSE @]
     expectedOK == g = "ok" /\ db.staged + batch \in 1..3 /\
                   ((new /\ db.expires = 0) \/ (~new /\ db.expires > minute))
     expected == IF expectedOK THEN
                 [db EXCEPT !.staged = db.staged + batch,
                    !.expires = IF db.expires = 0 THEN minute + 60 ELSE db.expires] ELSE db
 \* A second fresh set is outside this one-set system.
 IN /\ (~new \/ db.expires = 0)
    /\ db' = IF success THEN written ELSE db
    /\ last' = Written(mode,g,expected,expectedOK,success)
    /\ UNCHANGED minute

Submit(op,setID,g) ==
 LET creating == op = "create"
     validCase == IF creating THEN db.status = "absent" ELSE db.status = "opened"
     validSet == setID = "none" \/ (setID = "local" /\ Live)
     success == Authorized(g) /\ validCase /\ validSet /\ Commit(g)
     count == IF setID = "none" THEN 0 ELSE db.staged
     text == IF creating THEN "initial" ELSE "reply"
     changed == [Clean EXCEPT !.status = "opened", !.messages = Append(@,Body(text,count))]
     expectOK == g = "ok" /\
                 ((op = "create" /\ db.status = "absent") \/ (op = "reply" /\ db.status = "opened")) /\
                 (setID = "none" \/ (setID = "local" /\ minute < db.expires))
     snapshot == IF setID = "local" THEN db.staged ELSE 0
     history == IF creating THEN <<Body("initial",snapshot)>> ELSE db.messages \o <<Body("reply",snapshot)>>
     expected == IF expectOK THEN [Clean EXCEPT !.status = "opened", !.messages = history] ELSE db
 \* New cases use fresh native IDs. Only the first is represented here.
 IN /\ (~creating \/ db.status = "absent")
    /\ (Len(db.messages) < 2 \/ creating \/ db.status # "opened" \/ g # "ok" \/ ~validSet)
    /\ db' = IF success THEN changed ELSE db
    /\ last' = Written(op,g,expected,expectOK,success)
    /\ UNCHANGED minute

Resolve(g) ==
 LET success == Authorized(g) /\ db.status # "absent" /\ Commit(g)
     updated == [Clean EXCEPT !.status = "resolved"]
     expectOK == g = "ok" /\ db.status \in {"opened","resolved"}
     expected == IF expectOK THEN [Clean EXCEPT !.status = "resolved"] ELSE db
 IN /\ db' = IF success THEN updated ELSE db
    /\ last' = Written("resolve",g,expected,expectOK,success)
    /\ UNCHANGED minute

\* The lookup chooses either communication and its last attached file.
\* The resulting copy remains readable independently of staged data expiry.
Read(op,flag,slot,g) ==
 LET present == db.status # "absent"
     history == Len(db.messages)
     ownFile == IF slot > history THEN 0 ELSE Len(db.messages[slot].files)
     canRead == Authorized(g)
     listing == op \in {"cases","summary"}
     success == canRead /\ (listing \/ (op = "messages" /\ present) \/ (op = "file" /\ ownFile > 0))
     shown == IF listing THEN db.status = "opened" \/ (flag /\ db.status = "resolved") ELSE present
     answer == IF ~success THEN EmptyView ELSE
               [visible |-> shown, messages |-> IF shown /\ op \in {"cases","messages"} THEN history ELSE 0,
                file |-> IF op = "file" THEN db.messages[slot].files[ownFile] ELSE 0]
     expectOK == g = "ok" /\ (op \in {"cases","summary"} \/ (op = "messages" /\ db.status \in {"opened","resolved"}) \/
                               (op = "file" /\ ownFile # 0))
     visible == CASE op \notin {"cases","summary"} -> present [] db.status = "resolved" -> flag [] OTHER -> db.status = "opened"
     expected == IF ~expectOK THEN EmptyView ELSE
                 [visible |-> visible, messages |-> IF visible /\ op \in {"messages","cases"} THEN Len(db.messages) ELSE 0,
                  file |-> IF op = "file" THEN ownFile ELSE 0]
 IN /\ g \in {"ok","denied","foreign"}
    /\ db' = db
    /\ last' = Mark(op,g,db,expectOK,success,answer,expected)
    /\ UNCHANGED minute

Clock(t) ==
 /\ t \in Times /\ t >= minute
 /\ minute' = t /\ UNCHANGED db
 /\ last' = Written("clock","ok",db,TRUE,TRUE)
Restart ==
 /\ db' = db /\ UNCHANGED minute
 /\ last' = Written("restart","ok",db,TRUE,TRUE)
Next ==
 \/ \E mode \in {"new","append"}, n \in {1,2}, g \in Outcomes: Stage(mode,n,g)
 \/ \E op \in {"create","reply"}, s \in {"none","local","missing","foreign"}, g \in Outcomes: Submit(op,s,g)
 \/ \E g \in Outcomes: Resolve(g)
 \/ \E op \in {"cases","summary","messages","file"}:
      \E f \in (IF op \in {"cases","summary"} THEN BOOLEAN ELSE {FALSE}),
         slot \in (IF op = "file" THEN 1..2 ELSE {1}), g \in {"ok","denied","foreign"}: Read(op,f,slot,g)
 \/ \E t \in Times: Clock(t)
 \/ Restart

TypeOK ==
 /\ db.status \in {"absent","opened","resolved"} /\ db.staged \in 0..5 /\ db.expires \in 0..121
 /\ minute \in Times /\ Len(db.messages) \in 0..3
StageContract == last.op \notin {"new","append"} \/ (last.ok = last.want /\ db = last.expect)
SubmissionContract == last.op \notin {"create","reply"} \/ (last.ok = last.want /\ db = last.expect)
ResolutionContract == last.op # "resolve" \/ (last.ok = last.want /\ db = last.expect)
ReadProjection == last.op \notin {"cases","summary","messages","file"} \/
                  (last.ok = last.want /\ last.answer = last.reference /\ db = last.expect)
RestartAndClock == last.op \notin {"clock","restart"} \/ db = last.expect
PeerFrame == db.peer = Peer
=============================================================================
