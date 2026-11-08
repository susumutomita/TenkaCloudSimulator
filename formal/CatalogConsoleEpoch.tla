------------------------- MODULE CatalogConsoleEpoch -------------------------
(* Generic catalog console view: replies are applied only to the current     *)
(* service/epoch while mounted, one native write is in flight at a time, a   *)
(* failed write keeps its error visible, and only an acknowledged write      *)
(* reloads the list. Native request results themselves are not modeled.     *)
EXTENDS Naturals, FiniteSets
CONSTANT MaxEpoch
Services == {"svc-a", "svc-b"}
Requests == [service : Services, epoch : 0..MaxEpoch]
Actions == {"Init", "Read", "Deliver", "Switch", "Begin", "Ack", "Fail", "Unmount"}
VARIABLES service, epoch, mounted, pending, accepted, inflight, step, error, reloads
vars == <<service, epoch, mounted, pending, accepted, inflight, step, error, reloads>>

ReplyEpochOK(r) == r.epoch = epoch /\ r.service = service
MountedOK == mounted
WriterIdle == inflight = 0
ReloadAfterFailure == FALSE
Current == [service |-> service, epoch |-> epoch]

Init == /\ service = "svc-a" /\ epoch = 0 /\ mounted = TRUE /\ pending = {} /\ accepted = {}
        /\ inflight = 0 /\ step = "Init" /\ error = FALSE /\ reloads = {}
IssueRead == /\ mounted /\ pending' = pending \cup {Current} /\ error' = FALSE /\ step' = "Read"
             /\ UNCHANGED <<service, epoch, mounted, accepted, inflight, reloads>>
Deliver == \E r \in pending :
             /\ MountedOK /\ ReplyEpochOK(r)
             /\ pending' = pending \ {r} /\ step' = "Deliver"
             /\ accepted' = accepted \cup {[request |-> r, mounted |-> mounted, service |-> service, epoch |-> epoch]}
             /\ UNCHANGED <<service, epoch, mounted, inflight, error, reloads>>
Switch == /\ mounted /\ WriterIdle /\ epoch < MaxEpoch
          /\ service' \in Services \ {service} /\ epoch' = epoch + 1 /\ step' = "Switch"
          /\ UNCHANGED <<mounted, pending, accepted, inflight, error, reloads>>
BeginWrite == /\ mounted /\ WriterIdle /\ epoch < MaxEpoch
              /\ inflight' = inflight + 1 /\ epoch' = epoch + 1 /\ error' = FALSE /\ step' = "Begin"
              /\ UNCHANGED <<service, mounted, pending, accepted, reloads>>
\* The reload is issued synchronously with the acknowledgement in the view.
WriteAck == /\ inflight > 0 /\ inflight' = inflight - 1 /\ step' = "Ack"
            /\ pending' = IF mounted THEN pending \cup {Current} ELSE pending
            /\ reloads' = IF mounted THEN reloads \cup {"Ack"} ELSE reloads
            /\ UNCHANGED <<service, epoch, mounted, accepted, error>>
WriteFail == /\ inflight > 0 /\ inflight' = inflight - 1 /\ step' = "Fail"
             /\ error' = ~ReloadAfterFailure
             /\ pending' = IF ReloadAfterFailure /\ mounted THEN pending \cup {Current} ELSE pending
             /\ reloads' = IF ReloadAfterFailure /\ mounted THEN reloads \cup {"Fail"} ELSE reloads
             /\ UNCHANGED <<service, epoch, mounted, accepted>>
Unmount == /\ mounted /\ mounted' = FALSE /\ step' = "Unmount"
           /\ epoch' = IF epoch < MaxEpoch THEN epoch + 1 ELSE epoch
           /\ UNCHANGED <<service, pending, accepted, inflight, error, reloads>>
Next == IssueRead \/ Deliver \/ Switch \/ BeginWrite \/ WriteAck \/ WriteFail \/ Unmount \/ UNCHANGED vars

TypeOK == /\ service \in Services /\ epoch \in 0..MaxEpoch /\ mounted \in BOOLEAN
          /\ pending \subseteq Requests /\ inflight \in 0..2 /\ step \in Actions
          /\ error \in BOOLEAN /\ reloads \subseteq {"Ack", "Fail"}
OnlyCurrentReplies == \A a \in accepted : a.request.service = a.service /\ a.request.epoch = a.epoch
NoUnmountedAcceptance == \A a \in accepted : a.mounted
SingleWriter == inflight <= 1
ReloadOnlyAfterAck == reloads \subseteq {"Ack"}
FailureVisible == step = "Fail" => error
=============================================================================
