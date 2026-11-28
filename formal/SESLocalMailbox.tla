--------------------------- MODULE SESLocalMailbox ---------------------------
EXTENDS Naturals, Sequences, FiniteSets

Teams == {"A","B"}
Modes == {"simple","template","missing","outside","noProof","noSender","noTemplate"}
Event(k,t,m,n,op) == [kind |-> k, team |-> t, mode |-> m, id |-> n, operator |-> op]
Begin(t,m) == Event("start",t,m,0,FALSE)
Finish(k) == Event(k,"A","simple",0,FALSE)
Purge(t,id,authorized) == Event("purge",t,"simple",id,authorized)
Peek(k,t,id,authorized) == Event(k,t,"simple",id,authorized)
None == [team |-> "-", id |-> 0, mode |-> "-"]
Candidate(t,n,m) == [team |-> t, id |-> n, mode |-> m]
NextID(t,n) == 2*n + (IF t="A" THEN 1 ELSE 2)
Parts(m) == IF m="template" THEN "rendered-template" ELSE "literal-text"
Envelope(p) == [team |-> p.team, id |-> p.id, mode |-> p.mode]
Content(p) == [team |-> p.team, id |-> p.id, bytes |-> Parts(p.mode)]
Permit(m) == m # "outside" /\ m # "noProof" /\ m # "noSender" /\ m # "noTemplate"
Frame(r,b,n) == [rows |-> r, bodies |-> b, issued |-> n]
ForTeam(r,t) == {v \in r : v.team=t}
At(r,t,id) == {v \in r : v.team=t /\ v.id=id}

Trials == {"normal","abort","lost","capacity","permission","inspection"}
Seed == <<Begin("B","simple"),Finish("commit"),Begin("A","simple"),Finish("commit")>>
Steps(s) == CASE s="normal" -> Seed \o <<Begin("A","template"),Finish("commit"),Finish("restart"),Peek("get","A",3,TRUE),Peek("content","A",3,TRUE)>>
   [] s="abort" -> Seed \o <<Begin("A","template"),Finish("cancel"),Begin("A","missing"),Finish("veto"),Begin("A","missing"),Finish("commit"),Finish("restart"),Peek("content","A",3,TRUE)>>
   [] s="lost" -> Seed \o <<Purge("A",1,TRUE),Begin("A","simple"),Finish("lost"),Finish("restart"),Peek("list","A",0,TRUE),Begin("A","simple"),Finish("commit")>>
   [] s="capacity" -> Seed \o <<Begin("A","missing"),Finish("commit"),Begin("A","simple"),Purge("A",1,FALSE),Purge("A",1,TRUE),Begin("A","template"),Finish("commit"),Peek("content","A",5,TRUE)>>
   [] s="permission" -> Seed \o <<Begin("A","outside"),Begin("A","noProof"),Begin("A","noSender"),Begin("A","noTemplate"),Begin("A","simple"),Finish("commit")>>
   [] OTHER -> Seed \o <<Peek("content","A",2,TRUE),Peek("content","A",1,FALSE),Peek("get","B",2,TRUE),Purge("A",2,TRUE),Purge("A",1,TRUE),Peek("get","A",1,TRUE)>>

VARIABLES trial, step, rows, bodies, issued, staged, last, before, older, answer
vars == <<trial,step,rows,bodies,issued,staged,last,before,older,answer>>
Current == Frame(rows,bodies,issued)
Reply(status,vals) == [status |-> status, values |-> vals]
ReadAnswer(f,a) ==
 IF ~a.operator THEN Reply("denied",{})
 ELSE IF a.kind="list" THEN Reply("listed",ForTeam(f.rows,a.team))
 ELSE IF a.kind="get" THEN LET found==At(f.rows,a.team,a.id) IN Reply(IF found={} THEN "absent" ELSE "receipt",found)
 ELSE LET saved==At(f.bodies,a.team,a.id) IN Reply(IF saved={} THEN "absent" ELSE "bytes",saved)
Init == /\ trial \in Trials /\ step=0 /\ rows={} /\ bodies={} /\ issued=[t \in Teams |-> 0]
        /\ staged=None /\ older=None /\ before=Current /\ last=Finish("init") /\ answer=Reply("init",{})
