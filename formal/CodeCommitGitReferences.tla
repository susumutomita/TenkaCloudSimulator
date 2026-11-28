----------------------- MODULE CodeCommitGitReferences -----------------------
EXTENDS Naturals, FiniteSets

\* Finite transaction families, not every possible mutating schedule. Read-only
\* observations may interleave every phase; the two staged CAS writers resolve
\* in either order. IDs are mathematical identities, not Git serialization/SHA.
CONSTANT Cases
Repos == {"owned", "peer"}
Branches == {"main", "feature"}
Writers == {"one", "two"}
Commits == {"root", "left", "right", "metadata"}
Blobs == {"old-file", "left-file", "right-file"}
Absent == "absent"
Idle == [repo |-> "owned", branch |-> "main", commit |-> "root",
 expected |-> Absent, epoch |-> 1, allowed |-> FALSE, active |-> FALSE]
NoData == "no-data"
Other(b) == IF b = "main" THEN "feature" ELSE "main"
Parent(c) == CASE c = "root" -> Absent
                [] c = "metadata" -> "left"
                [] OTHER -> "root"
Blob(c) == CASE c = "root" -> "old-file"
              [] c = "right" -> "right-file"
              [] OTHER -> "left-file"
Seed(r, e) == IF r = "owned" THEN (IF e = 1 THEN "owned-legacy-1" ELSE "owned-legacy-2")
 ELSE (IF e = 1 THEN "peer-legacy-1" ELSE "peer-legacy-2")
Metadata == {Seed(r,e) : r \in Repos, e \in 1..2} \cup {Absent,"replacement"}
Proposal(r,b,c,parent,e,allow) ==
 [repo |-> r, branch |-> b, commit |-> c, expected |-> parent,
  epoch |-> e, allowed |-> allow, active |-> TRUE]
ProposalType == [repo: Repos, branch: Branches, commit: Commits,
 expected: Commits \cup {Absent}, epoch: 1..2, allowed: BOOLEAN, active: BOOLEAN]
StoreType == [live: [Repos -> BOOLEAN], epoch: [Repos -> 1..2],
 refs: [Repos -> [Branches -> Commits \cup {Absent}]],
 default: [Repos -> Branches \cup {Absent}], history: [Repos -> SUBSET Commits],
 blobs: [Repos -> SUBSET Blobs], metadata: [Repos -> Metadata],
 written: SUBSET (Repos \X (1..2) \X Commits)]
Fresh == [live |-> [r \in Repos |-> TRUE], epoch |-> [r \in Repos |-> 1],
 refs |-> [r \in Repos |-> [b \in Branches |-> Absent]],
 default |-> [r \in Repos |-> Absent], history |-> [r \in Repos |-> {}],
 blobs |-> [r \in Repos |-> {}], metadata |-> [r \in Repos |-> Seed(r,1)],
 written |-> {}]
Event(k,status,p,before,result) == [kind |-> k, status |-> status,
 repo |-> p.repo, branch |-> p.branch, commit |-> p.commit,
 expected |-> p.expected, epoch |-> p.epoch, allowed |-> p.allowed,
 before |-> before, result |-> result]

VARIABLES store, pending, scenario, phase, primary, last
vars == <<store,pending,scenario,phase,primary,last>>
Valid(s,p) == /\ s.live[p.repo]
 /\ s.epoch[p.repo] = p.epoch
 /\ s.refs[p.repo][p.branch] = p.expected
 /\ Parent(p.commit) = p.expected
 /\ (p.expected = Absent \/ p.expected \in s.history[p.repo])
Publish(s,p) == [s EXCEPT
 !.refs[p.repo][p.branch] = p.commit,
 !.default[p.repo] = IF @ = Absent THEN p.branch ELSE @,
 !.history[p.repo] = @ \cup {p.commit},
 !.blobs[p.repo] = @ \cup {Blob(p.commit)},
 !.written = @ \cup {<<p.repo,s.epoch[p.repo],p.commit>>}]
Init == /\ store = Fresh /\ pending = [w \in Writers |-> Idle]
 /\ scenario \in Cases /\ phase = 0 /\ primary \in Branches
 /\ last = Event("Init","Initial",Proposal("owned","main","root",Absent,1,TRUE),Fresh,NoData)

