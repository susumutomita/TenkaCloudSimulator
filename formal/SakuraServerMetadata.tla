----------------------- MODULE SakuraServerMetadata -----------------------
EXTENDS Naturals, FiniteSets
CONSTANTS A, B, Nil
Realms == {A,B}
Ids == 1..3
Values == 0..1
Row(i,n,d) == [generation |-> i, name |-> n, description |-> d, cpu |-> 2, memory |-> 4096]
RowDomain == {Row(i,n,d) : i \in Ids, n \in Values, d \in Values}
Initial == [r \in Realms |-> [binding |-> <<r,r>>, rows |-> [i \in Ids |-> Nil],
                                 next |-> 1, used |-> {}]]
VARIABLES stores, last
vars == <<stores,last>>
Req(r, sub, realm, marker, op, id, v) ==
  [r |-> r, sub |-> sub, realm |-> realm, marker |-> marker, op |-> op, id |-> id, v |-> v]
Authorized(q) == /\ <<q.sub,q.realm>> = stores[q.r].binding
                 /\ q.marker
ExpectedAuth(q,b) == /\ <<q.sub,q.realm>> = b[q.r].binding
                     /\ q.marker
FreshSlot(r) == stores[r].next
Ok(q, ans, op) == ans = "ok" /\ q.op = op
ExactCommit(q, prev, post, ans, obs, i) ==
  /\ IF Ok(q,ans,"create") THEN
       (post.rows = [prev.rows EXCEPT ![i] = Row(i,0,0)] /\ obs = post.rows[i])
     ELSE TRUE
  /\ IF Ok(q,ans,"name") THEN
       LET expected == [prev.rows[q.id] EXCEPT !.name = q.v]
       IN (post.rows = [prev.rows EXCEPT ![q.id] = expected] /\ obs = expected)
     ELSE TRUE
  /\ IF Ok(q,ans,"description") THEN
       LET expected == [prev.rows[q.id] EXCEPT !.description = q.v]
       IN (post.rows = [prev.rows EXCEPT ![q.id] = expected] /\ obs = expected)
     ELSE TRUE
  /\ IF Ok(q,ans,"delete") THEN
       (post.rows = [prev.rows EXCEPT ![q.id] = Nil] /\ obs = Nil)
     ELSE TRUE
