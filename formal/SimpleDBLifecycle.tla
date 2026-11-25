------------------------ MODULE SimpleDBLifecycle ------------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Scopes, Names, Values, MaxSteps
ASSUME /\ Cardinality(Scopes) = 2
       /\ Cardinality(Names) = 2
       /\ Cardinality(Values) = 2
       /\ MaxSteps \in Nat

\* One domain and one item per scope. Names/values stand for valid parsed input.
\* A request updates one attribute; its set-valued input represents repeated
\* Name entries. SQL, parser/XML/Base64, IAM evaluation, signing, pagination,
\* cryptography and SDK behavior are deliberately not modeled.
EmptyAttrs == [n \in Names |-> {}]
Empty == [domain |-> FALSE, item |-> FALSE, attrs |-> EmptyAttrs]
RowType == [domain : BOOLEAN, item : BOOLEAN,
            attrs : [Names -> SUBSET Values]]
StateType == [Scopes -> RowType]
Kinds == {"Create", "Put", "Delete"}
VARIABLES disk, visible, lastAck, phase, target, kind, name, supplied,
          replacing, previous, expected, result, authorized, steps
vars == <<disk, visible, lastAck, phase, target, kind, name, supplied,
          replacing, previous, expected, result, authorized, steps>>

Apply(old, s, k, n, vs, rep) ==
 CASE k = "Create" -> [old EXCEPT ![s].domain = TRUE]
   [] k = "Delete" -> [old EXCEPT ![s] = Empty]
   [] k = "Put" ->
        [old EXCEPT ![s].item = TRUE,
                    ![s].attrs[n] = IF rep THEN vs ELSE @ \cup vs]

Init ==
 /\ disk = [s \in Scopes |-> Empty]
 /\ visible = disk /\ lastAck = disk
 /\ phase = "Idle" /\ target \in Scopes /\ kind = "Create"
 /\ name \in Names /\ supplied = {} /\ replacing = FALSE
 /\ previous = disk /\ expected = disk
 /\ result = "Init" /\ authorized = FALSE /\ steps = 0

