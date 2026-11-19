----------------------- MODULE CloudDNSAtomicChange -----------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANTS Projects, Subjects, None
ASSUME /\ Cardinality(Projects) = 2 /\ Cardinality(Subjects) = 2
       /\ None \notin Projects \cup Subjects

\* Bounded Cloud DNS model. Two projects/subjects (including aliased
\* owners), one zone slot/project, generations 1..2, two user keys plus apex.
\* Values abstract IP and TTL to bits. Three successful changes globally bound
\* exploration; normal history budget two plus one whole-pair deletion cleanup.
\* The global cutoff is NOT a runtime quota. No fairness/eventual-cleanup claim.
\* Trusted owner map is fixed; constructor migration/SQL bootstrap is outside
\* this model. Tokens are assumed genuinely issued; HMAC is not modeled.
\* No SQL/Go refinement, actual triggers, HMAC/HTTP parser, byte accounting,
\* DNS serving/propagation, sockets, SDK or cloud comparison is established.
\* Native correspondence: operations.go applyChange; store.go verifyChangeState;
\* engine.go admission; pagination.go generation scope. Faults below abstract
\* possible final storage drift, not an implementation of SQLite triggers.
Values == {None} \cup [ip : 0..1, ttl : 0..1]
Zero == [ip |-> 0, ttl |-> 0]
EmptyPair == [left |-> None, right |-> None, apex |-> TRUE]
Pair(v) == [left |-> v, right |-> v, apex |-> TRUE]
EmptyZone == [present |-> FALSE, generation |-> 0,
              pair |-> EmptyPair, history |-> <<>>]
InitialDB == [p \in Projects |-> EmptyZone]
FirstProject == CHOOSE p \in Projects : TRUE
FirstSubject == CHOOSE s \in Subjects : TRUE
Receipt(id, gen, mode, value) ==
 [id |-> id, generation |-> gen, mode |-> mode, value |-> value]
NoTx == [phase |-> "idle", project |-> FirstProject,
 claimed |-> FirstProject, actor |-> FirstSubject, mode |-> "put",
 expectedDelete |-> None, value |-> Zero, before |-> InitialDB,
 expectedPair |-> EmptyPair, expectedHistory |-> <<>>,
 candidatePair |-> EmptyPair, candidateHistory |-> <<>>]
Event(kind, before, after, proposal) ==
 [kind |-> kind, before |-> before, after |-> after, proposal |-> proposal]

VARIABLES owners, db, tx, commits, last, tokens, tokenLast
vars == <<owners, db, tx, commits, last, tokens, tokenLast>>
Init ==
 /\ owners \in [Projects -> Subjects]
 /\ db = InitialDB /\ tx = NoTx /\ commits = 0
 /\ last = Event("none", InitialDB, InitialDB, NoTx)
 /\ tokens = {}
 /\ tokenLast = [accepted |-> FALSE, target |-> FirstProject,
       tokenProject |-> FirstProject, tokenGeneration |-> 0,
       seenGeneration |-> 0, present |-> FALSE, authorized |-> FALSE]

Create(p, actor) ==
 /\ tx.phase = "idle" /\ owners[p] = actor /\ ~db[p].present /\ db[p].generation < 2
 /\ db' = [db EXCEPT ![p] = [present |-> TRUE,
       generation |-> db[p].generation + 1, pair |-> EmptyPair, history |-> <<>>]]
 /\ last' = Event("create", db, db', [NoTx EXCEPT !.project = p, !.claimed = p, !.actor = actor])
 /\ UNCHANGED <<owners, tx, commits, tokens, tokenLast>>
Delete(p, actor) ==
 /\ tx.phase = "idle" /\ owners[p] = actor
 /\ db[p].present /\ db[p].pair = EmptyPair
 /\ db' = [db EXCEPT ![p].present = FALSE, ![p].history = <<>>]
 /\ last' = Event("delete", db, db', [NoTx EXCEPT !.project = p, !.claimed = p, !.actor = actor])
 /\ UNCHANGED <<owners, tx, commits, tokens, tokenLast>>

