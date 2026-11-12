---------------- MODULE ParticipantSessionLifecycle ----------------
EXTENDS Naturals, FiniteSets
VARIABLES d, last
vars == <<d,last>>
Blank == [prepared |-> FALSE, pin |-> FALSE, private |-> FALSE, artifact |-> "absent",
 phase |-> "empty", gen |-> 0, closed |-> FALSE,
 credential |-> TRUE, target |-> TRUE, cookie |-> TRUE,
 hello |-> FALSE, live |-> FALSE, token |-> "absent", claims |-> 0,
 handoff |-> "fresh", handoffClaims |-> 0, channel |-> FALSE,
 reading |-> FALSE, resumed |-> FALSE, driver |-> 0,
 drains |-> {}, exit |-> FALSE, joins |-> FALSE, clean |-> TRUE]
Record(event, input) == last' = [before |-> d, event |-> event, input |-> input]
IdentityOK(i) == i.participant /\ i.credential /\ i.role /\ i.target /\ i.origin
Current == d.credential /\ d.target /\ d.cookie
AdmitGuard(i) == IdentityOK(i) /\ Current /\ ~d.closed
ClaimGuard(i) == IdentityOK(i) /\ i.token /\ Current /\ d.token = "fresh"
ReadyGuard == d.hello /\ d.live /\ ~d.closed /\ Current
ResumeGuard == d.phase = "resuming" /\ ~d.closed /\ d.live /\ Current
ReadGuard == Current /\ ~d.closed /\ d.live /\ d.channel
ExitGuard == d.drains = {"process", "input", "output"} /\ d.clean
TerminalGuard == d.exit /\ d.joins /\ d.clean
FreshGuard == d.phase = "terminated" /\ d.exit /\ d.joins /\ d.clean /\ ~d.closed
Inputs == [participant : BOOLEAN, credential : BOOLEAN, role : BOOLEAN, target : BOOLEAN, origin : BOOLEAN, token : BOOLEAN]
Init == /\ d = Blank /\ last = [before |-> Blank, event |-> "init", input |-> CHOOSE i \in Inputs : TRUE]
Handoff(i) ==
 /\ d.handoff = "fresh" /\ AdmitGuard(i)
 /\ d' = [d EXCEPT !.handoff = "used", !.handoffClaims = @ + 1]
 /\ Record("handoff",i)
HandoffReplay ==
 /\ d.handoff = "used" /\ FALSE
 /\ d' = [d EXCEPT !.handoffClaims = @ + 1]
 /\ Record("handoff-replay",last.input)
ExpireHandoff == /\ d.handoff = "fresh"
 /\ d' = [d EXCEPT !.handoff = "expired"] /\ Record("expire-handoff",last.input)
Prepare(pin, private, optin) ==
 /\ ~d.prepared /\ pin /\ private /\ optin
 /\ d' = [d EXCEPT !.prepared = TRUE, !.pin = pin, !.private = private]
 /\ Record("prepare",[pin |-> pin, private |-> private, optin |-> optin])
Package(valid, absent) ==
 /\ d.artifact = "absent" /\ valid /\ absent
 /\ d' = [d EXCEPT !.artifact = "written"]
 /\ Record("package",[valid |-> valid, absent |-> absent])
Start(i) ==
 /\ d.prepared /\ d.phase = "empty" /\ d.handoff = "used" /\ AdmitGuard(i)
 /\ d' = [d EXCEPT !.phase = "starting", !.gen = 1, !.driver = 1, !.live = TRUE]
 /\ Record("start",i)
Hello(matches) ==
 /\ d.phase = "starting" /\ ~d.hello /\ matches
 /\ d' = [d EXCEPT !.hello = TRUE] /\ Record("hello",matches)
Ready ==
 /\ d.phase = "starting" /\ ReadyGuard
 /\ d' = [d EXCEPT !.phase = "active", !.token = "fresh"] /\ Record("ready",last.input)
Claim(i) ==
 /\ d.phase = "active" /\ ClaimGuard(i)
 /\ d' = [d EXCEPT !.token = "used", !.claims = @+1, !.channel = TRUE]
 /\ Record("claim",i)
ExpireToken == /\ d.token = "fresh"
 /\ d' = [d EXCEPT !.token = "expired"] /\ Record("expire-token",last.input)
BeginRead == /\ d.phase = "active" /\ d.channel /\ ~d.reading /\ ReadGuard
 /\ d' = [d EXCEPT !.reading = TRUE] /\ Record("begin-read",last.input)
PublishRead == /\ d.reading /\ ReadGuard
 /\ d' = [d EXCEPT !.reading = FALSE] /\ Record("publish",last.input)
Revoke(field) == /\ field \in {"credential","target","cookie"} /\ d[field]
 /\ d' = [d EXCEPT ![field] = FALSE] /\ Record("revoke",last.input)
BeginResume == /\ d.phase = "active" /\ ~d.resumed /\ Current /\ ~d.closed
 /\ d' = [d EXCEPT !.phase = "resuming", !.resumed = TRUE, !.channel = FALSE]
 /\ Record("begin-resume",last.input)
FinishResume == /\ ResumeGuard
 /\ d' = [d EXCEPT !.phase = "active", !.token = "fresh", !.claims = 0,
                         !.driver = d.driver]
 /\ Record("resume",last.input)
