--------------------- MODULE CognitoFederationConfiguration --------------------
EXTENDS Naturals, FiniteSets

\* Private configuration only. One Call represents one already authorized
\* SQLite transaction, NOT verification of a SAML assertion or a PKCE proof.
CONSTANT StepLimit, None
VARIABLE s
Target == <<"account", "east", 1>>
PeerAccount == <<"peer", "east", 1>>
PeerRegion == <<"account", "west", 1>>
Sibling == <<"account", "east", 2>>
Keys == {Target, PeerAccount, PeerRegion, Sibling}
Providers == {"Keycloak", "Entra"}
ClientIDs == 1..2
Writes == {"provider+", "provider-", "client+", "client-"}
Ops == Writes \cup {"binding"}
Grants == {"exact", "off", "denied", "otherAction", "otherPool", "oneDescribe"}
Versions == {"unbound", "current", "stale"}
R == [op : Ops, grant : Grants, provider : Providers, id : ClientIDs,
      version : Versions, callback : BOOLEAN, shape : BOOLEAN, pin : BOOLEAN, s256 : BOOLEAN, scopes : BOOLEAN]
Outcomes == {"ok", "input", "denied", "noPool", "unknown", "duplicate", "inUse", "stale"}
Row == [live : BOOLEAN, providers : SUBSET Providers, clients : [ClientIDs -> (Providers \cup {None})], rev : 0..StepLimit]
Blank(live) == [live |-> live, providers |-> {}, clients |-> [i \in ClientIDs |-> None], rev |-> 0]
Existing == [Blank(TRUE) EXCEPT !.providers = {"Keycloak"}, !.clients[1] = "Keycloak"]
Init == s \in {[rows |-> [k \in Keys |-> IF k = Target THEN row ELSE Existing],
                step |-> 0, last |-> None] : row \in {Blank(TRUE), Existing}}
Request(op, p, i) ==
  [op |-> op, grant |-> "exact", provider |-> p, id |-> i, version |-> "unbound",
   callback |-> TRUE, shape |-> TRUE, pin |-> TRUE, s256 |-> TRUE, scopes |-> TRUE]
Inputs ==
  LET inspect == Request("binding", "Keycloak", 1)
      addP == Request("provider+", "Entra", 1)
      addC == Request("client+", "Keycloak", 1)
  IN {Request(op,p,i) : op \in Ops, p \in Providers, i \in ClientIDs}
     \cup {[inspect EXCEPT !.grant = g] : g \in Grants}
     \cup {[addC EXCEPT !.grant = g] : g \in Grants \ {"oneDescribe"}}
     \cup {[addP EXCEPT !.shape = FALSE], [addP EXCEPT !.pin = FALSE], [addC EXCEPT !.pin = FALSE],
            [inspect EXCEPT !.version = "stale"], [inspect EXCEPT !.version = "current"],
            [inspect EXCEPT !.callback = FALSE], [inspect EXCEPT !.s256 = FALSE],
            [inspect EXCEPT !.scopes = FALSE], [inspect EXCEPT !.pin = FALSE]}

ShapeOK(r) == r.shape /\ (r.op = "provider+" => r.pin) /\ (r.op = "binding" => r.s256)
Allowed(r) == r.grant = "exact"
Referenced(row, provider) == \E i \in ClientIDs : row.clients[i] = provider
Fits(r, row) == r.provider \in row.providers /\ row.clients[r.id] = r.provider
                 /\ r.callback /\ r.scopes /\ r.pin
Result(r, row) ==
  IF ~ShapeOK(r) THEN "input"
  ELSE IF ~Allowed(r) THEN "denied"
  ELSE IF ~row.live THEN "noPool"
  ELSE CASE r.op = "provider+" -> IF r.provider \in row.providers THEN "duplicate" ELSE "ok"
         [] r.op = "provider-" -> IF r.provider \notin row.providers THEN "unknown"
                                   ELSE IF Referenced(row, r.provider) THEN "inUse" ELSE "ok"
         [] r.op = "client+" -> IF r.provider \notin row.providers \/ ~r.pin THEN "unknown"
                                 ELSE IF row.clients[r.id] # None THEN "duplicate" ELSE "ok"
         [] r.op = "client-" -> IF row.clients[r.id] = None THEN "unknown" ELSE "ok"
         [] OTHER -> IF r.version = "stale" THEN "stale" ELSE IF Fits(r, row) THEN "ok" ELSE "unknown"
