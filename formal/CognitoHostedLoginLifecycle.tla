------------------------- MODULE CognitoHostedLoginLifecycle ------------------------
EXTENDS Naturals

\* An authorization, verified ACS snapshot, committed grant, then public code
\* exchange, for one pending chain. Inputs are bounded protocol facts, not XML
\* or cryptographic operations. The mutable browser slot stays on its pool row.
CONSTANT None
VARIABLES w, receipt

Pool == <<"local", "east", "pool">>
Peer == <<"local", "west", "pool">>
Pools == {Pool, Peer}
Providers == {"Entra", "Keycloak"}
Registrations == {"first", "second", "replacement", "noID"}
Clients == {"a", "b"}
Refs == {"bound", "different"}
IDs == {"subjectA", "subjectB"}
Scopes == {"openid", "email", "profile", "extra"}
LocalOrigins == {"absent", "sim", "outside"}
SourceOrigins == LocalOrigins \cup {"selectedIdP", "differentIdP"} \cup Clients
Wire == {"localTLS", "plaintext", "peer", "host", "proxy", "native", "alias"}
Kinds == {"objectID", "persistentID", "transientID"}
Ops == {"start", "verify", "grant", "exchange"}
Results == {"pending", "verified", "granted", "issued", "refused"}
Revs == 0..2
Epochs == 0..1
Time == 0..2
Deadlines == 1..4

StartPair == {<<"a", "Entra">>, <<"a", "Keycloak">>, <<"b", "Keycloak">>}
ScopesFor(c) == IF c = "a" THEN {"openid", "email", "profile"} ELSE {"openid", "profile"}
IdentityKind(p) == IF p = "Entra" THEN "objectID" ELSE "persistentID"

Request == [op : Ops, wire : Wire, pool : Pools, origin : SourceOrigins,
            client : Clients, provider : Providers, redirect : Clients, scopes : SUBSET Scopes,
            state : Refs, nonce : Refs, cookie : Refs, request : Refs,
            signer : Refs \cup {"unsigned"}, issuer : Refs, audience : Pools, recipient : Pools,
            stableKind : Kinds, identity : IDs, qualifiers : Refs, code : Refs, verifier : Clients]
Pending == [pool : Pools, provider : Providers, registration : Registrations,
            client : Clients, redirect : Clients, scopes : SUBSET Scopes, state : Refs,
            nonce : Refs, cookie : Refs, request : Refs, rev : Revs, epoch : Epochs, until : Deadlines]
Proof == [p : Pending, identity : IDs, until : Deadlines]
Grant == [p : Pending, identity : IDs, until : Deadlines]
\* One fixed issuer + assertion ID pair (shared by the fixture IdPs), stamped
\* with the scope's current revision and process epoch when it was seen.
ReplayStamps == Revs \X Epochs
Subjects == Pools \X Providers \X Registrations \X IDs
Claims == {"email", "profile"}
Tokens == [issuer : Pools, idSubject : Subjects, accessSubject : Subjects,
           audience : Clients, clientID : Clients, idClaims : SUBSET Claims,
           accessClaims : SUBSET Claims, scope : SUBSET Scopes, nonce : Refs,
           idUse : {"id", "access"}, accessUse : {"id", "access"}, until : Deadlines,
           actor : {"none", "simulator"}]
Callback == [redirect : Clients, state : Refs, code : Refs]
World == [mode : {"default", "local"}, live : BOOLEAN, epoch : Epochs, rev : Revs,
          registration : [Providers -> Registrations], now : Time,
          started : BOOLEAN, pending : Pending \cup {None}, proof : Proof \cup {None},
          grant : Grant \cup {None}, replay : ReplayStamps \cup {None}, issued : 0..2,
          peerFrame : {"same", "changed"}]

