---------------------- MODULE SecretsVersionLifecycle ----------------------
EXTENDS Naturals, FiniteSets
CONSTANT Teams, VersionIDs, Values
VARIABLES active, generation, versions, history, current, previous, receipts
vars == <<active,generation,versions,history,current,previous,receipts>>
Record(t,g,i,v,a) == [team |-> t,gen |-> g,id |-> i,value |-> v,allowed |-> a]
ForTeam(t) == {r \in versions : r.team=t /\ r.gen=generation[t]}
IDsFor(t) == {r.id : r \in ForTeam(t)}
Init == /\ active={} /\ versions={} /\ history={} /\ receipts={}
        /\ generation=[t \in Teams |-> 0]
        /\ current=[t \in Teams |-> {}] /\ previous=[t \in Teams |-> {}]
Create(t,i,v,a) ==
    /\ t \notin active /\ generation[t]<2 /\ a
    /\ active'=active \cup {t}
    /\ generation'=[generation EXCEPT ![t]=@+1]
    /\ versions'=versions \cup {Record(t,generation[t]+1,i,v,a)}
    /\ history'=history \cup {Record(t,generation[t]+1,i,v,a)}
    /\ current'=[current EXCEPT ![t]={i}]
    /\ previous'=[previous EXCEPT ![t]={}]
    /\ UNCHANGED receipts
PutNew(t,i,v,a) ==
    /\ t \in active /\ a /\ i \notin IDsFor(t)
    /\ versions'=versions \cup {Record(t,generation[t],i,v,a)}
    /\ history'=history \cup {Record(t,generation[t],i,v,a)}
    /\ current'=[current EXCEPT ![t]={i}]
    /\ previous'=[previous EXCEPT ![t]=current[t]]
    /\ UNCHANGED <<active,generation,receipts>>
RetrySame(t,i,v,a) == /\ t \in active /\ a
                    /\ \E r \in ForTeam(t) : r.id=i /\ r.value=v
                    /\ UNCHANGED vars
RejectConflict(t,i,v,a) == /\ t \in active /\ a
                         /\ \E r \in ForTeam(t) : r.id=i /\ r.value#v
                         /\ UNCHANGED vars
Promote(t,target,remove,a) ==
    /\ t \in active /\ a /\ target \in IDsFor(t)
    /\ current[t]={remove} /\ target#remove
    /\ current'=[current EXCEPT ![t]={target}]
    /\ previous'=[previous EXCEPT ![t]={remove}]
    /\ UNCHANGED <<active,generation,versions,history,receipts>>
Delete(t,g,a) == /\ t \in active /\ g=generation[t] /\ a
                 /\ active'=active \ {t}
                 /\ versions'={r \in versions : r.team#t}
                 /\ current'=[current EXCEPT ![t]={}]
                 /\ previous'=[previous EXCEPT ![t]={}]
                 /\ UNCHANGED <<generation,history,receipts>>
Read(t,caller,g,a) ==
    /\ t \in active /\ caller=t /\ g=generation[t] /\ a
    /\ receipts'=receipts \cup {[team |-> t,caller |-> caller,
                   requestedGen |-> g,activeGen |-> generation[t],allowed |-> a]}
    /\ UNCHANGED <<active,generation,versions,history,current,previous>>
Next == (\E t \in Teams,i \in VersionIDs,v \in Values,a \in BOOLEAN :
          Create(t,i,v,a) \/ PutNew(t,i,v,a) \/ RetrySame(t,i,v,a) \/ RejectConflict(t,i,v,a))
        \/ (\E t \in Teams,target,remove \in VersionIDs,a \in BOOLEAN : Promote(t,target,remove,a))
        \/ (\E t \in Teams,g \in 1..2,a \in BOOLEAN : Delete(t,g,a))
        \/ (\E t,caller \in Teams,g \in 1..2,a \in BOOLEAN : Read(t,caller,g,a))
        \/ UNCHANGED vars
TypeOK == /\ active \subseteq Teams /\ generation \in [Teams -> 0..2]
          /\ versions \subseteq [team:Teams,gen:1..2,id:VersionIDs,value:Values,allowed:BOOLEAN]
          /\ history \subseteq [team:Teams,gen:1..2,id:VersionIDs,value:Values,allowed:BOOLEAN]
          /\ current \in [Teams -> SUBSET VersionIDs]
          /\ previous \in [Teams -> SUBSET VersionIDs]
          /\ receipts \subseteq [team:Teams,caller:Teams,requestedGen:1..2,activeGen:1..2,allowed:BOOLEAN]
ImmutableVersions == \A r,s \in history :
    (r.team=s.team /\ r.gen=s.gen /\ r.id=s.id) => r.value=s.value
NoOrphans == /\ \A r \in versions : r.team \in active /\ r.gen=generation[r.team]
             /\ \A t \in Teams : current[t] \cup previous[t] \subseteq IDsFor(t)
UniqueLabels == \A t \in Teams : Cardinality(current[t])<=1 /\ Cardinality(previous[t])<=1
SeparateCurrentPrevious == \A t \in Teams : current[t] \cap previous[t]={}
AuthorizedVersions == \A r \in history : r.allowed
AuthorizedDisclosure == \A r \in receipts : r.allowed /\ r.team=r.caller /\ r.requestedGen=r.activeGen
=============================================================================
