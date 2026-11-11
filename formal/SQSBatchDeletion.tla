------------------------ MODULE SQSBatchDeletion ------------------------
EXTENDS Naturals, Sequences, FiniteSets
\* One bounded request and one transaction. Receipt tuples are opaque identity
\* witnesses, not a parser. Queue replacement is an initial snapshot choice;
\* the transaction uses that current queue, not a cached HTTP incarnation.
CONSTANT None
Rows == 1..7
Target == <<"database-a", "region-a", "queue-a">>
Scope(r) == CASE r <= 3 -> Target
                [] r \in {4,7} -> <<"database-a", "region-a", "queue-b">>
                [] r = 5 -> <<"database-a", "region-b", "queue-a">>
                [] OTHER -> <<"database-b", "region-a", "queue-a">>
Handle(r,g,d) == <<Scope(r),g,r,d>>
Candidates == {Handle(1,1,2), Handle(2,1,2), Handle(3,1,2),
               Handle(1,1,1), Handle(1,2,2), Handle(4,1,2),
               Handle(5,1,2), Handle(6,1,2), Handle(1,1,3)}
Entries(xs) == [i \in 1..Len(xs) |-> [id |-> i, receipt |-> xs[i]]]
ShortRequests == {Entries(xs) : xs \in UNION {[1..n -> Candidates] : n \in 0..2}}
LongRequests == {Entries(<<Handle(2,1,2), Handle(1,1,3), Handle(1,1,2)>>),
                 Entries(<<Handle(1,1,3), Handle(1,1,2), Handle(1,1,1)>>),
                 Entries([i \in 1..10 |-> Handle(1,1,2)]),
                 Entries([i \in 1..11 |-> Handle(1,1,2)])}
Requests == ShortRequests \cup LongRequests
Pair == Entries(<<Handle(1,1,2), Handle(2,1,2)>>)
Malformed == { [Pair EXCEPT ![1].id = 0], [Pair EXCEPT ![2].id = 0],
               [Pair EXCEPT ![2].id = 1], [Pair EXCEPT ![1].receipt = None],
               [Pair EXCEPT ![2].receipt = None] }
Seeds == {"full", "missing-a", "missing-b", "empty", "recreated", "missing-queue", "duplicate"}
Policies == {"delete", "batch-only", "peer-only", "implicit-deny", "explicit-deny"}
Case(req,seed,policy,shape,url,writeOK,commitOK) ==
 [request |-> req, seed |-> seed, policy |-> policy, shape |-> shape,
  url |-> url, writeOK |-> writeOK, commitOK |-> commitOK, fault |-> "none"]
\* Exhaust short receipt orders for each seed. Other axes use explicit witnesses
\* rather than claiming their full Cartesian product or all ten-entry batches.
Cases == {Case(req,seed,"delete",TRUE,TRUE,TRUE,TRUE) : req \in Requests, seed \in Seeds}
 \cup {Case(req,"full",policy,shape,url,TRUE,TRUE) :
         req \in Malformed \cup {Pair}, policy \in Policies, shape \in BOOLEAN, url \in BOOLEAN}
 \cup {Case(req,seed,"delete",TRUE,TRUE,writeOK,commitOK) :
         req \in {Pair, Entries(<<Handle(1,1,2),Handle(1,1,3)>>),
                        Entries(<<Handle(1,1,3)>>)},
         seed \in {"full", "recreated", "duplicate"},
         writeOK \in BOOLEAN, commitOK \in BOOLEAN}
FaultCases == {[Case(req,"full","delete",TRUE,TRUE,TRUE,TRUE) EXCEPT !.fault = f] :
 req \in {Pair,Entries(<<Handle(1,1,2)>>),Entries(<<Handle(1,1,3)>>)},
 f \in {"ignored-purge","queue-drift","reinsert-id","rebind-receipt","late-expiry"}}
