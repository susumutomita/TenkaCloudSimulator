----------------------------- MODULE S3Versions -----------------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANT Keys
VARIABLES bucket, history, current
vars == <<bucket, history, current>>
HasObject(h) == IF Len(h) = 0 THEN FALSE ELSE h[Len(h)] = "object"
EmptyHistory == \A k \in Keys : Len(history[k]) = 0
Init == /\ bucket = FALSE
        /\ history = [k \in Keys |-> <<>>]
        /\ current = {}
Create == /\ ~bucket
          /\ bucket' = TRUE
          /\ UNCHANGED <<history, current>>
Put(k) == /\ bucket /\ Len(history[k]) < 3
          /\ history' = [history EXCEPT ![k] = Append(@, "object")]
          /\ current' = current \cup {k}
          /\ UNCHANGED bucket
MarkDeleted(k) == /\ bucket /\ Len(history[k]) < 3
                  /\ history' = [history EXCEPT ![k] = Append(@, "marker")]
                  /\ current' = current \ {k}
                  /\ UNCHANGED bucket
Erase(k, i) == LET h == history[k]
                   remaining == SubSeq(h, 1, i - 1) \o SubSeq(h, i + 1, Len(h))
               IN /\ bucket /\ i \in 1..Len(h)
                  /\ history' = [history EXCEPT ![k] = remaining]
                  /\ current' = IF HasObject(remaining) THEN current \cup {k}
                                ELSE current \ {k}
                  /\ UNCHANGED bucket
RemoveBucket == /\ bucket /\ EmptyHistory
                /\ bucket' = FALSE
                /\ UNCHANGED <<history, current>>
Next == Create \/ RemoveBucket \/ (\E k \in Keys : Put(k) \/ MarkDeleted(k)
                                  \/ (\E i \in 1..3 : Erase(k, i)))
TypeOK == /\ bucket \in BOOLEAN
          /\ history \in [Keys -> Seq({"object", "marker"})]
          /\ current \subseteq Keys
NoHistoryOrphans == ~bucket => EmptyHistory /\ current = {}
CurrentMatchesLatest == current = {k \in Keys : HasObject(history[k])}
Spec == Init /\ [][Next]_vars
=============================================================================
