------------------ MODULE EC2NativeRouteAssociationReplacement ------------------
EXTENDS Naturals

\* One explicit subnet move between two owned tables, and one retired-ID retry.
\* Normalize XML/actor facts to allow/deny and present/absent. Neither packet
\* state nor SQLite, SDK parsing, React or random ID generation is refined.
VARIABLES rows, network, phase, receipt, epoch, ack, notice, replay

NativeTables == {"source", "destination"}
Targets == NativeTables \cup {"other-vpc", "west-table", "cfn-table", "missing"}
Row(id, tab, sub, region, owner) ==
  [id |-> id, tab |-> tab, sub |-> sub, region |-> region, owner |-> owner]
Old == Row("old", "source", "subnet", "east", "native")
New == Row("new", "destination", "subnet", "east", "native")
Kept == {Row("source-sibling", "source", "sibling-a", "east", "native"),
         Row("dest-sibling", "destination", "sibling-b", "east", "native"),
         Row("cfn", "cfn-table", "cfn-subnet", "east", "cfn"),
         Row("peer", "west-table", "peer-subnet", "west", "native")}
InitialRows == Kept \cup {Old}
MovedRows == Kept \cup {New}
Network == {"original-parents", "original-routes", "original-cfn", "original-peer"}

Cases == {"valid", "missing-old", "cfn-old", "wrong-region", "handoff", "missing-destination-grant",
          "missing-source-grant", "missing-subnet-grant", "same-table", "foreign-vpc", "cfn-destination",
          "missing-destination", "retained-claim", "retained-declaration", "storage-fault", "durable-drift"}
Sources == {"old", "missing", "cfn"}
Kinds == {"complete", "source-absent", "wrong-new-id", "wrong-subnet", "wrong-table", "old-still-live",
          "missing-sibling", "changed-routes", "partial", "denied"}
Dest(k) ==
  CASE k = "same-table" -> "source"
    [] k = "foreign-vpc" -> "other-vpc"
    [] k = "wrong-region" -> "west-table"
    [] k = "cfn-destination" -> "cfn-table"
    [] k = "missing-destination" -> "missing"
    [] OTHER -> "destination"
Source(k) == IF k = "cfn-old" THEN "cfn" ELSE IF k = "missing-old" THEN "missing" ELSE "old"
Context(k) == IF k = "wrong-region" THEN "west" ELSE "east"
Known(k) == Context(k) = "east" /\ Source(k) # "missing"
Owner(k) == Source(k) = "old"
TargetGrant(k) == k # "missing-destination-grant"
SourceGrant(k) == k # "missing-source-grant"
SubnetGrant(k) == k # "missing-subnet-grant"
Authorized(k) ==
  /\ k # "handoff"
  /\ Known(k) /\ Owner(k)
  /\ TargetGrant(k) /\ SourceGrant(k) /\ SubnetGrant(k)
Eligible(k) ==
  /\ Dest(k) # "source" /\ Dest(k) # "missing"
  /\ Dest(k) # "other-vpc" /\ Dest(k) # "cfn-table"
  /\ k # "retained-claim" /\ k # "retained-declaration"
Persistent(k) == k # "storage-fault" /\ k # "durable-drift"

Results == {"none", "committed", "dry", "refused"}
Ack(k, generation) == [kind |-> k, generation |-> generation]
EmptyAck == Ack("none", 0)
NoReceipt == [input |-> "none", target |-> "none", dry |-> FALSE, result |-> "none", id |-> "none"]
Init ==
  /\ rows = InitialRows /\ network = Network
  /\ phase = "before" /\ receipt = NoReceipt
  /\ epoch = 0 /\ ack = EmptyAck /\ notice = "none" /\ replay = "none"

Replace(k, dry) ==
  LET allowed == Authorized(k)
      commit == allowed /\ ~dry /\ Eligible(k) /\ Persistent(k)
      status == IF commit THEN "committed" ELSE IF dry /\ allowed THEN "dry" ELSE "refused"
      fresh == Row("new", Dest(k), Old.sub, Context(k), "native")
      stored == (rows \ {Old}) \cup {fresh}
  IN /\ phase = "before"
     /\ rows' = IF commit THEN stored ELSE rows
     /\ receipt' = [input |-> k, target |-> Dest(k), dry |-> dry, result |-> status,
                    id |-> IF commit THEN fresh.id ELSE "none"]
     /\ phase' = IF commit THEN "await-read" ELSE "done"
     /\ notice' = "none"
     /\ UNCHANGED <<network, epoch, ack, replay>>