Seal == /\ ~d.closed
 /\ d' = [d EXCEPT !.closed = TRUE, !.channel = FALSE,
       !.phase = IF @ \in {"starting","active","resuming"} THEN "closing" ELSE @]
 /\ Record("seal",last.input)
RequestClose == /\ d.phase \in {"starting","active","resuming"}
 /\ d' = [d EXCEPT !.phase = "closing", !.channel = FALSE]
 /\ Record("request-close",last.input)
DriverClose == /\ d.live /\ d.phase # "empty"
 /\ d' = [d EXCEPT !.live = FALSE] /\ Record("driver-close",last.input)
Drain(which) == /\ d.phase = "closing" /\ which \notin d.drains
 /\ d' = [d EXCEPT !.drains = @ \cup {which}] /\ Record("drain",last.input)
UncertainClose == /\ d.phase = "closing" /\ d.clean /\ ~d.exit
 /\ d' = [d EXCEPT !.clean = FALSE] /\ Record("uncertain",last.input)
GuestExit == /\ d.phase = "closing" /\ ~d.exit /\ ExitGuard
 /\ d' = [d EXCEPT !.exit = TRUE] /\ Record("exit",last.input)
LocalJoin == /\ d.phase = "closing" /\ ~d.joins /\ ~d.live
 /\ d' = [d EXCEPT !.joins = TRUE] /\ Record("join",last.input)
Terminate == /\ d.phase = "closing" /\ TerminalGuard
 /\ d' = [d EXCEPT !.phase = "terminated"] /\ Record("terminate",last.input)
Fresh == /\ d.gen = 1 /\ FreshGuard /\ Current
 /\ d' = [d EXCEPT !.gen = 2, !.phase = "starting", !.driver = 2,
       !.hello = FALSE, !.live = TRUE, !.token = "absent", !.claims = 0,
       !.reading = FALSE, !.resumed = FALSE, !.drains = {}, !.exit = FALSE, !.joins = FALSE]
 /\ Record("fresh",last.input)
Next == (\E pin, private, optin \in BOOLEAN : Prepare(pin,private,optin))
 \/ (\E valid, absent \in BOOLEAN : Package(valid,absent))
 \/ (\E i \in Inputs : Handoff(i) \/ Start(i) \/ Claim(i))
 \/ HandoffReplay \/ ExpireHandoff \/ (\E matches \in BOOLEAN : Hello(matches))
 \/ Ready \/ ExpireToken \/ BeginRead \/ PublishRead
 \/ (\E field \in {"credential","target","cookie"} : Revoke(field))
 \/ BeginResume \/ FinishResume \/ Seal \/ RequestClose \/ DriverClose
 \/ (\E which \in {"process","input","output"} : Drain(which))
 \/ UncertainClose \/ GuestExit \/ LocalJoin \/ Terminate \/ Fresh
TypeOK == /\ d.phase \in {"empty","starting","active","resuming","closing","terminated"}
 /\ d.gen \in 0..2 /\ d.driver \in 0..2 /\ d.claims \in Nat /\ d.handoffClaims \in Nat
 /\ d.token \in {"absent","fresh","used","expired"} /\ d.handoff \in {"fresh","used","expired"}
 /\ d.drains \subseteq {"process","input","output"}
PreparedEvidence == d.prepared => d.pin /\ d.private
PreparationOptIn == last.event = "prepare" => last.input.optin
PackageEvidence == last.event = "package" => last.input.valid /\ last.input.absent
BoundAdmission == last.event \in {"handoff","start","claim"} =>
 last.input.participant /\ last.input.credential /\ last.input.role /\ last.input.target /\ last.input.origin
CurrentAdmission == last.event \in {"handoff","start","claim"} =>
 last.before.credential /\ last.before.target /\ last.before.cookie
ChannelTokenBinding == last.event = "claim" => last.input.token
HandoffExpiry == last.event = "handoff" => last.before.handoff = "fresh"
SingleUse == /\ d.claims <= 1 /\ d.handoffClaims <= 1
 /\ (last.event = "claim" => last.before.token = "fresh")
HelloEvidence == last.event = "hello" => last.input
ReadyEvidence == last.event = "ready" => last.before.hello /\ last.before.live /\ ~last.before.closed
NoPostRevokeOutput == last.event = "publish" => last.before.credential /\ last.before.target /\ last.before.cookie /\ ~last.before.closed /\ last.before.live /\ last.before.channel
ResumeKeepsDriver == last.event = "resume" => d.driver = last.before.driver
NoResurrection == last.event \in {"start","ready","resume","fresh"} => ~last.before.closed
GuestDrainEvidence == last.event = "exit" => last.before.drains = {"process","input","output"} /\ last.before.clean
TerminalEvidence == d.phase = "terminated" => d.exit /\ d.joins /\ d.clean
FreshEvidence == last.event = "fresh" => last.before.phase = "terminated" /\ last.before.exit /\ last.before.joins /\ last.before.clean
RevokeIsNotExit == last.event = "revoke" => d.exit = last.before.exit /\ d.joins = last.before.joins /\ d.phase = last.before.phase
=====================================================================
