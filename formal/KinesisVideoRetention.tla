---------------------- MODULE KinesisVideoRetention ----------------------
EXTENDS Naturals, Sequences

(* A single team database with two populated streams. The primary can be
   recreated. Times are local ticks; capacity is two primary or four total
   rows. An identity is (physical stream, incarnation, global number). *)
Row(s,g,n,p,t,exp) == [stream |-> s, gen |-> g, num |-> n, payload |-> p,
                         server |-> t, producer |-> 9, expires |-> exp]
Origin == [rows |-> <<Row("A",1,1,"first",1,4), Row("B",1,2,"peer",1,20)>>,
           gen |-> 1, ttl |-> 3, now |-> 2, serial |-> 2, backed |-> TRUE]
Reply(code, data, n) == [code |-> code, data |-> data, number |-> n]
Ack(n) == Reply("PERSISTED", <<>>, n)
Quiet == Reply("OK", <<>>, 0)
Refusal == Reply("refused", <<>>, 0)
Snapshot(data) == Reply("media", data, 0)
Outcome(db, reply) == [db |-> db, reply |-> reply]
Owned(r,d) == r.stream = "A" /\ r.gen = d.gen
Live(d) == SelectSeq(d.rows, LAMBDA r: r.expires > d.now)
IDs(rows) == [i \in 1..Len(rows) |-> <<rows[i].num,rows[i].gen,rows[i].payload>>]
A == <<1,1,"first">>
B == <<2,1,"peer">>
C == <<3,1,"second">>
D == <<3,2,"second">>
E == <<4,1,"second">>
Put(d, op) ==
  LET live == Live(d)
      own == SelectSeq(live, LAMBDA r: Owned(r,d))
      allowed == op # "denied" /\ op # "malformed" /\ d.backed
      committed == op # "cancel" /\ op # "failCommit"
      room == Len(own) < 2 /\ Len(live) < 4
      n == d.serial + 1
      new == Row("A",d.gen,n,"second",d.now,d.now+d.ttl)
  IN IF ~allowed \/ ~committed THEN Outcome(d,Refusal)
     ELSE IF d.ttl = 0 THEN Outcome([d EXCEPT !.rows = live],Reply("RECEIVED",<<>>,0))
     ELSE IF ~room THEN Outcome(d,Refusal)
     ELSE Outcome([d EXCEPT !.rows = Append(live,new), !.serial = n],
                  IF op = "lostReply" THEN Reply("lost",<<>>,0) ELSE Ack(n))
Read(d, op) ==
  LET live == Live(d)
      own == SelectSeq(live, LAMBDA r: Owned(r,d))
      n == CASE op = "earliest" -> 0 [] op = "afterFirst" -> 1 [] op = "foreign" -> 2 [] OTHER -> 3
      valid == n = 0 \/ (\E i \in 1..Len(own): own[i].num = n)
      after == SelectSeq(own, LAMBDA r: r.num > n)
  IN IF ~d.backed \/ op = "denyRead" \/ ~valid THEN Outcome(d,Refusal)
     ELSE Outcome([d EXCEPT !.rows = live],Snapshot(IDs(after)))
Retention(d, ttl) ==
  LET surviving == SelectSeq(d.rows, LAMBDA r: ~Owned(r,d) \/ (r.expires > d.now /\ ttl > 0 /\ r.server + ttl > d.now))
      renewed == [i \in 1..Len(surviving) |-> LET r == surviving[i] IN
                    IF Owned(r,d) THEN [r EXCEPT !.expires = r.server + ttl] ELSE r]
  IN Outcome([d EXCEPT !.rows = renewed, !.ttl = ttl],Quiet)
