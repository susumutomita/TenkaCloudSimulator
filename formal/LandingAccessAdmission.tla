----------------------- MODULE LandingAccessAdmission -----------------------
EXTENDS Naturals, FiniteSets

PublicPaths == {"/_simulator/", "/_simulator/landing/landing.css",
                "/_simulator/landing/landing.mjs",
                "/_simulator/landing/topology-data.mjs",
                "/_simulator/landing/topology-canvas.mjs"}
APIPaths == {"/", "/_simulator/v1/access", "/_simulator/v1/iam/identities"}
UnknownPaths == {"/_simulator/landing/unknown.mjs",
                 "/_simulator/landing/../v1/access"}
ReadMethods == {"GET", "HEAD"}
Requests == [path : PublicPaths \cup APIPaths \cup UnknownPaths,
             method : ReadMethods \cup {"POST"},
             hostOK : BOOLEAN, originOK : BOOLEAN, authorized : BOOLEAN]
Responses == {"Pending", "Forbidden", "Unauthorized", "Static", "Method", "API", "NotFound"}

VARIABLES request, done, response
vars == <<request, done, response>>

PublicMatch(r) == r.path \in PublicPaths
Route(r) == IF r.path \in PublicPaths
            THEN IF r.method \in ReadMethods THEN "Static" ELSE "Method"
            ELSE IF r.path \in APIPaths THEN "API" ELSE "NotFound"
Admission(r) == IF ~r.hostOK \/ ~r.originOK THEN "Forbidden"
                ELSE IF PublicMatch(r) THEN Route(r)
                ELSE IF ~r.authorized THEN "Unauthorized" ELSE Route(r)

Init == /\ request \in Requests /\ done = FALSE /\ response = "Pending"
Admit == /\ ~done /\ done' = TRUE /\ request' = request
         /\ response' = Admission(request)
Next == Admit
Spec == Init /\ [][Next]_vars

TypeOK == /\ request \in Requests /\ done \in BOOLEAN /\ response \in Responses
ExternalBoundary == done /\ (~request.hostOK \/ ~request.originOK) => response = "Forbidden"
PublicResponse == done /\ request.hostOK /\ request.originOK /\ request.path \in PublicPaths
                  => response = (IF request.method \in ReadMethods THEN "Static" ELSE "Method")
TokenBoundary == done /\ request.hostOK /\ request.originOK
                 /\ request.path \notin PublicPaths /\ ~request.authorized
                 => response = "Unauthorized"
UnknownNeverStatic == done /\ request.path \notin PublicPaths => response # "Static"
=============================================================================
