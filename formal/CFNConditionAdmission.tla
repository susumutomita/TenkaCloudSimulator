---------------------- MODULE CFNConditionAdmission ----------------------
EXTENDS Naturals, Sequences, FiniteSets
VARIABLES input, phase, selected, result
vars == <<input, phase, selected, result>>
None == "none"
Values == {"left", "right", "NoValue"}
Defects == {"valid", "definition", "default", "supplied", "missing",
            "unknownParameter", "cycle", "unknownCondition", "conditionSyntax",
            "disabledDeclaration", "thenSyntax", "elseSyntax", "selectedLink"}
Inputs == [defect : Defects, enabled : BOOLEAN, thenValue : Values,
           elseValue : Values]
Empty == [present |-> FALSE, mapValue |-> <<>>, listValue |-> <<>>,
          resources |-> {}, outputs |-> {}]

(* Operational gates are deliberately distinct from the literal expected
   admission formula below. Each defect is a finite witness, not a parser. *)
ParametersOK(i) == i.defect \notin
 {"definition", "default", "supplied", "missing", "unknownParameter"}
ConditionsOK(i) == i.defect \notin {"cycle", "unknownCondition", "conditionSyntax"}
DeclarationsOK(i) == i.defect # "disabledDeclaration"
BranchesOK(i) == i.defect \notin {"thenSyntax", "elseSyntax"}
LinksOK(i) == i.defect # "selectedLink"
Arm(i) == IF i.enabled THEN i.thenValue ELSE i.elseValue
Keep(v) == IF v="NoValue" THEN <<>> ELSE <<v>>
ResourceSet(i) == IF i.enabled THEN {"BaseRole", "ScoreItem"} ELSE {"BaseRole"}
OutputSet(i) == IF i.enabled THEN {"Public", "Coordination"} ELSE {"Public"}
Plan(i,v) == [present |-> TRUE, mapValue |-> Keep(v), listValue |-> Keep(v),
             resources |-> ResourceSet(i), outputs |-> OutputSet(i)]
(* Expected values do not call any operational gate, Arm, Keep, or Plan. *)
ExpectedValid(i) == i.defect="valid"
ExpectedArm(i) == CASE i.enabled=TRUE -> i.thenValue [] OTHER -> i.elseValue
ExpectedValue(i) == IF ExpectedArm(i)="NoValue" THEN <<>> ELSE <<ExpectedArm(i)>>
ExpectedResources(i) == IF i.enabled THEN {"BaseRole", "ScoreItem"} ELSE {"BaseRole"}
ExpectedOutputs(i) == IF i.enabled THEN {"Public", "Coordination"} ELSE {"Public"}

Init == /\ input \in Inputs /\ phase="Parameters"
        /\ selected=None /\ result=Empty
CheckParameters == /\ phase="Parameters"
 /\ phase'=(IF ParametersOK(input) THEN "Conditions" ELSE "Failed")
 /\ UNCHANGED <<input,selected,result>>
CheckConditions == /\ phase="Conditions"
 /\ phase'=(IF ConditionsOK(input) THEN "Declarations" ELSE "Failed")
 /\ UNCHANGED <<input,selected,result>>
CheckDeclarations == /\ phase="Declarations"
 /\ phase'=(IF DeclarationsOK(input) /\ BranchesOK(input) THEN "Select" ELSE "Failed")
 /\ UNCHANGED <<input,selected,result>>
Select == /\ phase="Select" /\ selected'=Arm(input) /\ phase'="Link"
 /\ UNCHANGED <<input,result>>
CheckLink == /\ phase="Link"
 /\ phase'=(IF LinksOK(input) THEN "Publish" ELSE "Failed")
 /\ UNCHANGED <<input,selected,result>>
Publish == /\ phase="Publish" /\ result'=Plan(input,selected)
 /\ phase'="Done" /\ UNCHANGED <<input,selected>>
Next == CheckParameters \/ CheckConditions \/ CheckDeclarations \/ Select \/ CheckLink \/ Publish

TypeOK == /\ input \in Inputs
 /\ phase \in {"Parameters","Conditions","Declarations","Select","Link","Publish","Done","Failed"}
 /\ selected \in Values \cup {None}
 /\ result.present \in BOOLEAN
 /\ result.mapValue \in {<<>>,<<"left">>,<<"right">>,<<"NoValue">>}
 /\ result.listValue \in {<<>>,<<"left">>,<<"right">>,<<"NoValue">>}
 /\ result.resources \subseteq {"BaseRole","ScoreItem"}
 /\ result.outputs \subseteq {"Public","Coordination"}
ParameterAdmission == phase="Done" => input.defect \notin
 {"definition","default","supplied","missing","unknownParameter"}
AllConditionsValidated == phase="Done" => input.defect \notin
 {"cycle","unknownCondition","conditionSyntax"}
DisabledDeclarationsValidated == phase="Done" => input.defect # "disabledDeclaration"
BothBranchesValidated == phase="Done" => input.defect \notin {"thenSyntax","elseSyntax"}
SelectedLinkValidated == phase="Done" => input.defect # "selectedLink"
NoResultOnInvalid == phase="Failed" => result=Empty
NoEarlyPublication == result.present => phase="Done"
SelectedBranchExact == phase \in {"Link","Publish","Done"} => selected=ExpectedArm(input)
MapNoValuePruned == result.present => result.mapValue=ExpectedValue(input)
ListNoValuePruned == result.present => result.listValue=ExpectedValue(input)
DisabledResourcesAbsent == result.present => result.resources=ExpectedResources(input)
ConditionalOutputsAbsent == result.present => result.outputs=ExpectedOutputs(input)
PlanExact == phase="Done" =>
 /\ ExpectedValid(input) /\ result.present
 /\ result.mapValue=ExpectedValue(input) /\ result.listValue=ExpectedValue(input)
 /\ result.resources=ExpectedResources(input) /\ result.outputs=ExpectedOutputs(input)

(* Bounded input: one abstract parameter family; one equality/alias graph with
   cycle/unknown witnesses; one conditional resource plus an unconditional role;
   one If in each of a map/list; one conditional output. enabled is the abstract
   equality result, not a proved Go evaluator. No real documents/secret values.
   Both branches have independent syntax-defect witnesses. SelectedLink denotes a failing unconditional BaseRole link (for example the
   crypto template trust policy AccountId/ExternalId reference), independent of
   the optional map/list arm. Even a pruned NoValue arm leaves this role link.
   Unused branch resource links are intentionally not read.
   A plan is pure data: no SQL, resource allocation, policy grant, output secrecy,
   NoEcho, deployment, grader execution, numeric/regex/UTF8/YAML proof follows. *)
Spec == Init /\ [][Next]_vars
=============================================================================
