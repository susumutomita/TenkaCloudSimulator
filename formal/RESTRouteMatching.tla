------------------------ MODULE RESTRouteMatching ------------------------
EXTENDS Integers, Sequences, FiniteSets
CONSTANT None
TemplateIDs == {"root", "literal", "parameter", "greedy", "allGreedy",
                "duplicate", "emptyLiteral", "badGreedy", "badName"}
PathIDs == {"root", "files", "fixed", "other", "filesThree", "dots",
            "empty", "escaped", "pair", "filesDots", "fixedTail"}
Lit(value) == [kind |-> "literal", value |-> value, name |-> None]
Cap(name) == [kind |-> "capture", value |-> None, name |-> name]
Greedy(name) == [kind |-> "greedy", value |-> None, name |-> name]
Tokens(t) == CASE t = "root" -> <<>>
 [] t = "literal" -> <<Lit("files"), Lit("fixed")>>
 [] t = "parameter" -> <<Lit("files"), Cap("id")>>
 [] t = "greedy" -> <<Lit("files"), Greedy("proxy")>>
 [] t = "allGreedy" -> <<Greedy("proxy")>>
 [] t = "duplicate" -> <<Cap("id"), Cap("id")>>
 [] t = "emptyLiteral" -> <<Lit("a"), Lit(""), Cap("id")>>
 [] t = "badGreedy" -> <<Greedy("proxy"), Lit("x")>>
 [] t = "badName" -> <<Cap(None)>>
Segments(p) == CASE p = "root" -> <<>> [] p = "files" -> <<"files">>
 [] p = "fixed" -> <<"files", "fixed">> [] p = "other" -> <<"files", "other">>
 [] p = "filesThree" -> <<"files", "a", "b">> [] p = "dots" -> <<"a", "..", "b">>
 [] p = "empty" -> <<"a", "", "b">> [] p = "escaped" -> <<"%2F", "b">>
 [] p = "pair" -> <<"x", "y">> [] p = "filesDots" -> <<"files", "..">>
 [] p = "fixedTail" -> <<"files", "fixed", "tail">>
Valid(ts) == \A i \in 1..Len(ts) :
 (ts[i].kind = "literal" \/ ts[i].name # None) /\
 (ts[i].kind = "greedy" => i = Len(ts))
HasGreedy(ts) == \E i \in 1..Len(ts) : ts[i].kind = "greedy"
Fits(ts, ps) == Valid(ts) /\
 (IF HasGreedy(ts) THEN Len(ps) >= Len(ts) ELSE Len(ps) = Len(ts)) /\
 (\A i \in 1..Len(ts) : ts[i].kind = "literal" =>
    IF i <= Len(ps) THEN ps[i] = ts[i].value ELSE FALSE)
CaptureNames(ts) == {ts[i].name : i \in {j \in 1..Len(ts) : ts[j].kind # "literal"}}
FinalSlot(ts,n) == CHOOSE i \in 1..Len(ts) : ts[i].name = n /\
 (\A j \in 1..Len(ts) : ts[j].name = n => j <= i)
Captures(ts,ps) == [n \in CaptureNames(ts) |->
 LET i == FinalSlot(ts,n)
 IN IF ts[i].kind = "greedy" THEN SubSeq(ps,i,Len(ps)) ELSE <<ps[i]>>]
Score(ts) == Cardinality({i \in 1..Len(ts) : ts[i].kind = "literal"})
ExpectedPaths(t) == CASE t = "root" -> {"root"}
 [] t = "literal" -> {"fixed"}
 [] t = "parameter" -> {"fixed", "other", "filesDots"}
 [] t = "greedy" -> {"fixed", "other", "filesThree", "filesDots", "fixedTail"}
 [] t = "allGreedy" -> PathIDs \ {"root"}
 [] t = "duplicate" -> {"fixed", "other", "escaped", "pair", "filesDots"}
 [] t = "emptyLiteral" -> {"empty"}
 [] OTHER -> {}
ExpectedScore(t) == CASE t = "literal" \/ t = "emptyLiteral" -> 2
 [] t \in {"parameter", "greedy"} -> 1 [] OTHER -> 0
LastPairValue(p) == CASE p = "fixed" -> "fixed" [] p = "other" -> "other"
 [] p = "escaped" -> "b" [] p = "pair" -> "y" [] p = "filesDots" -> ".."
ExpectedCaptures(t,p) ==
 IF t \in {"parameter", "duplicate"} THEN [id |-> <<LastPairValue(p)>>]
 ELSE IF t = "emptyLiteral" THEN [id |-> <<"b">>]
 ELSE IF t = "allGreedy" THEN [proxy |-> Segments(p)]
 ELSE IF t = "greedy" THEN [proxy |-> CASE p = "fixed" -> <<"fixed">>
   [] p = "other" -> <<"other">> [] p = "filesThree" -> <<"a","b">>
   [] p = "filesDots" -> <<"..">> [] p = "fixedTail" -> <<"fixed","tail">>]
 ELSE [n \in {} |-> n]
VARIABLES done, template, path, matched, score, captures
vars == <<done,template,path,matched,score,captures>>
Init == /\ done = FALSE /\ template = "root" /\ path = "root"
        /\ matched = FALSE /\ score = -1 /\ captures = [n \in {} |-> n]
Evaluate(t,p) ==
 LET ts == Tokens(t) ps == Segments(p) accepted == Fits(ts,ps)
 IN /\ ~done /\ done' = TRUE /\ template' = t /\ path' = p
    /\ matched' = accepted
    /\ score' = IF accepted THEN Score(ts) ELSE -1
    /\ captures' = IF accepted THEN Captures(ts,ps) ELSE [n \in {} |-> n]
Next == \E t \in TemplateIDs, p \in PathIDs : Evaluate(t,p)
TypeOK == /\ done \in BOOLEAN /\ template \in TemplateIDs /\ path \in PathIDs
          /\ matched \in BOOLEAN /\ score \in {-1,0,1,2}
MatchOracle == done => matched = (path \in ExpectedPaths(template))
Specificity == (done /\ matched) => score = ExpectedScore(template)
CaptureFidelity == (done /\ matched) => captures = ExpectedCaptures(template,path)
RejectedShape == (done /\ ~matched) => score = -1 /\ captures = [n \in {} |-> n]
InvalidTemplateRejected == (done /\ template \in {"badGreedy","badName"}) => ~matched
Spec == Init /\ [][Next]_vars
=============================================================================
