----------------------- MODULE CloudFrontConsoleEpoch -----------------------
EXTENDS Naturals, FiniteSets
CONSTANT Bound
VARIABLES scope, epoch, serial, pending, active, activeKind, content, receipt,
          caller, callerScope, nextCaller, uncertain, originalCaller,
          modal, confirmed, steps
vars == <<scope, epoch, serial, pending, active, activeKind, content, receipt,
          caller, callerScope, nextCaller, uncertain, originalCaller,
          modal, confirmed, steps>>
Empty == [scope |-> 0, epoch |-> 0, ticket |-> 0, safe |-> TRUE, ack |-> FALSE]
Observation(r, safe, ack) == [scope |-> r.scope, epoch |-> r.epoch,
                              ticket |-> r.ticket, safe |-> safe, ack |-> ack]
Own(r) == r.ticket = active /\ r.scope = scope /\ r.epoch = epoch
Init == /\ scope = 1 /\ epoch = 0 /\ serial = 0 /\ pending = {}
        /\ active = 0 /\ activeKind = "idle" /\ content = Empty /\ receipt = Empty
        /\ caller = 0 /\ callerScope = 0 /\ nextCaller = 0
        /\ uncertain = FALSE /\ originalCaller = 0
        /\ modal = FALSE /\ confirmed = FALSE /\ steps = 0
Switch == /\ steps < Bound /\ epoch < 2
          /\ scope' = 3 - scope /\ epoch' = epoch + 1
          /\ active' = 0 /\ activeKind' = "idle"
          /\ content' = Empty (* ClearScope *) /\ receipt' = Empty
          /\ caller' = 0 /\ callerScope' = 0 /\ uncertain' = FALSE /\ originalCaller' = 0
          /\ modal' = FALSE /\ confirmed' = FALSE /\ steps' = steps + 1
          /\ UNCHANGED <<serial, pending, nextCaller>>
Read == /\ steps < Bound /\ serial < 3 /\ activeKind # "write"
        /\ LET r == [kind |-> "read", scope |-> scope, epoch |-> epoch,
                      ticket |-> serial + 1, caller |-> 0, confirmed |-> FALSE]
           IN pending' = pending \cup {r}
        /\ serial' = serial + 1 /\ active' = serial + 1 /\ activeKind' = "read"
        /\ content' = Empty /\ steps' = steps + 1
        /\ UNCHANGED <<scope, epoch, receipt, caller, callerScope, nextCaller,
                       uncertain, originalCaller, modal, confirmed>>
Open(replay) == /\ steps < Bound /\ activeKind # "write" /\ ~modal
                /\ (IF replay THEN uncertain ELSE ~uncertain /\ nextCaller < 2)
                /\ caller' = (IF replay THEN caller ELSE nextCaller + 1)
                /\ callerScope' = (IF replay THEN callerScope ELSE scope)
                /\ nextCaller' = (IF replay THEN nextCaller ELSE nextCaller + 1)
                /\ modal' = TRUE /\ confirmed' = FALSE /\ steps' = steps + 1
                /\ UNCHANGED <<scope, epoch, serial, pending, active, activeKind,
                               content, receipt, uncertain, originalCaller>>
Confirm == /\ steps < Bound /\ modal /\ ~confirmed
           /\ confirmed' = TRUE /\ steps' = steps + 1
           /\ UNCHANGED <<scope, epoch, serial, pending, active, activeKind,
                          content, receipt, caller, callerScope, nextCaller,
                          uncertain, originalCaller, modal>>
CancelModal == /\ steps < Bound /\ modal /\ activeKind # "write"
               /\ modal' = FALSE /\ confirmed' = FALSE /\ steps' = steps + 1
               /\ UNCHANGED <<scope, epoch, serial, pending, active, activeKind,
                              content, receipt, caller, callerScope, nextCaller,
                              uncertain, originalCaller>>