Initial(mode, reg, replay) ==
  [mode |-> mode, live |-> TRUE, epoch |-> 0, rev |-> 0, registration |-> [i \in Providers |-> reg],
   now |-> 0, started |-> FALSE, pending |-> None, proof |-> None, grant |-> None,
   replay |-> replay, issued |-> 0, peerFrame |-> "same"]
Init ==
  /\ w \in {Initial("default", "first", None), Initial("local", "first", None),
            Initial("local", "first", <<0, 0>>), Initial("local", "noID", None)}
  /\ receipt = None

Base(op) ==
  [op |-> op, wire |-> "localTLS", pool |-> Pool, origin |-> "absent",
   client |-> "a", provider |-> "Entra", redirect |-> "a", scopes |-> {"openid"},
   state |-> "bound", nonce |-> "bound", cookie |-> "bound", request |-> "bound",
   signer |-> "bound", issuer |-> "bound", audience |-> Pool, recipient |-> Pool,
   stableKind |-> "objectID", identity |-> "subjectA", qualifiers |-> "bound",
   code |-> "bound", verifier |-> "a"]
StartInputs ==
  LET r == Base("start")
  IN {r,
      [r EXCEPT !.scopes = {"openid", "email", "profile"}, !.nonce = "different"],
      [r EXCEPT !.provider = "Keycloak", !.scopes = {"openid", "email"}],
      [r EXCEPT !.provider = "Keycloak", !.client = "b", !.redirect = "b",
                !.scopes = {"openid", "profile"}, !.state = "different"],
      [r EXCEPT !.client = "b", !.redirect = "b"],
      [r EXCEPT !.origin = "outside"], [r EXCEPT !.redirect = "b"],
      [r EXCEPT !.scopes = {"email"}], [r EXCEPT !.scopes = {"openid", "extra"}]}
     \cup {[r EXCEPT !.wire = bad] : bad \in Wire \ {"localTLS"}}
\* Explore one mismatch at a time in the initial profile; after any version,
\* time or epoch change, try the otherwise-current proof rather than combining
\* independent failures. Each trace can capture at most one ACS snapshot.
InitialContext(x) == x.rev = 0 /\ x.epoch = 0 /\ x.now = 0
AuthorizeInputs(x) ==
  IF x.started THEN {}
  ELSE IF InitialContext(x) THEN StartInputs ELSE {Base("start")}
ReturnInputs(x) ==
  IF x.proof # None THEN {}
  ELSE IF x.pending = None THEN {Base("verify")}
  ELSE LET p == x.pending
           r == [Base("verify") EXCEPT !.origin = "selectedIdP", !.stableKind = IdentityKind(p.provider)]
       IN IF ~InitialContext(x) THEN {r}
          ELSE {r, [r EXCEPT !.identity = "subjectB"], [r EXCEPT !.cookie = "different"],
                [r EXCEPT !.request = "different"], [r EXCEPT !.origin = "differentIdP"],
                [r EXCEPT !.signer = "different"], [r EXCEPT !.signer = "unsigned"],
                [r EXCEPT !.issuer = "different"], [r EXCEPT !.audience = Peer],
                [r EXCEPT !.recipient = Peer], [r EXCEPT !.stableKind = "transientID"],
                [r EXCEPT !.qualifiers = "different"]}
ExchangeFor(p) == [Base("exchange") EXCEPT !.client = p.client, !.redirect = p.redirect, !.verifier = p.client]
ExchangeInputs(x) ==
  IF x.grant = None THEN IF x.proof = None THEN {Base("exchange")} ELSE {ExchangeFor(x.proof.p)}
  ELSE LET p == x.grant.p
           r == ExchangeFor(p)
           other == IF p.client = "a" THEN "b" ELSE "a"
       IN IF ~InitialContext(x) THEN {r}
          ELSE {r, [r EXCEPT !.origin = p.redirect], [r EXCEPT !.origin = "outside"],
                [r EXCEPT !.client = other], [r EXCEPT !.redirect = other],
                [r EXCEPT !.verifier = other], [r EXCEPT !.code = "different"], [r EXCEPT !.pool = Peer]}