Stage(p, claimed, actor, mode, expectedDelete, value) ==
 LET old == db[p]
     authorized == claimed = p /\ owners[p] = actor
     valid == expectedDelete = old.pair.left /\ old.pair.left = old.pair.right
              /\ (mode # "remove" \/ expectedDelete # None)
     room == Len(old.history) < 2 \/ mode = "remove"
     nextPair == IF mode = "remove" THEN EmptyPair ELSE Pair(value)
     history == Append(old.history, Receipt(Len(old.history)+1,
           old.generation, mode, IF mode = "remove" THEN None ELSE value))
 IN
 /\ tx.phase = "idle" /\ old.present /\ commits < 3
 \* Denied proposals cannot proceed to Write and only phase/before remain observable.
 /\ tx' = IF authorized /\ valid /\ room
    THEN [phase |-> "staged",
       project |-> p, claimed |-> claimed, actor |-> actor, mode |-> mode,
       expectedDelete |-> expectedDelete, value |-> value, before |-> db,
       expectedPair |-> nextPair, expectedHistory |-> history,
       candidatePair |-> old.pair, candidateHistory |-> old.history]
    ELSE [NoTx EXCEPT !.phase = "denied", !.before = db]
 /\ UNCHANGED <<owners, db, commits, last, tokens, tokenLast>>
Write ==
 /\ tx.phase = "staged"
 /\ tx' = [tx EXCEPT !.phase = "written",
       !.candidatePair = tx.expectedPair, !.candidateHistory = tx.expectedHistory]
 /\ UNCHANGED <<owners, db, commits, last, tokens, tokenLast>>
Changed(v) == IF v = None THEN Zero ELSE [v EXCEPT !.ip = 1-v.ip]
DriftKinds == {"none", "reinsertOld", "wrongValue", "apexLost",
               "receiptLost", "receiptMoved", "priorHistoryChanged"}
Disturb(d) ==
 LET h == tx.candidateHistory
     pair == CASE d = "reinsertOld" -> tx.before[tx.project].pair
               [] d = "wrongValue" -> [tx.candidatePair EXCEPT
                       !.left = Changed(tx.candidatePair.left)]
               [] d = "apexLost" -> [tx.candidatePair EXCEPT !.apex = FALSE]
               [] OTHER -> tx.candidatePair
     history == CASE d = "receiptLost" -> SubSeq(h,1,Len(h)-1)
                  [] d = "receiptMoved" -> [h EXCEPT ![Len(h)].id =
                           IF h[Len(h)].id = 1 THEN 2 ELSE 1]
                  [] d = "priorHistoryChanged" /\ Len(h) > 1 ->
                       [h EXCEPT ![1].value = Changed(h[1].value)]
                  [] OTHER -> h
 IN
 /\ tx.phase = "written"
 /\ tx' = [tx EXCEPT !.phase = "observed",
       !.candidatePair = pair, !.candidateHistory = history]
 /\ UNCHANGED <<owners, db, commits, last, tokens, tokenLast>>
Verify ==
 LET matches == tx.candidatePair = tx.expectedPair
                /\ tx.candidateHistory = tx.expectedHistory
 IN
 /\ tx.phase = "observed"
 /\ tx' = [tx EXCEPT !.phase = IF matches THEN "verified" ELSE "refused"]
 /\ UNCHANGED <<owners, db, commits, last, tokens, tokenLast>>
Commit ==
 /\ tx.phase = "verified"
 /\ db' = [db EXCEPT ![tx.project].pair = tx.candidatePair,
                     ![tx.project].history = tx.candidateHistory]
 /\ last' = Event("commit", tx.before, db', tx)
 /\ tx' = NoTx /\ commits' = commits+1
 /\ UNCHANGED <<owners, tokens, tokenLast>>
\* Abort is possible at every active phase, including a failed COMMIT after
\* verification. Tentative storage never becomes publicly visible on failure.
Abort ==
 /\ tx.phase # "idle" /\ db' = db
 /\ last' = Event("abort", tx.before, db', NoTx)
 /\ tx' = NoTx
 /\ UNCHANGED <<owners, commits, tokens, tokenLast>>

IssueToken(p) ==
 /\ tx.phase = "idle" /\ db[p].present
 /\ tokens' = tokens \cup {[project |-> p, generation |-> db[p].generation]}
 /\ UNCHANGED <<owners, db, tx, commits, last, tokenLast>>
