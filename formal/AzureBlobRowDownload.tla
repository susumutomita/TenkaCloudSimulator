-------------------- MODULE AzureBlobRowDownload --------------------
EXTENDS Naturals

VARIABLES epoch, active, selected, pending, emitted
vars == <<epoch, active, selected, pending, emitted>>

Blobs == {"a", "b"}
NoRequest == [row |-> "none", target |-> "none", epoch |-> 0]
NoEmission == [job |-> NoRequest, result |-> "none", active |-> FALSE, epoch |-> 0]
Statuses == {"ok", "failure", "too-large"}
Init ==
  /\ epoch = 0 /\ active = TRUE /\ selected = "none"
  /\ pending = NoRequest /\ emitted = NoEmission

Choose(row) ==
  /\ active /\ pending = NoRequest /\ row \in Blobs
  /\ selected' = row /\ UNCHANGED <<epoch, active, pending, emitted>>

RequestTarget(row) == row
Start(row) ==
  /\ active /\ pending = NoRequest /\ row \in Blobs
  /\ epoch < 2 /\ epoch' = epoch + 1
  /\ pending' = [row |-> row, target |-> RequestTarget(row), epoch |-> epoch']
  /\ UNCHANGED <<active, selected, emitted>>

\* A canceled generation can still have an abstract completion; only the
\* captured epoch and still-active view can produce an anchor click.
Retire ==
  /\ active /\ active' = FALSE
  /\ epoch' = epoch + 1
  /\ UNCHANGED <<selected, pending, emitted>>

StillCurrent == active /\ pending.epoch = epoch
HasBytes(result) == result = "ok"
Finish(result) ==
  /\ pending /= NoRequest /\ result \in Statuses
  /\ pending' = NoRequest
  /\ emitted' = IF StillCurrent /\ HasBytes(result)
                 THEN [job |-> pending, result |-> result, active |-> active, epoch |-> epoch]
                 ELSE emitted
  /\ UNCHANGED <<epoch, active, selected>>

Next == (\E row \in Blobs: Choose(row) \/ Start(row))
        \/ Retire \/ (\E result \in Statuses: Finish(result))
Spec == Init /\ [][Next]_vars

TypeOK == /\ epoch \in 0..3 /\ active \in BOOLEAN /\ selected \in Blobs \cup {"none"}
          /\ pending \in {NoRequest} \cup [row: Blobs, target: Blobs \cup {"none"}, epoch: 1..2]
          /\ emitted \in {NoEmission} \cup
             [job: [row: Blobs, target: Blobs \cup {"none"}, epoch: 1..2],
              result: Statuses, active: BOOLEAN, epoch: 1..3]
RowIsRequestTarget == pending = NoRequest \/ pending.target = pending.row
CurrentDownloadOnly == emitted = NoEmission \/ (emitted.active /\ emitted.job.epoch = emitted.epoch)
SuccessfulBytesOnly == emitted = NoEmission \/ emitted.result = "ok"
=====================================================================
