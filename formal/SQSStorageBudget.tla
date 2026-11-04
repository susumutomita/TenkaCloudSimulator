-------------------------- MODULE SQSStorageBudget --------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Scopes, SeedIDs, ExtraID, MaxRows, MaxBytes
IDs == SeedIDs \cup {ExtraID}
Row(id,s,n,expired) == [id |-> id,scope |-> s,size |-> n,expired |-> expired]
RowSet == [id:IDs,scope:Scopes,size:1..2,expired:BOOLEAN]
SeedRows == [id:SeedIDs,scope:Scopes,size:1..2,expired:BOOLEAN]
VARIABLES rows, before, actor, kind, outcome, requestID, requestBytes,
          chargedRows, chargedBytes
vars == <<rows,before,actor,kind,outcome,requestID,requestBytes,
          chargedRows,chargedBytes>>
IDsOf(rs) == {r.id : r \in rs}
UniqueIDs(rs) == Cardinality(IDsOf(rs))=Cardinality(rs)
RECURSIVE Bytes(_)
Bytes(rs) == IF rs={} THEN 0 ELSE
             LET r == CHOOSE item \in rs : TRUE
             IN r.size + Bytes(rs \ {r})
PurgeOwn(rs,s) == {r \in rs : r.scope#s \/ ~r.expired}
Foreign(rs,s) == {r \in rs : r.scope#s}
LiveOwn(rs,s) == {r \in rs : r.scope=s /\ ~r.expired}
\* Expiry in another queue does not reclaim physical capacity for this caller.
Chargeable(rs,s) == rs
Fits(rs,s) == Cardinality(Chargeable(rs,s))<=MaxRows
              /\ Bytes(Chargeable(rs,s))<=MaxBytes
Record(s,k) == /\ before'=rows /\ actor'=s /\ kind'=k
Init ==
 /\ rows \in SUBSET SeedRows /\ UniqueIDs(rows)
 /\ before=rows /\ actor=CHOOSE s \in Scopes : TRUE
 /\ kind="Init" /\ outcome="Initial"
 /\ requestID=ExtraID /\ requestBytes=1 /\ chargedRows=0 /\ chargedBytes=0
\* Initial states deliberately include pre-existing rows above either cap.
\* New sends cannot increase them unless their own scoped purge makes room.
Send(s,id,n,storageOK) ==
 /\ id \notin IDsOf(rows)
 /\ LET post == PurgeOwn(rows,s)
        candidate == post \cup {Row(id,s,n,FALSE)}
    IN /\ IF ~storageOK
          THEN /\ outcome'="StorageError" /\ rows'=rows
          ELSE IF Fits(candidate,s)
               THEN /\ outcome'="Accepted" /\ rows'=candidate
               ELSE /\ outcome'="QuotaRejected" /\ rows'=rows
       /\ chargedRows'=Cardinality(Chargeable(candidate,s))
       /\ chargedBytes'=Bytes(Chargeable(candidate,s))
 /\ requestID'=id /\ requestBytes'=n
 /\ Record(s,"Send")
Read(s) ==
 /\ rows'=PurgeOwn(rows,s) /\ outcome'="ReadOK"
 /\ chargedRows'=0 /\ chargedBytes'=0
 /\ UNCHANGED <<requestID,requestBytes>>
 /\ Record(s,"Read")
\* An explicit owner-authorized cleanup removes that queue's physical rows.
\* Queue existence/receipt authorization are supplied by SQSLifecycle/native API.
Delete(s) ==
 /\ rows'={r \in rows : r.scope#s} /\ outcome'="DeleteOK"
 /\ chargedRows'=0 /\ chargedBytes'=0
 /\ UNCHANGED <<requestID,requestBytes>>
 /\ Record(s,"Delete")
Next == (\E s \in Scopes : Read(s) \/ Delete(s)
          \/ (\E id \in IDs,n \in 1..2,ok \in BOOLEAN : Send(s,id,n,ok)))
        \/ UNCHANGED vars
TypeOK ==
 /\ rows \subseteq RowSet /\ before \subseteq RowSet
 /\ UniqueIDs(rows) /\ UniqueIDs(before)
 /\ actor \in Scopes /\ requestID \in IDs /\ requestBytes \in 1..2
 /\ chargedRows \in 0..4 /\ chargedBytes \in 0..8
 /\ kind \in {"Init","Send","Read","Delete"}
 /\ outcome \in {"Initial","StorageError","Accepted","QuotaRejected","ReadOK","DeleteOK"}
FailedSendPreservesRows ==
 kind="Send" /\ outcome \in {"StorageError","QuotaRejected"} => rows=before
ForeignRowsPreserved == kind#"Init" => Foreign(rows,actor)=Foreign(before,actor)
LiveRowsPreserved == kind \in {"Send","Read"} => LiveOwn(before,actor) \subseteq rows
\* Independent accounting specification: remove only this scope's expired rows,
\* then charge the complete physical candidate, including expired foreign rows.
ExpectedAdmission ==
 (before \ {r \in before : r.scope=actor /\ r.expired})
 \cup {Row(requestID,actor,requestBytes,FALSE)}
BudgetAccountsPhysical == kind="Send" =>
 chargedRows=Cardinality(ExpectedAdmission) /\ chargedBytes=Bytes(ExpectedAdmission)
PhysicalRowCap == outcome="Accepted" => Cardinality(rows)<=MaxRows
PayloadByteCap == outcome="Accepted" => Bytes(rows)<=MaxBytes
ReadRemovesOwnExpired == kind="Read" => ~\E r \in rows : r.scope=actor /\ r.expired
DeleteScopeExact == kind="Delete" => rows=Foreign(before,actor)
\* This is availability under valid-queue/trusted-scope preconditions, not
\* liveness, scheduling fairness or a promise to accept an unauthorized request.
MaintenanceNotBudgetBlocked ==
 \A s \in Scopes : (ENABLED Read(s)) /\ (ENABLED Delete(s))
=============================================================================