Direct(r, x) == x.mode = "local" /\ r.wire = "localTLS" /\ r.pool = Pool /\ x.live
StartOK(r, x) ==
  /\ ~x.started /\ r.origin \in {"absent", "sim"}
  /\ <<r.client, r.provider>> \in StartPair /\ r.redirect = r.client
  /\ "openid" \in r.scopes /\ r.scopes \subseteq ScopesFor(r.client)
  /\ x.registration[r.provider] # "noID"
RecordPending(r, x) ==
  [pool |-> r.pool, provider |-> r.provider, registration |-> x.registration[r.provider],
   client |-> r.client, redirect |-> r.redirect, scopes |-> r.scopes, state |-> r.state,
   nonce |-> r.nonce, cookie |-> "bound", request |-> "bound",
   rev |-> x.rev, epoch |-> x.epoch, until |-> x.now + 2]
ReturnOK(r, x) ==
  IF x.pending = None THEN FALSE
  ELSE LET p == x.pending
       IN /\ p.rev = x.rev /\ p.epoch = x.epoch /\ x.now < p.until
          /\ r.origin = "selectedIdP" /\ r.cookie = p.cookie /\ r.request = p.request
          /\ r.signer = "bound" /\ r.issuer = "bound"
          /\ r.audience = p.pool /\ r.recipient = p.pool
          /\ r.stableKind = IdentityKind(p.provider) /\ r.qualifiers = "bound"
GrantOK(x) ==
  IF x.proof = None THEN FALSE
  ELSE LET p == x.proof.p
       IN /\ x.pending = p /\ p.rev = x.rev
          /\ p.epoch = x.epoch /\ x.now < p.until /\ x.now < x.proof.until
          /\ x.replay # <<x.rev, x.epoch>>
ExchangeOK(r, x) ==
  IF x.grant = None THEN FALSE
  ELSE LET g == x.grant
           p == g.p
       IN /\ p.rev = x.rev /\ p.epoch = x.epoch /\ x.now < g.until
          /\ r.client = p.client /\ r.redirect = p.redirect /\ r.code = "bound"
          /\ r.origin \in {"absent", p.redirect} /\ r.verifier = p.client

Classify(r, x) ==
  IF ~Direct(r, x) THEN "refused"
  ELSE CASE r.op = "start"    -> IF StartOK(r, x) THEN "pending" ELSE "refused"
         [] r.op = "verify"   -> IF ReturnOK(r, x) THEN "verified" ELSE "refused"
         [] r.op = "grant"    -> IF GrantOK(x) THEN "granted" ELSE "refused"
         [] r.op = "exchange" -> IF ExchangeOK(r, x) THEN "issued" ELSE "refused"

StableSubject(g) == <<g.p.pool, g.p.provider, g.p.registration, g.identity>>
TokenReply(g, now) ==
  [issuer |-> g.p.pool, idSubject |-> StableSubject(g), accessSubject |-> StableSubject(g),
   audience |-> g.p.client, clientID |-> g.p.client, idClaims |-> (g.p.scopes \ {"openid"}),
   accessClaims |-> {}, scope |-> g.p.scopes, nonce |-> g.p.nonce,
   idUse |-> "id", accessUse |-> "access", until |-> now + 2, actor |-> "none"]

Apply(r, x, result) ==
  CASE result = "pending" -> [x EXCEPT !.pending = RecordPending(r, x), !.started = TRUE]
    [] result = "verified" -> [x EXCEPT !.proof = [p |-> x.pending, identity |-> r.identity, until |-> x.now + 1]]
    [] result = "granted" -> [x EXCEPT !.grant = [p |-> x.proof.p, identity |-> x.proof.identity, until |-> x.now + 1],
                             !.pending = None, !.replay = <<x.rev, x.epoch>>]
    [] result = "issued" -> [x EXCEPT !.grant = None, !.issued = @ + 1]
    [] OTHER -> x