Generation(seed) == IF seed = "recreated" THEN 2 ELSE 1
Snapshot(seed) == [r \in Rows |->
 IF r <= 3 /\ (seed = "missing-queue" \/ (r <= 2 /\ seed = "empty")
      \/ (r = 1 /\ seed = "missing-a") \/ (r = 2 /\ seed = "missing-b"))
 THEN None ELSE
 [scope |-> Scope(r), incarnation |-> IF r <= 3 THEN Generation(seed) ELSE 1,
  receipt |-> IF seed = "duplicate" /\ r = 2 THEN Handle(1,1,2)
              ELSE Handle(r,IF r <= 3 THEN Generation(seed) ELSE 1,2),
  expired |-> r \in {3,7}]]
VARIABLES input, before, db, work, phase, cursor, selected, deleteCursor,
          good, bad, status, response, failure, error, iamCalled,
          iamAction, iamResource, transactionOpened, queueBefore, queueDB, queueWork
vars == <<input,before,queueBefore,db,work,phase,cursor,selected,deleteCursor,
          good,bad,status,response,failure,error,iamCalled,
          iamAction,iamResource,transactionOpened,queueDB,queueWork>>
Stable == <<input,before,queueBefore>>
Request == input.request
Indices == 1..Len(Request)
MinimumCount == Len(Request) >= 1
MaximumCount == Len(Request) <= 10
ShapeValid == input.shape
URLValid == input.url
IDsValid == \A i \in Indices : Request[i].id \in 1..11
IDsDistinct == \A i,j \in Indices : i # j => Request[i].id # Request[j].id
ReceiptsValid == \A i \in Indices : Request[i].receipt # None
Valid == MinimumCount /\ MaximumCount /\ ShapeValid /\ URLValid
         /\ IDsValid /\ IDsDistinct /\ ReceiptsValid
Allowed == input.policy = "delete"
ParentPresent == input.seed # "missing-queue"
Scoped(row) == row.scope = Target
Current(row,entry) == row.receipt = entry.receipt
Matches(entry) == {r \in Rows : work[r] # None /\ Scoped(work[r])
                  /\ ~work[r].expired /\ Current(work[r],entry)}
FailureEntry(id) == [Id |-> id, Code |-> "ReceiptHandleIsInvalid", SenderFault |-> TRUE]
\* Independent initial/final oracle: no Valid, Matches, selected, good, bad,
\* staging state, or mutable helper is used to compute the expected partition.
RawValid == /\ Len(Request) \in 1..10 /\ input.shape /\ input.url
            /\ \A i \in Indices : Request[i].id \in 1..11 /\ Request[i].receipt # None
            /\ \A i,j \in Indices : i # j => Request[i].id # Request[j].id
OracleMatches(i) == {r \in Rows : before[r] # None /\ before[r].scope = Target
                    /\ ~before[r].expired /\ before[r].receipt = Request[i].receipt}
GoodIndex(i) == /\ Cardinality(OracleMatches(i)) = 1
                /\ \A j \in 1..(i-1) : Request[j].receipt # Request[i].receipt
OracleIndices(n,success) == SelectSeq([i \in 1..n |-> i], LAMBDA i : GoodIndex(i) = success)
OracleGood(n) == LET xs == OracleIndices(n,TRUE)
                IN [j \in 1..Len(xs) |-> [Id |-> Request[xs[j]].id]]
OracleBad(n) == LET xs == OracleIndices(n,FALSE)
               IN [j \in 1..Len(xs) |-> [Id |-> Request[xs[j]].id,
                    Code |-> "ReceiptHandleIsInvalid", SenderFault |-> TRUE]]
