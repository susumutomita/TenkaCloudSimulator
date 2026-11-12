---------------- MODULE IAMConditionEvaluation ----------------
EXTENDS Naturals, FiniteSets
VARIABLES s
vars == <<s>>
Cases == {"allow", "deny", "absentContext", "duplicateContext", "missingKey",
 "badOperator", "null", "duplicateClause", "unknownVariable", "wrongTeam",
 "requestSpoof", "wildVariable", "literalWildcard", "likeWildcard", "deniedMissingContext"}
Input(c) == [defect |-> CASE c="badOperator" -> "operator" [] c="null" -> "null"
 [] c="duplicateClause" -> "duplicate" [] c="unknownVariable" -> "variable" [] OTHER -> "none",
 trusted |-> c \notin {"absentContext","deniedMissingContext"},
 user |-> IF c="wildVariable" THEN "a*" ELSE "a",
 team |-> CASE c="missingKey" -> "missing" [] c="duplicateContext" -> "duplicate" [] c \in {"wrongTeam","requestSpoof"} -> "b" [] OTHER -> "a",
 pattern |-> CASE c \in {"literalWildcard","likeWildcard"} -> "a*" [] OTHER -> "${userid}",
 resource |-> IF c="wildVariable" THEN "abc" ELSE "a",
 op |-> IF c="likeWildcard" THEN "Like" ELSE "Equals",
 deny |-> c \in {"deny","deniedMissingContext"}]
ParseOK(i) == i.defect="none"
ContextOK(i) == i.trusted
KeyOK(i) == i.team \notin {"missing","duplicate"}
BoundTeam(i) == i.team
Match(i) == IF i.pattern="${userid}" THEN i.resource=i.user
 ELSE IF i.op="Like" THEN i.resource \in {"a","abc"}
 ELSE i.resource=i.pattern
(* Equal/Like two-term AND is explicit. Value disjunction uses two literal
 witnesses {a,c}; variable content remains literal, never wildcard syntax. *)
TermsOK(i) == KeyOK(i) /\ BoundTeam(i) \in {"a","c"} /\ Match(i)
Init == \E c \in Cases: s=[case |-> c, input |-> Input(c), phase |-> "parse",
 parsed |-> FALSE, matched |-> FALSE, decision |-> "pending"]
Parse == /\ s.phase="parse"
 /\ s'=[s EXCEPT !.parsed=ParseOK(s.input), !.phase=IF ParseOK(s.input) THEN "context" ELSE "done",
 !.decision=IF ParseOK(s.input) THEN "pending" ELSE "implicitDeny"]
Context == /\ s.phase="context"
 /\ s'=[s EXCEPT !.phase=IF ContextOK(s.input) THEN "match" ELSE "done",
 !.decision=IF ContextOK(s.input) THEN "pending" ELSE "implicitDeny"]
Evaluate == /\ s.phase="match"
 /\ s'=[s EXCEPT !.matched=TermsOK(s.input), !.phase="resolve"]
Resolve == /\ s.phase="resolve"
 /\ s'=[s EXCEPT !.decision=IF s.input.deny THEN "explicitDeny" ELSE IF s.matched THEN "allowed" ELSE "implicitDeny", !.phase="done"]
Next == Parse \/ Context \/ Evaluate \/ Resolve
TypeOK == /\ s.case \in Cases /\ s.input=Input(s.case)
 /\ s.phase \in {"parse","context","match","resolve","done"}
 /\ s.parsed \in BOOLEAN /\ s.matched \in BOOLEAN
 /\ s.decision \in {"pending","allowed","implicitDeny","explicitDeny"}
StrictAdmission == s.parsed => s.input.defect="none"
TrustedContextRequired == s.decision="allowed" => s.input.trusted
AbsentContextDenyNotDiscarded == s.case="deniedMissingContext" /\ s.phase="done" => s.decision="implicitDeny"
MissingOrDuplicateKeyRefuses == s.matched => s.input.team \notin {"missing","duplicate"}
ProviderContextOnly == s.case="requestSpoof" => ~s.matched
AllTermsRequired == s.matched => s.input.team \in {"a","c"}
VariableIsLiteral == s.case="wildVariable" => ~s.matched
EqualityNotGlob == s.case="literalWildcard" => ~s.matched
ExplicitDenyPrecedence == s.case="deny" /\ s.phase="done" => s.decision="explicitDeny"
AllowedWitness == s.phase="done" /\ s.case \in {"allow","likeWildcard"} => s.decision="allowed"
Spec == Init /\ [][Next]_vars
=============================================================================
