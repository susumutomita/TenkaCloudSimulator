------------------------- MODULE RDSGuestPeerPreflight -------------------------
\* One bounded observation, not a forwarding or guest authentication model.
\* Inputs are canonical facts. A single bad static fact witnesses each decision.
\* NIC pre-seal and command retirement can additionally interleave. Independent
\* destination loss can occur between the first and final committed snapshots.
\* The reserved DB already held the original subnet group's identity. An
\* independent writer can edit that group or replace it under the same ARN.
VARIABLE s
Facts == {"ok", "wrong-receipt", "wrong-handle", "wrong-account", "wrong-region",
          "unavailable-owner", "stale-generation", "missing-owner-row",
          "public-network", "foreign-reservation", "different-vpc", "bad-nic-ip",
          "only-source-sg", "wrong-protocol", "wrong-port", "outside-cidr",
          "failed-commit", "failed-scram", "failed-final-commit"}
Phases == {"start", "snapshot", "probe", "recheck", "review", "observed", "denied"}
GroupIdentities == {"reserved", "same-arn-replacement"}
GroupRevisions == {1, 2}
TypeOK == s \in [f: Facts, p: Phases, nic: BOOLEAN, cmd: BOOLEAN, tx: BOOLEAN,
                 destination: BOOLEAN, finalCommitted: BOOLEAN, tried: BOOLEAN,
                 ticket: BOOLEAN, retiredByCheck: BOOLEAN,
                 group: GroupIdentities, rev: GroupRevisions,
                 firstGroup: GroupIdentities \cup {"unread"}, firstRev: GroupRevisions \cup {0},
                 finalGroup: GroupIdentities \cup {"unread"}, finalRev: GroupRevisions \cup {0}]
Init == \E fact \in Facts:
    s = [f |-> fact, p |-> "start", nic |-> TRUE, cmd |-> TRUE, tx |-> FALSE,
         destination |-> TRUE, finalCommitted |-> FALSE, tried |-> FALSE,
         ticket |-> FALSE, retiredByCheck |-> FALSE, group |-> "reserved",
         rev |-> 1, firstGroup |-> "unread", firstRev |-> 0,
         finalGroup |-> "unread", finalRev |-> 0]

Retained == s.f # "wrong-receipt"
Provider == s.f \notin {"wrong-handle", "wrong-account", "wrong-region", "unavailable-owner"}
Generation == s.f \notin {"stale-generation", "missing-owner-row"}
PrivateTopology == s.f \notin {"public-network", "foreign-reservation", "different-vpc", "bad-nic-ip"}
DestinationGroup == s.f # "only-source-sg"
TCP == s.f # "wrong-protocol"
PortCovers5432 == s.f # "wrong-port"
RangeCoversPeer == s.f # "outside-cidr"
CommitOK == s.f # "failed-commit"
FixedSCRAM == s.f # "failed-scram"
LiveDestination == s.destination
FinalCommitOK == s.f # "failed-final-commit"
InitialGroupOK == s.group = "reserved"
FinalGroupOK == s.group = "reserved"
SameGroupRevision == s.firstRev = s.rev

Deny == [s EXCEPT !.p = "denied", !.tx = FALSE, !.retiredByCheck = FALSE]
Begin ==
  /\ s.p = "start"
  /\ s' = IF Retained /\ Provider /\ s.nic /\ s.cmd
          THEN [s EXCEPT !.p = "snapshot", !.tx = TRUE] ELSE Deny
Join ==
  /\ s.p = "snapshot"
  /\ s' = IF Generation /\ PrivateTopology /\ DestinationGroup /\ TCP
              /\ PortCovers5432 /\ RangeCoversPeer /\ CommitOK /\ InitialGroupOK
          THEN [s EXCEPT !.p = "probe", !.tx = FALSE,
                        !.firstGroup = s.group, !.firstRev = s.rev] ELSE Deny
