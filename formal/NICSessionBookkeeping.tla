------------------- MODULE NICSessionBookkeeping -------------------
EXTENDS Naturals, FiniteSets
CONSTANT MaxDepth
IDs == 1..2
Services == {"web", "health"}
MaxFrames == 3
MaxBytes == 4
MaxQueue == 1
VARIABLES d, last, depth
vars == <<d, last, depth>>
Blank == [used |-> FALSE, port |-> 0, service |-> "web", closed |-> FALSE,
          watched |-> FALSE, drained |-> FALSE, reading |-> FALSE,
          queued |-> 0, writing |-> FALSE, effectPossible |-> FALSE,
          quiet |-> FALSE, frames |-> 0, bytes |-> 0]
Empty == [live |-> 0, closed |-> FALSE, watched |-> FALSE,
          drained |-> FALSE, sending |-> 0,
          sessions |-> [k \in IDs |-> Blank]]
Live(k, service) == [Blank EXCEPT !.used = TRUE, !.port = k, !.service = service]
One == [Empty EXCEPT !.live = 1, !.sessions[1] = Live(1, "web")]
Two == [One EXCEPT !.live = 2, !.sessions[2] = Live(2, "health")]
Queued == [One EXCEPT !.sessions[1].frames = 1, !.sessions[1].bytes = 1, !.sessions[1].queued = 1]
Writing == [One EXCEPT !.sessions[1].frames = 1, !.sessions[1].bytes = 1, !.sending = 1,
                       !.sessions[1].writing = TRUE, !.sessions[1].effectPossible = TRUE]
ClosingWrite == [Writing EXCEPT !.sessions[1].closed = TRUE, !.sessions[1].watched = TRUE]
Retired == [One EXCEPT !.live = 0, !.sessions[1].closed = TRUE,
                      !.sessions[1].watched = TRUE, !.sessions[1].drained = TRUE]
ClosingRead == [One EXCEPT !.closed = TRUE, !.watched = TRUE,
                          !.sessions[1].closed = TRUE, !.sessions[1].watched = TRUE,
                          !.sessions[1].reading = TRUE]
Seeds == {Empty, One, Two, Queued, Writing, ClosingWrite, Retired, ClosingRead}
CloseSession(s) == [s EXCEPT !.closed = TRUE, !.queued = 0]
CloseNIC(x) == [x EXCEPT !.closed = TRUE,
  !.sessions = [k \in IDs |-> IF x.sessions[k].used THEN CloseSession(x.sessions[k]) ELSE x.sessions[k]]]
Admitted(x, k) == k \in IDs /\ x.sessions[k].used /\ ~x.sessions[k].closed /\ ~x.closed
\* Budgets belong to one tuple incarnation; reuse after quarantine starts a fresh one.
Chargeable(s, size) == size \in 1..2 /\ s.frames < MaxFrames /\ s.bytes + size <= MaxBytes
Charge(s, size) == [s EXCEPT !.frames = @ + 1, !.bytes = @ + size]
Charged(x, k, size) == IF Chargeable(x.sessions[k], size)
                       THEN [x EXCEPT !.sessions[k] = Charge(@, size)] ELSE x
\* A tuple is reusable only once unused, or drained and past the TCP quarantine.
Free(x, k) == ~x.sessions[k].used \/ (x.sessions[k].drained /\ x.sessions[k].quiet)
Record(event, k, ok, size, success) ==
  /\ last' = [before |-> d, event |-> event, k |-> k, ok |-> ok,
               size |-> size, success |-> success]
  /\ depth' = depth + 1

Open(service) ==
  /\ ~d.closed /\ d.live < 2
  /\ \E k \in IDs:
       /\ Free(d, k)
       /\ d' = [d EXCEPT !.live = @ + 1,
                          !.sessions[k] = Live(k, service)]
       /\ Record("open", k, TRUE, 0, TRUE)

\* Tuple+direction validation is a supplied abstract Boolean, NOT byte parsing.
Receive(k, ok, size) ==
  /\ ~d.closed
  /\ LET target == k \in IDs /\ d.sessions[k].used
         valid == k \in IDs /\ d.sessions[k].used /\ ok
         fits == target /\ Chargeable(d.sessions[k], size)
         charged == IF target THEN Charged(d, k, size) ELSE d
         accepted == fits /\ valid /\ ~d.sessions[k].closed /\ d.sessions[k].queued < MaxQueue
     IN /\ d' = IF ~fits \/ ~valid THEN CloseNIC(charged)
                ELSE IF d.sessions[k].closed THEN charged
                ELSE IF d.sessions[k].queued = MaxQueue THEN CloseNIC(charged)
                ELSE [charged EXCEPT !.sessions[k].queued = @ + 1]
        /\ Record("receive", k, ok, size, accepted)

