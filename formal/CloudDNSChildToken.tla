----------------------- MODULE CloudDNSChildToken -----------------------
EXTENDS Naturals, FiniteSets
CONSTANTS Projects, Subjects
ASSUME /\ Cardinality(Projects) = 2 /\ Cardinality(Subjects) = 2

\* Generation and authorization projection of local child page-token scope.
\* Deletion overapproximates storage: it need not check for user records.
\* Issued tokens are genuine by assumption; no encoding/HMAC is modeled.
VARIABLES owners, zones, tokens, tokenLast
vars == <<owners, zones, tokens, tokenLast>>
FirstProject == CHOOSE p \in Projects : TRUE
EmptyZone == [present |-> FALSE, generation |-> 0]
Init ==
 /\ owners \in [Projects -> Subjects]
 /\ zones = [p \in Projects |-> EmptyZone]
 /\ tokens = {}
 /\ tokenLast = [accepted |-> FALSE, target |-> FirstProject,
       tokenProject |-> FirstProject, tokenGeneration |-> 0,
       seenGeneration |-> 0, present |-> FALSE, authorized |-> FALSE]
Create(p, actor) ==
 /\ owners[p] = actor /\ ~zones[p].present /\ zones[p].generation < 2
 /\ zones' = [zones EXCEPT ![p] = [present |-> TRUE,
       generation |-> zones[p].generation + 1]]
 /\ UNCHANGED <<owners, tokens, tokenLast>>
Delete(p, actor) ==
 /\ owners[p] = actor /\ zones[p].present
 /\ zones' = [zones EXCEPT ![p].present = FALSE]
 /\ UNCHANGED <<owners, tokens, tokenLast>>
IssueToken(p) ==
 /\ zones[p].present
 /\ tokens' = tokens \cup {[project |-> p, generation |-> zones[p].generation]}
 /\ UNCHANGED <<owners, zones, tokenLast>>
UseToken(p, actor, token) ==
 LET allowed == zones[p].present /\ owners[p] = actor
                /\ token.project = p /\ token.generation = zones[p].generation
 IN
 /\ token \in tokens
 /\ tokenLast' = [accepted |-> allowed, target |-> p,
       tokenProject |-> token.project, tokenGeneration |-> token.generation,
       seenGeneration |-> zones[p].generation, present |-> zones[p].present,
       authorized |-> owners[p] = actor]
 /\ UNCHANGED <<owners, zones, tokens>>
Next ==
 (\E p \in Projects :
     (\E actor \in Subjects : Create(p,actor) \/ Delete(p,actor))
     \/ IssueToken(p)
     \/ (\E actor \in Subjects, token \in tokens : UseToken(p,actor,token)))
 \/ UNCHANGED vars

TypeOK ==
 /\ owners \in [Projects -> Subjects]
 /\ zones \in [Projects -> [present:BOOLEAN,generation:0..2]]
 /\ tokens \subseteq [project:Projects,generation:1..2]
NoOldGenerationReuse == tokenLast.accepted =>
 /\ tokenLast.present /\ tokenLast.authorized
 /\ tokenLast.tokenProject = tokenLast.target
 /\ tokenLast.tokenGeneration = tokenLast.seenGeneration
=============================================================================