Call(r) ==
  LET result == Classify(r, w)
  IN /\ w' = Apply(r, w, result)
     /\ receipt' = [r |-> r, prior |-> w, result |-> result,
                   callback |-> IF result = "granted" THEN [redirect |-> w.proof.p.redirect,
                                  state |-> w.proof.p.state, code |-> "bound"] ELSE None,
                   tokens |-> IF result = "issued" THEN TokenReply(w.grant, w.now) ELSE None]

\* The revision changes even for unrelated client edits, while registration
\* identity does not. A replacement stands for the signed remove/re-add/rebind
\* sequence; no intermediate permission is modeled. Raw stale slots may stay.
Edit ==
  /\ w.mode = "local" /\ w.live /\ w.rev = 0
  /\ w' \in {[w EXCEPT !.rev = 1], [w EXCEPT !.rev = 1, !.registration["Entra"] = "second"]}
  /\ receipt' = None
Restart ==
  /\ w.mode = "local" /\ w.epoch = 0
  /\ w' = [w EXCEPT !.epoch = 1] /\ receipt' = None
Tick ==
  /\ w.mode = "local" /\ w.started /\ w.now < 2
  /\ w' = [w EXCEPT !.now = @ + 1] /\ receipt' = None
Delete ==
  /\ w.mode = "local" /\ w.live
  /\ w' = [w EXCEPT !.live = FALSE, !.pending = None, !.grant = None, !.replay = None]
  /\ receipt' = None
Recreate ==
  /\ w.mode = "local" /\ ~w.live /\ w.rev < 2
  /\ w' = [w EXCEPT !.live = TRUE, !.rev = 2, !.registration = [i \in Providers |-> "replacement"]]
  /\ receipt' = None

Next ==
  \/ (\E r \in AuthorizeInputs(w) \cup ReturnInputs(w) \cup {Base("grant")} \cup ExchangeInputs(w) : Call(r))
  \/ Edit \/ Restart \/ Tick \/ Delete \/ Recreate

TypeOK ==
  /\ w \in World
  /\ receipt \in {None} \cup [r : Request, prior : World, result : Results,
                               callback : Callback \cup {None}, tokens : Tokens \cup {None}]
PeerPoolFrame == w.peerFrame = "same"
PoolRemovalClearsSlots == w.live \/ (w.pending = None /\ w.grant = None /\ w.replay = None)

