----------------------- MODULE SakuraDiskMetadata -----------------------
EXTENDS Naturals, FiniteSets
CONSTANTS RealmA, RealmB, OwnerA, OwnerB, None, MaxSteps, Quota
Realms == {RealmA, RealmB}
Owners == {OwnerA, OwnerB}
IDs == 1..3
Names == {"same", "other"}
Owner(r) == IF r = RealmA THEN OwnerA ELSE OwnerB
EmptyRows == [i \in IDs |-> None]
Live(rows) == {i \in IDs : rows[i] # None}
Rows == [IDs -> Names \cup {None}]
RecordSet == [binding : Owners, next : 1..4, rows : Rows, issued : SUBSET IDs]
Ops == {"Create", "Update", "Delete", "Get", "List", "Reopen"}
Statuses == {"Init", "Accepted", "Fault", "Quota", "Missing", "Scope"}
VARIABLES db, steps, last
vars == <<db, steps, last>>
InitialDB == [r \in Realms |-> [binding |-> Owner(r), next |-> 1,
                               rows |-> EmptyRows, issued |-> {}]]
Init ==
 /\ db = InitialDB /\ steps = 0
 /\ last = [op |-> "Init", realm |-> RealmA,
     id |-> 1, name |-> "same", status |-> "Init", before |-> InitialDB,
     candidate |-> InitialDB[RealmA], replyRows |-> EmptyRows, replyTotal |-> 0]
NewID(r) == db[r].next
Step(op, r, id, name, candidateOwner, storageOK) ==
 LET before == db[r]
     exists == before.rows[id] # None
     room == Cardinality(Live(before.rows)) < Quota /\ before.next \in IDs
     status == IF op = "Reopen" THEN
                  IF candidateOwner = before.binding THEN "Accepted" ELSE "Scope"
               ELSE IF op = "Create" /\ ~room THEN "Quota"
               ELSE IF op \in {"Get", "Update", "Delete"} /\ ~exists THEN "Missing"
               ELSE IF ~storageOK THEN "Fault" ELSE "Accepted"
     candidate ==
       IF op = "Create" /\ room THEN
         [before EXCEPT !.rows[NewID(r)] = name,
           !.issued = @ \cup {NewID(r)}, !.next = @ + 1]
       ELSE IF op = "Update" /\ exists THEN [before EXCEPT !.rows[id] = name]
       ELSE IF op = "Delete" /\ exists THEN [before EXCEPT !.rows[id] = None]
       ELSE IF op = "Reopen" THEN [before EXCEPT !.binding = candidateOwner]
       ELSE before
     committed == IF status = "Accepted" THEN candidate ELSE before
     reply == IF status = "Accepted" /\ op = "List" THEN before.rows
              ELSE IF status = "Accepted" /\ op = "Get" THEN
                [i \in IDs |-> IF i = id THEN before.rows[i] ELSE None]
              ELSE EmptyRows
 IN /\ steps < MaxSteps
    /\ db' = [db EXCEPT ![r] = committed]
    /\ steps' = steps + 1
    /\ last' = [op |-> op, realm |-> r,
        id |-> IF op = "Create" /\ room THEN NewID(r) ELSE id,
        name |-> name, status |-> status, before |-> db,
        candidate |-> candidate, replyRows |-> reply,
        replyTotal |-> IF status = "Accepted" /\ op = "List"
                      THEN Cardinality(Live(before.rows)) ELSE 0]
Next == (\E r \in Realms, name \in Names, ok \in BOOLEAN :
           Step("Create",r,1,name,Owner(r),ok)) \/
        (\E r \in Realms, id \in IDs, name \in Names, ok \in BOOLEAN :
           Step("Update",r,id,name,Owner(r),ok)) \/
        (\E op \in {"Delete","Get"}, r \in Realms, id \in IDs, ok \in BOOLEAN :
           Step(op,r,id,"same",Owner(r),ok)) \/
        (\E r \in Realms, ok \in BOOLEAN : Step("List",r,1,"same",Owner(r),ok)) \/
        (\E r \in Realms, candidateOwner \in Owners :
           Step("Reopen",r,1,"same",candidateOwner,TRUE))
Accepted(op) == last.op = op /\ last.status = "Accepted"
TypeOK == /\ db \in [Realms -> RecordSet] /\ steps \in 0..MaxSteps
          /\ last.op \in Ops \cup {"Init"} /\ last.status \in Statuses
BindingFrozen == \A r \in Realms : db[r].binding = Owner(r)
FailureFrame == last.status \notin {"Init", "Accepted"} => db = last.before
PeerFrame == \A r \in Realms : r # last.realm => db[r] = last.before[r]
FreshCommittedID == Accepted("Create") =>
 /\ last.id \notin last.before[last.realm].issued
 /\ db[last.realm].issued = last.before[last.realm].issued \cup {last.id}
 /\ db[last.realm].next = last.before[last.realm].next + 1
AllocatorOnlyCreate == ~Accepted("Create") =>
 \A r \in Realms : /\ db[r].next = last.before[r].next
                  /\ db[r].issued = last.before[r].issued
CreateExact == Accepted("Create") =>
 db[last.realm].rows = [last.before[last.realm].rows EXCEPT ![last.id] = last.name]
UpdateExact == Accepted("Update") =>
 db[last.realm].rows = [last.before[last.realm].rows EXCEPT ![last.id] = last.name]
DeleteExact == Accepted("Delete") =>
 db[last.realm].rows = [last.before[last.realm].rows EXCEPT ![last.id] = None]
StoredIDsBound == \A r \in Realms :
 /\ Live(db[r].rows) \subseteq db[r].issued
 /\ (\A i \in Live(db[r].rows) : i < db[r].next)
ActiveQuota == \A r \in Realms : Cardinality(Live(db[r].rows)) <= Quota
QuotaRefusal ==
 (last.op = "Create" /\ Cardinality(Live(last.before[last.realm].rows)) >= Quota) =>
 last.status # "Accepted"
ReadSnapshot ==
 /\ (Accepted("List") =>
      /\ last.replyRows = last.before[last.realm].rows
      /\ last.replyTotal = Cardinality(Live(last.before[last.realm].rows)))
 /\ (Accepted("Get") => last.replyRows[last.id] = last.before[last.realm].rows[last.id])
 /\ (last.status \notin {"Init", "Accepted"} => last.replyRows = EmptyRows)
Spec == Init /\ [][Next]_vars
=============================================================================