Apply(r, row, result) ==
  IF result # "ok" \/ r.op = "binding" THEN row
  ELSE LET rev == s.step + 1
       IN CASE r.op = "provider+" -> [row EXCEPT !.providers = @ \cup {r.provider}, !.rev = rev]
            [] r.op = "provider-" -> [row EXCEPT !.providers = @ \ {r.provider}, !.rev = rev]
            [] r.op = "client+" -> [row EXCEPT !.clients[r.id] = r.provider, !.rev = rev]
            [] OTHER -> [row EXCEPT !.clients[r.id] = None, !.rev = rev]
Install(rows,row) == [rows EXCEPT ![Target] = row]
Call(r) ==
  /\ s.step < StepLimit
  /\ LET result == Result(r,s.rows[Target])
         row == Apply(r,s.rows[Target],result)
     IN s' = [s EXCEPT !.step = @ + 1, !.rows = Install(s.rows,row),
                       !.last = [r |-> r, before |-> s.rows, result |-> result, touched |-> ShapeOK(r) /\ Allowed(r),
                                 binding |-> IF r.op = "binding" /\ result = "ok" THEN row.rev ELSE None]]
ReplacePool ==
  /\ s.step < StepLimit
  /\ s' = [s EXCEPT !.rows = Install(s.rows,Blank(~s.rows[Target].live)), !.step = @ + 1, !.last = None]
Next == (\E r \in Inputs : Call(r)) \/ ReplacePool \/ UNCHANGED s

TypeOK ==
  /\ s.rows \in [Keys -> Row] /\ s.step \in 0..StepLimit
  /\ s.last \in {None} \cup [r : R, before : [Keys -> Row], result : Outcomes,
                             touched : BOOLEAN, binding : (0..StepLimit) \cup {None}]
NoDanglingClient == \A k \in Keys :
  LET row == s.rows[k]
  IN \A i \in ClientIDs : row.clients[i] # None =>
        row.live /\ row.clients[i] \in row.providers

\* The witness records the before image; the independent checks below do
\* not reuse dispatch, shape, reference or matching helpers.
AuthorityFrame == IF s.last = None THEN TRUE ELSE
  LET w == s.last
      r == w.r
      trusted == r.grant = "exact" /\ r.shape
                  /\ (r.op # "provider+" \/ r.pin) /\ (r.op # "binding" \/ r.s256)
  IN /\ w.touched = trusted
     /\ IF trusted THEN w.result \notin {"denied", "input"}
        ELSE w.result \in {"denied", "input"} /\ w.binding = None /\ s.rows = w.before

ExactBinding == IF s.last = None THEN TRUE ELSE IF s.last.r.op # "binding" THEN TRUE ELSE
  LET w == s.last
      r == w.r
      prior == w.before[Target]
      admissible == w.touched /\ prior.live /\ r.version # "stale" /\ r.callback /\ r.pin /\ r.scopes
                    /\ r.provider \in prior.providers /\ prior.clients[r.id] = r.provider
  IN /\ s.rows = w.before
     /\ IF admissible THEN w.result = "ok" /\ w.binding = prior.rev
        ELSE w.result # "ok" /\ w.binding = None

MutationFrame == IF s.last = None THEN TRUE ELSE
  LET w == s.last
      r == w.r
      prior == w.before[Target]
      out == s.rows[Target]
      changed == w.result = "ok" /\ r.op \in Writes
      orderedRefs == {i \in ClientIDs : prior.clients[i] = r.provider}
      canWrite == w.touched /\ prior.live /\
        (CASE r.op = "provider+" -> r.provider \notin prior.providers
           [] r.op = "provider-" -> r.provider \in prior.providers /\ orderedRefs = {}
           [] r.op = "client+" -> prior.clients[r.id] = None /\ r.provider \in prior.providers /\ r.pin
           [] r.op = "client-" -> prior.clients[r.id] # None
           [] OTHER -> FALSE)
  IN /\ \A k \in Keys \ {Target} : s.rows[k] = w.before[k]
     /\ changed = canWrite
     /\ IF changed THEN
          /\ prior.live /\ out.live /\ out.rev > prior.rev
          /\ CASE r.op = "provider+" -> r.provider \notin prior.providers /\ out.providers = prior.providers \cup {r.provider} /\ out.clients = prior.clients
                 [] r.op = "provider-" -> r.provider \in prior.providers /\ orderedRefs = {} /\ out.providers = prior.providers \ {r.provider} /\ out.clients = prior.clients
                 [] r.op = "client+" -> prior.clients[r.id] = None /\ r.provider \in prior.providers /\ r.pin /\ out.providers = prior.providers /\ out.clients = [prior.clients EXCEPT ![r.id] = r.provider]
                 [] OTHER -> prior.clients[r.id] # None /\ out.providers = prior.providers /\ out.clients = [prior.clients EXCEPT ![r.id] = None]
        ELSE out = prior
=============================================================================
