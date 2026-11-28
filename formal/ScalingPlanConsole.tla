-------------------------- MODULE ScalingPlanConsole --------------------------
EXTENDS Naturals

CONSTANT None, CallLimit
Kinds == {"plan", "capacity", "list", "refresh", "write"}
Reads == Kinds \ {"write"}
Tickets == 1..CallLimit
Calls == [id : Tickets, scope : 0..1, kind : Kinds]

VARIABLE v
Init == v = [scope |-> 0, issued |-> 0, active |-> None, pending |-> {},
             result |-> None, notice |-> None, acks |-> {}, uncertain |-> FALSE,
             invalidPublication |-> FALSE, invalidRetry |-> FALSE,
             hadUncertainty |-> FALSE, recovered |-> FALSE, retried |-> FALSE]

\* Scope is the mounted combination of service route, principal and region.
\* The single affected plan is implicit. List and capacity do not identify it.
\* Switching the mount retires all old visible state; a sent call may still settle.
Switch == /\ v.scope = 0
          /\ v' = [v EXCEPT !.scope = 1, !.active = None, !.notice = None,
                    !.result = None, !.uncertain = FALSE, !.hadUncertainty = FALSE,
                    !.recovered = FALSE, !.retried = FALSE]

MayWrite == ~v.uncertain
Begin(k) ==
  /\ v.active = None /\ v.issued < CallLimit /\ (k = "write" => MayWrite)
  /\ LET q == [id |-> v.issued + 1, scope |-> v.scope, kind |-> k]
     IN v' = [v EXCEPT !.issued = q.id, !.active = q.id, !.pending = @ \cup {q},
               !.result = None,
               !.invalidRetry = @ \/ (k = "write" /\ v.uncertain),
               !.retried = @ \/ (k = "write" /\ v.recovered)]

CancelRead == /\ \E q \in v.pending : q.id = v.active /\ q.kind \in Reads
              /\ v' = [v EXCEPT !.active = None, !.result = None]

Current(q) == v.active = q.id /\ v.scope = q.scope
Publish(q) == Current(q)
Acknowledged(status) == status = "ok"
Recovers(q, status) == q.kind = "refresh" /\ status = "ok"

Deliver(q, status) ==
  /\ q \in v.pending
  /\ LET accept == Publish(q)
         write == q.kind = "write"
         notice == IF accept /\ write /\ Acknowledged(status) THEN q ELSE v.notice
         result == IF accept /\ ~write /\ status = "ok" THEN q ELSE v.result
         active == IF accept THEN None ELSE v.active
         uncertain == IF accept /\ write /\ status = "unknown" THEN TRUE
                      ELSE IF accept /\ Recovers(q, status) THEN FALSE ELSE v.uncertain
     IN v' = [v EXCEPT !.pending = @ \ {q}, !.active = active, !.result = result,
               !.notice = notice, !.uncertain = uncertain,
               !.acks = IF write /\ status = "ok" THEN @ \cup {q.id} ELSE @,
               !.hadUncertainty = @ \/ (accept /\ write /\ status = "unknown"),
               !.recovered = @ \/ (accept /\ v.uncertain /\ q.kind = "refresh" /\ status = "ok"),
               !.invalidPublication = @ \/ (~Current(q) /\
                 <<result, notice, active, uncertain>> # <<v.result, v.notice, v.active, v.uncertain>>)]

Next == \/ Switch \/ CancelRead \/ (\E k \in Kinds : Begin(k))
        \/ (\E q \in v.pending, status \in {"ok","rejected","unknown"} : Deliver(q,status))
        \/ UNCHANGED v

TypeOK == /\ v.scope \in 0..1 /\ v.issued \in 0..CallLimit /\ v.active \in Tickets \cup {None}
          /\ v.pending \subseteq Calls /\ v.result \in Calls \cup {None}
          /\ v.notice \in Calls \cup {None} /\ v.acks \subseteq Tickets /\ v.uncertain \in BOOLEAN
OnlyCurrentReplyPublishes == ~v.invalidPublication
SuccessRequiresAck == IF v.notice = None THEN TRUE ELSE v.notice.id \in v.acks
RetryRequiresNamedRefresh == ~v.invalidRetry /\ ((v.hadUncertainty /\ ~v.recovered) => v.uncertain)
=============================================================================
