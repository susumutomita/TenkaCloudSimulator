-------------------------- MODULE SSMDocumentState ---------------------------
EXTENDS Naturals, Sequences, FiniteSets, TLC
CONSTANTS Addresses, Values, MaxVersions, MaxDocuments
VARIABLES versions, last
vars == <<versions, last>>
Empty == [a \in Addresses |-> <<>>]
Present(s) == {a \in Addresses : Len(s[a]) > 0}
Permitted(allow, deny) == allow /\ ~deny
Init == /\ versions = Empty
        /\ last = [op |-> "init", address |-> CHOOSE a \in Addresses : TRUE,
                    before |-> Empty, permitted |-> TRUE, changed |-> FALSE, expected |-> 0]
Apply(op, a, value, expected, allow, deny) ==
  LET permit == Permitted(allow, deny)
      admitted == permit /\
        CASE op = "create" -> Len(versions[a]) = 0 /\ Cardinality(Present(versions)) < MaxDocuments
          [] op = "update" -> Len(versions[a]) > 0 /\ expected = Len(versions[a])
                             /\ Len(versions[a]) < MaxVersions
                             /\ versions[a][Len(versions[a])] # value
          [] op = "delete" -> Len(versions[a]) > 0
          [] OTHER -> FALSE
      next == IF ~admitted THEN versions ELSE
        [versions EXCEPT ![a] = IF op = "delete" THEN <<>> ELSE Append(@, value)]
  IN /\ versions' = next
     /\ last' = [op |-> op, address |-> a, before |-> versions,
                 permitted |-> (allow /\ ~deny), changed |-> admitted, expected |-> expected]
Next == \E op \in {"create", "update", "delete"}, a \in Addresses,
           value \in Values, expected \in 0..MaxVersions,
           allow \in BOOLEAN, deny \in BOOLEAN : Apply(op,a,value,expected,allow,deny)
TypeOK == /\ versions \in [Addresses -> Seq(Values)]
          /\ \A a \in Addresses : Len(versions[a]) <= MaxVersions
          /\ Cardinality(Present(versions)) <= MaxDocuments
DeniedPreserves == ~last.permitted => versions = last.before
RefusalPreserves == ~last.changed => versions = last.before
AddressIsolation == \A a \in Addresses \ {last.address} : versions[a] = last.before[a]
HistoryImmutable == (last.op = "update" /\ last.changed) =>
  /\ Len(versions[last.address]) = Len(last.before[last.address]) + 1
  /\ SubSeq(versions[last.address],1,Len(last.before[last.address])) = last.before[last.address]
DefaultPreserved == (last.op = "update" /\ last.changed) =>
  versions[last.address][1] = last.before[last.address][1]
DeleteCleanup == (last.op = "delete" /\ last.changed) => versions[last.address] = <<>>
LatestPrecondition == (last.op = "update" /\ last.changed) =>
  last.expected = Len(last.before[last.address])
CreateOnlyAbsent == (last.op = "create" /\ last.changed) => Len(last.before[last.address]) = 0
Spec == Init /\ [][Next]_vars
=============================================================================
