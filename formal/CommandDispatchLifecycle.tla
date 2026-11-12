---------------- MODULE CommandDispatchLifecycle ----------------
EXTENDS Naturals, TLC
VARIABLES now, calls, retained, forwardedDeadline, originalForwardedDeadline, executions,
 badAdmission, renewed, readExecuted, unknownSettled, duplicateRejected,
 domainAdmitted, lateReadBlocked, badRead
vars == <<now,calls,retained,forwardedDeadline,originalForwardedDeadline,executions,
 badAdmission,renewed,readExecuted,unknownSettled,duplicateRejected,
 domainAdmitted,lateReadBlocked,badRead>>
Init == /\ now \in {0,1} /\ calls = 0 /\ retained \in {"none","exact","foreign"}
 /\ forwardedDeadline = (IF retained = "none" THEN 0 ELSE 2)
 /\ originalForwardedDeadline = forwardedDeadline /\ executions = 0
 /\ badAdmission = FALSE /\ renewed = FALSE /\ readExecuted = FALSE
 /\ unknownSettled = FALSE /\ duplicateRejected = FALSE
 /\ domainAdmitted = FALSE /\ lateReadBlocked = FALSE /\ badRead = FALSE
Tick == /\ now < 3 /\ now' = now+1
 /\ UNCHANGED <<calls,retained,forwardedDeadline,originalForwardedDeadline,executions,
 badAdmission,renewed,readExecuted,unknownSettled,duplicateRejected,
 domainAdmitted,lateReadBlocked,badRead>>
Command(kind,shape,binding,service,domain,deadline,admitted) ==
 LET matched == shape /\ binding /\ service
     duplicate == retained = "exact"
     domainOK == domain
     qualified == now >= 1 /\ now < 3 /\ deadline <= 3
     fresh == qualified /\ now < deadline
     entered == kind = "start" /\ matched /\ domainOK /\
                (duplicate \/ (retained = "none" /\ fresh))
     newExecution == entered /\ ~duplicate /\ admitted
     readable == kind = "read" /\ matched
     readResponse == IF readable /\ retained="none" THEN "unknown" ELSE "snapshot-or-error"
     startResponse == IF entered /\ ~admitted THEN "unknown" ELSE "snapshot-or-error"
 IN /\ calls < 2 /\ calls' = calls+1
 /\ executions' = (executions + (IF newExecution THEN 1 ELSE 0))
 /\ retained' = (IF newExecution THEN "exact" ELSE retained)
 /\ forwardedDeadline' = (IF newExecution THEN deadline ELSE forwardedDeadline)
 /\ originalForwardedDeadline' = (IF newExecution THEN deadline ELSE originalForwardedDeadline)
 /\ badAdmission' = (badAdmission \/ (entered /\ ~(shape /\ binding /\ service /\ domain /\ (duplicate \/ (now>=1 /\ now<3 /\ deadline<=3 /\ now<deadline /\ retained="none")))))
 /\ renewed' = (renewed \/ (duplicate /\ forwardedDeadline' # forwardedDeadline))
 /\ readExecuted' = (readExecuted \/ (kind="read" /\ newExecution))
 /\ domainAdmitted' = (domainAdmitted \/ (entered /\ ~domain))
 /\ unknownSettled' = (unknownSettled \/ ((readable /\ readResponse="settled") \/ (entered /\ ~admitted /\ startResponse="settled")))
 /\ duplicateRejected' = (duplicateRejected \/ (kind="start" /\ matched /\ domain /\ duplicate /\ ~entered))
 /\ lateReadBlocked' = (lateReadBlocked \/ (kind="read" /\ matched /\ now=3 /\ ~readable))
 /\ badRead' = (badRead \/ (readable /\ ~(shape /\ binding /\ service)))
 /\ UNCHANGED now
Next == Tick \/ \E k \in {"start","read"}, sh \in BOOLEAN, b \in BOOLEAN,
 s \in BOOLEAN, d \in BOOLEAN, dl \in 1..4, a \in BOOLEAN: Command(k,sh,b,s,d,dl,a)
TypeOK == /\ now \in 0..3 /\ calls \in 0..2 /\ executions \in 0..2
 /\ retained \in {"none","exact","foreign"} /\ forwardedDeadline \in 0..4
 /\ originalForwardedDeadline \in 0..4
 /\ {badAdmission,renewed,readExecuted,unknownSettled,duplicateRejected,domainAdmitted,lateReadBlocked,badRead} \subseteq BOOLEAN
Admission == ~badAdmission
ReadAdmission == ~badRead
OriginalDeadline == forwardedDeadline = originalForwardedDeadline /\ ~renewed
ReadOnly == ~readExecuted
UnknownNotSettled == ~unknownSettled
DuplicatePreserved == ~duplicateRejected
ExactDomain == ~domainAdmitted
ReadAfterExpiry == ~lateReadBlocked
AtMostOnce == executions <= 1
=================================================================