Observe(q,b,after,ans,obs,i) ==
  LET prev == b[q.r]
      post == after[q.r]
  IN [allocatedType |-> i \in 0..3,
      httpOwner |-> ((ans # "initial" /\ q.op # "reopen" /\ ans # "denied")
                       => ExpectedAuth(q,b)),
      reopenBinding |-> ((ans # "initial" /\ q.op = "reopen") =>
                           ((ans = "ok") <=> (<<q.sub,q.realm>> = prev.binding))),
      failFrame |-> (ans \in {"denied","missing","refused","failedCommit"} =>
                       (after = b /\ obs = Nil)),
      peerFrame |-> (\A r \in Realms \ {q.r}: after[r] = b[r]),
      snapshot |-> ((Ok(q,ans,"get") => (after = b /\ obs = prev.rows[q.id])) /\
                    (Ok(q,ans,"reopen") => (after = b /\ obs = Nil))),
      fresh |-> (Ok(q,ans,"create") =>
                  (i = prev.next /\ i \notin prev.used /\ post.used = prev.used \cup {i})),
      allocator |-> (IF Ok(q,ans,"create") THEN post.next = prev.next + 1
                     ELSE (post.next = prev.next /\ post.used = prev.used)),
      exact |-> ExactCommit(q,prev,post,ans,obs,i)]
Init == /\ stores = Initial
        /\ last = Observe(Req(A,A,A,TRUE,"get",1,0),Initial,Initial,"initial",Nil,0)
Finish(q,s,ans,obs,i) ==
  LET after == [stores EXCEPT ![q.r] = s]
  IN /\ stores' = after
     /\ last' = Observe(q,stores,after,ans,obs,i)
Patch(q,s,old) ==
  LET row == IF q.op = "name" THEN [old EXCEPT !.name = q.v]
                              ELSE [old EXCEPT !.description = q.v]
  IN Finish(q,[s EXCEPT !.rows[q.id] = row],"ok",row,0)
Allowed(q) ==
  LET s == stores[q.r]
      old == s.rows[q.id]
  IN IF q.op = "create" THEN
       IF s.next \in Ids /\ Cardinality({i \in Ids : s.rows[i] # Nil}) < 2
       THEN LET i == FreshSlot(q.r)
                row == Row(i,0,0)
            IN Finish(q,[s EXCEPT !.rows[i] = row, !.next = @+1,
                                  !.used = @ \cup {i}],"ok",row,i)
       ELSE Finish(q,s,"refused",Nil,0)
     ELSE IF old = Nil THEN Finish(q,s,"missing",Nil,0)
     ELSE IF q.op = "get" THEN Finish(q,s,"ok",old,0)
     ELSE IF q.op = "delete" THEN
       Finish(q,[s EXCEPT !.rows[q.id] = Nil],"ok",Nil,0)
     ELSE Patch(q,s,old)
Mount(q) ==
  IF <<q.sub,q.realm>> = stores[q.r].binding
  THEN Finish(q,stores[q.r],"ok",Nil,0)
  ELSE Finish(q,stores[q.r],"denied",Nil,0)
Apply(q) ==
  IF q.op = "reopen" THEN Mount(q)
  ELSE IF Authorized(q)
       THEN Allowed(q) \/
            (q.op \in {"create","name","description","delete"} /\
             Finish(q,stores[q.r],"failedCommit",Nil,0))
       ELSE Finish(q,stores[q.r],"denied",Nil,0)
OwnerRequests(r) ==
  \/ Apply(Req(r,r,r,TRUE,"create",1,0))
  \/ \E op \in {"get","delete"}, id \in Ids:
       Apply(Req(r,r,r,TRUE,op,id,0))
  \/ \E op \in {"name","description"}, id \in Ids, v \in Values:
       Apply(Req(r,r,r,TRUE,op,id,v))
AdmissionProbes(r) ==
  \E sub, realm \in Realms, m \in BOOLEAN:
    /\ <<sub,realm,m>> # <<r,r,TRUE>>
    /\ Apply(Req(r,sub,realm,m,"get",1,0))
ReopenRequests(r) ==
  \E sub, realm \in Realms:
    Apply(Req(r,sub,realm,TRUE,"reopen",1,0))
Next == \E r \in Realms:
          OwnerRequests(r) \/ AdmissionProbes(r) \/ ReopenRequests(r)
Spec == Init /\ [][Next]_vars
TypeOK ==
  /\ last.allocatedType
  /\ stores \in [Realms -> [binding: {<<r,r>> : r \in Realms},
                              rows: [Ids -> RowDomain \cup {Nil}],
                              next: 1..4, used: SUBSET Ids]]
FrozenScope == \A r \in Realms: stores[r].binding = <<r,r>>
OnlyOwnerRealmLookup == last.httpOwner
ReopenBinding == last.reopenBinding
FailureFrame == last.failFrame
PeerFrame == last.peerFrame
ReadAndReopenSnapshot == last.snapshot
FreshCommittedID == last.fresh
AllocatorOnlyCreate == last.allocator
CommitExact == last.exact
StoredIDsAndQuota ==
  \A r \in Realms:
    /\ Cardinality({i \in Ids : stores[r].rows[i] # Nil}) <= 2
    /\ \A i \in Ids:
         /\ (stores[r].rows[i] # Nil =>
              (i \in stores[r].used /\ stores[r].rows[i].generation = i))
         /\ (i \in stores[r].used => i < stores[r].next)
============================================================================
