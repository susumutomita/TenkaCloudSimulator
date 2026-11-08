--------------------------- MODULE ProviderPath ---------------------------
EXTENDS Naturals
CONSTANT None
Registered == {"aws","azure","gcp","probe"}
DefaultProvider == "aws"
Modes == {"Root","Literal","EncodedID","EncodedDelimiter","DoubleEncodedDelimiter"}
CarrierIDs == 1..13
\* Explicit finite transport examples, NOT an implementation of URL decoding.
\* Several escaped spellings intentionally have the SAME decoded path.
Carriers == <<
 [decoded |-> "/bucket/a/b",escaped |-> "/bucket/a/b"],
 [decoded |-> "/bucket/a/b",escaped |-> "/bucket/a%2Fb"],
 [decoded |-> "/bucket/a/b",escaped |-> "/bucket/a%2fb"],
 [decoded |-> "/bucket/a%2Fb",escaped |-> "/bucket/a%252Fb"],
 [decoded |-> "/bucket/a%2fb",escaped |-> "/bucket/a%252fb"],
 [decoded |-> "/bucket/a//b",escaped |-> "/bucket/a//b"],
 [decoded |-> "/bucket/./a",escaped |-> "/bucket/./a"],
 [decoded |-> "/bucket/../a",escaped |-> "/bucket/../a"],
 [decoded |-> "/bucket/./a",escaped |-> "/bucket/%2E/a"],
 [decoded |-> "/bucket/../a",escaped |-> "/bucket/%2E%2E/a"],
 [decoded |-> "/bucket/日",escaped |-> "/bucket/%E6%97%A5"],
 [decoded |-> "/bucket/%E6%97%A5",escaped |-> "/bucket/%25E6%2597%25A5"],
 [decoded |-> "/bucket/a b",escaped |-> "/bucket/a%20b"] >>
\* Named finite corruption maps model WHAT must not happen, not Go Clean or
\* percent-decoder implementations. Actual HTTP tests bridge those operations.
Clean(path) == CASE path = "/bucket/a//b" -> "/bucket/a/b"
                  [] path = "/bucket/./a" -> "/bucket/a"
                  [] path = "/bucket/../a" -> "/a"
                  [] OTHER -> path
DecodeAgain(path) == CASE path \in {"/bucket/a%2Fb","/bucket/a%2fb"} -> "/bucket/a/b"
                        [] path = "/bucket/%E6%97%A5" -> "/bucket/日"
                        [] OTHER -> path
PathValues == {Carriers[c].decoded:c \in CarrierIDs} \cup
              {Carriers[c].escaped:c \in CarrierIDs} \cup {"","/bucket/a","/a"}
PairType == [decoded:PathValues,escaped:PathValues]
EmptyPair == [decoded |-> "",escaped |-> ""]
Contexts == {
 [actor |-> "bound-participant",query |-> "query-a",headers |-> "headers-a",
  body |-> "body-a",requestURI |-> "original-full-uri-a"],
 [actor |-> "operator-context",query |-> "query-b",headers |-> "headers-b",
  body |-> "body-b",requestURI |-> "original-full-uri-b"]}
ContextType == [actor:{"bound-participant","operator-context",None},
 query:{"query-a","query-b"},headers:{"headers-a","headers-b"},
 body:{"body-a","body-b"},requestURI:{"original-full-uri-a","original-full-uri-b"}]
RequestType == [mode:Modes,provider:Registered \cup {"missing"},
                left:CarrierIDs,right:CarrierIDs,context:Contexts]
OutputType == [accepted:BOOLEAN,provider:Registered \cup {None},
               left:PairType,right:PairType,context:ContextType \cup {None}]
VARIABLES request,original,output,done
vars == <<request,original,output,done>>
Forward(carrier) == Carriers[carrier]
BoundaryAllowed == request.mode \in {"Root","Literal"}
SelectedProvider == IF request.mode = "Root" THEN DefaultProvider ELSE request.provider
ForwardContext == request.context
Init ==
 /\ request \in RequestType
 /\ request.left # request.right
 /\ (request.mode = "Root" => request.provider = DefaultProvider)
 /\ original = request /\ done = FALSE
 /\ output = [accepted |-> FALSE,provider |-> None,
               left |-> EmptyPair,right |-> EmptyPair,context |-> None]
Route ==
 LET accepted == BoundaryAllowed /\ SelectedProvider \in Registered
 IN /\ ~done
    /\ output' = [accepted |-> accepted,
       provider |-> IF accepted THEN SelectedProvider ELSE None,
       left |-> IF accepted THEN Forward(request.left) ELSE EmptyPair,
       right |-> IF accepted THEN Forward(request.right) ELSE EmptyPair,
       context |-> IF accepted THEN ForwardContext ELSE None]
    /\ done' = TRUE
    /\ UNCHANGED <<request,original>>
Next == Route \/ (done /\ UNCHANGED vars)
TypeOK == request \in RequestType /\ original \in RequestType /\
          output \in OutputType /\ done \in BOOLEAN
SourcePathImmutable == request = original
ActorPreserved == (done /\ output.accepted) =>
 output.context.actor = original.context.actor
ContextPreserved == (done /\ output.accepted) => output.context = original.context
UnregisteredRejected ==
 (done /\ original.mode # "Root" /\ original.provider \notin Registered) =>
    ~output.accepted /\ output.provider = None
EncodedProviderRejected ==
 (done /\ original.mode \in {"EncodedID","EncodedDelimiter","DoubleEncodedDelimiter"}) =>
    ~output.accepted /\ output.provider = None
TailPreserved == (done /\ output.accepted) =>
 /\ output.left.decoded = Carriers[original.left].decoded
 /\ output.right.decoded = Carriers[original.right].decoded
EscapedTailPreserved == (done /\ output.accepted) =>
 /\ output.left.escaped = Carriers[original.left].escaped
 /\ output.right.escaped = Carriers[original.right].escaped
\* Carrier PAIR injectivity only. This does NOT assert that different escaped
\* spellings name different logical cloud objects; that belongs to each leaf.
DistinctCarriersRemainDistinct == (done /\ output.accepted) => output.left # output.right
ProviderSelectedCorrectly == (done /\ output.accepted) =>
 output.provider = IF original.mode = "Root" THEN DefaultProvider ELSE original.provider
=============================================================================
