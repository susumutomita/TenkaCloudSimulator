------------------------- MODULE RDSGuestPeerPreflight -------------------------
\* One bounded observation, not a forwarding or guest authentication model.
\* Inputs are canonical facts. A single bad fact witnesses each decision; NIC
\* pre-seal and command retirement may additionally interleave at any phase.
VARIABLE s
Facts == {"ok", "wrong-receipt", "wrong-handle", "wrong-account", "wrong-region",
          "unavailable-owner", "stale-generation", "missing-owner-row",
          "public-network", "foreign-reservation", "different-vpc", "bad-nic-ip",
          "only-source-sg", "wrong-protocol", "wrong-port", "outside-cidr",
          "failed-commit", "failed-scram"}
Phases == {"start", "snapshot", "probe", "review", "observed", "denied"}
TypeOK == s \in [f: Facts, p: Phases, nic: BOOLEAN, cmd: BOOLEAN, tx: BOOLEAN,
                 tried: BOOLEAN, ticket: BOOLEAN, retiredByCheck: BOOLEAN]
Init == /\ TypeOK /\ s.p = "start" /\ s.nic /\ s.cmd
        /\ ~s.tx /\ ~s.tried /\ ~s.ticket /\ ~s.retiredByCheck

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
          THEN [s EXCEPT !.p = "review", !.tried = TRUE]
          ELSE [Deny EXCEPT !.tried = TRUE]
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
Next == Begin \/ Join \/ Probe \/ Review \/ LoseNIC \/ LoseCommand
Spec == Init /\ [][Next]_s

\* Oracles describe observable phases/facts, not the implementation guards.
Entered == s.p \in {"snapshot", "probe", "review", "observed"} \/ s.tried
AtProbe == s.p \in {"probe", "review", "observed"} \/ s.tried
CanonicalGuest == Entered => s.f # "wrong-receipt"
SameProviderOwner == Entered =>
    s.f \notin {"wrong-handle", "wrong-account", "wrong-region", "unavailable-owner"}
CurrentPrivateGeneration == AtProbe =>
    s.f \notin {"stale-generation", "missing-owner-row", "public-network",
                "foreign-reservation", "different-vpc", "bad-nic-ip"}
DestinationIngress == AtProbe =>
    s.f \notin {"only-source-sg", "wrong-protocol", "wrong-port", "outside-cidr"}
SnapshotClosedBeforeHost == AtProbe => (~s.tx /\ s.f # "failed-commit")
HostAuthenticated == s.p \in {"review", "observed"} => (s.tried /\ s.f # "failed-scram")
LiveAtReturn == s.p = "observed" => (s.nic /\ s.cmd)
ObservationOnly == ~s.ticket
DenialDoesNotRetireOwner == ~s.retiredByCheck
HealthyObservation == s.f = "ok" /\ s.nic /\ s.cmd => s.p # "denied"
===============================================================================
