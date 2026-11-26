-------------------- MODULE RDSOwnedOperatorComposition --------------------
VARIABLES plan, run

Modes == {"metadata", "battle", "postgres", "mixed"}
Selections == {"none", "metadata", "battle", "postgres"}
Phases == {"new", "refused", "serving", "owner", "lambda", "app-engine",
           "health", "sqlite", "returned"}

PlanType == [mode: Modes, localAuth: BOOLEAN, topology: BOOLEAN,
               hostAccepted: BOOLEAN, pinnedBundle: BOOLEAN, privateState: BOOLEAN]
RunType == [phase: Phases, selection: Selections, openedSQLite: BOOLEAN,
              ownerDebt: BOOLEAN, triedLambda: BOOLEAN, lambdaError: BOOLEAN,
              triedAppEngine: BOOLEAN, appEngineError: BOOLEAN,
              joinedHealth: BOOLEAN, triedSQLiteClose: BOOLEAN,
              reportedDebt: BOOLEAN, reportedIndependent: BOOLEAN]

TypeOK == plan \in PlanType /\ run \in RunType

Init ==
  /\ plan \in PlanType
  /\ run = [phase |-> "new", selection |-> "none", openedSQLite |-> FALSE,
            ownerDebt |-> FALSE, triedLambda |-> FALSE, lambdaError |-> FALSE,
            triedAppEngine |-> FALSE, appEngineError |-> FALSE,
            joinedHealth |-> FALSE, triedSQLiteClose |-> FALSE,
            reportedDebt |-> FALSE, reportedIndependent |-> FALSE]

OwnPreflight ==
  plan.localAuth /\ plan.topology /\ plan.hostAccepted /\ plan.pinnedBundle /\ plan.privateState

Startable ==
  CASE plan.mode = "postgres" -> OwnPreflight
    [] plan.mode = "battle" -> plan.localAuth /\ plan.hostAccepted
    [] plan.mode = "mixed" -> FALSE
    [] OTHER -> TRUE

Admit ==
  /\ run.phase = "new"
  /\ run' = [run EXCEPT !.phase = IF Startable THEN "serving" ELSE "refused",
              !.openedSQLite = Startable,
              !.selection = IF Startable THEN
                           (IF plan.mode = "mixed" THEN "postgres" ELSE plan.mode)
                           ELSE "none"]

StartClose ==
  /\ run.phase = "serving"
  /\ run.selection \in {"metadata", "postgres"}
  /\ run' = [run EXCEPT !.phase = "owner"]

OwnerResult ==
  /\ run.phase = "owner"
  /\ \E debt \in (IF run.selection = "postgres" THEN BOOLEAN ELSE {FALSE}):
       run' = [run EXCEPT !.phase = "lambda", !.ownerDebt = debt]

CloseLambda ==
  /\ run.phase = "lambda"
  /\ \E failure \in BOOLEAN:
       run' = [run EXCEPT !.phase = "app-engine", !.triedLambda = TRUE, !.lambdaError = failure]

CloseAppEngine ==
  /\ run.phase = "app-engine"
  /\ \E failure \in BOOLEAN:
       run' = [run EXCEPT !.phase = "health", !.triedAppEngine = TRUE, !.appEngineError = failure]

JoinHealth ==
  /\ run.phase = "health"
  /\ run' = [run EXCEPT !.phase = "sqlite", !.joinedHealth = TRUE]

MayCloseSQLite == ~run.ownerDebt
ReportDebt == run.ownerDebt
ReportIndependent == run.lambdaError \/ run.appEngineError

Finish ==
  /\ run.phase = "sqlite"
  /\ run' = [run EXCEPT !.phase = "returned", !.triedSQLiteClose = MayCloseSQLite,
              !.reportedDebt = ReportDebt, !.reportedIndependent = ReportIndependent]

Next ==
  /\ UNCHANGED plan
  /\ (Admit \/ StartClose \/ OwnerResult \/ CloseLambda \/ CloseAppEngine \/ JoinHealth \/ Finish)

Spec == Init /\ [][Next]_<<plan, run>>

MixedRefusesBeforeEffects ==
  plan.mode = "mixed" => (run.phase \in {"new", "refused"} /\ ~run.openedSQLite /\ run.selection = "none")
OwnedAdmissionRequiresPreflight ==
  (plan.mode = "postgres" /\ run.selection = "postgres") =>
    plan.localAuth /\ plan.topology /\ plan.hostAccepted /\ plan.pinnedBundle /\ plan.privateState
DefaultRemainsMetadata ==
  (plan.mode = "metadata" /\ run.openedSQLite) => run.selection = "metadata"
IndependentClosersAttempted ==
  run.phase \in {"health", "sqlite", "returned"} => run.triedLambda /\ run.triedAppEngine
ObservedJoinBeforeReturn ==
  run.phase = "returned" => run.joinedHealth
DebtKeepsSQLite ==
  run.ownerDebt => ~run.triedSQLiteClose
CleanAttemptsSQLiteClose ==
  (run.phase = "returned" /\ ~run.ownerDebt) => run.triedSQLiteClose
ReportsCloseCauses ==
  run.phase = "returned" =>
    (run.reportedDebt = run.ownerDebt /\ run.reportedIndependent = (run.lambdaError \/ run.appEngineError))
============================================================================