\* Admission is a trusted boolean input, not a model of authentication.
\* Invalid input and absent parent are refused before staging a write.
Begin(s, k, n, vs, rep, auth, valid) ==
 /\ phase = "Idle" /\ steps < MaxSteps
 /\ target' = s /\ kind' = k /\ name' = n
 /\ supplied' = vs /\ replacing' = rep /\ authorized' = auth
 /\ previous' = disk
 /\ LET accepted == auth /\ valid /\ (k # "Put" \/ disk[s].domain)
        candidate == Apply(disk, s, k, n, vs, rep)
    IN /\ phase' = IF accepted THEN "Pending" ELSE "Idle"
       /\ expected' = IF accepted THEN candidate ELSE disk
       /\ result' = IF accepted THEN "Pending" ELSE "Rejected"
 /\ UNCHANGED <<disk, visible, lastAck>>
 /\ steps' = steps + 1

Commit ==
 /\ phase = "Pending" /\ steps < MaxSteps
 /\ disk' = expected /\ visible' = expected /\ lastAck' = expected
 /\ phase' = "Idle" /\ result' = "Success" /\ steps' = steps + 1
 /\ UNCHANGED <<target, kind, name, supplied, replacing, previous,
                expected, authorized>>

Fail ==
 /\ phase = "Pending" /\ steps < MaxSteps
 /\ UNCHANGED <<disk, visible, lastAck, target, kind, name, supplied,
                replacing, previous, expected, authorized>>
 /\ phase' = "Idle" /\ result' = "Failed" /\ steps' = steps + 1

\* Restart abandons a staged transaction, then reads only committed state.
Restart ==
 /\ steps < MaxSteps
 /\ visible' = disk /\ previous' = disk /\ expected' = disk
 /\ UNCHANGED <<disk, lastAck, target, kind, name, supplied,
                replacing, authorized>>
 /\ phase' = "Idle" /\ result' = "Restarted" /\ steps' = steps + 1

\* Canonicalize ignored inputs for Create/Delete, not their possible states.
\* Put still enumerates both names, every nonempty value subset and replace flag.
\* This reduces Begin choices from 288 to 112 per Idle state:
\* 2 scopes * 2 kinds * 2 auth * 2 valid = 16, plus
\* 2 scopes * 2 names * 3 subsets * 2 replace * 2 auth * 2 valid = 96.
\* Nine steps permit the complete witness below. This bound is not an observed
\* TLC state count or a cost guarantee; official pinned TLC has NOT RUN.
Next ==
 \/ \E s \in Scopes, k \in {"Create", "Delete"},
        auth \in BOOLEAN, valid \in BOOLEAN:
        Begin(s, k, CHOOSE n \in Names : TRUE,
              {CHOOSE v \in Values : TRUE}, FALSE, auth, valid)
 \/ \E s \in Scopes, n \in Names, vs \in (SUBSET Values) \ {{ }},
        rep \in BOOLEAN, auth \in BOOLEAN, valid \in BOOLEAN:
        Begin(s, "Put", n, vs, rep, auth, valid)
 \/ Commit \/ Fail \/ Restart
 \/ /\ steps = MaxSteps /\ UNCHANGED vars

TypeOK ==
 /\ disk \in StateType /\ visible \in StateType /\ lastAck \in StateType
 /\ previous \in StateType /\ expected \in StateType
 /\ phase \in {"Idle", "Pending"} /\ target \in Scopes
 /\ kind \in Kinds /\ name \in Names /\ supplied \subseteq Values
 /\ replacing \in BOOLEAN /\ authorized \in BOOLEAN
 /\ steps \in 0..MaxSteps
 /\ result \in {"Init", "Pending", "Rejected", "Failed", "Success", "Restarted"}

NoOrphans ==
 \A s \in Scopes:
   /\ (~disk[s].domain => ~disk[s].item /\ disk[s].attrs = EmptyAttrs)
   /\ (~disk[s].item => disk[s].attrs = EmptyAttrs)
PendingInvisible == phase = "Pending" => disk = previous /\ visible = previous
RefusalUnchanged == result \in {"Rejected", "Failed"} => disk = previous /\ visible = previous
AuthorizedCommit == result = "Success" => authorized
PeerFrame == result = "Success" => \A s \in Scopes \ {target}: disk[s] = previous[s]
CommittedExact == result = "Success" => disk = expected /\ visible = expected
DurableAck == disk = lastAck
RestartExact == result = "Restarted" => visible = lastAck
DeleteEmpty == result = "Success" /\ kind = "Delete" => disk[target] = Empty
CreateIdempotent ==
 result = "Success" /\ kind = "Create" /\ previous[target].domain =>
 disk = previous
PutSetExact ==
 result = "Success" /\ kind = "Put" =>
 /\ disk[target].attrs[name] =
       IF replacing THEN supplied ELSE previous[target].attrs[name] \cup supplied
 /\ \A n \in Names \ {name}: disk[target].attrs[n] = previous[target].attrs[n]

\* STATIC REACHABILITY WITNESS (not a TLC execution receipt)
\* Choose s=scope1, n=name1, v=value1 for Put. Let cn=CHOOSE n \in Names:TRUE
\* and cv=CHOOSE v \in Values:TRUE, exactly as Next's Create/Delete branch.
\* All Begin auth/valid inputs are TRUE.
\* Initial: both scopes Empty; phase Idle, steps=0.
\* 1 Begin(s,Create,cn,{cv},FALSE,TRUE,TRUE): Pending, expected domain present.
\* 2 Commit: disk[s].domain=TRUE, item=FALSE; lastAck=disk; Idle.
\* 3 Begin(s,Put,n,{v},FALSE,TRUE,TRUE): Pending, parent guard holds.
\* 4 Commit: disk[s].item=TRUE, attrs[n]={v}; lastAck=disk; Idle.
\* 5 Begin(s,Delete,cn,{cv},FALSE,TRUE,TRUE): Pending; previous retains the item.
\* 6 Commit: disk[s]=Empty; previous still contains the pre-delete item.
\* 7 Restart: visible[s]=Empty, previous=expected=disk=lastAck; Idle.
\* 8 Begin(s,Create,cn,{cv},FALSE,TRUE,TRUE): Pending; expected item remains FALSE.
\* 9 Commit: domain TRUE, item FALSE, all attrs empty; no old data reappears.
\* Every numbered action starts with steps<9; scope2 remains Empty throughout.
\* Step8 is only staging: this two-phase model needs step9 for recreation ACK.
\* This is a symbolic enabled-action trace checked by inspection, not enumeration.

\* EXACT-ARTIFACT MUTATION PROPOSAL (not installed, registered or executed)
\* Edits below are local to the named operator; other conjuncts stay unchanged.
\* Registered controls must assert the actual first violation after TLC runs.
\* M1 Begin accepted: remove "auth /\ ". Path: denied Create Begin; Commit (2).
\*    Target AuthorizedCommit (RefusalUnchanged need not fire: result is Pending).
\* M2 Begin: replace UNCHANGED visible with visible'=expected'.
\*    Path Create Begin (1); target PendingInvisible.
\* M3 Fail: replace UNCHANGED disk with disk'=expected.
\*    Path Create Begin; Fail (2); target RefusalUnchanged or DurableAck.
\* M4 Commit disk': replace expected with [expected EXCEPT ![peer]=Empty],
\*    peer=CHOOSE t \in Scopes \ {target}:TRUE. Path Create(peer), Commit,
\*    Begin Create(target), Commit (4); target PeerFrame or CommittedExact.
\* M5 Commit: replace disk'=expected with UNCHANGED disk; retain lastAck'=expected.
\*    Path Create Begin; Commit (2); target DurableAck or CommittedExact.
\* M6 Restart: replace visible'=disk with visible'=previous.
\*    Witness steps1..7 above; target RestartExact (resurrects pre-delete item).
\* M7 Apply/Delete: replace [old EXCEPT ![s]=Empty] with
\*    [old EXCEPT ![s].domain=FALSE]. Witness steps1..6; target NoOrphans/DeleteEmpty.
\* M8 Apply/Create: also set ![s].item=FALSE, ![s].attrs=EmptyAttrs.
\*    Path Create/Commit, Put/Commit, Create/Commit (6); target CreateIdempotent.
\* M9 Apply/Put: change ELSE @ \cup vs to ELSE vs.
\*    Path Create/Commit, Put({value1},FALSE)/Commit,
\*    Put({value2},FALSE)/Commit (6); target PutSetExact.
\* M10 Apply/Put: change THEN vs to THEN @ \cup vs.
\*    Path Create/Commit, Put({value1},FALSE)/Commit,
\*    Put({value2},TRUE)/Commit (6); target PutSetExact.
\* M11 Apply/Put: clear attrs[other] (other=CHOOSE m \in Names \ {n}:TRUE).
\*    Path Create/Commit, Put(name1)/Commit, Put(name2)/Commit (6);
\*    target PutSetExact's other-name frame.
\* M12 Begin accepted: remove parent term "(k # Put \/ disk[s].domain)".
\*    Path Put on initial absent domain; Commit (2); target NoOrphans.
\* Parser, XML/Base64, real IAM, SQL refinement, SDK and pagination are nonclaims.

=============================================================================