\* A frame from the trusted peer whose only mismatch is another configured service
\* port: a late retransmission for an earlier incarnation of a reused tuple. It is
\* charged to the current incarnation and dropped, never delivered.
ReceiveStale(k, size) ==
  /\ ~d.closed /\ k \in IDs /\ d.sessions[k].used
  /\ LET fits == Chargeable(d.sessions[k], size)
         charged == Charged(d, k, size)
         delivered == FALSE
     IN /\ d' = IF ~fits THEN CloseNIC(charged) ELSE charged
        /\ Record("receive", k, FALSE, size, delivered)

Send(k, ok, size) ==
  /\ Admitted(d, k)
  /\ LET charged == Charged(d, k, size)
         accepted == Chargeable(d.sessions[k], size) /\ ok /\ d.sending = 0
     IN /\ d' = IF ~Chargeable(d.sessions[k], size) \/ ~ok THEN CloseNIC(charged)
                ELSE IF d.sending # 0 THEN charged
                ELSE [charged EXCEPT !.sending = k, !.sessions[k].writing = TRUE,
                                      !.sessions[k].effectPossible = TRUE]
        /\ Record("send", k, ok, size, accepted)

ReadStart(k) ==
  /\ Admitted(d, k) /\ ~d.sessions[k].reading
  /\ d' = [d EXCEPT !.sessions[k].reading = TRUE]
  /\ Record("read-start", k, TRUE, 0, TRUE)
ReadReturn(k) ==
  /\ d.sessions[k].reading
  /\ LET publish == Admitted(d, k) /\ d.sessions[k].queued > 0 IN
       /\ d' = [d EXCEPT !.sessions[k].reading = FALSE,
                 !.sessions[k].queued = IF publish THEN @ - 1 ELSE @]
       /\ Record("read-return", k, TRUE, 0, publish)

WriteReturn(k, callbackOK) ==
  /\ d.sending = k /\ d.sessions[k].writing
  /\ LET publish == callbackOK /\ Admitted(d, k)
         joined == [d EXCEPT !.sending = 0, !.sessions[k].writing = FALSE]
     IN /\ d' = IF publish THEN joined ELSE CloseNIC(joined)
        /\ Record("write-return", k, callbackOK, 0, publish)
RevokeSession(k) ==
  /\ d.sessions[k].used /\ ~d.sessions[k].closed
  /\ d' = [d EXCEPT !.sessions[k] = CloseSession(@)]
  /\ Record("revoke-session", k, TRUE, 0, FALSE)
RevokeNIC ==
  /\ ~d.closed
  /\ d' = CloseNIC(d)
  /\ Record("revoke-nic", 0, TRUE, 0, FALSE)
WatchSession(k) ==
  /\ d.sessions[k].closed /\ ~d.sessions[k].watched
  /\ d' = [d EXCEPT !.sessions[k].watched = TRUE]
  /\ Record("watch-session", k, TRUE, 0, FALSE)
WatchNIC ==
  /\ d.closed /\ ~d.watched
  /\ d' = [d EXCEPT !.watched = TRUE]
  /\ Record("watch-nic", 0, TRUE, 0, FALSE)
DrainSession(k) ==
  /\ d.sessions[k].closed /\ d.sessions[k].watched /\ ~d.sessions[k].drained
  /\ ~d.sessions[k].reading /\ ~d.sessions[k].writing
  /\ d' = [d EXCEPT !.sessions[k].drained = TRUE, !.live = @ - 1]
  /\ Record("drain-session", k, TRUE, 0, TRUE)
\* The 2*MSL quarantine elapses only after the incarnation has drained.
Quiesce(k) ==
  /\ d.sessions[k].drained /\ ~d.sessions[k].quiet
  /\ d' = [d EXCEPT !.sessions[k].quiet = TRUE]
  /\ Record("quiesce", k, TRUE, 0, FALSE)
DrainNIC ==
  /\ d.closed /\ d.watched /\ ~d.drained /\ d.live = 0
  /\ d' = [d EXCEPT !.drained = TRUE]
  /\ Record("drain-nic", 0, TRUE, 0, TRUE)
\* Copies/read-only discovery do not construct another registry or budget.
Rewrap == /\ d' = d /\ Record("rewrap", 0, TRUE, 0, FALSE)

Init ==
  /\ d \in Seeds
  /\ depth = 0
  /\ last = [before |-> d, event |-> "init", k |-> 0, ok |-> TRUE, size |-> 0, success |-> FALSE]