OracleDeleted == UNION {OracleMatches(i) : i \in {j \in Indices : GoodIndex(j)}}
OracleExpired == {r \in Rows : before[r] # None /\ before[r].scope = Target /\ before[r].expired}
OracleDB == [r \in Rows |-> IF r \in OracleDeleted \cup OracleExpired THEN None ELSE before[r]]
Ambiguous == \E i \in Indices : Cardinality(OracleMatches(i)) > 1
Init ==
 /\ input \in Cases \cup FaultCases /\ before = Snapshot(input.seed)
 /\ queueBefore = IF input.seed = "missing-queue" THEN 0 ELSE Generation(input.seed)
 /\ queueDB = queueBefore /\ queueWork = queueBefore
 /\ db = before /\ work = before /\ phase = "validate" /\ cursor = 1
 /\ selected = <<>> /\ deleteCursor = 1 /\ good = <<>> /\ bad = <<>>
 /\ status = "pending" /\ response = None /\ failure = "none" /\ error = ""
 /\ iamCalled = FALSE /\ iamAction = "" /\ iamResource = None /\ transactionOpened = FALSE
Validate ==
 /\ phase = "validate"
 /\ phase' = IF Valid THEN "authorize" ELSE "done"
 /\ status' = IF Valid THEN "pending" ELSE "invalid"
 /\ UNCHANGED <<Stable,db,work,cursor,selected,deleteCursor,good,bad,response,
                 failure,error,iamCalled,iamAction,iamResource,transactionOpened,queueDB,queueWork>>
Authorize ==
 /\ phase = "authorize" /\ iamCalled' = TRUE
 /\ iamAction' = "sqs:DeleteMessage" /\ iamResource' = Target
 /\ phase' = IF Allowed THEN "begin" ELSE "done"
 /\ status' = IF Allowed THEN "pending" ELSE "denied"
 /\ UNCHANGED <<Stable,db,work,cursor,selected,deleteCursor,good,bad,response,failure,error,transactionOpened,queueDB,queueWork>>
Begin ==
 /\ phase = "begin" /\ transactionOpened' = TRUE
 /\ phase' = IF ParentPresent THEN "purge" ELSE "done"
 /\ status' = IF ParentPresent THEN "pending" ELSE "missing-queue"
 /\ UNCHANGED <<Stable,db,work,cursor,selected,deleteCursor,good,bad,response,failure,error,iamCalled,iamAction,iamResource,queueDB,queueWork>>
NoExpired(rows) == ~\E r \in Rows : rows[r] # None /\ rows[r].scope = Target /\ rows[r].expired
Purge ==
 /\ phase = "purge"
 /\ work' = IF input.fault = "ignored-purge" THEN work ELSE
      [r \in Rows |-> IF work[r] # None /\ work[r].scope = Target /\ work[r].expired THEN None ELSE work[r]]
 /\ phase' = "purge-check"
 /\ UNCHANGED <<Stable,db,cursor,selected,deleteCursor,good,bad,status,response,failure,error,iamCalled,iamAction,iamResource,transactionOpened,queueDB,queueWork>>
Rollback(reason) ==
 /\ phase' = "done" /\ status' = "internal" /\ failure' = reason
 /\ error' = "InternalError" /\ response' = None
 /\ db' = before /\ work' = before /\ queueDB' = queueBefore /\ queueWork' = queueBefore /\ good' = <<>> /\ bad' = <<>>
 /\ UNCHANGED <<Stable,cursor,selected,deleteCursor,iamCalled,iamAction,iamResource,transactionOpened>>
PurgeCheck ==
 /\ phase = "purge-check"
 /\ IF ~NoExpired(work) THEN Rollback("purge") ELSE
      /\ phase' = "resolve"
      /\ UNCHANGED <<Stable,db,work,cursor,selected,deleteCursor,good,bad,status,response,failure,error,iamCalled,iamAction,iamResource,transactionOpened,queueDB,queueWork>>
Resolve ==
 /\ phase = "resolve" /\ cursor <= Len(Request)
 /\ LET matches == Matches(Request[cursor])
        row == CHOOSE r \in matches : TRUE
        available == Cardinality(matches) = 1 /\ row \notin {selected[i] : i \in 1..Len(selected)}
    IN IF Cardinality(matches) > 1 THEN Rollback("duplicate")
       ELSE /\ selected' = IF available THEN Append(selected,row) ELSE selected
            /\ good' = IF available THEN Append(good,[Id |-> Request[cursor].id]) ELSE good
            /\ bad' = IF available THEN bad ELSE Append(bad,FailureEntry(Request[cursor].id))
            /\ cursor' = cursor + 1
            /\ UNCHANGED <<Stable,db,work,phase,deleteCursor,status,response,failure,error,iamCalled,iamAction,iamResource,transactionOpened,queueDB,queueWork>>
Resolved ==
 /\ phase = "resolve" /\ cursor > Len(Request) /\ phase' = "delete"
 /\ UNCHANGED <<Stable,db,work,cursor,selected,deleteCursor,good,bad,status,response,failure,error,iamCalled,iamAction,iamResource,transactionOpened,queueDB,queueWork>>
Delete ==
 /\ phase = "delete" /\ deleteCursor <= Len(selected)
 /\ LET row == selected[deleteCursor]
        affected == input.writeOK /\ work[row] # None
    IN IF ~affected THEN Rollback("ignored-delete")
       ELSE /\ work' = IF input.writeOK THEN [work EXCEPT ![row] = None] ELSE work
            /\ deleteCursor' = deleteCursor + 1
            /\ UNCHANGED <<Stable,db,phase,cursor,selected,good,bad,status,response,failure,error,iamCalled,iamAction,iamResource,transactionOpened,queueDB,queueWork>>
Deleted ==
 /\ phase = "delete" /\ deleteCursor > Len(selected) /\ phase' = "verify"
 /\ queueWork' = IF input.fault = "queue-drift" THEN queueBefore + 1 ELSE queueWork
 /\ work' = CASE input.fault = "late-expiry" -> [work EXCEPT ![3] = before[3]]
      [] input.fault = "reinsert-id" /\ Len(selected) > 0 ->
           [work EXCEPT ![selected[1]] = [before[selected[1]] EXCEPT !.receipt = Handle(1,1,3)]]
      [] input.fault = "rebind-receipt" /\ Len(selected) > 0 -> [work EXCEPT ![3] = before[selected[1]]]
      [] OTHER -> work
 /\ UNCHANGED <<Stable,db,queueDB,cursor,selected,deleteCursor,good,bad,status,response,failure,error,iamCalled,iamAction,iamResource,transactionOpened>>
FinalQueueValid == queueWork = queueBefore
FinalExpiryValid == NoExpired(work)
FinalIDsAbsent == \A i \in 1..Len(selected) : work[selected[i]] = None
FinalReceiptsAbsent == \A i \in 1..Len(selected), r \in Rows :
 work[r] # None /\ work[r].scope = Target => work[r].receipt # before[selected[i]].receipt
Verify ==
 /\ phase = "verify"
 /\ IF ~(FinalQueueValid /\ FinalExpiryValid /\ FinalIDsAbsent /\ FinalReceiptsAbsent)
    THEN Rollback("final-check") ELSE
      /\ phase' = "commit"
      /\ UNCHANGED <<Stable,db,work,queueDB,queueWork,cursor,selected,deleteCursor,good,bad,status,response,failure,error,iamCalled,iamAction,iamResource,transactionOpened>>
Commit ==
 /\ phase = "commit"
 /\ IF input.commitOK
    THEN /\ phase' = "done" /\ status' = "success"
         /\ db' = work /\ queueDB' = queueWork /\ response' = [Successful |-> good, Failed |-> bad]
         /\ UNCHANGED <<Stable,work,cursor,selected,deleteCursor,good,bad,failure,error,iamCalled,iamAction,iamResource,transactionOpened,queueWork>>
    ELSE Rollback("commit")
Fail(reason) == /\ phase \in {"begin","purge","purge-check","resolve","delete","verify","commit"}
                /\ reason \in {"canceled","storage"} /\ Rollback(reason)
Next == Validate \/ Authorize \/ Begin \/ Purge \/ PurgeCheck \/ Resolve \/ Resolved
        \/ Delete \/ Deleted \/ Verify \/ Commit \/ Fail("canceled") \/ Fail("storage") \/ UNCHANGED vars
TypeOK ==
 /\ input \in Cases \cup FaultCases /\ DOMAIN before = Rows /\ DOMAIN db = Rows /\ DOMAIN work = Rows
 /\ phase \in {"validate","authorize","begin","purge","purge-check","resolve","delete","verify","commit","done"}
 /\ cursor \in 1..12 /\ deleteCursor \in 1..12 /\ selected \in Seq(Rows) /\ Len(selected) <= 11
 /\ Len(good) <= 11 /\ Len(bad) <= 11
 /\ status \in {"pending","invalid","denied","missing-queue","internal","success"}
 /\ failure \in {"none","duplicate","ignored-delete","commit","canceled","storage","purge","final-check"}
 /\ queueBefore \in 0..2 /\ queueDB \in 0..3 /\ queueWork \in 0..3
 /\ iamCalled \in BOOLEAN /\ transactionOpened \in BOOLEAN
PreflightBeforeEffects == (iamCalled \/ transactionOpened \/ db # before) => RawValid
IAMBinding == iamCalled => iamAction = "sqs:DeleteMessage" /\ iamResource = Target
AuthorizationBeforeTransaction == transactionOpened => input.policy = "delete"
ParentBeforeMutation == (phase \in {"purge","purge-check","resolve","delete","verify","commit"} \/ status = "success") => input.seed # "missing-queue"
PendingInvisible == phase # "done" => db = before /\ queueDB = queueBefore
NoEarlyResponse == status # "success" => response = None
FailureAtomic == (phase = "done" /\ status # "success") => db = before /\ queueDB = queueBefore
PeerFrame == \A r \in Rows : Scope(r) # Target => db[r] = before[r]
ResolutionExact == phase \in {"resolve","delete","commit"} =>
                    good = OracleGood(cursor-1) /\ bad = OracleBad(cursor-1)
BothArraysPresent == status = "success" => DOMAIN response = {"Successful","Failed"}
OrderedPartition == (status = "success" /\ DOMAIN response = {"Successful","Failed"}) =>
                     response.Successful = OracleGood(Len(Request)) /\ response.Failed = OracleBad(Len(Request))
CommittedStateExact == status = "success" => db = OracleDB /\ queueDB = queueBefore
DurableAck == status = "success" => input.commitOK /\ (input.writeOK \/ Len(good) = 0) /\ ~Ambiguous
SemanticFailuresCommit ==
 (phase = "done" /\ RawValid /\ input.policy = "delete" /\ input.seed # "missing-queue"
  /\ ~Ambiguous /\ input.commitOK /\ input.writeOK /\ input.fault = "none" /\ failure \notin {"canceled","storage"}) => status = "success"
PurgeBeforeResolution == phase \in {"resolve","delete"} =>
 ~\E r \in Rows : work[r] # None /\ work[r].scope = Target /\ work[r].expired
VerifiedBeforeCommit == phase = "commit" =>
 /\ queueWork = queueBefore
 /\ ~\E r \in Rows : work[r] # None /\ work[r].scope = Target /\ work[r].expired
 /\ \A i \in 1..Len(selected) : work[selected[i]] = None
 /\ \A i \in 1..Len(selected), r \in Rows : work[r] # None /\ work[r].scope = Target => work[r].receipt # before[selected[i]].receipt
DeleteAcknowledged ==
 (phase \in {"delete","verify","commit"} /\ deleteCursor > 1) => input.writeOK
SanitizedFailure == status = "internal" => error = "InternalError" /\ response = None
=============================================================================