\* Only one old generation is awaiting a read. A view change discards its
\* notices; fresh ordinary reads can reveal the saved move without replay.
Readback(k) ==
  LET decoded == k # "denied" /\ k # "partial"
      sourceRemains == k # "source-absent"
      exact == k \notin {"wrong-new-id", "wrong-subnet", "wrong-table"}
      retired == k # "old-still-live"
      siblings == k # "missing-sibling" /\ k # "changed-routes"
      valid == decoded /\ sourceRemains /\ exact /\ retired /\ siblings
  IN /\ phase = "await-read"
     /\ ack' = Ack(k, 0)
     /\ phase' = IF valid THEN "verified" ELSE "unconfirmed"
     /\ UNCHANGED <<rows, network, receipt, epoch, notice, replay>>

Publish ==
  /\ phase = "verified" /\ receipt.result = "committed"
  /\ ack.generation = epoch
  /\ notice' = receipt.id /\ phase' = "done"
  /\ UNCHANGED <<rows, network, receipt, epoch, ack, replay>>
ChangeScope ==
  /\ phase # "before" /\ epoch = 0
  /\ epoch' = 1 /\ notice' = "none"
  /\ UNCHANGED <<rows, network, phase, receipt, ack, replay>>
Refresh ==
  /\ phase \in {"unconfirmed", "verified"}
  /\ phase' = "done" /\ notice' = "none" /\ ack' = EmptyAck
  /\ UNCHANGED <<rows, network, receipt, epoch, replay>>
RetryRetired ==
  /\ receipt.result = "committed" /\ replay = "none"
  /\ replay' = "refused" /\ rows' = rows
  /\ UNCHANGED <<network, phase, receipt, epoch, ack, notice>>
Finish ==
  /\ phase = "done"
  /\ UNCHANGED <<rows, network, phase, receipt, epoch, ack, notice, replay>>
Next ==
  \/ \E k \in Cases, dry \in BOOLEAN : Replace(k, dry)
  \/ \E k \in Kinds : Readback(k)
  \/ Publish \/ ChangeScope \/ Refresh \/ RetryRetired \/ Finish

AdmittedRowShape == InitialRows \cup {Row(i, t, Old.sub, r, "native") :
                      i \in {"old", "new"}, t \in Targets, r \in {"east", "west"}}
TypeOK ==
  /\ rows \subseteq AdmittedRowShape
  /\ phase \in {"before", "await-read", "verified", "unconfirmed", "done"}
  /\ receipt.result \in Results /\ receipt.id \in {"old", "new", "none"}
  /\ epoch \in 0..1 /\ ack.kind \in Kinds \cup {"none"} /\ ack.generation \in 0..1
  /\ notice \in {"new", "none"} /\ replay \in {"none", "refused", "applied"}

\* The independent oracle is an explicit set of admitted inputs, not the
\* conjunctions above that are tested by sensitivity controls.
PermissionFailures == {"missing-old", "cfn-old", "wrong-region", "handoff",
                        "missing-destination-grant", "missing-source-grant", "missing-subnet-grant"}
ExactOutcome ==
  receipt.input \in Cases =>
    receipt.result = (IF receipt.input \in PermissionFailures THEN "refused"
                      ELSE IF receipt.dry THEN "dry"
                      ELSE IF receipt.input = "valid" THEN "committed" ELSE "refused")
NoEffectOnRefusal ==
  receipt.result # "committed" => rows = InitialRows /\ receipt.id = "none"
OneExplicit ==
  \A left, right \in rows: left.region = right.region /\ left.sub = right.sub => left = right
ExactMove ==
  receipt.result = "committed" =>
    rows = MovedRows /\ receipt.id = "new" /\ receipt.target = "destination"
OtherStateUnchanged == network = Network /\ Kept \subseteq rows
ConfirmedNotice ==
  notice # "none" => receipt.result = "committed" /\ notice = "new" /\ ack.kind = "complete" /\ ack.generation = epoch
RetiredRetryNeverApplies ==
  replay # "none" => replay = "refused" /\ rows = MovedRows /\ Old \notin rows
=============================================================================
