---- MODULE SimpleDBDomainMetadata ----
EXTENDS Naturals, FiniteSets

\* Positive indices model ordered item names. The three value widths model
\* an empty value, a one-byte value, and a multibyte value, respectively.
Items == 1..2
Keys == 1..2
ItemBytes(i) == IF i = 1 THEN 1 ELSE 3
KeyBytes(k) == IF k = 1 THEN 1 ELSE 3
ValueBytes(v) == CASE v = 1 -> 0 [] v = 2 -> 1 [] OTHER -> 4
Pairs == {<<1,1,1>>, <<1,2,3>>, <<2,1,1>>, <<2,1,3>>, <<2,2,2>>}

VARIABLES exists, allowed, wellFormed, rows, pairs, phase, done,
          seen, itemCount, itemSize, keyCount, keySize, valueCount, valueSize, result
vars == <<exists, allowed, wellFormed, rows, pairs, phase, done,
          seen, itemCount, itemSize, keyCount, keySize, valueCount, valueSize, result>>
inputs == <<exists, allowed, wellFormed, rows, pairs>>
totals == <<done, seen, itemCount, itemSize, keyCount, keySize, valueCount, valueSize>>
Init ==
 /\ exists \in BOOLEAN /\ allowed \in BOOLEAN /\ wellFormed \in BOOLEAN
 /\ rows \in (IF exists THEN SUBSET Items ELSE {{}})
 /\ pairs \in SUBSET {p \in Pairs : p[1] \in rows}
 /\ phase = "init" /\ done = {} /\ seen = {} /\ result = "none"
 /\ itemCount = 0 /\ itemSize = 0 /\ keyCount = 0 /\ keySize = 0 /\ valueCount = 0 /\ valueSize = 0

Admit ==
 /\ phase = "init" /\ wellFormed /\ allowed /\ exists
 /\ phase' = "reading" /\ UNCHANGED <<inputs, totals, result>>
Reject ==
 /\ phase = "init" /\ ~(wellFormed /\ allowed /\ exists)
 /\ result' = IF ~wellFormed THEN "invalid" ELSE IF ~allowed THEN "denied" ELSE "missing"
 /\ phase' = "finished" /\ UNCHANGED <<inputs, totals>>

RECURSIVE SumValues(_)
SumValues(xs) ==
 IF xs = {} THEN 0 ELSE
   LET x == CHOOSE y \in xs : TRUE IN ValueBytes(x[3]) + SumValues(xs \ {x})

KeySet(ps) == {p[2] : p \in ps}
Advance(i) ==
 /\ phase = "reading" /\ i \in rows \ done
 /\ LET vs == {p \in pairs : p[1] = i}
        newKeys == KeySet(vs) \ seen
    IN /\ done' = done \cup {i}
       /\ seen' = seen \cup newKeys
       /\ itemCount' = itemCount + 1
       /\ itemSize' = itemSize + ItemBytes(i)
       /\ keyCount' = keyCount + Cardinality(newKeys)
       /\ keySize' = keySize + (IF 1 \in newKeys THEN KeyBytes(1) ELSE 0) + (IF 2 \in newKeys THEN KeyBytes(2) ELSE 0)
       /\ valueCount' = valueCount + Cardinality(vs)
       /\ valueSize' = valueSize + SumValues(vs)
 /\ UNCHANGED <<inputs, phase, result>>
Finish ==
 /\ phase = "reading" /\ done = rows
 /\ result' = "metadata" /\ phase' = "finished" /\ UNCHANGED <<inputs, totals>>
Next == Admit \/ Reject \/ (\E i \in Items : Advance(i)) \/ Finish \/
        (/\ phase = "finished" /\ UNCHANGED vars)

TypeOK ==
 /\ phase \in {"init","reading","finished"} /\ done \subseteq rows /\ seen \subseteq Keys
 /\ result \in {"none","metadata","invalid","denied","missing"}
 /\ \A n \in {itemCount,itemSize,keyCount,keySize,valueCount,valueSize} : n \in Nat
ReadRequiresDomainAuthority ==
 /\ phase = "reading" => (wellFormed /\ allowed /\ exists)
 /\ phase = "finished" =>
     result = (IF ~wellFormed THEN "invalid" ELSE IF ~allowed THEN "denied"
               ELSE IF ~exists THEN "missing" ELSE "metadata")
 /\ result \in {"invalid","denied","missing"} =>
      /\ done = {} /\ seen = {}
      /\ <<itemCount,itemSize,keyCount,keySize,valueCount,valueSize>> = <<0,0,0,0,0,0>>
ExactCommittedTotals ==
 LET selected == {p \in pairs : p[1] \in done}
     keys == KeySet(selected)
 IN /\ (result = "metadata" => done = rows)
    /\ itemCount = Cardinality(done)
    /\ itemSize = Cardinality(done \cap {1}) + 3 * Cardinality(done \cap {2})
    /\ seen = keys /\ keyCount = Cardinality(keys)
    /\ keySize = Cardinality(keys \cap {1}) + 3 * Cardinality(keys \cap {2})
    /\ valueCount = Cardinality(selected)
    /\ valueSize = Cardinality({p \in selected : p[3] = 2}) +
                    4 * Cardinality({p \in selected : p[3] = 3})
====
