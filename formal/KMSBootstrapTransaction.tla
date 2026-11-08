----------------------- MODULE KMSBootstrapTransaction -----------------------
EXTENDS Naturals
\* Logical durable schema/cursor rows only; no SQLite/Go/token refinement.
Regions == {0,1}
Faults == {"none","bootstrap","authorize","operation","commit"}
Phases == {"validate","bootstrap","authorize","operation","commit","publish","done"}
Empty == [r \in Regions |-> 0]
Boot(d,r) == [d EXCEPT !.schema = TRUE, !.rows = @ \cup {r}]
Operate(d,r,w) == IF w THEN [d EXCEPT !.data[r] = @ + 1] ELSE d
Other(r) == 1-r
VARIABLE s
vars == <<s>>
Init ==
 \E schema \in BOOLEAN, rows \in SUBSET Regions, region \in Regions,
     valid \in BOOLEAN, lateValid \in BOOLEAN, policy \in BOOLEAN, action \in BOOLEAN,
     alias \in BOOLEAN, key \in BOOLEAN, write \in BOOLEAN, fault \in Faults :
 /\ (~schema => rows = {})
 /\ LET d == [schema |-> schema, rows |-> rows, data |-> Empty]
    IN s = [phase |-> "validate", durable |-> d, before |-> d, working |-> d,
            region |-> region, valid |-> valid, lateValid |-> lateValid, policy |-> policy,
            action |-> action, alias |-> alias, key |-> key, write |-> write,
            fault |-> fault, status |-> "pending", published |-> FALSE,
            committed |-> FALSE]
Allowed == s.policy /\ s.action /\ s.alias /\ s.key
Abort(reason) == [s EXCEPT !.phase = "done", !.status = reason]
Validate ==
 /\ s.phase = "validate"
 /\ s' = IF s.valid /\ s.policy THEN [s EXCEPT !.phase = "bootstrap"]
         ELSE Abort("refused")
Bootstrap ==
 /\ s.phase = "bootstrap"
 /\ s' = IF s.fault = "bootstrap" THEN Abort("fault")
         ELSE [s EXCEPT !.working = Boot(s.working,s.region), !.phase = "authorize"]
Authorize ==
 /\ s.phase = "authorize"
 /\ s' = IF s.fault = "authorize" THEN Abort("fault")
         ELSE IF Allowed THEN [s EXCEPT !.phase = "operation"]
         ELSE Abort("denied")
Operation ==
 /\ s.phase = "operation"
 /\ s' = IF ~s.lateValid THEN Abort("refused")
         ELSE IF s.fault = "operation" THEN Abort("fault")
         ELSE [s EXCEPT !.working = Operate(s.working,s.region,s.write),
                        !.phase = "commit"]
Commit ==
 /\ s.phase = "commit"
 /\ s' = IF s.fault = "commit" THEN Abort("fault")
         ELSE [s EXCEPT !.durable = s.working, !.committed = TRUE,
                        !.phase = "publish"]
Publish ==
 /\ s.phase = "publish"
 /\ s' = [s EXCEPT !.published = TRUE, !.status = "success", !.phase = "done"]
Next == Validate \/ Bootstrap \/ Authorize \/ Operation \/ Commit \/ Publish
Spec == Init /\ [][Next]_vars
DurableType(d) == /\ d.schema \in BOOLEAN /\ d.rows \subseteq Regions
                  /\ d.data \in [Regions -> 0..1] /\ (~d.schema => d.rows={})
TypeOK == /\ s.phase \in Phases /\ DurableType(s.durable)
          /\ DurableType(s.before) /\ DurableType(s.working)
          /\ s.region \in Regions /\ s.fault \in Faults
          /\ s.status \in {"pending","refused","denied","fault","success"}
          /\ s.published \in BOOLEAN /\ s.committed \in BOOLEAN
PreCommitFrame == ~s.committed => s.durable=s.before
RejectedDurableFrame == (s.phase="done" /\ s.status#"success") => s.durable=s.before
AdmissionRequired == s.phase \in {"bootstrap","authorize","operation","commit","publish"} => s.valid /\ s.policy
OperationAuthorized == s.phase \in {"operation","commit","publish"} => Allowed
CommitAuthorized == s.committed => s.valid /\ s.lateValid /\ Allowed /\ s.fault="none"
PublishedAfterCommit == s.published => s.committed /\ s.status="success"
\* Independent literal oracle: never call the operational Boot/Operate helpers.
ExpectedDurable == [schema |-> TRUE, rows |-> s.before.rows \cup {s.region},
 data |-> [r \in Regions |-> s.before.data[r] +
                       (IF r=s.region /\ s.write THEN 1 ELSE 0)]]
SuccessfulScope == s.committed => s.durable=ExpectedDurable
PeerFrame == /\ s.durable.data[Other(s.region)]=s.before.data[Other(s.region)]
             /\ (Other(s.region) \in s.durable.rows) = (Other(s.region) \in s.before.rows)
SuccessComplete == (s.phase="done" /\ s.status="success") =>
                   s.committed /\ s.published /\ s.durable.schema /\ s.region \in s.durable.rows
=============================================================================
