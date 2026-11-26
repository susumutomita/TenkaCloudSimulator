------------------------- MODULE RDSGuestPeerPreflight -------------------------
\* One bounded observation, not a forwarding or guest authentication model.
\* Inputs are canonical facts. A single bad static fact witnesses each decision.
\* NIC pre-seal and command retirement can additionally interleave. Independent
\* destination loss can occur between the first and final committed snapshots.
VARIABLE s
Facts == {"ok", "wrong-receipt", "wrong-handle", "wrong-account", "wrong-region",
          "unavailable-owner", "stale-generation", "missing-owner-row",
          "public-network", "foreign-reservation", "different-vpc", "bad-nic-ip",
          "only-source-sg", "wrong-protocol", "wrong-port", "outside-cidr",
          "failed-commit", "failed-scram", "failed-final-commit"}
Phases == {"start", "snapshot", "probe", "recheck", "review", "observed", "denied"}
TypeOK == s \in [f: Facts, p: Phases, nic: BOOLEAN, cmd: BOOLEAN, tx: BOOLEAN,
                 destination: BOOLEAN, finalCommitted: BOOLEAN, tried: BOOLEAN,
                 ticket: BOOLEAN, retiredByCheck: BOOLEAN]
Init == /\ TypeOK /\ s.p = "start" /\ s.nic /\ s.cmd /\ s.destination
        /\ ~s.tx /\ ~s.finalCommitted /\ ~s.tried /\ ~s.ticket /\ ~s.retiredByCheck

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

Deny == [s EXCEPT !.p = "denied", !.tx = FALSE, !.retiredByCheck = FALSE]
Begin ==
  /\ s.p = "start"
  /\ s' = IF Retained /\ Provider /\ s.nic /\ s.cmd
          THEN [s EXCEPT !.p = "snapshot", !.tx = TRUE] ELSE Deny
Join ==
  /\ s.p = "snapshot"
  /\ s' = IF Generation /\ PrivateTopology /\ DestinationGroup /\ TCP
              /\ PortCovers5432 /\ RangeCoversPeer /\ CommitOK
          THEN [s EXCEPT !.p = "probe", !.tx = FALSE] ELSE Deny
Probe ==
  /\ s.p = "probe"
  /\ s' = IF FixedSCRAM
          THEN [s EXCEPT !.p = "recheck", !.tried = TRUE]
          ELSE [Deny EXCEPT !.tried = TRUE]

\* One abstract read + commit after the probe. The value at this waypoint
\* is an observation, not a continuity requirement across both snapshots.
Recheck ==
  /\ s.p = "recheck"
  /\ s' = IF LiveDestination /\ FinalCommitOK
          THEN [s EXCEPT !.p = "review", !.finalCommitted = TRUE] ELSE Deny

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
Next == Begin \/ Join \/ Probe \/ Recheck \/ Review \/ LoseNIC \/ LoseCommand \/ LoseDestination
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
LiveAtReturn == s.p = "observed" => (s.nic /\ s.cmd)
ObservationOnly == ~s.ticket
DenialDoesNotRetireOwner == ~s.retiredByCheck
HealthyObservation == s.f = "ok" /\ s.nic /\ s.cmd /\ s.destination => s.p # "denied"
===============================================================================
