--------------------- MODULE GuestCommandLifecycle ---------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Jobs, MaxJobs, MaxPolls
ASSUME Jobs = 1..MaxJobs /\ MaxJobs > 0 /\ MaxPolls > 0
VARIABLES d, last
vars == <<d, last>>

\* This is an observation/ownership abstraction, not a guest process model.
\* mayRun means a start could have been accepted, not that it is still running.
Blank == [phase |-> "unused", mayRun |-> FALSE, cleanup |-> FALSE,
          proof |-> FALSE, abort |-> FALSE, polling |-> FALSE, polls |-> 0]
Initial == [closed |-> FALSE, active |-> 0, issued |-> 0,
            jobs |-> [j \in Jobs |-> Blank]]
Terminal == {"rejected", "exited", "truncated", "uncertain"}
Observed(j) == [phase |-> j.phase, mayRun |-> j.mayRun,
                cleanup |-> j.cleanup, proof |-> j.proof,
                abort |-> j.abort, polls |-> j.polls]
Record(event, target, detail) ==
  last' = [before |-> d, event |-> event, target |-> target, detail |-> detail]
Quarantined(s, k) == [s EXCEPT !.closed = TRUE,
  !.jobs[k].phase = "uncertain", !.jobs[k].cleanup = TRUE]

Start ==
  /\ d.issued < MaxJobs
  /\ ~d.closed /\ d.active = 0
  /\ LET k == d.issued + 1 IN
       /\ d' = [d EXCEPT !.issued = k, !.active = k,
                         !.jobs[k].phase = "starting"]
       /\ Record("start", k, "")

ResolveStart(outcome, correlated) ==
  /\ d.active # 0
  /\ LET k == d.active
         old == d.jobs[k]
         possible == outcome \in {"accepted", "unknown"}
         known == outcome \in {"unsent", "rejected"}
         uncertain == ~known /\ (outcome = "unknown" \/ ~correlated \/ old.abort)
         phase == IF known THEN "rejected" ELSE IF uncertain THEN "uncertain" ELSE "accepted"
     IN /\ old.phase = "starting"
        /\ d' = [d EXCEPT !.closed = @ \/ uncertain \/ (outcome = "unsent"),
                          !.active = IF known THEN 0 ELSE k,
                          !.jobs[k].mayRun = possible,
                          !.jobs[k].phase = phase,
                          !.jobs[k].cleanup = uncertain]
        /\ Record("resolve-start", k, [outcome |-> outcome, correlated |-> correlated])

\* A delayed timer/cancellation for a retired handle cannot target a later job.
Abandon(k) ==
  /\ IF d.active = k
     THEN LET old == d.jobs[d.active] IN
       /\ old.phase \in {"starting", "accepted", "uncertain"}
       /\ d' = IF old.phase = "starting"
                THEN [d EXCEPT !.closed = TRUE, !.jobs[d.active].abort = TRUE]
                ELSE IF old.phase = "accepted" THEN Quarantined(d, d.active) ELSE d
     ELSE d' = d
  /\ Record("abandon", k, "")

CloseIdle ==
  /\ d.active = 0
  /\ d' = [d EXCEPT !.closed = TRUE]
  /\ Record("close-idle", 0, "")

BeginPoll(k) ==
  /\ d.active = k /\ d.jobs[k].phase = "accepted" /\ ~d.closed
  /\ ~d.jobs[k].polling
  /\ IF d.jobs[k].polls < MaxPolls
     THEN d' = [d EXCEPT !.jobs[k].polling = TRUE, !.jobs[k].polls = @ + 1]
     ELSE d' = Quarantined(d, k)
  /\ Record("begin-poll", k, "")

PollResult(k, outcome, correlated) ==
  /\ d.jobs[k].polling
  /\ LET cleared == [d EXCEPT !.jobs[k].polling = FALSE]
         knownExit == correlated /\ outcome \in {"exited", "truncated"}
     IN d' = IF d.jobs[k].phase # "accepted" THEN cleared
              ELSE IF ~correlated \/ outcome = "error" THEN Quarantined(cleared, k)
              ELSE IF knownExit THEN [cleared EXCEPT !.active = 0,
                    !.jobs[k].phase = outcome, !.jobs[k].proof = TRUE,
                    !.jobs[k].mayRun = FALSE]
              ELSE cleared
  /\ Record("poll-result", k, [outcome |-> outcome, correlated |-> correlated])

