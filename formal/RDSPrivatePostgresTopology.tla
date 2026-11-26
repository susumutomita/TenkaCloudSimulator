--------------------- MODULE RDSPrivatePostgresTopology ---------------------
EXTENDS Naturals
VARIABLE s

TypeOK ==
  s \in [optIn: BOOLEAN, localAuth: BOOLEAN, authorized: BOOLEAN,
         qualifies: BOOLEAN, ownedTopology: BOOLEAN, finished: BOOLEAN,
         initialRevision: 0..1, revision: 0..2, nativeRead: BOOLEAN,
         wrote: BOOLEAN, endpoint: BOOLEAN, available: BOOLEAN]

Init == /\ TypeOK
        /\ ~s.finished /\ s.revision=s.initialRevision
        /\ ~s.nativeRead /\ ~s.wrote /\ ~s.endpoint /\ ~s.available

StartupAllowed == ~s.optIn \/ s.localAuth
NeedsNative == s.optIn /\ s.qualifies
ReadNative == StartupAllowed /\ s.authorized /\ NeedsNative
Accept == StartupAllowed /\ s.authorized /\ (~NeedsNative \/ s.ownedTopology)

Request ==
  /\ ~s.finished
  /\ s' = [s EXCEPT !.finished = TRUE, !.nativeRead = ReadNative,
            !.wrote = Accept, !.revision = IF Accept THEN @ + 1 ELSE @,
            !.endpoint = FALSE, !.available = FALSE]
Next == Request
Spec == Init /\ [][Next]_s

LocalStartupRequired ==
  (s.finished /\ s.optIn /\ ~s.localAuth) => (~s.wrote /\ ~s.nativeRead)
AuthorityBeforeNative ==
  (s.finished /\ ~s.authorized) => (~s.nativeRead /\ ~s.wrote)
LegacyConfigurationPreserved ==
  (s.finished /\ StartupAllowed /\ s.authorized /\ (~s.optIn \/ ~s.qualifies))
    => (s.wrote /\ ~s.nativeRead)
OwnedTopologyBeforeWrite ==
  (s.finished /\ s.wrote /\ s.optIn /\ s.qualifies)
    => (s.nativeRead /\ s.ownedTopology)
AtomicRefusal ==
  (s.finished /\ ~s.wrote) => s.revision=s.initialRevision
ConfigurationOnly == ~s.endpoint /\ ~s.available
=============================================================================
