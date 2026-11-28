---------------------- MODULE KinesisVideoConsole ----------------------
EXTENDS Integers, Sequences, FiniteSets

(* Physical stream context = team, region and stream incarnation. The DB is a
   ghost observation. The UI learns its contents only through an explicit read. *)
Origin == <<0,0,0>>
Sites == {Origin, <<1,0,0>>, <<0,1,0>>, <<0,0,1>>}
None == "none"
BaseTicket == [site |-> Origin, generation |-> 0, serial |-> 0, mode |-> None, after |-> 0]
Upload == <<"pick", "upload", "endpoint">>
Read == <<"earliest", "endpoint", "read">>
Resume == <<"resume", "endpoint", "read">>
Switches == {"team","region","stream","remount"}
Where(ev) == CASE ev="team" -> <<1,0,0>> [] ev="region" -> <<0,1,0>>
                 [] ev="stream" -> <<0,0,1>> [] OTHER -> Origin
Programs == {
   [id |-> "persist", steps |-> Upload \o <<"store","persist">> \o Read \o <<"resumeFirst","endpoint","read">> \o Resume],
   [id |-> "short", steps |-> Upload \o <<"short","zero">> \o Read],
   [id |-> "short-positive", steps |-> Upload \o <<"store","short","positive">> \o Read],
   [id |-> "short-foreign", steps |-> Upload \o <<"short","foreignZero">>],
   [id |-> "late-zero", steps |-> Upload \o <<"short","cancel","lateZero">>],
   [id |-> "short-denied", steps |-> Upload \o <<"short","retentionDenied">>],
   [id |-> "partial", steps |-> Upload \o <<"store","partial">> \o Read],
   [id |-> "lost-applied", steps |-> Upload \o <<"store","lost","pick","upload","earliest","endpoint","readDenied">> \o Read \o Upload \o <<"store","persist">>],
   [id |-> "lost-unapplied", steps |-> Upload \o <<"lost","pick","upload">> \o Read \o Upload \o <<"store","persist">>],
   [id |-> "pre-cancel", steps |-> <<"pick","upload","cancel","lateEndpoint","upload","endpoint","store","persist">>],
   [id |-> "after-cancel", steps |-> Upload \o <<"store","cancel","latePersist">> \o Read],
   [id |-> "canceled-unapplied", steps |-> Upload \o <<"cancel">> \o Read],
   [id |-> "refused", steps |-> Upload \o <<"refused","upload","endpoint","store","persist">>],
   [id |-> "discovery", steps |-> <<"pick","upload","badEndpoint","upload","endpointDenied">>],
   [id |-> "busy", steps |-> <<"pick","upload","upload","endpoint","upload","store","persist">>],
   [id |-> "denied-read", steps |-> Read \o <<"resume","endpoint","readDenied">>],
   [id |-> "canceled-read", steps |-> Read \o <<"resume","endpoint","cancel","earliest","endpoint","lateRead","read">>],
   [id |-> "visible-switch", steps |-> Read \o <<"pick","stream">> \o Read],
   [id |-> "empty", steps |-> Read]
 } \cup {
   [id |-> a, steps |-> <<"earliest","endpoint",a,"earliest","endpoint","lateRead","read">>]: a \in Switches
 } \cup {
   [id |-> "late-upload", steps |-> <<"pick","upload","stream","pick","upload","lateEndpoint","endpoint","store","persist">>],
   [id |-> "lost-switch", steps |-> Upload \o <<"store","lost","region","pick","upload","endpoint","store","persist">>]
 }

VARIABLES program, step, last, before, view
vars == <<program,step,last,before,view>>
Initial(p) == [site |-> Origin, generation |-> 0, serial |-> 0,
 active |-> BaseTicket, old |-> BaseTicket, phase |-> "idle", picked |-> FALSE,
 uncertain |-> FALSE, rows |-> {}, cursor |-> 0, notice |-> None, receipt |-> 0,
 sends |-> 0, commands |-> 0, db |-> [s \in Sites |->
     IF s=Origin /\ p.id \notin {"empty","short","canceled-unapplied"} THEN {1} ELSE {}]]
Init == /\ program \in Programs /\ step=0 /\ last=None /\ view=Initial(program) /\ before=view

