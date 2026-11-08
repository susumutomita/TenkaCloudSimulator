---------------------- MODULE CFNParameterSnapshot ----------------------
EXTENDS Naturals
VARIABLES input, phase, source, changed, captured, snapshot, reads, rows, reply
vars == <<input, phase, source, changed, captured, snapshot, reads, rows, reply>>
Keys == {"/local/a", "/local/b"}
Values == {"valueA", "valueB"}
Cells == Values \cup {"missing", "wrongType"}
Sources == [Keys -> Cells]
Empty == [k \in Keys |-> "none"]
Inputs == [defect : {"valid", "carrier", "keyConstraint", "plan"},
           allowed : SUBSET Keys, replay : {"match", "mismatch", "denied"}]
Absent == [stack |-> FALSE, resources |-> 0, events |-> 0,
           keys |-> {}, values |-> Empty, ref |-> Empty, condition |-> FALSE]
InputOK(i) == i.defect \notin {"carrier", "keyConstraint"}
AllAllowed(i) == i.allowed=Keys
StringSources(s) == \A k \in Keys: s[k] \in Values
PlanOK(i) == i.defect # "plan"

Init == /\ input \in Inputs /\ source \in Sources /\ phase="Validate"
 /\ changed=FALSE /\ captured=Empty /\ snapshot=Empty /\ reads=0
 /\ rows=Absent /\ reply="none"
Validate == /\ phase="Validate"
 /\ phase'=(IF InputOK(input) THEN "Authorize" ELSE "Failed")
 /\ UNCHANGED <<input,source,changed,captured,snapshot,reads,rows,reply>>
Authorize == /\ phase="Authorize"
 /\ phase'=(IF AllAllowed(input) THEN "Read" ELSE "Failed")
 /\ UNCHANGED <<input,source,changed,captured,snapshot,reads,rows,reply>>
Read == /\ phase="Read" /\ reads'=reads+1
 /\ captured'=source /\ snapshot'=source
 /\ phase'=(IF StringSources(source) /\ PlanOK(input) THEN "Publish" ELSE "Failed")
 /\ UNCHANGED <<input,source,changed,rows,reply>>
Publish == /\ phase="Publish"
 /\ rows'=[stack |-> TRUE, resources |-> 1, events |-> 1,
            keys |-> Keys, values |-> snapshot, ref |-> snapshot,
            condition |-> (snapshot["/local/a"]="valueA")]
 /\ phase'="Stored"
 /\ UNCHANGED <<input,source,changed,captured,snapshot,reads,reply>>
(* One arbitrary external replacement/deletion may interleave before commit
   or after creation. No requirement to recapture the latest source at commit. *)
ChangeSource == /\ phase \in {"Publish", "Stored"} /\ ~changed
 /\ source' \in Sources /\ changed'=TRUE
 /\ UNCHANGED <<input,phase,captured,snapshot,reads,rows,reply>>
Replay == /\ phase="Stored"
 /\ reply'=(IF input.replay="match" THEN "existingID" ELSE "rejected")
 /\ phase'="Done"
 /\ UNCHANGED <<input,source,changed,captured,snapshot,reads,rows>>
Next == Validate \/ Authorize \/ Read \/ Publish \/ ChangeSource \/ Replay

TypeOK == /\ input \in Inputs /\ source \in Sources /\ changed \in BOOLEAN
 /\ phase \in {"Validate","Authorize","Read","Publish","Stored","Done","Failed"}
 /\ captured \in Sources \cup {Empty} /\ snapshot \in Sources \cup {Empty}
 /\ reads \in 0..2 /\ reply \in {"none","existingID","rejected"}
 /\ rows \in [stack : BOOLEAN, resources : 0..1, events : 0..1,
                keys : SUBSET Keys, values : Sources \cup {Empty},
                ref : Sources \cup {Empty}, condition : BOOLEAN]
(* Literal obligations below do not invoke the operational admission helpers. *)
ValidateBeforeRead == reads>0 => input.defect \notin {"carrier","keyConstraint"}
AuthorizeEveryKeyBeforeRead == reads>0 => \A k \in Keys: k \in input.allowed
OneAtomicSnapshot == reads>0 => snapshot=captured
NoFailedWrites == phase="Failed" => rows=Absent
NoEarlyWrites == phase \notin {"Stored","Done"} => rows=Absent
AdmissionExact == rows.stack =>
 /\ input.defect="valid" /\ input.allowed=Keys
 /\ \A k \in Keys: captured[k] \in {"valueA","valueB"}
(* Resolved values deliberately differ from keys: key constraints must not
   accidentally be applied to the values returned by the source lookup. *)
ValidInputsPublish == phase="Failed" =>
 input.defect # "valid" \/ input.allowed # Keys \/
 (reads=1 /\ \E k \in Keys: captured[k] \notin {"valueA","valueB"})
KeysPreserved == rows.stack => rows.keys={"/local/a","/local/b"}
SnapshotFrozen == rows.stack => rows.values=captured
RefsUseResolvedValues == rows.stack => rows.ref=captured
ConditionsUseResolvedValues == rows.stack =>
 rows.condition=(captured["/local/a"]="valueA")
CompletePublication == rows.stack => rows.resources=1 /\ rows.events=1
ReplayNoSourceRead == reads<=1
ReplayIdentityExact == phase="Done" =>
 reply=(CASE input.replay="match" -> "existingID" [] OTHER -> "rejected")
ReplayPreservesStack == phase="Done" => rows.stack /\ rows.values=captured

(* Two keys, two String values, absent/wrong-type witnesses; four admission
   classes, four permission subsets, three replay outcomes, one source edit.
   SQL/IAM/parser/real AMI behavior and crash recovery are not modeled. *)
Spec == Init /\ [][Next]_vars
=============================================================================
