----------------------- MODULE SSMDocumentVersionPage -----------------------
EXTENDS Naturals, Sequences
CONSTANTS Addresses, Subjects
VARIABLE observed

Sizes == 1..2
Counts == 0..2
Stamps == 0..1
First == [present |-> FALSE,
          address |-> CHOOSE a \in Addresses : TRUE,
          subject |-> CHOOSE s \in Subjects : TRUE,
          size |-> 1, stamp |-> 0, offset |-> 0]
Cursors == {First} \cup
  {[present |-> TRUE, address |-> a, subject |-> s,
    size |-> size, stamp |-> stamp, offset |-> offset] :
     a \in Addresses, s \in Subjects, size \in Sizes, stamp \in Stamps, offset \in Sizes}

\* A snapshot stamp abstracts equality only. It is not a token or hash model.
CursorMatches(input, cursor) ==
  \/ ~cursor.present
  \/ /\ cursor.address = input.address
     /\ cursor.subject = input.subject
     /\ cursor.stamp = input.stamp
     /\ cursor.size = input.size
     /\ cursor.offset < input.count
     /\ cursor.offset % input.size = 0
PageEnd(offset, size, count) == IF offset + size < count THEN offset + size ELSE count
Reject(status) == [status |-> status, rows |-> <<>>, next |-> FALSE]
Reply(input, cursor) ==
  IF ~input.allowed THEN Reject("Denied")
  ELSE IF input.count = 0 THEN Reject("InvalidDocument")
  ELSE IF ~CursorMatches(input, cursor) THEN Reject("InvalidNextToken")
  ELSE LET offset == cursor.offset
           end == PageEnd(offset, input.size, input.count)
       IN [status |-> "OK",
           rows |-> [i \in 1..(end-offset) |->
                    [version |-> offset+i, isDefault |-> offset+i = 1, hasBody |-> FALSE]],
           next |-> end < input.count]

Init == observed = <<>>
ChooseRequest ==
  /\ Len(observed) = 0
  /\ \E a \in Addresses, s \in Subjects, stamp \in Stamps, count \in Counts,
        size \in Sizes, allowed \in BOOLEAN, cursor \in Cursors :
    LET input == [address |-> a, subject |-> s, stamp |-> stamp,
                  size |-> size, allowed |-> allowed, count |-> count]
    IN observed' = <<[input |-> input, cursor |-> cursor, reply |-> Reply(input, cursor)]>>
Next == ChooseRequest \/ /\ Len(observed) = 1 /\ UNCHANGED observed
Spec == Init /\ [][Next]_observed

TypeOK ==
  /\ Len(observed) \in 0..1
  /\ \A entry \in {observed[i] : i \in DOMAIN observed} :
       /\ entry.reply.status \in {"Denied", "InvalidDocument", "InvalidNextToken", "OK"}
       /\ entry.reply.rows \in Seq([version : Sizes, isDefault : BOOLEAN, hasBody : BOOLEAN])
       /\ entry.reply.next \in BOOLEAN
DeniedHasNoHistory ==
  IF Len(observed) = 0 THEN TRUE
  ELSE LET hit == observed[1] IN
    ~hit.input.allowed => hit.reply = Reject("Denied")
MissingHasNoHistory ==
  IF Len(observed) = 0 THEN TRUE
  ELSE LET hit == observed[1] IN
    (hit.input.allowed /\ hit.input.count = 0) => hit.reply = Reject("InvalidDocument")
ContinuationScope ==
  IF Len(observed) = 0 THEN TRUE
  ELSE LET hit == observed[1]
           q == hit.input
           c == hit.cursor
       IN (q.allowed /\ q.count > 0 /\ c.present /\
           (<<q.address, q.subject, q.stamp, q.size>> # <<c.address, c.subject, c.stamp, c.size>>
            \/ c.offset >= q.count \/ c.offset % q.size # 0))
          => hit.reply = Reject("InvalidNextToken")
AdmittedHasPage ==
  IF Len(observed) = 0 THEN TRUE
  ELSE LET hit == observed[1] IN
    (hit.input.allowed /\ hit.input.count > 0 /\ CursorMatches(hit.input, hit.cursor))
    => hit.reply.status = "OK"
MetadataOnly ==
  IF Len(observed) = 0 THEN TRUE
  ELSE LET items == observed[1].reply.rows IN
    \A i \in DOMAIN items : ~items[i].hasBody
NumericWindow ==
  IF Len(observed) = 0 THEN TRUE
  ELSE LET hit == observed[1]
           offset == hit.cursor.offset
           items == hit.reply.rows
       IN hit.reply.status = "OK" =>
             /\ Len(items) = PageEnd(offset, hit.input.size, hit.input.count) - offset
             /\ \A i \in DOMAIN items :
                     /\ items[i].version = offset+i
                     /\ items[i].isDefault = (offset+i = 1)
ContinuationEnd ==
  IF Len(observed) = 0 THEN TRUE
  ELSE LET hit == observed[1]
       IN hit.reply.status = "OK" =>
    (hit.reply.next \equiv hit.cursor.offset + Len(hit.reply.rows) < hit.input.count)
=============================================================================
