-------------------------- MODULE LambdaSourceEdit --------------------------
EXTENDS Naturals, FiniteSets
CONSTANT Revisions
VARIABLE result
vars == <<result>>
Init == result = {}
Read(allowGet, bounded) ==
 /\ allowGet /\ ~bounded
 /\ result' = {[kind |-> "read", get |-> allowGet, update |-> FALSE,
                 bounded |-> bounded, revision |-> 0, expected |-> 0,
                 valid |-> TRUE, helper |-> 1, config |-> 1]}
Write(allowGet, allowUpdate, bounded, revision, expected, valid) ==
 /\ allowGet /\ allowUpdate /\ ~bounded
 /\ revision = expected /\ valid
 /\ result' = {[kind |-> "write", get |-> allowGet, update |-> allowUpdate,
                 bounded |-> bounded, revision |-> revision, expected |-> expected,
                 valid |-> valid, helper |-> 1, config |-> 1]}
Next == (\E g,b \in BOOLEAN: Read(g,b)) \/
        (\E g,u,b,v \in BOOLEAN, r,e \in Revisions: Write(g,u,b,r,e,v)) \/ UNCHANGED vars
TypeOK == result \subseteq [kind: {"read","write"}, get: BOOLEAN, update: BOOLEAN,
 bounded: BOOLEAN, revision: Revisions \cup {0}, expected: Revisions \cup {0},
 valid: BOOLEAN, helper: {0,1}, config: {0,1}]
AuthorizedRead == \A x \in result: x.get /\ ~x.bounded
AuthorizedWrite == \A x \in result: x.kind = "write" => x.update
FreshWrite == \A x \in result: x.kind = "write" => x.revision = x.expected
ValidWrite == \A x \in result: x.kind = "write" => x.valid
PreservedPackage == \A x \in result: x.helper = 1 /\ x.config = 1
=============================================================================
