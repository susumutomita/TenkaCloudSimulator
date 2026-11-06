----------------------- MODULE CFNConditionMemoDepth -----------------------
EXTENDS Naturals, FiniteSets, Sequences
CONSTANT AbsentCondition
Nodes == {1,2,3}
Orders == {<<1,2,3>>,<<1,3,2>>,<<2,1,3>>,<<2,3,1>>,<<3,1,2>>,<<3,2,1>>}
Leaf(v) == [kind |-> "Leaf",target |-> 0,value |-> v]
Alias(n) == [kind |-> "Alias",target |-> n,value |-> FALSE]
Malformed == [kind |-> "Malformed",target |-> 0,value |-> FALSE]
Choices == {Leaf(TRUE),Leaf(FALSE),Malformed} \cup {Alias(n):n \in 0..3}
Graphs == [Nodes -> Choices]
ZeroHeights == [n \in Nodes |-> 0]
ZeroValues == [n \in Nodes |-> FALSE]

\* Independent oracle: literally enumerate at most three named definitions.
\* A fourth dereference cannot reach a new name in this three-name universe.
ExpectedKind(g,n) ==
 IF g[n].kind="Leaf" THEN "Valid"
 ELSE IF g[n].kind="Malformed" THEN "Malformed"
 ELSE LET a==g[n].target IN
  IF a \notin Nodes THEN "Unknown" ELSE IF a=n THEN "Cycle"
  ELSE IF g[a].kind="Leaf" THEN "Valid"
  ELSE IF g[a].kind="Malformed" THEN "Malformed"
  ELSE LET b==g[a].target IN
   IF b \notin Nodes THEN "Unknown" ELSE IF b \in {n,a} THEN "Cycle"
   ELSE IF g[b].kind="Leaf" THEN "Valid"
   ELSE IF g[b].kind="Malformed" THEN "Malformed"
   ELSE IF g[b].target \notin Nodes THEN "Unknown" ELSE "Cycle"
ExpectedHeight(g,n) ==
 IF g[n].kind="Leaf" THEN 1
 ELSE IF g[n].kind#"Alias" THEN 0
 ELSE LET a==g[n].target IN
  IF a \notin Nodes THEN 0 ELSE IF g[a].kind="Leaf" THEN 2
  ELSE IF g[a].kind#"Alias" THEN 0
  ELSE LET b==g[a].target IN
   IF b \notin Nodes THEN 0 ELSE IF g[b].kind="Leaf" THEN 3 ELSE 0
ExpectedValue(g,n) ==
 IF g[n].kind="Leaf" THEN g[n].value
 ELSE IF g[n].kind#"Alias" THEN FALSE
 ELSE LET a==g[n].target IN
  IF a \notin Nodes THEN FALSE ELSE IF g[a].kind="Leaf" THEN g[a].value
  ELSE IF g[a].kind#"Alias" THEN FALSE
  ELSE LET b==g[a].target IN
   IF b \notin Nodes THEN FALSE ELSE IF g[b].kind="Leaf" THEN g[b].value ELSE FALSE
ExpectedAdmission(g,bound) ==
 \A n \in Nodes : ExpectedKind(g,n)="Valid" /\ ExpectedHeight(g,n)<=bound

Outcome(ok,h,v,height,value) == [ok |-> ok,heights |-> h,values |-> v,height |-> height,value |-> value]
Failure(h,v) == Outcome(FALSE,h,v,0,FALSE)
RECURSIVE Evaluate(_,_,_,_,_,_,_)
Evaluate(g,bound,n,depth,visiting,h,v) ==
 IF depth>bound THEN Failure(h,v)
 ELSE IF n \notin Nodes THEN Failure(h,v) (* UnknownRefusal *)
 ELSE IF h[n]>0 THEN
   IF depth+h[n]-1<=bound (* CachedDepthAdmission *)
   THEN Outcome(TRUE,h,v,h[n],v[n]) (* CachedValue *) ELSE Failure(h,v)
 ELSE IF n \in visiting THEN Failure(h,v) (* VisitingRefusal *)
 ELSE IF g[n].kind="Malformed" THEN Failure(h,v) (* MalformedRefusal *)
 ELSE IF g[n].kind="Leaf" THEN
   Outcome(TRUE,[h EXCEPT ![n]=1],[v EXCEPT ![n]=g[n].value],1,g[n].value)
 ELSE LET child==Evaluate(g,bound,g[n].target,depth+1,visiting \cup {n},h,v)
          height==1+child.height (* AliasStructuralHeight *)
      IN IF ~child.ok THEN Failure(child.heights,child.values)
         ELSE Outcome(TRUE,[child.heights EXCEPT ![n]=height],
                           [child.values EXCEPT ![n]=child.value],height,child.value)

VARIABLE state
vars == <<state>>
Init == \E graph \in Graphs,bound \in {2,3},order \in Orders :
 state=[graph |-> graph,bound |-> bound,order |-> order,index |-> 0,
        heights |-> ZeroHeights,values |-> ZeroValues,
        done |-> FALSE,accepted |-> FALSE,published |-> AbsentCondition,last |-> AbsentCondition]
Step ==
 LET root==state.order[state.index+1]
     result==Evaluate(state.graph,state.bound,root,1,{},state.heights,state.values)
     finished==~result.ok \/ state.index+1=3 (* ValidateEveryDefinition *)
 IN /\ ~state.done /\ state.index<3
    /\ state'=[state EXCEPT !.index=@+1,!.heights=result.heights,!.values=result.values,
         !.done=finished,!.accepted=finished /\ result.ok,
         !.published=IF finished /\ result.ok THEN result.values ELSE AbsentCondition,
         !.last=[root |-> root,beforeHeights |-> state.heights,
                  beforeValues |-> state.values,ok |-> result.ok]]
Next == Step
Spec == Init /\ [][Next]_vars

TypeOK == /\ state.graph \in Graphs /\ state.bound \in {2,3} /\ state.order \in Orders
          /\ state.index \in 0..3 /\ state.heights \in [Nodes -> 0..3]
          /\ state.values \in [Nodes -> BOOLEAN] /\ state.done \in BOOLEAN
          /\ state.accepted \in BOOLEAN
RejectedPublishesNoValues == state.done /\ ~state.accepted => state.published=AbsentCondition
AllDefinitionsValidated == state.done /\ state.accepted => \A n \in Nodes : state.heights[n]>0
NoInvalidGraphAccepted == state.done /\ state.accepted => \A n \in Nodes : ExpectedKind(state.graph,n)="Valid"
NoOverDepthAccepted == state.done /\ state.accepted => \A n \in Nodes : ExpectedHeight(state.graph,n)<=state.bound
NoCycleMemoized == \A n \in Nodes : state.heights[n]>0 => ExpectedKind(state.graph,n)#"Cycle"
NoUnknownMemoized == \A n \in Nodes : state.heights[n]>0 => ExpectedKind(state.graph,n)#"Unknown"
NoMalformedMemoized == \A n \in Nodes : state.heights[n]>0 => ExpectedKind(state.graph,n)#"Malformed"
MemoHeightsExact == \A n \in Nodes : state.heights[n]>0 => state.heights[n]=ExpectedHeight(state.graph,n)
MemoValuesExact == \A n \in Nodes : state.heights[n]>0 => state.values[n]=ExpectedValue(state.graph,n)
PublishedValuesExact == state.done /\ state.accepted => state.published=[n \in Nodes |-> ExpectedValue(state.graph,n)]
AdmissionExact == state.done => state.accepted=ExpectedAdmission(state.graph,state.bound)
=============================================================================