Next ==
  /\ depth < MaxDepth
  /\ \/ \E service \in Services: Open(service)
     \/ \E k \in 0..2, ok \in BOOLEAN, size \in 0..2: Receive(k, ok, size)
     \/ \E k \in IDs, size \in 0..2: ReceiveStale(k, size)
     \/ \E k \in IDs, ok \in BOOLEAN, size \in 0..2: Send(k, ok, size)
     \/ \E k \in IDs: ReadStart(k) \/ ReadReturn(k) \/ RevokeSession(k) \/ WatchSession(k) \/ DrainSession(k) \/ Quiesce(k)
     \/ \E k \in IDs, ok \in BOOLEAN: WriteReturn(k, ok)
     \/ RevokeNIC \/ WatchNIC \/ DrainNIC \/ Rewrap

TypeOK ==
  /\ d.live \in 0..2
  /\ d.sending \in 0..2 /\ depth \in 0..MaxDepth
  /\ d.closed \in BOOLEAN /\ d.watched \in BOOLEAN /\ d.drained \in BOOLEAN
  /\ \A k \in IDs:
       /\ d.sessions[k].port \in 0..2 /\ d.sessions[k].service \in Services
       /\ d.sessions[k].queued \in 0..MaxQueue
       /\ d.sessions[k].frames \in 0..MaxFrames /\ d.sessions[k].bytes \in 0..MaxBytes
       /\ \A field \in {"used", "closed", "watched", "drained", "reading", "writing", "effectPossible", "quiet"}: d.sessions[k][field] \in BOOLEAN
Reopened(k) == last.event = "open" /\ last.k = k
TupleTombstones ==
  /\ \A k \in IDs: d.sessions[k].used => d.sessions[k].port = k
  /\ \A k \in IDs: d.sessions[k].quiet => d.sessions[k].drained
  /\ \A k \in IDs: last.before.sessions[k].used => d.sessions[k].used
  /\ last.event = "open" /\ last.before.sessions[last.k].used =>
       last.before.sessions[last.k].drained /\ last.before.sessions[last.k].quiet
ImmutableTuples == \A k \in IDs: last.before.sessions[k].used /\ ~Reopened(k) =>
  /\ d.sessions[k].port = last.before.sessions[k].port
  /\ d.sessions[k].service = last.before.sessions[k].service
LiveAccounting == d.live = Cardinality({k \in IDs: d.sessions[k].used /\ ~d.sessions[k].drained})
DrainRequiresJoins ==
  /\ \A k \in IDs: d.sessions[k].drained =>
       d.sessions[k].closed /\ d.sessions[k].watched /\ ~d.sessions[k].reading /\ ~d.sessions[k].writing
  /\ d.drained => d.closed /\ d.watched /\ d.live = 0
SingleDelivery ==
  /\ Cardinality({k \in IDs: d.sessions[k].writing}) <= 1
  /\ \A k \in IDs: d.sessions[k].writing <=> d.sending = k
RevocationRetainsDebt == last.event \in {"revoke-session", "revoke-nic"} =>
  /\ d.live = last.before.live /\ d.sending = last.before.sending
  /\ \A k \in IDs: /\ d.sessions[k].reading = last.before.sessions[k].reading
                        /\ d.sessions[k].writing = last.before.sessions[k].writing
NoClosedAdmission == last.event \in {"open", "send", "read-start"} /\ last.success =>
  ~last.before.closed /\ (last.event = "open" \/ ~last.before.sessions[last.k].closed)
BudgetMonotone == \A k \in IDs: ~Reopened(k) =>
  d.sessions[k].frames >= last.before.sessions[k].frames /\ d.sessions[k].bytes >= last.before.sessions[k].bytes
ChargedAttempts == last.event \in {"send", "receive"} /\ last.k \in IDs /\ last.before.sessions[last.k].used
  /\ Chargeable(last.before.sessions[last.k], last.size) =>
  /\ d.sessions[last.k].frames = last.before.sessions[last.k].frames + 1
  /\ d.sessions[last.k].bytes = last.before.sessions[last.k].bytes + last.size
TupleDirectionGuard == last.event \in {"send", "receive"} /\ last.success => last.ok
IncomingIsolation == last.event = "receive" /\ last.success =>
  /\ last.k \in IDs /\ ~last.before.sessions[last.k].closed
  /\ d.sessions[last.k].queued = last.before.sessions[last.k].queued + 1
  /\ \A k \in IDs \ {last.k}: d.sessions[k] = last.before.sessions[k]
ClosedQueuesEmpty == \A k \in IDs: d.sessions[k].closed => d.sessions[k].queued = 0
LateSuccessRejected == last.event \in {"write-return", "read-return"} /\ last.success => Admitted(last.before, last.k)
RewrapPreservesState == last.event = "rewrap" => d = last.before
EffectNotCleanupProof == \A k \in IDs: last.before.sessions[k].effectPossible /\ ~Reopened(k) => d.sessions[k].effectPossible
=============================================================================
