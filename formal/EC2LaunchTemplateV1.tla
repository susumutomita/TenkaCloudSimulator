------------------------- MODULE EC2LaunchTemplateV1 -------------------------
EXTENDS Naturals, FiniteSets, TLC
(* Two immutable template identities, two tokens, one reusable name, two
   owner SG lifetimes and a foreign SG/VPC. Atomic abstract commit only.
   No SQL/Go/XML/IAM parser, clock/ABA, provisioning or scheduler proof. *)
VARIABLES rows, retired, binding, version, tokens, fingerprint, live, peer, last, step
vars == <<rows,retired,binding,version,tokens,fingerprint,live,peer,last,step>>
Ids == {"a","b"}
Tokens == {"t1","t2"}
Groups == {"g1","g2","foreign"}
None == "none"
Owner(g) == IF g = "foreign" THEN "other" ELSE "owner"
VPC(g) == IF g = "foreign" THEN "v2" ELSE "v1"
PeerInitial == {"independent-populated-template"}
Current(g) == g \in live /\ Owner(g) = "owner" /\ VPC(g) = "v1"
Active == rows \ retired
Event(op,a,allow,commit,i,t,g,accepted) ==
 [op |-> op, actor |-> a, allow |-> allow, commit |-> commit,
  id |-> i, token |-> t, group |-> g, accepted |-> accepted,
  beforeRows |-> rows, beforeRetired |-> retired,
  beforeTokens |-> tokens, beforeBinding |-> binding,
  beforeFingerprint |-> fingerprint, beforeVersion |-> version,
  beforeLive |-> live]
Init == /\ rows = {} /\ retired = {}
 /\ binding = [i \in Ids |-> None] /\ version = [i \in Ids |-> 1]
 /\ tokens = [t \in Tokens |-> None] /\ fingerprint = [t \in Tokens |-> None]
 /\ live = {"g1","foreign"} /\ peer = PeerInitial /\ step = 0
 /\ last = [op |-> "none", actor |-> "owner", allow |-> TRUE, commit |-> TRUE,
    id |-> "a", token |-> "t1", group |-> "g1", accepted |-> FALSE,
    beforeRows |-> {}, beforeRetired |-> {},
    beforeTokens |-> [t \in Tokens |-> None], beforeBinding |-> [i \in Ids |-> None],
    beforeFingerprint |-> [t \in Tokens |-> None], beforeVersion |-> [i \in Ids |-> 1],
    beforeLive |-> {"g1","foreign"}]
Create(a,allow,commit,i,t,g) ==
 LET replay == tokens[t] # None
     same == replay /\ fingerprint[t] = g
     fresh == ~replay /\ Current(g) /\ Active = {} /\ i \notin rows
     publish == a = "owner" /\ allow /\ commit /\ (same \/ fresh)
 IN /\ rows' = IF publish /\ fresh THEN rows \cup {i} ELSE rows
    /\ retired' = retired
    /\ binding' = IF publish /\ fresh THEN [binding EXCEPT ![i] = g] ELSE binding
    /\ version' = IF publish /\ fresh THEN [version EXCEPT ![i] = 1] ELSE version
    /\ tokens' = IF publish /\ fresh THEN [tokens EXCEPT ![t] = i] ELSE tokens
    /\ fingerprint' = IF publish /\ fresh THEN [fingerprint EXCEPT ![t] = g] ELSE fingerprint
    /\ last' = Event("create",a,allow,commit,i,t,g,publish)
    /\ peer' = peer /\ UNCHANGED live /\ step' = step + 1
Delete(a,allow,commit,i) ==
 /\ retired' = IF a = "owner" /\ allow /\ commit /\ i \in Active THEN retired \cup {i} ELSE retired
 /\ last' = Event("delete",a,allow,commit,i,"t1","g1",FALSE)
 /\ UNCHANGED <<rows,binding,version,tokens,fingerprint,live,peer>>
 /\ step' = step + 1
