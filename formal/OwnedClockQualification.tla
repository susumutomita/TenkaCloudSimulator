---------------- MODULE OwnedClockQualification ----------------
EXTENDS Naturals, TLC
VARIABLES phase, now, owner, proofOwner, proof, provenance, stopped, qualified,
 pendingOwner, retained, badQualification, badResponse, badAdmission,
 badDeadline, badRead, badUnauthorizedRead, forwarded
vars == <<phase,now,owner,proofOwner,proof,provenance,stopped,qualified,
 pendingOwner,retained,badQualification,badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
Init == /\ phase="new" /\ now=0 /\ owner=1 /\ proofOwner=0
 /\ proof=FALSE /\ provenance=FALSE /\ stopped=FALSE /\ qualified=FALSE
 /\ pendingOwner=0 /\ retained=FALSE /\ forwarded=0
 /\ badQualification=FALSE /\ badResponse=FALSE /\ badAdmission=FALSE
 /\ badDeadline=FALSE /\ badRead=FALSE /\ badUnauthorizedRead=FALSE
Launch(owned,ns,fd,platform) ==
 LET admitted == owned /\ ns /\ fd /\ platform
 IN /\ phase="new" /\ phase'="launched"
 /\ proof'=admitted /\ provenance'=(owned /\ ns /\ fd /\ platform)
 /\ proofOwner'=(IF admitted THEN owner ELSE 0)
 /\ UNCHANGED <<now,owner,stopped,qualified,pendingOwner,retained,
 badQualification,badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
BeginSample == /\ phase="launched" /\ proof /\ ~stopped /\ owner=proofOwner
 /\ phase'="pending" /\ pendingOwner'=owner
 /\ UNCHANGED <<now,owner,proofOwner,proof,provenance,stopped,qualified,retained,
 badQualification,badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
Sample(mac,nonce,domain,bounds) ==
 LET current == proof /\ ~stopped /\ owner=proofOwner /\ pendingOwner=owner
     authenticated == mac /\ nonce /\ domain
     accepted == current /\ authenticated /\ bounds /\ now<4
 IN /\ phase="pending" /\ phase'="sampled" /\ qualified'=accepted
 /\ badQualification'=(badQualification \/ (accepted /\ ~(proof /\ provenance /\ ~stopped /\ owner=proofOwner /\ pendingOwner=owner /\ mac /\ nonce /\ domain /\ bounds /\ now<4)))
 /\ UNCHANGED <<now,owner,proofOwner,proof,provenance,stopped,pendingOwner,retained,
 badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
Response(mac,nonce,domain,pathBodyStatus) ==
 LET accepted == proof /\ ~stopped /\ owner=proofOwner /\ mac /\ nonce /\ domain /\ pathBodyStatus
 IN /\ phase \in {"sampled","command"}
 /\ badResponse'=(badResponse \/ (accepted /\ ~(provenance /\ ~stopped /\ owner=proofOwner /\ mac /\ nonce /\ domain /\ pathBodyStatus)))
 /\ UNCHANGED <<phase,now,owner,proofOwner,proof,provenance,stopped,qualified,
 pendingOwner,retained,badQualification,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
Start(version,ticks,domain) ==
 LET current == proof /\ ~stopped /\ owner=proofOwner
     live == qualified /\ now<4
     format == version=2 /\ ticks /\ domain
     absolute == 3
     converted == absolute-1
     admitted == current /\ live /\ format /\ now<3 /\ converted>now
 IN /\ phase="sampled" /\ phase'="command"
 /\ retained'=admitted /\ forwarded'=(IF admitted THEN converted ELSE forwarded)
 /\ badAdmission'=(badAdmission \/ (admitted /\ ~(provenance /\ ~stopped /\ owner=proofOwner /\ qualified /\ now<4 /\ version=2 /\ ticks /\ domain /\ now<3)))
 /\ badDeadline'=(badDeadline \/ (admitted /\ converted>3))
 /\ UNCHANGED <<now,owner,proofOwner,proof,provenance,stopped,qualified,pendingOwner,
 badQualification,badResponse,badRead,badUnauthorizedRead>>
Read ==
 LET current == proof /\ ~stopped /\ owner=proofOwner
     readable == current /\ retained
 IN /\ phase="command"
 /\ badRead'=(badRead \/ (current /\ retained /\ ~readable))
 /\ badUnauthorizedRead'=(badUnauthorizedRead \/ (readable /\ ~(current /\ retained)))
 /\ UNCHANGED <<phase,now,owner,proofOwner,proof,provenance,stopped,qualified,
 pendingOwner,retained,badQualification,badResponse,badAdmission,badDeadline,forwarded>>
Stop == /\ phase#"new" /\ ~stopped /\ stopped'=TRUE /\ qualified'=FALSE
 /\ UNCHANGED <<phase,now,owner,proofOwner,proof,provenance,pendingOwner,retained,
 badQualification,badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
Rotate == /\ phase#"new" /\ owner=1 /\ owner'=2
 /\ UNCHANGED <<phase,now,proofOwner,proof,provenance,stopped,qualified,pendingOwner,retained,
 badQualification,badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
Tick == /\ now<5 /\ now'=now+1
 /\ UNCHANGED <<phase,owner,proofOwner,proof,provenance,stopped,qualified,pendingOwner,retained,
 badQualification,badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead,forwarded>>
Next == Tick \/ Stop \/ Rotate \/ BeginSample \/ Read
 \/ (\E a,b,c,d \in BOOLEAN: Launch(a,b,c,d) \/ Sample(a,b,c,d) \/ Response(a,b,c,d))
 \/ (\E v \in {1,2},t,d \in BOOLEAN: Start(v,t,d))
TypeOK == /\ phase \in {"new","launched","pending","sampled","command"}
 /\ now \in 0..5 /\ owner \in {1,2} /\ proofOwner \in 0..2 /\ pendingOwner \in 0..2
 /\ forwarded \in 0..6
 /\ {proof,provenance,stopped,qualified,retained,badQualification,badResponse,badAdmission,badDeadline,badRead,badUnauthorizedRead} \subseteq BOOLEAN
OwnedProof == proof => provenance
QualifiedOnlyCurrent == ~badQualification
AuthenticatedResponses == ~badResponse
StopInvalidates == stopped => ~qualified
V2Admission == ~badAdmission
ConservativeOriginalDeadline == ~badDeadline
RetainedReadAfterExpiry == ~badRead
ReadRequiresCurrentOwner == ~badUnauthorizedRead
=================================================================
