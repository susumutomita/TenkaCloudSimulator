-------------------------- MODULE EC2VpcResourceMap --------------------------
\* Bounded projection for one already witnessed VPC in one current view.
\* Independent Describe replies are normalized before the join, not a
\* transaction or an authority grant. A single named own attachment is
\* sufficient to check that a peer/detached/missing target is not inferred.
VARIABLES f, verdict, published
vars == <<f, verdict, published>>
Targets == {"own", "detached", "peer"}
Conflicts == {"peer", "orphan", "duplicate"}
Facts == [owner: {"native", "stack"}, subnetRead: {"ready", "unavailable"},
          hasSubnet: BOOLEAN, tables: {"ready", "denied", "malformed"},
          main: {"absent", "witnessed", "wrong-cidr"},
          claim: {"none", "owned"} \cup Conflicts,
          gateways: {"ready", "denied", "wrong-owner"},
          routes: SUBSET Targets, attached: BOOLEAN]
Verdicts == [quality: {"ready", "unconfirmed", "inconsistent"},
             association: {"none", "explicit", "main"},
             edges: SUBSET Targets, routeQuality: {"ready", "partial", "unconfirmed"}]
Empty == [quality |-> "unconfirmed", association |-> "none",
          edges |-> {}, routeQuality |-> "unconfirmed"]
Init ==
  /\ f \in Facts
  /\ (f.claim = "owned" => f.hasSubnet)
  /\ verdict = Empty /\ published = FALSE

TableUsable ==
  /\ f.tables = "ready" /\ f.main # "wrong-cidr"
  /\ (f.owner = "native" \/ f.main = "absent")
JoinAssessment ==
  IF ~(f.subnetRead = "ready" /\ TableUsable) THEN "unconfirmed"
  ELSE IF f.claim \in Conflicts THEN "inconsistent" ELSE "ready"
SubnetProjection ==
  IF JoinAssessment # "ready" \/ ~f.hasSubnet THEN "none"
  ELSE IF f.claim = "owned" THEN "explicit"
  ELSE IF f.owner = "native" /\ f.main = "witnessed" THEN "main"
  ELSE "none"
GatewayInputs == TableUsable /\ f.gateways = "ready"
VerifiedTargets == IF f.attached THEN {"own"} ELSE {}
GatewayProjection == IF GatewayInputs THEN f.routes \cap VerifiedTargets ELSE {}
GatewayAssessment ==
  IF ~GatewayInputs THEN "unconfirmed"
  ELSE IF f.routes \ VerifiedTargets # {} THEN "partial" ELSE "ready"
Project ==
  /\ ~published /\ published' = TRUE
  /\ verdict' = [quality |-> JoinAssessment, association |-> SubnetProjection,
                  edges |-> GatewayProjection, routeQuality |-> GatewayAssessment]
  /\ UNCHANGED f
Next == Project \/ (published /\ UNCHANGED vars)
TypeOK == f \in Facts /\ verdict \in Verdicts /\ published \in BOOLEAN

\* Independent, per-claim evidence obligations (not equality to Project).
ConfirmedTableFacts ==
  /\ f.tables = "ready"
  /\ (f.main = "absent" \/ (f.main = "witnessed" /\ f.owner = "native"))
ObservedSubnetAndTables == f.subnetRead = "ready" /\ ConfirmedTableFacts
SomeAmbiguousClaim == f.claim # "none" /\ f.claim # "owned"
AssociationQualityExact == published =>
  /\ ((verdict.quality = "ready") <=> (ObservedSubnetAndTables /\ ~SomeAmbiguousClaim))
  /\ ((verdict.quality = "inconsistent") <=> (ObservedSubnetAndTables /\ SomeAmbiguousClaim))
ExplicitNeedsEvidence == published =>
  ((verdict.association = "explicit") <=>
    (f.hasSubnet /\ ObservedSubnetAndTables /\ f.claim = "owned"))
MainNeedsWitness == published =>
  ((verdict.association = "main") <=>
    (ObservedSubnetAndTables /\ f.hasSubnet /\ f.claim = "none"
      /\ f.owner = "native" /\ f.main = "witnessed"))
ObservedGatewayAndTables == ConfirmedTableFacts /\ f.gateways = "ready"
ExactAttachedGatewayEdges == published => \A target \in Targets :
  (target \in verdict.edges) <=>
    (target \in f.routes /\ target = "own" /\ ObservedGatewayAndTables /\ f.attached)
GatewayFailureNotEmpty == published =>
  ((verdict.routeQuality = "unconfirmed") <=> ~ObservedGatewayAndTables)
GatewayHoleReported == published =>
  ((verdict.routeQuality = "partial") <=>
    (ObservedGatewayAndTables /\ (\E target \in f.routes : target # "own" \/ ~f.attached)))
=============================================================================