UseToken(p, actor, token) ==
 LET allowed == db[p].present /\ owners[p] = actor
                /\ token.project = p /\ token.generation = db[p].generation
 IN
 /\ tx.phase = "idle" /\ token \in tokens
 /\ tokenLast' = [accepted |-> allowed, target |-> p,
       tokenProject |-> token.project, tokenGeneration |-> token.generation,
       seenGeneration |-> db[p].generation, present |-> db[p].present,
       authorized |-> owners[p] = actor]
 /\ UNCHANGED <<owners, db, tx, commits, last, tokens>>
Next ==
 (\E p \in Projects : (\E actor \in Subjects : Create(p,actor) \/ Delete(p,actor)) \/ IssueToken(p)
   \/ (\E claimed \in Projects, actor \in Subjects, mode \in {"put","remove"},
          old \in Values, value \in Values \ {None} :
             Stage(p,claimed,actor,mode,old,value))
   \/ (\E actor \in Subjects, token \in tokens : UseToken(p,actor,token)))
 \/ Write \/ (\E d \in DriftKinds : Disturb(d)) \/ Verify \/ Commit \/ Abort
 \/ UNCHANGED vars

TypeOK ==
 /\ owners \in [Projects -> Subjects] /\ commits \in 0..3
 /\ tx.phase \in {"idle","denied","staged","written","observed","verified","refused"}
 /\ \A p \in Projects :
       /\ db[p].present \in BOOLEAN /\ db[p].generation \in 0..2
       /\ db[p].pair \in [left:Values,right:Values,apex:BOOLEAN]
       /\ Len(db[p].history) <= 3
 /\ tokens \subseteq [project:Projects,generation:1..2]
NoOrphanRecords == \A p \in Projects : ~db[p].present =>
 db[p].pair = EmptyPair /\ db[p].history = <<>>
AtomicPair == \A p \in Projects : db[p].pair.left = db[p].pair.right
ApexPreserved == \A p \in Projects : db[p].pair.apex = TRUE
PendingInvisible == tx.phase # "idle" => db = tx.before
DeniedStateUnchanged == last.kind = "abort" => last.after = last.before
OnlyOwnerCommits == last.kind \in {"commit","create","delete"} =>
 /\ last.proposal.claimed = last.proposal.project
 /\ owners[last.proposal.project] = last.proposal.actor
ExactDeletionRequired == last.kind = "commit" =>
 LET r == last.proposal
 IN /\ r.expectedDelete = last.before[r.project].pair.left
    /\ r.expectedDelete = last.before[r.project].pair.right
    /\ (r.mode # "remove" \/ r.expectedDelete # None)
CommittedStateMatchesIntent == last.kind = "commit" =>
 LET r == last.proposal IN
 /\ last.after[r.project].pair = r.expectedPair
 /\ last.after[r.project].history = r.expectedHistory
OtherProjectUnchanged == last.kind = "commit" =>
 \A p \in Projects \ {last.proposal.project} : last.after[p] = last.before[p]
HistoryPrefixPreserved == last.kind = "commit" =>
 LET p == last.proposal.project
     old == last.before[p].history
     new == last.after[p].history
 IN /\ Len(new) = Len(old)+1 /\ SubSeq(new,1,Len(old)) = old
ReceiptSequenceExact == \A p \in Projects :
 \A i \in 1..Len(db[p].history) :
   /\ db[p].history[i].id = i
   /\ db[p].history[i].generation = db[p].generation
NonemptyDeletionRefused == last.kind = "delete" =>
 last.before[last.proposal.project].pair = EmptyPair
NoOldGenerationReuse == tokenLast.accepted =>
 /\ tokenLast.present /\ tokenLast.authorized
 /\ tokenLast.tokenProject = tokenLast.target
 /\ tokenLast.tokenGeneration = tokenLast.seenGeneration
CleanupAdmissibleAtQuota == \A p \in Projects :
 (tx.phase = "idle" /\ db[p].present /\ db[p].pair.left # None
  /\ Len(db[p].history) >= 2 /\ commits < 3) =>
 ENABLED (Stage(p,p,owners[p],"remove",db[p].pair.left,Zero)
          /\ tx'.phase = "staged")
=============================================================================
