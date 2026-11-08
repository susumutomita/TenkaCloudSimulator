----------------------- MODULE NativeWireProjection -----------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANT None
Positions == 1..3
Kinds == {"zero", "false", "nil", "empty", "scalar", "bad"}
EmitKind(kind) == kind
Field(i,kind) == [position |-> i, kind |-> kind]
RECURSIVE BuildParts(_,_,_)
BuildParts(values,omitted,i) ==
 IF i > 3 THEN <<>>
 ELSE IF omitted[i] THEN BuildParts(values,omitted,i+1)
 ELSE <<Field(i,EmitKind(values[i]))>> \o BuildParts(values,omitted,i+1)
Expected(values,omitted) ==
 (IF omitted[1] THEN <<>> ELSE <<[position |-> 1, kind |-> values[1]]>>) \o
 (IF omitted[2] THEN <<>> ELSE <<[position |-> 2, kind |-> values[2]]>>) \o
 (IF omitted[3] THEN <<>> ELSE <<[position |-> 3, kind |-> values[3]]>>)
ExpectedError(values,omitted) ==
 (~omitted[1] /\ values[1] = "bad") \/
 (~omitted[2] /\ values[2] = "bad") \/
 (~omitted[3] /\ values[3] = "bad")
BadIncluded(values,omitted) == \E i \in Positions : ~omitted[i] /\ values[i] = "bad"
FirstBad(values,omitted) == CHOOSE i \in Positions :
 ~omitted[i] /\ values[i] = "bad" /\
 (\A j \in Positions : (~omitted[j] /\ values[j] = "bad") => i <= j)
VARIABLES done, values, omitted, failed, published
vars == <<done,values,omitted,failed,published>>
Init == /\ done = FALSE /\ values = [i \in Positions |-> "zero"]
        /\ omitted = [i \in Positions |-> FALSE]
        /\ failed = FALSE /\ published = None
Build(v,o) ==
 LET error == BadIncluded(v,o)
     parts == BuildParts(v,o,1)
 IN /\ ~done /\ done' = TRUE /\ values' = v /\ omitted' = o
    /\ failed' = error
    /\ published' = IF error THEN None ELSE parts
Next == \E v \in [Positions -> Kinds], o \in [Positions -> BOOLEAN] : Build(v,o)
TypeOK == /\ done \in BOOLEAN /\ values \in [Positions -> Kinds]
          /\ omitted \in [Positions -> BOOLEAN] /\ failed \in BOOLEAN
BadValueRefused == done => failed = ExpectedError(values,omitted)
FailureNoPartial == (done /\ failed) => published = None
OmissionExact == (done /\ ~failed) =>
 {published[i].position : i \in 1..Len(published)} = {j \in Positions : ~omitted[j]}
OrderExact == (done /\ ~failed) =>
 \A i,j \in 1..Len(published) : i < j => published[i].position < published[j].position
ValueKindsPreserved == (done /\ ~failed) =>
 \A i \in 1..Len(published) : published[i].kind = values[published[i].position]
CompleteProjection == (done /\ ~failed) => published = Expected(values,omitted)
SkippedBadDoesNotFail ==
 (done /\ \A i \in Positions : values[i] = "bad" => omitted[i]) => ~failed
Spec == Init /\ [][Next]_vars
=============================================================================
