------------------------- MODULE WAFRequestEvaluation -------------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANT RuleCount
ASSUME RuleCount \in Nat \ {0}
Rules == 1..RuleCount
Actions == {"COUNT", "ALLOW", "BLOCK"}
TerminalActions == {"ALLOW", "BLOCK"}
VARIABLES priorities, actions, matched, defaultAction,
          aclExists, stageExists, associated,
          phase, snapshotAssociated, pc, counted, decision, terminating
vars == <<priorities, actions, matched, defaultAction,
          aclExists, stageExists, associated,
          phase, snapshotAssociated, pc, counted, decision, terminating>>
configuration == <<priorities, actions, matched, defaultAction>>
lifecycle == <<aclExists, stageExists, associated>>
request == <<phase, snapshotAssociated, pc, counted, decision, terminating>>

\* Unique priorities are validated before the real evaluator sorts rules.
UniquePriorities == \A a,b \in Rules: priorities[a] = priorities[b] => a = b
RuleAt(k) == CHOOSE r \in Rules: priorities[r] = k
CurrentRule == RuleAt(pc)
Minimum(s) == CHOOSE m \in s: \A x \in s: m <= x
TerminalPriorities == {priorities[r]: r \in {r \in matched: actions[r] \in TerminalActions}}
FirstTerminalPriority == Minimum(TerminalPriorities \cup {RuleCount + 1})
RECURSIVE CountPrefix(_)
CountPrefix(k) == IF k = 0 THEN <<>> ELSE
                 CountPrefix(k-1) \o
                 (IF RuleAt(k) \in matched /\ actions[RuleAt(k)] = "COUNT"
                  THEN <<RuleAt(k)>> ELSE <<>>)
ExpectedCounts == CountPrefix(FirstTerminalPriority - 1)

Init == /\ priorities \in [Rules -> Rules] /\ UniquePriorities
        /\ actions \in [Rules -> Actions] /\ matched \in SUBSET Rules
        /\ defaultAction \in TerminalActions
        /\ aclExists = FALSE /\ stageExists = FALSE /\ associated = FALSE
        /\ phase = "idle" /\ snapshotAssociated = FALSE
        /\ pc = 1 /\ counted = <<>> /\ decision = "PENDING" /\ terminating = 0
CreateACL == /\ ~aclExists /\ aclExists' = TRUE
             /\ UNCHANGED <<configuration, stageExists, associated, request>>
CreateStage == /\ ~stageExists /\ stageExists' = TRUE
               /\ UNCHANGED <<configuration, aclExists, associated, request>>
Associate == /\ aclExists /\ stageExists /\ ~associated /\ associated' = TRUE
             /\ UNCHANGED <<configuration, aclExists, stageExists, request>>
Disassociate == /\ associated /\ associated' = FALSE
                /\ UNCHANGED <<configuration, aclExists, stageExists, request>>
DeleteACL == /\ aclExists /\ ~associated /\ aclExists' = FALSE
             /\ UNCHANGED <<configuration, stageExists, associated, request>>
DeleteStage == /\ stageExists /\ ~associated /\ stageExists' = FALSE
               /\ UNCHANGED <<configuration, aclExists, associated, request>>

\* The real Evaluate reads one ACL document; a running request keeps that
\* snapshot even if association state changes after the read. Match results are
\* arbitrary booleans, abstracting the concrete matcher/transform/SQLi engine.
BeginRequest == /\ phase = "idle" /\ stageExists
                /\ snapshotAssociated' = associated
                /\ phase' = IF associated THEN "evaluating" ELSE "done"
                /\ decision' = IF associated THEN "PENDING" ELSE "ALLOW"
                /\ pc' = 1 /\ counted' = <<>> /\ terminating' = 0
                /\ UNCHANGED <<configuration, lifecycle>>
SkipRule == /\ phase = "evaluating" /\ pc <= RuleCount
            /\ CurrentRule \notin matched /\ pc' = pc + 1
            /\ UNCHANGED <<configuration, lifecycle, phase, snapshotAssociated,
                           counted, decision, terminating>>
CountRule == /\ phase = "evaluating" /\ pc <= RuleCount
             /\ CurrentRule \in matched /\ actions[CurrentRule] = "COUNT"
             /\ counted' = Append(counted, CurrentRule) /\ pc' = pc + 1
             /\ UNCHANGED <<configuration, lifecycle, phase, snapshotAssociated,
                            decision, terminating>>
TerminateRule == /\ phase = "evaluating" /\ pc <= RuleCount
                 /\ CurrentRule \in matched /\ actions[CurrentRule] \in TerminalActions
                 /\ phase' = "done" /\ decision' = actions[CurrentRule]
                 /\ terminating' = CurrentRule
                 /\ UNCHANGED <<configuration, lifecycle, snapshotAssociated, pc, counted>>
FinishDefault == /\ phase = "evaluating" /\ pc > RuleCount
                 /\ phase' = "done" /\ decision' = defaultAction
                 /\ terminating' = (RuleCount+1)
                 /\ UNCHANGED <<configuration, lifecycle, snapshotAssociated, pc, counted>>
ResetRequest == /\ phase = "done" /\ phase' = "idle"
                /\ snapshotAssociated' = FALSE /\ pc' = 1 /\ counted' = <<>>
                /\ decision' = "PENDING" /\ terminating' = 0
                /\ UNCHANGED <<configuration, lifecycle>>
Next == CreateACL \/ CreateStage \/ Associate \/ Disassociate \/ DeleteACL \/ DeleteStage
        \/ BeginRequest \/ SkipRule \/ CountRule \/ TerminateRule \/ FinishDefault
        \/ ResetRequest \/ UNCHANGED vars

TypeOK == /\ priorities \in [Rules -> Rules] /\ UniquePriorities
          /\ actions \in [Rules -> Actions] /\ matched \in SUBSET Rules
          /\ defaultAction \in TerminalActions
          /\ aclExists \in BOOLEAN /\ stageExists \in BOOLEAN /\ associated \in BOOLEAN
          /\ phase \in {"idle", "evaluating", "done"} /\ snapshotAssociated \in BOOLEAN
          /\ pc \in 1..(RuleCount+1) /\ counted \in Seq(Rules) /\ Len(counted) <= RuleCount
          /\ decision \in TerminalActions \cup {"PENDING"}
          /\ terminating \in 0..(RuleCount+1)
NoAssociationOrphans == associated => aclExists /\ stageExists
CountContinues == phase = "evaluating" =>
                  snapshotAssociated /\ decision = "PENDING" /\ terminating = 0
                  /\ pc <= FirstTerminalPriority /\ counted = CountPrefix(pc-1)
FirstTerminatingRuleWins == (phase = "done" /\ snapshotAssociated) =>
                  /\ counted = ExpectedCounts
                  /\ IF FirstTerminalPriority <= RuleCount
                     THEN /\ decision = actions[RuleAt(FirstTerminalPriority)]
                          /\ terminating = RuleAt(FirstTerminalPriority)
                     ELSE /\ decision = defaultAction /\ terminating = (RuleCount+1)
UnassociatedAllows == (phase = "done" /\ ~snapshotAssociated) =>
                      decision = "ALLOW" /\ terminating = 0 /\ counted = <<>>
=============================================================================
