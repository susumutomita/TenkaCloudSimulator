------------------------ MODULE SSMBatchDeletion ------------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANTS RegionA, RegionB, NameA, NameB, Peer, None
Regions == {RegionA, RegionB}
Names == {NameA, NameB}
Keys == Regions \X (Names \cup {Peer})
RequestedKeys == Regions \X Names
\* Exhaust all two-name sequences of length 0..3, plus raw-count boundaries.
Requests == UNION {[1..n -> Names] : n \in 0..3}
            \cup {[i \in 1..10 |-> NameA], [i \in 1..11 |-> NameA]}
Seed(k) == [value |-> k, version |-> IF k[2] = NameA THEN 1 ELSE 2]
Records == [value: Keys, version: 1..2]
Snapshot(present) == [k \in Keys |-> IF k[2] = Peer \/ k \in present
                                   THEN Seed(k) ELSE None]
RECURSIVE Unique(_)
Unique(xs) == IF Len(xs) = 0 THEN <<>> ELSE
              <<Head(xs)>> \o Unique(SelectSeq(Tail(xs), LAMBDA n : n # Head(xs)))
VARIABLES request, region, policy, validity, commitOK, before, db, work,
          phase, cursor, status, deleted, missing
vars == <<request, region, policy, validity, commitOK, before, db, work,
          phase, cursor, status, deleted, missing>>
Inputs == <<request, region, policy, validity, commitOK, before>>
Targets == {request[i] : i \in 1..Len(request)}
OrderedTargets == Unique(request)
CountValid == Len(request) >= 1 /\ Len(request) <= 10
ShapeValid == validity # "bad-shape"
EveryNameValid == validity \notin {"bad-first", "bad-last"}
EveryTargetAuthorized == \A n \in Targets : policy[n] = "allow"
Admission == CountValid /\ ShapeValid /\ EveryNameValid /\ EveryTargetAuthorized
TargetKeys == {<<region,n>> : n \in Targets}
\* Independent response oracle: first occurrence indices, not Unique or work.
FirstIndices == SelectSeq([i \in 1..Len(request) |-> i],
                  LAMBDA i : \A j \in 1..(i-1) : request[j] # request[i])
ExpectedResult(present) ==
 LET indices == SelectSeq(FirstIndices,
                  LAMBDA i : (before[<<region,request[i]>>] # None) = present)
 IN [i \in 1..Len(indices) |-> request[indices[i]]]
ExpectedDB == [k \in Keys |-> IF k \in TargetKeys THEN None ELSE before[k]]
Init ==
 /\ request \in Requests /\ region \in Regions
 /\ policy \in [Names -> {"allow", "implicit-deny", "explicit-deny"}]
 /\ validity \in {"valid", "bad-shape", "bad-first", "bad-last"}
 /\ commitOK \in BOOLEAN
 /\ \E present \in SUBSET RequestedKeys : before = Snapshot(present)
 /\ db = before /\ work = before
 /\ phase = "ready" /\ cursor = 1 /\ status = "pending"
 /\ deleted = <<>> /\ missing = <<>>
Preflight ==
 /\ phase = "ready"
 /\ phase' = IF Admission THEN "transaction" ELSE "done"
 /\ status' = IF Admission THEN "pending" ELSE "rejected"
 /\ UNCHANGED <<Inputs, db, work, cursor, deleted, missing>>
Stage ==
 /\ phase = "transaction" /\ cursor <= Len(OrderedTargets)
 /\ LET k == <<region, OrderedTargets[cursor]>> IN
      work' = [work EXCEPT ![k] = None]
 /\ cursor' = cursor + 1
 /\ UNCHANGED <<Inputs, db, phase, status, deleted, missing>>
Abort(reason) ==
 /\ phase \in {"ready", "transaction"}
 /\ reason \in {"canceled", "storage-error"}
 /\ (reason = "storage-error" => phase = "transaction")
 /\ phase' = "done" /\ status' = reason
 /\ work' = before
 /\ UNCHANGED <<Inputs, db, cursor, deleted, missing>>
Commit ==
 /\ phase = "transaction" /\ cursor > Len(OrderedTargets)
 /\ LET accepted == commitOK
        found == SelectSeq(OrderedTargets, LAMBDA n : before[<<region,n>>] # None)
        absent == SelectSeq(OrderedTargets, LAMBDA n : before[<<region,n>>] = None)
    IN /\ db' = IF accepted THEN work ELSE before
       /\ work' = IF accepted THEN work ELSE before
       /\ status' = IF accepted THEN "success" ELSE "commit-error"
       /\ deleted' = IF accepted THEN found ELSE <<>>
       /\ missing' = IF accepted THEN absent ELSE <<>>
 /\ phase' = "done"
 /\ UNCHANGED <<Inputs, cursor>>
Next == Preflight \/ Stage \/ Abort("canceled") \/ Abort("storage-error")
        \/ Commit \/ UNCHANGED vars
TypeOK ==
 /\ request \in Requests /\ region \in Regions
 /\ policy \in [Names -> {"allow", "implicit-deny", "explicit-deny"}]
 /\ validity \in {"valid", "bad-shape", "bad-first", "bad-last"}
 /\ commitOK \in BOOLEAN
 /\ before \in [Keys -> Records \cup {None}]
 /\ db \in [Keys -> Records \cup {None}]
 /\ work \in [Keys -> Records \cup {None}]
 /\ phase \in {"ready", "transaction", "done"}
 /\ cursor \in 1..12
 /\ status \in {"pending", "rejected", "success", "canceled", "storage-error", "commit-error"}
 /\ deleted \in Seq(Names) /\ Len(deleted) <= 11
 /\ missing \in Seq(Names) /\ Len(missing) <= 11
AdmissionBeforeMutation ==
 (phase = "transaction" \/ status = "success") =>
   /\ Len(request) >= 1 /\ Len(request) <= 10
   /\ validity = "valid"
   /\ \A n \in Targets : policy[n] = "allow"
PendingInvisible == phase # "done" => db = before
NoEarlyResponse == status # "success" => deleted = <<>> /\ missing = <<>>
FailureAtomic == (phase = "done" /\ status # "success") => db = before
DurableAck == status = "success" => commitOK /\ phase = "done"
ExactDeletion == status = "success" => db = ExpectedDB
PeerFrame == \A k \in Keys \ TargetKeys : db[k] = before[k]
SuccessfulResultsExact == status = "success" =>
 /\ deleted = ExpectedResult(TRUE)
 /\ missing = ExpectedResult(FALSE)
\* No presence requirement: an admitted completed transaction with missing names
\* is successful, including all-missing requests. Canceled requests may fail.
MissingIsNotFailure ==
 (phase = "done" /\ status \notin {"canceled", "storage-error"}
  /\ Len(request) >= 1 /\ Len(request) <= 10 /\ validity = "valid"
  /\ (\A n \in Targets : policy[n] = "allow") /\ commitOK) => status = "success"
CanceledPreservesState == status = "canceled" => db = before /\ deleted = <<>> /\ missing = <<>>
=============================================================================