Write == /\ steps < Bound /\ serial < 3 /\ activeKind # "write"
         /\ modal /\ confirmed (* AffirmativeConfirmation *) /\ caller # 0 /\ callerScope = scope
         /\ LET r == [kind |-> "write", scope |-> scope, epoch |-> epoch,
                       ticket |-> serial + 1, caller |-> caller, confirmed |-> confirmed]
            IN pending' = pending \cup {r}
         /\ serial' = serial + 1 /\ active' = serial + 1 /\ activeKind' = "write"
         /\ content' = Empty /\ receipt' = Empty /\ steps' = steps + 1
         /\ UNCHANGED <<scope, epoch, caller, callerScope, nextCaller,
                        uncertain, originalCaller, modal, confirmed>>
ReadReply(r, safe) == /\ steps < Bound /\ r \in pending /\ r.kind = "read"
                     /\ content' = (IF Own(r) /\ safe (* ProtectedContent *)
                                     THEN Observation(r, safe, FALSE) ELSE content)
                     /\ active' = (IF r.ticket = active THEN 0 ELSE active)
                     /\ activeKind' = (IF r.ticket = active THEN "idle" ELSE activeKind) (* ReadFinish *)
                     /\ pending' = pending \ {r} /\ steps' = steps + 1
                     /\ UNCHANGED <<scope, epoch, serial, receipt, caller, callerScope,
                                    nextCaller, uncertain, originalCaller, modal, confirmed>>
WriteReply(r, result) == /\ steps < Bound /\ r \in pending /\ r.kind = "write"
                        /\ receipt' = (IF Own(r) /\ result = "ok" (* Acknowledged *)
                                        THEN Observation(r, TRUE, result = "ok") ELSE receipt)
                        /\ uncertain' = (IF Own(r) THEN result = "unknown" ELSE uncertain)
                        /\ originalCaller' = (IF Own(r) /\ result = "unknown" THEN r.caller ELSE originalCaller)
                        /\ modal' = (IF Own(r) THEN FALSE ELSE modal)
                        /\ confirmed' = (IF Own(r) THEN FALSE ELSE confirmed)
                        /\ active' = (IF r.ticket = active THEN 0 ELSE active)
                        /\ activeKind' = (IF r.ticket = active THEN "idle" ELSE activeKind)
                        /\ pending' = pending \ {r} /\ steps' = steps + 1
                        /\ UNCHANGED <<scope, epoch, serial, content, caller, callerScope, nextCaller>>
Next == Switch \/ Read \/ Open(FALSE) \/ Open(TRUE) \/ Confirm \/ CancelModal \/ Write
        \/ (\E r \in pending, safe \in BOOLEAN : ReadReply(r, safe))
        \/ (\E r \in pending, result \in {"ok", "refused", "unknown"} : WriteReply(r, result))
Spec == Init /\ [][Next]_vars
TypeOK == /\ pending \subseteq [kind : {"read", "write"}, scope : 1..2, epoch : 0..2,
                              ticket : 1..3, caller : 0..2, confirmed : BOOLEAN]
          /\ content \in [scope : 0..2, epoch : 0..2, ticket : 0..3, safe : BOOLEAN, ack : BOOLEAN]
          /\ receipt \in [scope : 0..2, epoch : 0..2, ticket : 0..3, safe : BOOLEAN, ack : BOOLEAN]
          /\ scope \in 1..2 /\ epoch \in 0..2 /\ serial \in 0..3
          /\ active \in 0..3 /\ activeKind \in {"idle", "read", "write"}
          /\ caller \in 0..2 /\ callerScope \in 0..2 /\ nextCaller \in 0..2
          /\ uncertain \in BOOLEAN /\ originalCaller \in 0..2
          /\ modal \in BOOLEAN /\ confirmed \in BOOLEAN /\ steps \in 0..Bound
CurrentContent == content.ticket = 0 \/ (content.scope = scope /\ content.epoch = epoch)
CurrentReceipt == receipt.ticket = 0 \/ (receipt.scope = scope /\ receipt.epoch = epoch)
ProtectedDownload == content.ticket = 0 \/ content.safe
AcknowledgedReceipt == receipt.ticket = 0 \/ receipt.ack
StableUnknownCaller == ~uncertain \/ (caller = originalCaller /\ callerScope = scope)
ExplicitWriteConfirmation == \A r \in pending : r.kind = "write" => r.confirmed
OneCurrentWrite == Cardinality({r \in pending : r.kind = "write" /\ r.scope = scope /\ r.epoch = epoch}) <= 1
=============================================================================
