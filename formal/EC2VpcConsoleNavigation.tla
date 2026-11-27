--------------------------- MODULE EC2VpcConsoleNavigation --------------------
EXTENDS Integers, FiniteSets

CONSTANTS Sections, Regions, LastEpoch
VARIABLES screen, authenticated, hasRead, hasWrite, pending, lease, display, checkedDispatch, safeNavigation

Vars == <<screen, authenticated, hasRead, hasWrite, pending, lease, display, checkedDispatch, safeNavigation>>
Screens == [section : Sections, region : Regions, epoch : 0..LastEpoch]
Operations == [origin : Screens, kind : {"read","write"}]
Receipts == [op : Operations, verified : BOOLEAN]

ReadAdvertised == authenticated /\ screen.section \in hasRead
WriteAdvertised == ReadAdvertised /\ screen.section \in hasWrite
Ready == \E receipt \in display : receipt.op.origin = screen
CanRead == ReadAdvertised
CanWrite == WriteAdvertised
CurrentReply(op) == op.origin = screen
ReceiptVerified(verified) == verified
ReleaseOwner(op) == lease = op.origin.epoch
UserCanNavigate == lease = -1

Init ==
  /\ screen \in [section : Sections, region : Regions, epoch : {0}]
  /\ authenticated \in BOOLEAN
  /\ hasRead \in SUBSET Sections
  /\ hasWrite \in SUBSET hasRead
  /\ pending = {} /\ lease = -1 /\ display = {}
  /\ checkedDispatch = TRUE /\ safeNavigation = TRUE

DispatchRead ==
  /\ CanRead
  /\ ~\E op \in pending : op.origin = screen
  /\ pending' = pending \cup {[origin |-> screen, kind |-> "read"]}
  /\ checkedDispatch' = (checkedDispatch /\ ReadAdvertised)
  /\ UNCHANGED <<screen, authenticated, hasRead, hasWrite, lease, display, safeNavigation>>

DispatchWrite ==
  /\ CanWrite /\ Ready /\ lease = -1
  /\ ~\E op \in pending : op.origin = screen
  /\ pending' = pending \cup {[origin |-> screen, kind |-> "write"]}
  /\ lease' = screen.epoch
  /\ checkedDispatch' = (checkedDispatch /\ WriteAdvertised)
  /\ UNCHANGED <<screen, authenticated, hasRead, hasWrite, display, safeNavigation>>

Navigate(section,region,source) ==
  /\ screen.epoch < LastEpoch
  /\ section # screen.section \/ region # screen.region \/ source = "history"
  /\ source = "history" \/ UserCanNavigate
  /\ screen' = [section |-> section, region |-> region, epoch |-> screen.epoch + 1]
  /\ safeNavigation' = (safeNavigation /\ (source = "history" \/ lease = -1))
  /\ lease' = -1 /\ display' = {}
  /\ UNCHANGED <<authenticated, hasRead, hasWrite, pending, checkedDispatch>>

Reply(op,verified) ==
  /\ pending' = pending \ {op}
  /\ lease' = IF op.kind = "write" /\ ReleaseOwner(op) THEN -1 ELSE lease
  /\ display' = IF CurrentReply(op) /\ ReceiptVerified(verified) THEN {[op |-> op, verified |-> verified]} ELSE display
  /\ UNCHANGED <<screen, authenticated, hasRead, hasWrite, checkedDispatch, safeNavigation>>

Next ==
  \/ DispatchRead
  \/ DispatchWrite
  \/ \E section \in Sections, region \in Regions, source \in {"button","history"} : Navigate(section,region,source)
  \/ \E op \in pending, verified \in BOOLEAN : Reply(op,verified)

TypeOK == /\ screen \in Screens /\ authenticated \in BOOLEAN
          /\ hasRead \in SUBSET Sections /\ hasWrite \in SUBSET hasRead
          /\ pending \in SUBSET Operations /\ lease \in {-1} \cup (0..LastEpoch)
          /\ display \in SUBSET Receipts /\ Cardinality(display) <= 1
          /\ checkedDispatch \in BOOLEAN /\ safeNavigation \in BOOLEAN
AdvertisedOnly == checkedDispatch
CurrentRegionAndView == \A receipt \in display : receipt.op.origin = screen
VerifiedDisplay == \A receipt \in display : receipt.verified
NoLocalDepartureDuringWrite == safeNavigation
CurrentWriteOwnsLease ==
  \A op \in pending : (op.kind = "write" /\ op.origin = screen) => lease = screen.epoch
==============================================================================