Match(ticket,v) == /\ v.phase#"idle" /\ ticket.site=v.site
                   /\ ticket.generation=v.generation /\ ticket.serial=v.active.serial
BusyOK(v) == v.phase="idle"
CanPick(v) == ~v.uncertain /\ BusyOK(v)
NewTicket(v,mode) == [site |-> v.site, generation |-> v.generation, serial |-> v.serial+1,
                      mode |-> mode, after |-> CASE mode="resume" -> v.cursor
                      [] mode="resumeFirst" -> CHOOSE n \in v.rows: \A k \in v.rows:n<=k
                      [] OTHER -> 0]
Start(v,mode) ==
 IF BusyOK(v) /\ (mode#"upload" \/ (v.picked /\ ~v.uncertain)) /\ (mode \notin {"resume","resumeFirst"} \/ v.cursor>0)
 THEN [v EXCEPT !.active=NewTicket(v,mode), !.serial=@+1, !.phase="discovery",
                !.commands=IF mode="upload" THEN @+1 ELSE @, !.rows={}, !.cursor=0,
                !.notice=None, !.receipt=0]
 ELSE v
Dispatch(v,ticket) ==
 IF Match(ticket,v) /\ v.phase="discovery"
 THEN [v EXCEPT !.phase=IF ticket.mode="upload" THEN "put" ELSE "reading",
                !.sends=IF ticket.mode="upload" THEN @+1 ELSE @]
 ELSE v
Uncertain(v) == [v EXCEPT !.phase="idle", !.uncertain=TRUE, !.picked=FALSE, !.notice="unknown", !.receipt=0]
Retire(v) == [v EXCEPT !.old=v.active, !.phase="idle", !.rows={}, !.cursor=0, !.notice="canceled"]
Cancel(v) == IF v.phase \in {"put","checkingZero"} THEN Uncertain(Retire(v)) ELSE Retire(v)
Mount(v,ev) == [v EXCEPT !.old=v.active, !.active=BaseTicket, !.site=Where(ev),
 !.generation=IF ev="remount" THEN @+1 ELSE 0, !.serial=0, !.phase="idle",
 !.picked=FALSE, !.rows={}, !.cursor=0, !.uncertain=FALSE, !.receipt=0, !.notice=None]
Record(v) == [v EXCEPT !.db[v.active.site]=@ \cup {v.sends+1}]
Saved(v,ticket) == IF Match(ticket,v) /\ v.phase="put"
 THEN [v EXCEPT !.phase="idle", !.picked=FALSE, !.notice="persisted", !.receipt=v.sends+1] ELSE v
Short(v) == [v EXCEPT !.phase="checkingZero"]
IsConfirmedZero(ev) == ev="zero"
Confirm(v,ev,ticket) == IF ~(Match(ticket,v) /\ v.phase="checkingZero") THEN v
 ELSE IF IsConfirmedZero(ev)
 THEN [v EXCEPT !.phase="idle", !.picked=FALSE, !.notice="received", !.receipt=0]
 ELSE Uncertain(v)
Observed(v,ticket) == {n \in v.db[ticket.site]: n>ticket.after}
ReadBack(v,ticket) ==
 IF Match(ticket,v) /\ v.phase="reading" THEN
 LET found == Observed(v,ticket) IN
 [v EXCEPT !.phase="idle", !.rows=found, !.cursor=IF found={} THEN 0 ELSE CHOOSE n \in found: \A k \in found:k<=n,
           !.notice="observed", !.receipt=0, !.uncertain=IF ticket.mode="earliest" THEN FALSE ELSE @]
 ELSE v
Refusal(v) == [v EXCEPT !.phase="idle", !.rows={}, !.cursor=0, !.notice="refused", !.receipt=0]

Apply(v,ev) ==
 CASE ev="pick" -> IF CanPick(v) THEN [v EXCEPT !.picked=TRUE] ELSE v
 [] ev \in {"upload","earliest","resume","resumeFirst"} -> Start(v,ev)
 [] ev="endpoint" -> Dispatch(v,v.active)
 [] ev="lateEndpoint" -> Dispatch(v,v.old)
 [] ev="store" -> Record(v)
 [] ev="persist" -> Saved(v,v.active)
 [] ev="latePersist" -> Saved(v,v.old)
 [] ev="short" -> Short(v)
 [] ev \in {"zero","positive","foreignZero","retentionDenied"} -> Confirm(v,ev,v.active)
 [] ev="lateZero" -> Confirm(v,"zero",v.old)
 [] ev \in {"lost","partial"} -> Uncertain(v)
 [] ev \in {"readDenied","refused","badEndpoint","endpointDenied"} -> Refusal(v)
 [] ev="cancel" -> Cancel(v)
 [] ev \in Switches -> Mount(v,ev)
 [] ev="read" -> ReadBack(v,v.active)
 [] ev="lateRead" -> ReadBack(v,v.old)
Next == /\ step<Len(program.steps) /\ step'=step+1 /\ last'=program.steps[step+1] /\ before'=view
        /\ view'=Apply(view,program.steps[step+1]) /\ UNCHANGED program
Spec == Init /\ [][Next]_vars
Event == IF step=0 THEN None ELSE program.steps[step]
TypeOK == /\ program \in Programs /\ step \in 0..Len(program.steps) /\ view.site \in Sites
          /\ view.phase \in {"idle","discovery","put","checkingZero","reading"} /\ view.uncertain \in BOOLEAN /\ view.picked \in BOOLEAN
          /\ view.rows \subseteq 1..3 /\ view.receipt \in 0..3 /\ view.cursor \in 0..3
          /\ \A s \in Sites: view.db[s] \subseteq 1..3
OneCommand == (Event \in {"upload","earliest","resume","resumeFirst"} /\ before.phase#"idle") => view=before
PhysicalSend == view.sends#before.sends =>
  /\ Event="endpoint" /\ before.phase="discovery" /\ before.active.mode="upload"
  /\ before.active.site=before.site /\ view.sends=before.sends+1 /\ view.sends<=view.commands
RetiredReply == Event \in {"lateRead","lateEndpoint","latePersist","lateZero"} => view=before
FreshMount == Event \in Switches => /\ view.rows={} /\ view.cursor=0 /\ ~view.picked
                                  /\ ~view.uncertain /\ view.phase="idle" /\ view.receipt=0
UnknownResult == Event \in {"partial","lost","positive","foreignZero","retentionDenied"} =>
  /\ view.uncertain /\ view.notice="unknown" /\ view.receipt=0 /\ ~view.picked
CancelBoundary == Event="cancel" =>
  /\ view.phase="idle" /\ view.rows={} /\ view.cursor=0
  /\ IF before.phase \in {"put","checkingZero"} THEN view.uncertain /\ ~view.picked /\ view.receipt=0
     ELSE view.uncertain=before.uncertain /\ view.picked=before.picked
ExplicitRecovery == before.uncertain /\ Event \notin Switches =>
  /\ (Event \in {"pick","upload"} => view=before)
  /\ (~view.uncertain => Event="read" /\ before.active.mode="earliest" /\ view.notice="observed" /\ view.receipt=0)
ConfirmedOutcome ==
 /\ (Event="short" => view.phase="checkingZero" /\ view.receipt=0 /\ view.notice# "received")
 /\ (Event="zero" => view.notice="received" /\ view.receipt=0 /\ ~view.uncertain)
 /\ (Event="persist" => view.notice="persisted" /\ view.receipt=before.sends+1 /\ view.receipt \in view.db[view.site])
ReadProjection == Event="read" => /\ view.rows={n \in before.db[before.site]:n>before.active.after}
                                  /\ (view.cursor=0 <=> view.rows={}) /\ (view.rows#{} => view.cursor \in view.rows)
                                  /\ view.notice="observed" /\ view.receipt=0
ClearRead ==
 /\ (Event \in {"readDenied","refused","endpointDenied","badEndpoint"} =>
       view.phase="idle" /\ view.rows={} /\ view.cursor=0 /\ view.notice="refused" /\ view.uncertain=before.uncertain)
 /\ (view.phase="discovery" /\ before.phase="idle" => view.rows={} /\ view.cursor=0)
StoreFrame == IF Event="store" THEN view.db=[before.db EXCEPT ![before.site]=@ \cup {before.sends+1}]
 ELSE view.db=before.db
=============================================================================
