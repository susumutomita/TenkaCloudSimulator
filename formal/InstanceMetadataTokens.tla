-------------------- MODULE InstanceMetadataTokens --------------------
EXTENDS Naturals, FiniteSets
VARIABLE s
Init == s=[now |-> 0, deadline |-> 2, closed |-> FALSE, tokens |-> {},
 expires |-> 0, request |-> "none", emitted |-> FALSE, bad |-> {}]
\* A request linearizes at its entry context check. In-flight replies may race
\* Close; this is not a claim that cancellation retracts already admitted I/O.
Issue(methodOK,ttlOK,forwarded,randomOK,ttl) == /\ ~s.closed /\ s.now<s.deadline
 /\ s.request="none" /\ methodOK /\ ttlOK /\ ~forwarded /\ randomOK
 /\ Cardinality(s.tokens)<1
 /\ s'=[s EXCEPT !.tokens={"local"}, !.expires=IF s.now+ttl<s.deadline THEN s.now+ttl ELSE s.deadline,
 !.bad=IF s.closed \/ s.now>=s.deadline \/ ~methodOK \/ ~ttlOK \/ forwarded \/ ~randomOK THEN @ \cup {"issue"} ELSE @]
Read(token,methodOK,path) == /\ ~s.closed /\ s.now<s.deadline
 /\ s.request="none" /\ token \in s.tokens /\ s.now<s.expires /\ methodOK
 /\ path \in {"instance","alias","ip","region"}
 /\ s'=[s EXCEPT !.request=path,
 !.bad=IF s.closed \/ s.now>=s.deadline \/ token#"local" \/ s.now>=s.expires \/ ~methodOK \/
 path \notin {"instance","alias","ip","region"} THEN @ \cup {"read"} ELSE @]
Deliver == /\ s.request#"none" /\ ~s.emitted
 /\ s'=[s EXCEPT !.emitted=TRUE]
Close == /\ ~s.closed /\ s'=[s EXCEPT !.closed=TRUE]
Tick == /\ s.now<2 /\ s'=[s EXCEPT !.now=@+1]
Purge == /\ s.now>=s.expires /\ s.tokens#{} /\ s'=[s EXCEPT !.tokens={}]
Next == (\E a,b,c,d \in BOOLEAN,t \in {1,2}: Issue(a,b,c,d,t))
 \/ (\E t \in {"local","foreign"},m \in BOOLEAN,p \in {"instance","alias","ip","region","credentials","signature"}: Read(t,m,p))
 \/ Deliver \/ Close \/ Tick \/ Purge
TypeOK == s.now \in 0..2 /\ s.tokens \subseteq {"local"} /\ s.bad \subseteq {"issue","read"}
TokenAdmission == "issue" \notin s.bad
InstanceBoundRead == "read" \notin s.bad
NoCredentials == s.request \notin {"credentials","signature"}
BoundedExpiry == s.expires<=s.deadline
Spec == Init /\ [][Next]_s
=============================================================================