Init ==
  /\ d = Initial
  /\ last = [before |-> Initial, event |-> "initial", target |-> 0, detail |-> ""]
Next == Start \/ CloseIdle
  \/ (\E outcome \in {"unsent", "rejected", "accepted", "unknown"}, correlated \in BOOLEAN : ResolveStart(outcome, correlated))
  \/ (\E k \in Jobs : Abandon(k) \/ BeginPoll(k))
  \/ (\E k \in Jobs, outcome \in {"pending", "exited", "truncated", "error"}, correlated \in BOOLEAN : PollResult(k, outcome, correlated))

TypeOK ==
  /\ d.closed \in BOOLEAN /\ d.active \in Jobs \cup {0}
  /\ d.issued \in 0..MaxJobs
  /\ \A k \in Jobs :
       /\ d.jobs[k].phase \in {"unused", "starting", "accepted"} \cup Terminal
       /\ d.jobs[k].mayRun \in BOOLEAN /\ d.jobs[k].cleanup \in BOOLEAN
       /\ d.jobs[k].proof \in BOOLEAN /\ d.jobs[k].abort \in BOOLEAN
       /\ d.jobs[k].polling \in BOOLEAN /\ d.jobs[k].polls \in Nat
ExclusiveAdmission ==
  /\ Cardinality({k \in Jobs : d.jobs[k].phase \in {"starting", "accepted", "uncertain"}}) <= 1
  /\ \A k \in Jobs : d.jobs[k].phase \in {"starting", "accepted", "uncertain"} => d.active = k
ExitEvidence == \A k \in Jobs : d.jobs[k].phase \in {"exited", "truncated"} => d.jobs[k].proof
RejectedCannotRun == \A k \in Jobs : d.jobs[k].phase = "rejected" => ~d.jobs[k].mayRun
CleanupRequired == \A k \in Jobs : d.jobs[k].phase = "uncertain" => d.jobs[k].cleanup
CleanupOnlyPossible == \A k \in Jobs : d.jobs[k].cleanup => d.jobs[k].mayRun
QuarantineSticky == last.before.closed => d.closed
NoClosedAdmission == last.event = "start" => ~last.before.closed
PollBound == \A k \in Jobs : d.jobs[k].polls <= MaxPolls
StaleAbandonFrame == last.event = "abandon" /\ last.before.active # last.target => d = last.before
TerminalObservationsSticky == \A k \in Jobs :
  last.before.jobs[k].phase \in Terminal => Observed(d.jobs[k]) = Observed(last.before.jobs[k])
CorrelationGuard ==
  /\ (last.event = "resolve-start" /\ last.detail.outcome = "accepted" /\ ~last.detail.correlated)
      => d.jobs[last.target].phase = "uncertain"
  /\ (last.event = "poll-result" /\ ~last.detail.correlated /\ last.before.jobs[last.target].phase = "accepted")
      => d.jobs[last.target].phase = "uncertain"
StartEvidence ==
  last.event = "resolve-start" /\ d.jobs[last.target].phase = "accepted"
  => last.detail.outcome = "accepted" /\ last.detail.correlated /\ ~last.before.jobs[last.target].abort
UnsentCloses == last.event = "resolve-start" /\ last.detail.outcome = "unsent" => d.closed
StartingAbortDefersCleanup == \A k \in Jobs : d.jobs[k].phase = "starting" => ~d.jobs[k].cleanup
AbandonDoesNotStop ==
  (last.event = "abandon" /\ last.before.active = last.target /\ last.before.jobs[last.target].phase = "accepted")
  => d.jobs[last.target].phase = "uncertain"
\* Event evidence is independent of the branch-assigned proof flag.
TerminalPublicationEvidence == \A k \in Jobs :
  (last.before.jobs[k].phase \notin {"exited", "truncated"}
    /\ d.jobs[k].phase \in {"exited", "truncated"})
  => /\ last.event = "poll-result"
     /\ last.target = k
     /\ last.before.jobs[k].phase = "accepted"
     /\ last.before.jobs[k].polling
     /\ last.detail.correlated
     /\ last.detail.outcome \in {"exited", "truncated"}
     /\ d.jobs[k].phase = last.detail.outcome
=============================================================================
