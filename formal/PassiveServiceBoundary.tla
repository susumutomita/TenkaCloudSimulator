---------------- MODULE PassiveServiceBoundary ----------------
EXTENDS Naturals, Integers, FiniteSets
Base == [lip |-> 1, pip |-> 2, lm |-> 1, pm |-> 2, lp |-> 2, pp |-> 0, any |-> TRUE]
Routes == {Base, [Base EXCEPT !.lip=2], [Base EXCEPT !.lip=3],
 [Base EXCEPT !.pip=1], [Base EXCEPT !.pm=1], [Base EXCEPT !.lm=2],
 [Base EXCEPT !.lp=0], [Base EXCEPT !.lp=1], [Base EXCEPT !.any=FALSE],
 [Base EXCEPT !.pp=1], [Base EXCEPT !.lm=0],
 [Base EXCEPT !.pip=3], [Base EXCEPT !.lm=2, !.pm=1]}
ValidRoute(r) == r.lip \in {1,2,3} /\ r.pip \in {1,2,3} /\ r.lip # r.pip
 /\ r.lm \in {1,2} /\ r.pm \in {1,2} /\ r.lm # r.pm /\ r.lp # 0
 /\ ((r.any /\ r.pp=0) \/ (~r.any /\ r.pp # 0))
Binding(r) == r.pip=2 /\ r.pm=2 /\ r.lm=1 /\ r.any
Overlap(r) == r.lip=1 /\ r.pip=2 /\ r.lm=1 /\ r.pm=2 /\ r.lp=1 /\ (r.any \/ r.pp=1)
Inputs ==
 { [mode |-> "deadline", requested |-> d, parent |-> p, present |-> n, canceled |-> c] :
    d \in 0..7, p \in 0..6, n,c \in BOOLEAN } \cup
 { [mode |-> "constructor", route |-> r, mixed |-> m, service |-> v] : r \in Routes, m,v \in BOOLEAN } \cup
 { [mode |-> "receive", route |-> r, engine |-> e, service |-> v, expected |-> k] :
    r \in {-1,0,1}, e,v \in BOOLEAN, k \in {0,1} } \cup
 { [mode |-> "send", route |-> r, expected |-> k, used |-> u, bytes |-> b, persistent |-> p] :
    r \in {-1,0,1}, k \in {0,1}, u \in 0..2, b \in 1..2, p \in BOOLEAN } \cup
 { [mode |-> "error", err |-> e, lifetime |-> l, context |-> c] :
    e \in {"closed","wrapped","distinct"}, l \in {"none","expired","revoked"}, c \in BOOLEAN }
VARIABLES input, done, allowed, expiry, target, accounted, result, transferred
vars == <<input,done,allowed,expiry,target,accounted,result,transferred>>
Init == /\ input \in Inputs /\ done=FALSE /\ allowed=FALSE /\ expiry=0
 /\ target="none" /\ accounted=FALSE /\ result="none" /\ transferred=FALSE
Effective(d,p) == IF p # 0 /\ p<d THEN p ELSE d
Deadline == /\ ~done /\ input.mode="deadline"
 /\ LET i==input
        ok==i.present /\ ~i.canceled /\ i.requested # 0 /\ i.requested>2 /\ i.requested<=5
            /\ Effective(i.requested,i.parent)>2
    IN /\ allowed'=ok /\ expiry'=IF ok THEN Effective(i.requested,i.parent) ELSE 0
 /\ done'=TRUE /\ UNCHANGED <<input,target,accounted,result,transferred>>
Constructor == /\ ~done /\ input.mode="constructor"
 /\ LET i==input
        ok==i.service /\ i.route.any /\ ValidRoute(i.route)
            /\ (~i.mixed \/ (Binding(i.route) /\ ~Overlap(i.route)))
    IN /\ allowed'=ok /\ target'=IF ok THEN "delegate" ELSE "none"
 /\ done'=TRUE /\ UNCHANGED <<input,expiry,accounted,result,transferred>>
Receive == /\ ~done /\ input.mode="receive"
 /\ (LET i==input IN target'=IF i.route=-1 THEN "none"
       ELSE IF i.engine /\ i.route=0 THEN "engineCommit"
       ELSE IF i.service /\ i.route=i.expected THEN "serviceCommit" ELSE "none")
 /\ done'=TRUE /\ UNCHANGED <<input,allowed,expiry,accounted,result,transferred>>
Send == /\ ~done /\ input.mode="send"
 /\ accounted'=TRUE
 /\ (LET i==input IN target'=IF i.used+i.bytes>2 \/ i.route=-1 \/ i.route # i.expected THEN "none"
       ELSE IF i.persistent THEN "queue" ELSE "ownerCommit")
 /\ done'=TRUE /\ UNCHANGED <<input,allowed,expiry,result,transferred>>
Error == /\ ~done /\ input.mode="error"
 /\ (LET i==input IN result'=IF i.err="closed" /\ i.lifetime # "none" THEN i.lifetime
       ELSE IF i.err="closed" /\ i.context THEN "context" ELSE i.err)
 /\ done'=TRUE /\ UNCHANGED <<input,allowed,expiry,target,accounted,transferred>>
Next == Deadline \/ Constructor \/ Receive \/ Send \/ Error
DeadlineOracle == input.present /\ ~input.canceled /\ input.requested \in 3..5
 /\ input.parent \in {0,3,4,5,6}
DeadlineExact == input.mode="deadline" /\ done => allowed=DeadlineOracle
ExpiryExact == input.mode="deadline" /\ allowed =>
 ((expiry=input.requested /\ (input.parent=0 \/ input.parent>=input.requested))
 \/ (expiry=input.parent /\ input.parent # 0 /\ input.parent<input.requested))
\* Independent enumeration of accepted route profiles, not constructor expressions.
ConstructorExact == input.mode="constructor" /\ done =>
 allowed=(input.service /\ (IF input.mixed THEN input.route \in {Base,[Base EXCEPT !.lip=3]}
 ELSE input.route \in {Base,[Base EXCEPT !.lip=3],[Base EXCEPT !.lp=1],
 [Base EXCEPT !.pip=3],[Base EXCEPT !.lm=2, !.pm=1]}))
NoOwnershipTransfer == ~transferred
DelegationExact == input.mode="constructor" /\ done => (target="delegate")=allowed
ReceiveExact == input.mode="receive" /\ done =>
 /\ ((target="engineCommit") <=> (input.engine /\ input.route=0))
 /\ ((target="serviceCommit") <=> (~(input.engine /\ input.route=0) /\ input.service /\ input.route # -1 /\ input.route=input.expected))
AccountRejected == input.mode="send" /\ done => accounted
OutputExact == input.mode="send" /\ done =>
 /\ ((target # "none") <=> (input.used+input.bytes<=2 /\ input.route \in {0,1} /\ input.route=input.expected))
 /\ (target # "none" => (target="queue")=input.persistent)
PreserveFailure == input.mode="error" /\ done /\ input.err # "closed" => result=input.err
ErrorPrecedence == input.mode="error" /\ done /\ input.err="closed" =>
 /\ (input.lifetime # "none" => result=input.lifetime)
 /\ (input.lifetime="none" /\ input.context => result="context")
 /\ (input.lifetime="none" /\ ~input.context => result="closed")
=============================================================================