\* The safety oracles below use the recorded input and before/after image,
\* rather than reusing the protocol's selection and transition helpers.
AdmissionAndRefusal ==
  IF receipt = None THEN TRUE
  ELSE LET b == receipt.prior
           r == receipt.r
       IN /\ IF receipt.result = "refused"
               THEN w = b /\ receipt.callback = None /\ receipt.tokens = None
               ELSE b.mode = "local" /\ b.live /\ r.pool = Pool /\ r.wire = "localTLS"
          /\ (receipt.result # "granted" => receipt.callback = None)
          /\ (receipt.result # "issued" => receipt.tokens = None)

ExactPending ==
  IF receipt = None \/ receipt.result # "pending" THEN TRUE
  ELSE LET r == receipt.r
           b == receipt.prior
           p == w.pending
       IN /\ r.op = "start" /\ ~b.started /\ w.started /\ r.origin \in {"absent", "sim"}
          /\ p # None
          /\ p.pool = Pool /\ p.provider = r.provider /\ p.registration = b.registration[r.provider]
          /\ p.registration # "noID" /\ (r.client = "b" => r.provider = "Keycloak")
          /\ p.client = r.client /\ p.redirect = r.redirect /\ p.redirect = r.client
          /\ p.scopes = r.scopes /\ "openid" \in p.scopes /\ "extra" \notin p.scopes
          /\ (r.client = "b" => "email" \notin p.scopes)
          /\ p.cookie = "bound" /\ p.request = "bound" /\ p.state = r.state /\ p.nonce = r.nonce
          /\ p.rev = b.rev /\ p.epoch = b.epoch /\ p.until = b.now + 2
          /\ w.grant = b.grant /\ w.proof = b.proof /\ w.replay = b.replay

VerifiedReturn ==
  IF receipt = None \/ receipt.result # "verified" THEN TRUE
  ELSE LET r == receipt.r
           b == receipt.prior
           p == b.pending
       IN /\ r.op = "verify" /\ p # None /\ w.proof # None
          /\ b.rev = p.rev /\ b.epoch = p.epoch /\ b.now < p.until
          /\ r.origin = "selectedIdP" /\ r.signer = "bound" /\ r.issuer = "bound"
          /\ r.audience = p.pool /\ r.recipient = p.pool
          /\ r.cookie = p.cookie /\ r.request = p.request /\ r.qualifiers = "bound"
          /\ CASE p.provider = "Entra" -> r.stableKind = "objectID"
                [] OTHER -> r.stableKind = "persistentID"
          /\ w.proof.p = p /\ w.proof.identity = r.identity /\ w.proof.until = b.now + 1
          /\ w.pending = p /\ w.grant = b.grant /\ w.replay = b.replay

CommitFence ==
  IF receipt = None \/ receipt.result # "granted" THEN TRUE
  ELSE LET r == receipt.r
           b == receipt.prior
           a == b.proof
           p == a.p
       IN /\ r.op = "grant" /\ a # None /\ b.pending = p /\ p.pool = Pool
          /\ b.rev = p.rev /\ b.epoch = p.epoch /\ b.now < p.until /\ b.now < a.until
          /\ b.registration[p.provider] = p.registration /\ b.replay # <<b.rev, b.epoch>>
          /\ w.pending = None /\ w.replay = <<b.rev, b.epoch>>
          /\ w.grant = [p |-> p, identity |-> a.identity, until |-> b.now + 1]
          /\ receipt.callback = [redirect |-> p.redirect, state |-> p.state, code |-> "bound"]

ExactExchange ==
  IF receipt = None \/ receipt.result # "issued" THEN TRUE
  ELSE LET r == receipt.r
           b == receipt.prior
           g == b.grant
           p == g.p
       IN /\ r.op = "exchange" /\ g # None /\ p.pool = r.pool
          /\ b.rev = p.rev /\ b.epoch = p.epoch /\ b.now < g.until
          /\ b.registration[p.provider] = p.registration
          /\ r.client = p.client /\ r.redirect = p.redirect /\ r.code = "bound"
          /\ (r.origin = "absent" \/ r.origin = p.redirect) /\ r.verifier = p.client

CodeConsumed ==
  /\ w.issued <= 1
  /\ IF receipt = None \/ receipt.result # "issued" THEN TRUE
     ELSE w.grant = None /\ receipt.prior.issued = 0 /\ w.issued = 1

ExactTokenProjection ==
  IF receipt = None \/ receipt.result # "issued" THEN TRUE
  ELSE LET b == receipt.prior
           g == b.grant
           t == receipt.tokens
           p == g.p
           subject == <<Pool, p.provider, b.registration[p.provider], g.identity>>
       IN /\ t # None /\ t.issuer = Pool /\ t.idUse = "id" /\ t.accessUse = "access"
          /\ t.idSubject = subject /\ t.accessSubject = subject
          /\ t.audience = p.client /\ t.clientID = p.client /\ t.nonce = p.nonce
          /\ t.scope = p.scopes /\ t.accessClaims = {}
          /\ ("email" \in t.idClaims) = ("email" \in p.scopes)
          /\ ("profile" \in t.idClaims) = ("profile" \in p.scopes)
          /\ t.actor = "none" /\ t.until = b.now + 2
=============================================================================
