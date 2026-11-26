--------------------- MODULE CognitoFederationNativeAdmission ----------------
EXTENDS Naturals

\* A bounded check of opt-in, dispatch and projected acknowledgment only.
\* The actual SigV4 verifier and typed configuration have separate checks.
CONSTANT StepLimit, None
VARIABLE s
Local == <<"local","east",1>>
PeerAccount == <<"other","east",1>>
PeerRegion == <<"local","west",1>>
Sibling == <<"local","east",2>>
Keys == {Local, PeerAccount, PeerRegion, Sibling}
Rows == [live : BOOLEAN, provider : BOOLEAN, client : BOOLEAN]
Blank(live) == [live |-> live, provider |-> FALSE, client |-> FALSE]
Writes == {"CreateProvider", "CreateClient"}
Reads == {"DescribeProvider", "DescribeClient"}
ConfigOps == Writes \cup Reads
Ops == ConfigOps \cup {"LegacyPool", "Login", "Other"}
Replies == {"unauthenticated", "unsupported", "input", "denied", "missing", "duplicate", "legacy", "ok"}
Projections == {None, "provider", "client", "raw"}
Proofs == {"verified", "unsigned", "dummy", "browser", "wrongService"}
Grants == {"exact", "wrongAction", "wrongPool", "denied"}
R == [op : Ops, enabled : BOOLEAN, proof : Proofs, direct : BOOLEAN, sdk : BOOLEAN,
      shape : BOOLEAN, scoped : BOOLEAN, iam : Grants]
Request(op) == [op |-> op, enabled |-> TRUE, proof |-> "verified", direct |-> TRUE,
                sdk |-> TRUE, shape |-> TRUE, scoped |-> TRUE, iam |-> "exact"]
Inputs ==
  LET r == Request("CreateProvider")
      client == Request("DescribeClient")
      desc == Request("DescribeProvider")
  IN {Request(op) : op \in Ops}
     \cup {[Request(op) EXCEPT !.enabled = FALSE] : op \in Ops}
     \cup {[r EXCEPT !.proof = proof] : proof \in Proofs}
     \cup {[r EXCEPT !.direct = FALSE], [r EXCEPT !.sdk = FALSE], [r EXCEPT !.shape = FALSE],
            [client EXCEPT !.scoped = FALSE], [desc EXCEPT !.iam = "wrongAction"],
            [client EXCEPT !.iam = "wrongPool"], [r EXCEPT !.iam = "denied"]}

Gate(r) ==
  IF r.proof # "verified" THEN "unauthenticated"
  ELSE IF r.op = "LegacyPool" THEN "legacy"
  ELSE IF ~r.enabled \/ r.op \notin ConfigOps THEN "unsupported"
  ELSE IF ~r.direct \/ ~r.sdk THEN "unauthenticated"
  ELSE IF ~r.shape \/ ~r.scoped THEN "input"
  ELSE IF r.iam # "exact" THEN "denied"
  ELSE "ok"

Resolve(r, row, admitted) ==
  IF admitted # "ok" THEN admitted
  ELSE IF ~row.live THEN "missing"
  ELSE CASE r.op = "CreateProvider" -> IF row.provider THEN "duplicate" ELSE "ok"
          [] r.op = "CreateClient" -> IF row.provider THEN "ok" ELSE "missing"
          [] r.op = "DescribeProvider" -> IF row.provider THEN "ok" ELSE "missing"
          [] OTHER -> IF row.client THEN "ok" ELSE "missing"

Projection(r, result) ==
  IF result # "ok" THEN None
  ELSE IF r.op \in {"CreateProvider","DescribeProvider"} THEN "provider" ELSE "client"

Frame(r, row, result) ==
  IF result # "ok" THEN row
  ELSE CASE r.op = "CreateProvider" -> [row EXCEPT !.provider = TRUE]
          [] r.op = "CreateClient" -> [row EXCEPT !.client = TRUE]
          [] OTHER -> row
Install(rows, next) == [rows EXCEPT ![Local] = next]
Init ==
  s = [rows |-> [k \in Keys |-> IF k = Local THEN Blank(TRUE)
                                ELSE [Blank(TRUE) EXCEPT !.provider = TRUE, !.client = TRUE]],
       count |-> 0, last |-> None]
Call(r) ==
  /\ s.count < StepLimit
  /\ LET admitted == Gate(r)
         old == s.rows[Local]
         result == Resolve(r,old,admitted)
         next == Frame(r,old,result)
     IN s' = [s EXCEPT !.rows = Install(s.rows,next), !.count = @ + 1,
                       !.last = [input |-> r, prior |-> s.rows, result |-> result,
                                 touched |-> admitted = "ok", output |-> Projection(r,result)]]
NativeDelete ==
  /\ s.count < StepLimit
  /\ s' = [s EXCEPT !.rows = Install(s.rows,Blank(FALSE)), !.count = @ + 1, !.last = None]
Next == (\E r \in Inputs : Call(r)) \/ NativeDelete \/ UNCHANGED s

TypeOK ==
  /\ s.rows \in [Keys -> Rows] /\ s.count \in 0..StepLimit
  /\ s.last \in {None} \cup [input : R, prior : [Keys -> Rows], result : Replies, touched : BOOLEAN, output : Projections]
GateOracle(r) ==
  r.enabled /\ r.proof = "verified" /\ r.op \in ConfigOps /\ r.direct /\ r.sdk /\ r.shape /\ r.scoped /\ r.iam = "exact"

ExactNativeAdmission == IF s.last = None THEN TRUE ELSE
  LET w == s.last
      r == w.input
  IN /\ w.touched = GateOracle(r)
     /\ (r.proof # "verified" => w.result = "unauthenticated")
     /\ (r.proof = "verified" /\ r.op = "LegacyPool" => w.result = "legacy")
     /\ (r.proof = "verified" /\ r.op # "LegacyPool" /\ (~r.enabled \/ r.op \notin ConfigOps) => w.result = "unsupported")
     /\ (~GateOracle(r) => w.output = None /\ s.rows = w.prior)

NativeProjection == IF s.last = None THEN TRUE ELSE
  LET w == s.last
      r == w.input
      row == w.prior[Local]
      successful == GateOracle(r) /\ row.live /\
        (CASE r.op = "CreateProvider" -> ~row.provider
            [] r.op = "DescribeClient" -> row.client
            [] OTHER -> row.provider)
  IN /\ (w.result = "ok") = successful
     /\ IF successful
        THEN w.output = (IF r.op \in {"CreateProvider","DescribeProvider"} THEN "provider" ELSE "client")
        ELSE w.output = None

CanonicalFrame == IF s.last = None THEN TRUE ELSE
  LET w == s.last
      r == w.input
      prior == w.prior[Local]
  IN /\ \A key \in Keys \ {Local} : s.rows[key] = w.prior[key]
     /\ IF w.result = "ok" /\ r.op \in Writes
        THEN IF r.op = "CreateProvider"
             THEN s.rows[Local] = [prior EXCEPT !.provider = TRUE]
             ELSE s.rows[Local] = [prior EXCEPT !.client = TRUE]
        ELSE s.rows = w.prior
=============================================================================
