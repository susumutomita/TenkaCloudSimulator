---------------------- MODULE AzureQueueLifecycle ----------------------
EXTENDS Naturals, FiniteSets
\* Independent finite Queue lifecycle model; see the registry for bounds.
\* Two separately owned databases; one message per owner; three receipt epochs.
\* Atomic SQLite commits are abstracted, not proven. No HTTP/auth/crypto model.
\* Excludes peek output/dequeue counts, payload/size, authentication, real SQL,
\* HTTP and cryptography. Owner noninterference is a frame property, not auth.
\* Mutation plans: omit Receive visibility advance; allow early Receive;
\* mutate BlockedReceive state/receipt; accept stale/zero Update receipt;
\* mutate RejectUpdate; fail to rotate on Update; accept stale Delete;
\* mutate Peek/Fail; clear Restart; modify peer row; ignore expiry guard.
\* Registered controls exercise finite sensitivity; additional ideas above are not claims.
\* WitnessSteps is an ordered, owner-specific outcome trace, not label coverage.
\* A separate witness run checks NotWitness for its expected violation.
CONSTANTS Owners, OwnerA, OwnerB
ASSUME Owners = {OwnerA, OwnerB} /\ OwnerA # OwnerB
VARIABLES db, clock, before, actor, op, seen, supplied, witness
vars == <<db, clock, before, actor, op, seen, supplied, witness>>
Empty == [live |-> FALSE, epoch |-> 0, visible |-> 0, expires |-> 3]
Init == /\ db = [o \in Owners |-> Empty]
        /\ clock = 0 /\ before = db /\ actor \in Owners
        /\ op = "Init" /\ seen = {} /\ supplied = 0 /\ witness = 0
Live(o) == db[o].live /\ clock < db[o].expires
Visible(o) == Live(o) /\ clock >= db[o].visible
Record(o, name, receipt) == /\ before' = db /\ actor' = o
    /\ op' = name /\ supplied' = receipt /\ seen' = seen \cup {name}
Save(o, name, row, receipt) == /\ Record(o,name,receipt)
    /\ db' = [db EXCEPT ![o] = row] /\ UNCHANGED clock
Put(o) == /\ ~db[o].live /\ db[o].epoch = 0 /\ clock < 3
    /\ Save(o,"Put",[Empty EXCEPT !.live = TRUE],0)
Peek(o) == /\ Visible(o) /\ Save(o,"Peek",db[o],0)
Receive(o) == /\ Visible(o) /\ db[o].epoch < 3
    /\ Save(o,"Receive",[db[o] EXCEPT !.epoch = @+1, !.visible = clock+1],0)
BlockedReceive(o) == /\ Live(o) /\ clock < db[o].visible
    /\ Save(o,"BlockedReceive",db[o],0)
Update(o,r) == /\ Live(o) /\ db[o].epoch \in 1..2
    /\ r > 0 /\ r = db[o].epoch
    /\ Save(o,"Update",[db[o] EXCEPT !.epoch = @+1, !.visible = clock],r)
Delete(o,r) == /\ Live(o) /\ r > 0 /\ r = db[o].epoch
    /\ Save(o,"Delete",[db[o] EXCEPT !.live = FALSE],r)
RejectUpdate(o,r) == /\ Live(o) /\ (r = 0 \/ r # db[o].epoch)
    /\ Save(o,"RejectUpdate",db[o],r)
Reject(o,r) == /\ Live(o) /\ r > 0 /\ r # db[o].epoch
    /\ Save(o,"Reject",db[o],r)
Fail(o) == /\ Live(o) /\ Save(o,"Fail",db[o],0)
Restart(o) == Save(o,"Restart",db[o],0)
Expire(o) == /\ db[o].live /\ clock >= db[o].expires
    /\ Save(o,"Expire",[db[o] EXCEPT !.live = FALSE],0)
Tick == /\ clock < 3 /\ clock' = clock+1 /\ before' = db
    /\ op' = "Tick" /\ seen' = seen \cup {"Tick"}
    /\ actor' = OwnerA /\ supplied' = 0 /\ UNCHANGED db
RawNext == Tick \/ (\E o \in Owners: Put(o) \/ Peek(o) \/ Receive(o) \/ BlockedReceive(o)
    \/ Fail(o) \/ Restart(o) \/ Expire(o)
    \/ (\E r \in 0..3: Update(o,r) \/ RejectUpdate(o,r) \/ Delete(o,r) \/ Reject(o,r)))
\* Each entry: actor, operation, supplied receipt, resulting epoch/live/visibility, clock.
WitnessSteps == <<
 <<OwnerA,"Put",0,0,TRUE,0,0>>, <<OwnerB,"Put",0,0,TRUE,0,0>>,
 <<OwnerA,"Peek",0,0,TRUE,0,0>>, <<OwnerA,"Receive",0,1,TRUE,1,0>>,
 <<OwnerA,"BlockedReceive",0,1,TRUE,1,0>>,
 <<OwnerA,"Update",1,2,TRUE,0,0>>,
 <<OwnerA,"RejectUpdate",1,2,TRUE,0,0>>,
 <<OwnerA,"RejectUpdate",0,2,TRUE,0,0>>,
 <<OwnerA,"Reject",1,2,TRUE,0,0>>, <<OwnerA,"Fail",0,2,TRUE,0,0>>,
 <<OwnerA,"Restart",0,2,TRUE,0,0>>, <<OwnerA,"Delete",2,2,FALSE,0,0>>,
 <<OwnerA,"Tick",0,2,FALSE,0,1>>, <<OwnerA,"Tick",0,2,FALSE,0,2>>,
 <<OwnerA,"Tick",0,2,FALSE,0,3>>, <<OwnerB,"Expire",0,0,FALSE,0,3>> >>
Next == /\ RawNext
    /\ witness' = IF witness = 16 THEN 16 ELSE
        IF <<actor',op',supplied',db'[actor'].epoch,db'[actor'].live,
             db'[actor'].visible,clock'>> = WitnessSteps[witness+1]
        THEN witness+1 ELSE 0
Spec == Init /\ [][Next]_vars
TypeOK == /\ clock \in 0..3 /\ actor \in Owners /\ supplied \in 0..3 /\ witness \in 0..16
    /\ db \in [Owners -> [live: BOOLEAN,epoch:0..3,visible:0..3,expires:{3}]]
Isolation == \A o \in Owners \ {actor}: db[o] = before[o]
ReadRollbackDurable == op \in {"Peek","Reject","RejectUpdate","BlockedReceive","Fail","Restart"} => db = before
Rotation == op \in {"Receive","Update"} => db[actor].epoch = before[actor].epoch+1
ReceiptRequired == op = "Delete" => supplied > 0 /\ supplied = before[actor].epoch /\ ~db[actor].live
ExpiryGuard == op \in {"Receive","Peek","Update","Delete"} => clock < before[actor].expires
ExpiryRemoval == op = "Expire" => clock >= before[actor].expires /\ ~db[actor].live
VisibilityAdvance == op = "Receive" =>
    clock >= before[actor].visible /\ db[actor].visible = clock+1
EarlyReceiveBlocked == op = "BlockedReceive" =>
    clock < before[actor].visible /\ db = before
UpdateReceipt == op = "Update" =>
    supplied > 0 /\ supplied = before[actor].epoch /\ db[actor].visible = clock
InvalidUpdate == op = "RejectUpdate" =>
    (supplied = 0 \/ supplied # before[actor].epoch) /\ db = before
NotWitness == witness # 16
=============================================================================