Advance(a) ==
 LET start == a.kind="start"
     commit == a.kind \in {"commit","lost"} /\ staged # None
     eligible == Permit(a.mode) /\ Cardinality(ForTeam(rows,a.team))<2
     remove == a.kind="purge" /\ a.operator /\ At(rows,a.team,a.id) # {}
     read == a.kind \in {"list","get","content"}
 IN /\ last'=a /\ before'=Current /\ older'=staged
    /\ rows'=IF commit THEN rows \cup {Envelope(staged)}
             ELSE IF remove THEN rows \ At(rows,a.team,a.id) ELSE rows
    /\ bodies'=IF commit /\ staged.mode # "missing" THEN bodies \cup {Content(staged)}
               ELSE IF remove THEN bodies \ At(bodies,a.team,a.id) ELSE bodies
    /\ issued'=IF commit THEN [issued EXCEPT ![staged.team]=@+1] ELSE issued
    /\ staged'=IF start /\ eligible THEN Candidate(a.team,NextID(a.team,issued[a.team]),a.mode)
               ELSE IF a.kind \in {"commit","lost","cancel","veto"} THEN None ELSE staged
    /\ answer'=IF read THEN ReadAnswer(Current,a)
               ELSE IF start THEN Reply(IF eligible THEN "staged" ELSE "refused",{})
               ELSE IF commit THEN IF a.kind="lost" THEN Reply("unknown",{}) ELSE Reply("accepted",{staged.id})
               ELSE IF a.kind="purge" THEN Reply(IF ~a.operator THEN "denied" ELSE IF remove THEN "removed" ELSE "absent",{})
               ELSE Reply(a.kind,{})
Scheduled == /\ step<Len(Steps(trial)) /\ Advance(Steps(trial)[step+1]) /\ step'=step+1 /\ UNCHANGED trial
Probe == /\ \E t \in Teams, n \in 1..5, k \in {"list","get","content"}, right \in BOOLEAN: Advance(Peek(k,t,n,right))
         /\ UNCHANGED <<step,trial>>
Next == Scheduled \/ Probe
Spec == Init /\ [][Next]_vars

TypeOK == /\ trial \in Trials /\ step \in 0..Len(Steps(trial)) /\ issued \in [Teams -> 0..3]
          /\ \A r \in rows : r.team \in Teams /\ r.mode \in Modes /\ r.id \in 1..6
Committed == last.kind \in {"commit","lost"} /\ older # None
Publication == Committed => rows=before.rows \cup {Envelope(older)} /\ issued=[before.issued EXCEPT ![older.team]=@+1]
ContentBinding == bodies={Content(r):r \in {e \in rows:e.mode # "missing"}}
UnpublishedFrame == last.kind \in {"start","cancel","veto","restart","list","get","content"} => Current=before
Admission == last.kind="start" =>
 LET allowed == last.mode \in {"simple","template","missing"} /\ Cardinality(ForTeam(before.rows,last.team))<2
 IN IF allowed THEN staged=Candidate(last.team,NextID(last.team,before.issued[last.team]),last.mode) /\ answer=Reply("staged",{})
               ELSE staged=None /\ answer=Reply("refused",{})
NoFalseAck == /\ (Committed => IF last.kind="lost" THEN answer=Reply("unknown",{}) ELSE answer=Reply("accepted",{older.id}))
              /\ (last.kind \in {"cancel","veto"} => staged=None /\ answer=Reply(last.kind,{}))
Capacity == \A t \in Teams: Cardinality(ForTeam(rows,t))<=2
Deletion == last.kind="purge" =>
 LET owned == {r \in before.rows:r.team=last.team /\ r.id=last.id}
     allowed == last.operator /\ owned # {}
 IN /\ rows=(IF allowed THEN before.rows \ owned ELSE before.rows) /\ issued=before.issued
    /\ answer=Reply(IF ~last.operator THEN "denied" ELSE IF allowed THEN "removed" ELSE "absent",{})
ReadProjection == last.kind \in {"list","get","content"} =>
 LET permitted == last.operator
     records == {v \in before.rows:v.team=last.team /\ (last.kind="list" \/ v.id=last.id)}
     existing == {r \in records:r.mode # "missing"}
     result == IF ~permitted THEN Reply("denied",{})
               ELSE IF last.kind="list" THEN Reply("listed",records)
               ELSE IF last.kind="get" THEN Reply(IF records={} THEN "absent" ELSE "receipt",records)
               ELSE Reply(IF existing={} THEN "absent" ELSE "bytes",{Content(r):r \in existing})
 IN answer=result
=============================================================================