Probe ==
  /\ s.p = "probe"
  /\ s' = IF FixedSCRAM
          THEN [s EXCEPT !.p = "recheck", !.tried = TRUE]
          ELSE [Deny EXCEPT !.tried = TRUE]

\* One abstract read + commit after the probe. Only the original group
\* incarnation and the observed group revision are matched across reads.
Recheck ==
  /\ s.p = "recheck"
  /\ s' = IF LiveDestination /\ FinalCommitOK /\ FinalGroupOK /\ SameGroupRevision
          THEN [s EXCEPT !.p = "review", !.finalCommitted = TRUE,
                        !.finalGroup = s.group, !.finalRev = s.rev] ELSE Deny

Review ==
  /\ s.p = "review"
  /\ s' = IF s.nic /\ s.cmd
          THEN [s EXCEPT !.p = "observed", !.ticket = FALSE] ELSE Deny
LoseNIC ==
  /\ s.p \notin {"observed", "denied"} /\ s.nic
  /\ s' = [s EXCEPT !.nic = FALSE]
LoseCommand ==
  /\ s.p \notin {"observed", "denied"} /\ s.cmd
  /\ s' = [s EXCEPT !.cmd = FALSE]
LoseDestination ==
  /\ s.p \in {"probe", "recheck"} /\ s.destination
  /\ s' = [s EXCEPT !.destination = FALSE]
EditGroup ==
  /\ s.p \notin {"observed", "denied"} /\ s.group = "reserved" /\ s.rev = 1
  /\ s' = [s EXCEPT !.rev = 2]
ReplaceGroup ==
  /\ s.p \notin {"observed", "denied"} /\ s.group = "reserved"
  \* An equal revision number does not make the replacement the same row.
  /\ s' = [s EXCEPT !.group = "same-arn-replacement", !.rev = 1]
Next == Begin \/ Join \/ Probe \/ Recheck \/ Review \/ LoseNIC \/ LoseCommand
        \/ LoseDestination \/ EditGroup \/ ReplaceGroup
Spec == Init /\ [][Next]_s

\* Oracles describe observable phases/facts, not the implementation guards.
Entered == s.p \in {"snapshot", "probe", "recheck", "review", "observed"} \/ s.tried
AtProbe == s.p \in {"probe", "recheck", "review", "observed"} \/ s.tried
CanonicalGuest == Entered => s.f # "wrong-receipt"
SameProviderOwner == Entered =>
    s.f \notin {"wrong-handle", "wrong-account", "wrong-region", "unavailable-owner"}
CurrentPrivateGeneration == AtProbe =>
    s.f \notin {"stale-generation", "missing-owner-row", "public-network",
                "foreign-reservation", "different-vpc", "bad-nic-ip"}
DestinationIngress == AtProbe =>
    s.f \notin {"only-source-sg", "wrong-protocol", "wrong-port", "outside-cidr"}
SnapshotClosedBeforeHost == AtProbe => (~s.tx /\ s.f # "failed-commit")
HostAuthenticated == s.p \in {"recheck", "review", "observed"} => (s.tried /\ s.f # "failed-scram")
FinalSnapshotCommitted == s.p = "observed" => (s.finalCommitted /\ s.f # "failed-final-commit")
DestinationAtFinalSnapshot == s.p = "observed" => s.destination
OriginalGroupBeforeProbe == AtProbe => s.firstGroup = "reserved"
OriginalGroupAtFinalSnapshot == s.finalCommitted => s.finalGroup = "reserved"
MatchingGroupSnapshotRevisions == s.finalCommitted =>
    /\ s.firstRev \in GroupRevisions /\ s.firstRev = s.finalRev
LiveAtReturn == s.p = "observed" => (s.nic /\ s.cmd)
ObservationOnly == ~s.ticket
DenialDoesNotRetireOwner == ~s.retiredByCheck
HealthyObservation ==
    s.f = "ok" /\ s.nic /\ s.cmd /\ s.destination /\ s.group = "reserved"
    /\ s.firstRev \in {0, s.rev} => s.p # "denied"
===============================================================================
