-------------------- MODULE S3MultipartConsolePaging --------------------
(* Pure console ListParts handoff; no S3 mutation or host state model. *)
EXTENDS Naturals, Sequences

CONSTANT MaxPart, MaxReads
PartValues == 0..MaxPart
Pages == {<<>>} \cup {<<p>> : p \in PartValues} \cup
         {<<p,q>> : p,q \in PartValues}
Flags == {"final", "more", "invalid"}
Statuses == {"read", "between", "ready", "failed", "stopped"}

VARIABLES status, marker, parts, exposed, reads, terminal, rowsValid,
          cursorsValid, stopped, readAfterStop
vars == <<status, marker, parts, exposed, reads, terminal, rowsValid,
          cursorsValid, stopped, readAfterStop>>

LastPart(rows, prior) == IF Len(rows) = 0 THEN prior ELSE rows[Len(rows)]
CanonicalRows(rows, prior) ==
  /\ \A i \in DOMAIN rows : rows[i] > prior /\ rows[i] <= MaxPart
  /\ \A i,j \in DOMAIN rows : i < j => rows[i] < rows[j]
CanonicalCursor(rows, prior, cursor) ==
  /\ Len(rows) > 0
  /\ cursor > prior
  /\ cursor < MaxPart
  /\ cursor = LastPart(rows, prior)
RowsOK(rows, prior) == CanonicalRows(rows, prior)
CursorOK(rows, prior, cursor) == CanonicalCursor(rows, prior, cursor)
FinalFlag(flag) == flag = "final"
ExposeOnContinue == FALSE
ActiveOK == ~stopped

Init == /\ status = "read" /\ marker = 0 /\ parts = <<>> /\ exposed = <<>>
        /\ reads = 0 /\ terminal = FALSE /\ rowsValid = TRUE
        /\ cursorsValid = TRUE /\ stopped = FALSE /\ readAfterStop = FALSE

Receive(flag, rows, cursor) ==
  /\ status = "read" /\ reads < MaxReads
  /\ reads' = reads + 1
  /\ LET done == RowsOK(rows, marker) /\ FinalFlag(flag)
         more == RowsOK(rows, marker) /\ flag = "more" /\ CursorOK(rows, marker, cursor)
         publish == done \/ (more /\ ExposeOnContinue)
     IN /\ status' = IF publish THEN "ready" ELSE IF more THEN "between" ELSE "failed"
        /\ marker' = IF more THEN cursor ELSE marker
        /\ parts' = IF done \/ more THEN parts \o rows ELSE <<>>
        /\ exposed' = IF publish THEN parts \o rows ELSE <<>>
        /\ terminal' = (flag = "final")
        /\ rowsValid' = (IF done \/ more THEN rowsValid /\ CanonicalRows(rows, marker) ELSE rowsValid)
        /\ cursorsValid' = (IF more THEN cursorsValid /\ CanonicalCursor(rows, marker, cursor) ELSE cursorsValid)
  /\ UNCHANGED <<stopped, readAfterStop>>

Cancel == /\ status = "between" /\ ~stopped /\ stopped' = TRUE
          /\ UNCHANGED <<status, marker, parts, exposed, reads, terminal, rowsValid,
                         cursorsValid, readAfterStop>>
Continue == /\ status = "between" /\ ActiveOK /\ reads < MaxReads
            /\ status' = "read" /\ readAfterStop' = stopped
            /\ UNCHANGED <<marker, parts, exposed, reads, terminal, rowsValid,
                           cursorsValid, stopped>>
Stop == /\ status = "between" /\ stopped
        /\ status' = "stopped" /\ parts' = <<>>
        /\ UNCHANGED <<marker, exposed, reads, terminal, rowsValid, cursorsValid,
                       stopped, readAfterStop>>
Next == (\E flag \in Flags, rows \in Pages, cursor \in PartValues : Receive(flag, rows, cursor))
        \/ Cancel \/ Continue \/ Stop \/ UNCHANGED vars

TypeOK == /\ status \in Statuses /\ marker \in PartValues /\ reads \in 0..MaxReads
          /\ parts \in Seq(PartValues) /\ exposed \in Seq(PartValues)
          /\ Len(parts) <= 2 * MaxReads /\ Len(exposed) <= 2 * MaxReads
          /\ terminal \in BOOLEAN /\ rowsValid \in BOOLEAN /\ cursorsValid \in BOOLEAN
          /\ stopped \in BOOLEAN /\ readAfterStop \in BOOLEAN
OnlyCanonicalRows == rowsValid
OnlyProgressingCursor == cursorsValid
OnlyCompleteListing == IF status = "ready" THEN terminal /\ exposed = parts ELSE exposed = <<>>
NoReadAfterStop == ~readAfterStop
=========================================================================
