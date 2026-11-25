---------------- MODULE SimpleDBQueryAdmission ----------------
EXTENDS Naturals
Ops == {"CreateDomain","DeleteDomain","ListDomains","PutAttributes","GetAttributes","Unknown"}
Defects == {"none","transport","decode","version","domain","item","attributes","list-limit","read-option"}
Backend == {"ok","user-error","internal-error"}
Rejects == {"gateway-denied","gateway-invalid","input-error","policy-denied"}
Inputs == [op : Ops, defect : Defects, actor : BOOLEAN, configured : BOOLEAN,
           region : BOOLEAN, unique : BOOLEAN, decision : BOOLEAN]
VARIABLES req, phase, calls, selected, resource, outcome, response, payload
vars == <<req,phase,calls,selected,resource,outcome,response,payload>>
ItemOps == {"PutAttributes","GetAttributes"}
DomainOps == ItemOps \cup {"CreateDomain","DeleteDomain"}
GoodShape(r) ==
 /\ r.op # "Unknown"
 /\ r.defect \notin {"transport","decode","version"}
 /\ (r.op \in DomainOps => r.defect # "domain")
 /\ (r.op \in ItemOps => r.defect \notin {"item","attributes"})
 /\ (r.op = "ListDomains" => r.defect # "list-limit")
 /\ (r.op = "GetAttributes" => r.defect # "read-option")
Trusted(r) == r.actor /\ r.configured /\ r.region /\ r.unique /\ r.decision
ExpectedResource(op) == IF op = "ListDomains" THEN "wildcard" ELSE "configured"
ResultKind(op) ==
 CASE op = "ListDomains" -> "domains"
   [] op = "GetAttributes" -> "attributes"
   [] OTHER -> "none"
Init ==
 /\ req \in Inputs /\ phase = "Input" /\ calls = 0
 /\ selected = "none" /\ resource = "none" /\ outcome = "none"
 /\ response = "none" /\ payload = "none"
Dispatch ==
 /\ phase = "Input" /\ Trusted(req)
 /\ GoodShape(req)
 /\ phase' = "Dispatched" /\ calls' = calls + 1
 /\ selected' = "configured"
 /\ resource' = ExpectedResource(req.op)
 /\ UNCHANGED <<req,outcome,response,payload>>
Reject ==
 /\ phase = "Input" /\ ~(Trusted(req) /\ GoodShape(req))
 /\ phase' = "Finished"
 /\ response' =
      IF ~req.actor \/ ~req.configured THEN "gateway-denied"
      ELSE IF ~req.region \/ ~req.unique THEN "gateway-invalid"
      ELSE IF ~GoodShape(req) THEN "input-error"
      ELSE "policy-denied"
 /\ UNCHANGED <<req,calls,selected,resource,outcome,payload>>
Complete(b) ==
 /\ phase = "Dispatched" /\ b \in Backend
 /\ phase' = "Finished" /\ outcome' = b
 /\ response' = IF b = "ok" THEN "action-success" ELSE b
 /\ payload' = IF b = "ok" THEN ResultKind(req.op) ELSE "none"
 /\ UNCHANGED <<req,calls,selected,resource>>
Done == /\ phase = "Finished" /\ UNCHANGED vars
Next == Dispatch \/ Reject \/ (\E b \in Backend : Complete(b)) \/ Done
TypeOK ==
 /\ req \in Inputs /\ phase \in {"Input","Dispatched","Finished"}
 /\ calls \in 0..1 /\ selected \in {"none","configured","peer"}
 /\ resource \in {"none","wildcard","configured","peer"}
 /\ outcome \in Backend \cup {"none"}
 /\ response \in Rejects \cup {"none","action-success","user-error","internal-error"}
 /\ payload \in {"none","domains","attributes"}
OneDispatch ==
 /\ (phase = "Input" => calls = 0)
 /\ ((phase = "Dispatched" \/ outcome \in Backend) => calls = 1)
 /\ ((phase = "Finished" /\ outcome = "none") => calls = 0)
MalformedNeverDispatches == calls = 1 => GoodShape(req)
TrustedAdmissionBeforeDispatch == calls = 1 => Trusted(req)
ConfiguredScopeBinding ==
 /\ (calls = 1 => (selected = "configured" /\ resource = ExpectedResource(req.op)))
 /\ (calls = 0 => (selected = "none" /\ resource = "none"))
EnvelopeMatchesOutcome ==
 /\ (phase # "Finished" => (response = "none" /\ payload = "none"))
 /\ (phase = "Finished" =>
      CASE outcome = "ok" -> response = "action-success" /\ payload = ResultKind(req.op)
        [] outcome \in {"user-error","internal-error"} -> response = outcome /\ payload = "none"
        [] outcome = "none" -> response \in Rejects /\ payload = "none")
=================================================================