Begin(w,r,b,c,parent,allow) ==
 /\ pending[w] = Idle
 /\ LET p == Proposal(r,b,c,parent,store.epoch[r],allow) IN
    /\ pending' = [pending EXCEPT ![w] = p]
    /\ store' = store
    /\ last' = Event("Begin","Staged",p,store,NoData)
 /\ phase' = phase + 1
 /\ UNCHANGED <<scenario,primary>>
Resolve(w,outcome) ==
 /\ pending[w] # Idle
 /\ LET p == pending[w]
        ok == outcome = "Commit" /\ p.allowed /\ Valid(store,p)
        status == CASE outcome = "Cancel" -> "Canceled"
                    [] outcome = "Fail" -> "Failed"
                    [] ~p.allowed -> "Denied"
                    [] ok -> "Success"
                    [] ~store.live[p.repo] \/ store.epoch[p.repo] # p.epoch
                       \/ store.refs[p.repo][p.branch] # p.expected -> "Stale"
                    [] OTHER -> "Rejected"
    IN /\ store' = IF ok THEN Publish(store,p) ELSE store
       /\ last' = Event("Put",status,p,store,NoData)
 /\ pending' = [pending EXCEPT ![w] = Idle]
 /\ phase' = phase + 1
 /\ UNCHANGED <<scenario,primary>>
Fork(r,b,c) ==
 LET p == Proposal(r,b,c,Absent,store.epoch[r],TRUE)
     own == c \in store.history[r]
     ok == store.live[r] /\ store.refs[r][b] = Absent /\ own
 IN /\ store' = IF ok THEN [store EXCEPT !.refs[r][b] = c] ELSE store
    /\ last' = Event("Fork",IF ok THEN "Success" ELSE "Rejected",p,store,NoData)
    /\ phase' = phase + 1
    /\ UNCHANGED <<pending,scenario,primary>>
Delete(r) ==
 /\ store.live[r]
 /\ store' = [store EXCEPT !.live[r] = FALSE,
      !.refs[r] = [b \in Branches |-> Absent], !.default[r] = Absent,
      !.history[r] = {}, !.blobs[r] = {}, !.metadata[r] = Absent]
 /\ last' = Event("Delete","Deleted",Proposal(r,primary,"root",Absent,store.epoch[r],TRUE),store,NoData)
 /\ phase' = phase + 1
 /\ UNCHANGED <<pending,scenario,primary>>
Recreate(r) ==
 /\ ~store.live[r] /\ store.epoch[r] = 1
 /\ store' = [store EXCEPT !.live[r] = TRUE, !.epoch[r] = 2,
      !.metadata[r] = Seed(r,2)]
 /\ last' = Event("Create","Created",Proposal(r,primary,"root",Absent,2,TRUE),store,NoData)
 /\ phase' = phase + 1
 /\ UNCHANGED <<pending,scenario,primary>>
Read(r,c,advance) ==
 LET result == IF store.live[r] /\ c \in store.history[r] THEN Blob(c) ELSE NoData
 IN /\ store' = store
    /\ last' = Event("Read","Read",Proposal(r,primary,c,Absent,store.epoch[r],TRUE),store,result)
    /\ phase' = IF advance THEN phase + 1 ELSE phase
    /\ UNCHANGED <<pending,scenario,primary>>

History == CASE phase = 0 -> Begin("one","owned",primary,"root",Absent,TRUE)
 [] phase = 1 -> Resolve("one","Commit")
 [] phase = 2 -> Begin("one","owned",primary,"left","root",TRUE)
 [] phase = 3 -> Resolve("one","Commit")
 [] phase = 4 -> Fork("owned",Other(primary),"root")
 [] phase = 5 -> Begin("one","owned",Other(primary),"right","root",TRUE)
 [] phase = 6 -> Resolve("one","Commit")
 [] phase = 7 -> Read("owned","root",TRUE)
 [] phase = 8 -> Begin("one","owned",primary,"metadata","left",TRUE)
 [] phase = 9 -> Resolve("one","Commit")
 [] phase = 10 -> Read("owned","left",TRUE)
 [] OTHER -> FALSE
