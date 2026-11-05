--------------------- MODULE RESTProxyEventBoundary ---------------------
EXTENDS Naturals, Sequences, FiniteSets
CONSTANT None, Limit
HeaderNames == {"x-app", "cookie", "x-simulator-token", "x-amz-security-token",
                "connection", "proxy-authorization", "authorization"}
BlockedNames == {"cookie", "x-simulator-token", "x-amz-security-token",
                 "connection", "proxy-authorization"}
HeaderCases == {"empty", "mixed", "signedOnly", "mixedAuthorization"}
QueryCases == {"empty", "duplicates", "malformed"}
SizeCases == {"small", "rawOver", "encodedOver"}
RawHeaders(h) == [k \in HeaderNames |->
 CASE h = "empty" -> <<>>
 [] k = "x-app" /\ h = "mixed" -> <<"first","last">>
 [] k \in BlockedNames /\ h = "mixed" -> <<"local-credential">>
 [] k = "authorization" /\ h = "mixed" -> <<"bearer-app">>
 [] k = "authorization" /\ h = "signedOnly" -> <<"aws-signature">>
 [] k = "authorization" /\ h = "mixedAuthorization" -> <<"aws-signature","bearer-app">>
 [] OTHER -> <<>>]
Allowed(k,v) == k \notin BlockedNames /\
               ~(k = "authorization" /\ v = "aws-signature")
Keep(k,vs) ==
 IF Len(vs) = 0 THEN <<>>
 ELSE IF Len(vs) = 1 THEN IF Allowed(k,vs[1]) THEN vs ELSE <<>>
 ELSE IF Allowed(k,vs[1]) /\ Allowed(k,vs[2]) THEN vs
 ELSE IF Allowed(k,vs[1]) THEN <<vs[1]>>
 ELSE IF Allowed(k,vs[2]) THEN <<vs[2]>> ELSE <<>>
ProjectHeaders(h) ==
 LET raw == RawHeaders(h)
 IN [k \in {n \in HeaderNames : Len(Keep(n,raw[n])) > 0} |-> Keep(k,raw[k])]
LastValues(m) == [k \in DOMAIN m |-> m[k][Len(m[k])]]
Query(q) == IF q = "duplicates" THEN [q |-> <<"first","last">>, slash |-> <<"a/b">>]
            ELSE [n \in {} |-> n]
RawSize(s) == IF s = "rawOver" THEN Limit + 1 ELSE 1
EncodedSize(s) == IF s = "small" THEN Limit ELSE Limit + 1
ActualPath == "/files/actual"
ResourceTemplate == "/files/{id}"
StagePath == "/prod/files/actual"
EmptyMap == [n \in {} |-> n]
EmptyEvent == [path |-> None, resource |-> None, stagePath |-> None,
 headers |-> EmptyMap, multiHeaders |-> EmptyMap,
 query |-> None, multiQuery |-> None, body |-> None, isBase64Encoded |-> FALSE]
VARIABLES done, headerCase, queryCase, sizeCase, validText, validContext,
          accepted, event
vars == <<done,headerCase,queryCase,sizeCase,validText,validContext,accepted,event>>
Init == /\ done = FALSE /\ headerCase = "empty" /\ queryCase = "empty"
        /\ sizeCase = "small" /\ validText = TRUE /\ validContext = TRUE
        /\ accepted = FALSE /\ event = EmptyEvent
Build(h,q,s,textOK,contextOK) ==
 LET admit == textOK /\ contextOK /\ q # "malformed" /\
              RawSize(s) <= Limit /\ EncodedSize(s) <= Limit
     projected == ProjectHeaders(h)
     queryValues == Query(q)
     result == [path |-> ActualPath, resource |-> ResourceTemplate, stagePath |-> StagePath,
       headers |-> LastValues(projected), multiHeaders |-> projected,
       query |-> IF DOMAIN queryValues = {} THEN None ELSE LastValues(queryValues),
       multiQuery |-> IF DOMAIN queryValues = {} THEN None ELSE queryValues,
       body |-> "application-body", isBase64Encoded |-> FALSE]
 IN /\ ~done /\ done' = TRUE
    /\ headerCase' = h /\ queryCase' = q /\ sizeCase' = s
    /\ validText' = textOK /\ validContext' = contextOK
    /\ accepted' = admit
    /\ event' = IF admit THEN result ELSE EmptyEvent
Next == \E h \in HeaderCases, q \in QueryCases, s \in SizeCases,
           textOK \in BOOLEAN, contextOK \in BOOLEAN : Build(h,q,s,textOK,contextOK)
TypeOK == /\ done \in BOOLEAN /\ headerCase \in HeaderCases
          /\ queryCase \in QueryCases /\ sizeCase \in SizeCases
          /\ validText \in BOOLEAN /\ validContext \in BOOLEAN /\ accepted \in BOOLEAN
Admission == done => accepted =
 (validText /\ validContext /\ queryCase # "malformed" /\ sizeCase = "small")
FailureNoEvent == (done /\ ~accepted) => event = EmptyEvent
CredentialSealed == (done /\ accepted) =>
 /\ DOMAIN event.multiHeaders \cap {"cookie", "x-simulator-token", "x-amz-security-token",
   "connection", "proxy-authorization"} = {}
 /\ ("authorization" \in DOMAIN event.multiHeaders =>
      \A i \in 1..Len(event.multiHeaders["authorization"]) :
        event.multiHeaders["authorization"][i] # "aws-signature")
ApplicationData == (done /\ accepted) =>
 /\ (headerCase = "mixed" =>
       IF "x-app" \in DOMAIN event.multiHeaders /\ "x-app" \in DOMAIN event.headers THEN
        /\ event.multiHeaders["x-app"] = <<"first","last">>
        /\ event.headers["x-app"] = "last" ELSE FALSE)
 /\ (headerCase \in {"mixed","mixedAuthorization"} =>
       IF "authorization" \in DOMAIN event.multiHeaders /\ "authorization" \in DOMAIN event.headers THEN
        /\ event.multiHeaders["authorization"] = <<"bearer-app">>
        /\ event.headers["authorization"] = "bearer-app" ELSE FALSE)
ActualRoute == (done /\ accepted) =>
 /\ event.path = "/files/actual" /\ event.resource = "/files/{id}"
 /\ event.stagePath = "/prod/files/actual"
QueryProjection == (done /\ accepted) =>
 IF queryCase = "duplicates" THEN
  /\ event.multiQuery = [q |-> <<"first","last">>, slash |-> <<"a/b">>]
  /\ event.query = [q |-> "last", slash |-> "a/b"]
 ELSE event.query = None /\ event.multiQuery = None
BodyPreserved == (done /\ accepted) =>
 event.body = "application-body" /\ event.isBase64Encoded = FALSE
Spec == Init /\ [][Next]_vars
=============================================================================