Step(d,op) ==
  CASE op \in {"put","denied","malformed","cancel","failCommit","lostReply"} -> Put(d,op)
    [] op \in {"earliest","afterFirst","afterLast","foreign","denyRead"} -> Read(d,op)
    [] op = "short" -> Retention(d,2)
    [] op = "zero" -> Retention(d,0)
    [] op = "extend" -> Retention(d,7)
    [] op = "tick" -> Outcome([d EXCEPT !.now = @+1],Quiet)
    [] op = "recreate" -> Outcome([d EXCEPT !.gen = @+1,
          !.rows = SelectSeq(@, LAMBDA r: r.stream # "A"), !.ttl = 3],Quiet)
    [] op = "custom" -> Outcome([d EXCEPT !.backed = FALSE],Quiet)
    [] op = "reopen" -> Outcome(d,Quiet)
    [] op = "putPeer" -> LET n == d.serial+1 IN Outcome([d EXCEPT !.serial = n,
           !.rows = Append(@,Row("B",1,n,"peer-next",d.now,20))],Ack(n))

(* Each history's terminal service reply and stored rows are specified
   literally. These expected values do not call the transitions above. *)
Case(ops,ans,primary,peer) == [ops |-> ops, ans |-> ans, primary |-> primary, peer |-> peer]
H(ops,ans,primary) == Case(ops,ans,primary,<<B>>)
Histories == {
 H(<<"put">>,Ack(3),<<A,C>>), H(<<"earliest">>,Snapshot(<<A>>),<<A>>),
 H(<<"put","earliest">>,Snapshot(<<A,C>>),<<A,C>>),
 H(<<"put","afterFirst">>,Snapshot(<<C>>),<<A,C>>), H(<<"put","afterLast">>,Snapshot(<<>>),<<A,C>>),
 H(<<"foreign">>,Refusal,<<A>>), H(<<"put","denyRead">>,Refusal,<<A,C>>),
 H(<<"denied">>,Refusal,<<A>>), H(<<"put","denied">>,Refusal,<<A,C>>), H(<<"malformed">>,Refusal,<<A>>),
 H(<<"cancel">>,Refusal,<<A>>), H(<<"failCommit">>,Refusal,<<A>>),
 H(<<"put","put">>,Refusal,<<A,C>>),
 Case(<<"putPeer","put">>,Ack(4),<<A,E>>,<<B,<<3,1,"peer-next">>>>),
 Case(<<"putPeer","putPeer","put">>,Refusal,<<A>>,<<B,<<3,1,"peer-next">>,<<4,1,"peer-next">>>>),
 H(<<"lostReply">>,Reply("lost",<<>>,0),<<A,C>>), H(<<"lostReply","reopen","earliest">>,Snapshot(<<A,C>>),<<A,C>>),
 H(<<"zero","put">>,Reply("RECEIVED",<<>>,0),<<>>), H(<<"zero","malformed">>,Refusal,<<>>),
 H(<<"zero","extend","earliest">>,Snapshot(<<>>),<<>>),
 H(<<"tick","earliest">>,Snapshot(<<A>>),<<A>>),
 H(<<"tick","tick","earliest">>,Snapshot(<<>>),<<>>),
 H(<<"tick","tick","extend","earliest">>,Snapshot(<<>>),<<>>),
 H(<<"tick","short","earliest">>,Snapshot(<<>>),<<>>),
 H(<<"short","tick","earliest">>,Snapshot(<<>>),<<>>),
 H(<<"put","tick","tick","afterFirst">>,Refusal,<<A,C>>),
 H(<<"put","tick","tick","earliest">>,Snapshot(<<C>>),<<C>>),
 H(<<"put","tick","tick","tick","earliest">>,Snapshot(<<>>),<<>>),
 H(<<"put","tick","tick","earliest","put">>,Ack(4),<<C,E>>),
 H(<<"recreate","put">>,Ack(3),<<D>>), H(<<"recreate","put","afterFirst">>,Refusal,<<D>>),
 H(<<"recreate","put","earliest">>,Snapshot(<<D>>),<<D>>),
 H(<<"custom","extend","put">>,Refusal,<<A>>), H(<<"custom","earliest">>,Refusal,<<A>>),
 H(<<"tick","tick","denyRead">>,Refusal,<<A>>)
}
VARIABLES history, pos, store, response
vars == <<history,pos,store,response>>
Init == /\ history \in Histories /\ pos = 0 /\ store = Origin /\ response = Quiet
Next == IF pos < Len(history.ops)
        THEN LET result == Step(store,history.ops[pos+1]) IN
             /\ store' = result.db /\ response' = result.reply /\ pos' = pos+1 /\ UNCHANGED history
        ELSE UNCHANGED vars
Spec == Init /\ [][Next]_vars
Done == pos = Len(history.ops)
TypeOK == pos \in 0..Len(history.ops) /\ store.gen \in {1,2} /\ store.now \in 2..5 /\ store.backed \in BOOLEAN
ReplyKind == ~Done \/ response.code = history.ans.code
FragmentNumber == ~Done \/ response.number = history.ans.number
ReturnedMedia == ~Done \/ response.data = history.ans.data
RetainedRows == ~Done \/ IDs(SelectSeq(store.rows, LAMBDA r: r.stream = "A")) = history.primary
PeerRows == ~Done \/ IDs(SelectSeq(store.rows, LAMBDA r: r.stream = "B")) = history.peer
=============================================================================