Race == CASE phase = 0 -> Begin("one","owned",primary,"root",Absent,TRUE)
 [] phase = 1 -> Resolve("one","Commit")
 [] phase = 2 -> Begin("one","owned",primary,"left","root",TRUE)
 [] phase = 3 -> Begin("two","owned",primary,"right","root",TRUE)
 [] phase = 4 -> Resolve("one","Commit") \/ Resolve("two","Commit")
 [] phase = 5 -> Resolve("one","Commit") \/ Resolve("two","Commit")
 [] OTHER -> FALSE
Lifetime == CASE phase = 0 -> Begin("one","owned",primary,"root",Absent,TRUE)
 [] phase = 1 -> Resolve("one","Commit")
 [] phase = 2 -> Begin("two","owned",primary,"left","root",TRUE)
 [] phase = 3 -> Delete("owned")
 [] phase = 4 -> Recreate("owned")
 [] phase = 5 -> Read("owned","root",TRUE)
 [] phase = 6 -> Begin("one","owned",primary,"root",Absent,TRUE)
 [] phase = 7 -> Resolve("one","Commit")
 [] phase = 8 -> Resolve("two","Commit")
 [] phase = 9 -> Read("owned","root",TRUE)
 [] OTHER -> FALSE
LateFirst == CASE phase = 0 -> Begin("one","owned",primary,"root",Absent,TRUE)
 [] phase = 1 -> Delete("owned")
 [] phase = 2 -> Recreate("owned")
 [] phase = 3 -> Resolve("one","Commit")
 [] phase = 4 -> Begin("one","owned",primary,"root",Absent,TRUE)
 [] phase = 5 -> Resolve("one","Commit")
 [] phase = 6 -> Read("owned","root",TRUE)
 [] OTHER -> FALSE
Peer == CASE phase = 0 -> Begin("one","owned",primary,"root",Absent,TRUE)
 [] phase = 1 -> Resolve("one","Commit")
 [] phase = 2 -> Fork("peer",Other(primary),"root")
 [] phase = 3 -> Begin("one","peer",primary,"root",Absent,TRUE)
 [] phase = 4 -> Resolve("one","Commit")
 [] phase = 5 -> Read("peer","root",TRUE)
 [] OTHER -> FALSE