Observe(i,admission) ==
 /\ last' = Event(IF admission THEN "admit" ELSE "describe","owner",TRUE,TRUE,i,"t1","g1",
       i \in Active /\ ( ~admission \/ Current(binding[i]) ))
 /\ UNCHANGED <<rows,retired,binding,version,tokens,fingerprint,live,peer>>
 /\ step' = step + 1
DependencyRetire ==
 /\ live' = live \ {"g1"}
 /\ last' = Event("dependency","owner",TRUE,TRUE,"a","t1","g1",FALSE)
 /\ UNCHANGED <<rows,retired,binding,version,tokens,fingerprint,peer>>
 /\ step' = step + 1
DependencyRecreate ==
 /\ "g1" \notin live /\ live' = live \cup {"g2"}
 /\ last' = Event("dependency","owner",TRUE,TRUE,"a","t1","g2",FALSE)
 /\ UNCHANGED <<rows,retired,binding,version,tokens,fingerprint,peer>>
 /\ step' = step + 1
Next == /\ step < 6
 /\ (\/ \E a \in {"owner","other"},allow \in BOOLEAN,commit \in BOOLEAN,i \in Ids,t \in Tokens,g \in Groups: Create(a,allow,commit,i,t,g)
     \/ \E a \in {"owner","other"},allow \in BOOLEAN,commit \in BOOLEAN,i \in Ids: Delete(a,allow,commit,i)
     \/ \E i \in Ids,admission \in BOOLEAN: Observe(i,admission)
     \/ DependencyRetire \/ DependencyRecreate)
TypeOK == /\ rows \subseteq Ids /\ retired \subseteq rows
 /\ binding \in [Ids -> (Groups \cup {None})]
 /\ version \in [Ids -> {1,2}] /\ tokens \in [Tokens -> (Ids \cup {None})]
 /\ fingerprint \in [Tokens -> (Groups \cup {None})] /\ live \subseteq Groups
 /\ peer = PeerInitial /\ Cardinality(Active) <= 1
AuthorizedCommit == (last.op \in {"create","delete"} /\
 (~last.allow \/ ~last.commit \/ last.actor # "owner")) =>
 <<rows,retired,binding,version,tokens,fingerprint>> =
 <<last.beforeRows,last.beforeRetired,last.beforeBinding,last.beforeVersion,last.beforeTokens,last.beforeFingerprint>>
ImmutableVersion == /\ \A i \in rows: version[i] = 1
 /\ \A i \in last.beforeRows: binding[i] = last.beforeBinding[i]
TokenIdentity == \A t \in Tokens: tokens[t] # None =>
 tokens[t] \in rows /\ fingerprint[t] = binding[tokens[t]]
ReplayStable == (last.op = "create" /\ last.beforeTokens[last.token] # None) =>
 <<rows,retired,tokens>> = <<last.beforeRows,last.beforeRetired,last.beforeTokens>>
ConflictRefused == (last.op = "create" /\ last.beforeTokens[last.token] # None /\
 last.beforeFingerprint[last.token] # last.group) => ~last.accepted
FreshIdentity == (last.op = "create" /\ last.beforeTokens[last.token] = None /\ last.accepted) =>
 last.id \notin last.beforeRows /\ last.id \in (rows \ retired) /\ tokens[last.token] = last.id
RetiredForever == last.beforeRetired \subseteq retired
FreshAdmission == last.op = "admit" =>
 last.accepted = (last.id \in (last.beforeRows \ last.beforeRetired) /\
 last.beforeBinding[last.id] \in last.beforeLive /\
 Owner(last.beforeBinding[last.id]) = "owner" /\ VPC(last.beforeBinding[last.id]) = "v1")
HistoricalRead == last.op = "describe" =>
 last.accepted = (last.id \in (last.beforeRows \ last.beforeRetired))
NativeScope == \A i \in rows: Owner(binding[i]) = "owner" /\ VPC(binding[i]) = "v1"
=============================================================================