ErrorCase == CASE phase = 0 -> Begin("one","owned",primary,"root",Absent,scenario # "Denied")
 [] phase = 1 -> Resolve("one",CASE scenario = "Canceled" -> "Cancel"
                                  [] scenario = "Failed" -> "Fail" [] OTHER -> "Commit")
 [] OTHER -> FALSE
Scheduled == CASE scenario = "History" -> History [] scenario = "Race" -> Race
 [] scenario = "Lifetime" -> Lifetime [] scenario = "LateFirst" -> LateFirst
 [] scenario = "Peer" -> Peer [] OTHER -> ErrorCase
Next == Scheduled \/ (\E r \in Repos, c \in Commits: Read(r,c,FALSE))
Spec == Init /\ [][Next]_vars

TypeOK == /\ store \in StoreType /\ pending \in [Writers -> ProposalType \cup {Idle}]
 /\ scenario \in {"History","Race","Lifetime","LateFirst","Peer","Canceled","Failed","Denied"}
 /\ primary \in Branches /\ phase \in 0..11
AtomicVisibility == (last.kind = "Begin" \/ last.status \in {"Canceled","Failed","Denied","Rejected","Stale"})
                    => store = last.before
OwnWritten(s,r) == {t \in s.written : t[1] = r}
RepoState(s,r) == <<s.live[r],s.epoch[r],s.refs[r],s.default[r],s.history[r],s.blobs[r],s.metadata[r],OwnWritten(s,r)>>
PeerFrame == \A r \in Repos \ {last.repo}: RepoState(store,r) = RepoState(last.before,r)
LegacyMetadataPreserved == \A r \in Repos: store.live[r] => store.metadata[r] = Seed(r,store.epoch[r])
CASExpectedParent == last.kind = "Put" /\ last.status = "Success" =>
 /\ last.before.live[last.repo] /\ last.before.epoch[last.repo] = last.epoch
 /\ last.before.refs[last.repo][last.branch] = last.expected
 /\ Parent(last.commit) = last.expected
DefaultBranchCorrect ==
 /\ (last.kind = "Put" /\ last.status = "Success" => store.default[last.repo] =
       IF last.before.default[last.repo] = Absent THEN last.branch ELSE last.before.default[last.repo])
 /\ (last.kind = "Fork" /\ last.status = "Success" => store.default[last.repo] = last.before.default[last.repo])
ForkIsolation == last.kind = "Fork" /\ last.status = "Success" =>
 /\ \A b \in Branches: last.before.refs[last.repo][b] # Absent => store.refs[last.repo][b] = last.before.refs[last.repo][b]
 /\ store.history[last.repo] = last.before.history[last.repo]
 /\ store.blobs[last.repo] = last.before.blobs[last.repo]
HistoryRetained == \A t \in store.written:
 store.live[t[1]] /\ store.epoch[t[1]] = t[2] => t[3] \in store.history[t[1]]
LifetimeIsolation == \A r \in Repos: \A c \in store.history[r]: <<r,store.epoch[r],c>> \in store.written
ReferenceIntegrity == \A r \in Repos:
 /\ (store.default[r] # Absent => store.refs[r][store.default[r]] # Absent)
 /\ (\A b \in Branches: store.refs[r][b] # Absent =>
       store.refs[r][b] \in store.history[r] /\ Blob(store.refs[r][b]) \in store.blobs[r])
 /\ (\A c \in store.history[r]: Parent(c) = Absent \/ Parent(c) \in store.history[r])
 /\ (~store.live[r] => store.history[r] = {} /\ store.blobs[r] = {}
       /\ store.refs[r] = [b \in Branches |-> Absent] /\ store.default[r] = Absent /\ store.metadata[r] = Absent)
ReadExact == last.kind = "Read" =>
 /\ store = last.before
 /\ last.result = IF last.before.live[last.repo] /\ last.commit \in last.before.history[last.repo]
                 THEN Blob(last.commit) ELSE NoData

\* Reachability checks are run separately by negating these predicates. They
\* are positive scenario witnesses, not safety failures of the baseline.
WitnessHistory == scenario = "History" /\ phase = 11 /\ store.history["owned"] = Commits
 /\ store.refs["owned"][primary] = "metadata" /\ store.refs["owned"][Other(primary)] = "right"
 /\ Blob("metadata") = Blob("left") /\ last.kind = "Read" /\ last.result = "left-file"
WitnessRace == scenario = "Race" /\ phase = 6 /\ last.status = "Stale"
 /\ Cardinality(store.history["owned"]) = 2
WitnessRaceLeft == WitnessRace /\ store.refs["owned"][primary] = "left"
WitnessRaceRight == WitnessRace /\ store.refs["owned"][primary] = "right"
WitnessLifetime == scenario = "Lifetime" /\ phase = 9 /\ last.status = "Stale"
 /\ store.epoch["owned"] = 2 /\ store.history["owned"] = {"root"}
WitnessLateFirst == scenario = "LateFirst" /\ phase = 4 /\ last.status = "Stale"
 /\ store.epoch["owned"] = 2 /\ store.history["owned"] = {}
WitnessFreshRetry == scenario = "LateFirst" /\ phase = 7 /\ store.refs["owned"][primary] = "root"
 /\ <<"owned",2,"root">> \in store.written
WitnessPeer == scenario = "Peer" /\ phase = 6 /\ store.history["owned"] = {"root"}
 /\ store.history["peer"] = {"root"} /\ last.result = "old-file"
WitnessCanceled == scenario = "Canceled" /\ phase = 2 /\ last.status = "Canceled" /\ store = Fresh
WitnessFailed == scenario = "Failed" /\ phase = 2 /\ last.status = "Failed" /\ store = Fresh
WitnessDenied == scenario = "Denied" /\ phase = 2 /\ last.status = "Denied" /\ store = Fresh
=============================================================================
