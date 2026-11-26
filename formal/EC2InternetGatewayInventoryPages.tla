------------------- MODULE EC2InternetGatewayInventoryPages -------------------
EXTENDS Naturals
CONSTANT Last
VARIABLES snapshot, origin, seen, proof, publication
vars == <<snapshot, origin, seen, proof, publication>>

Good == [allowed |-> TRUE, actor |-> TRUE, selector |-> "all",
         requested |-> TRUE, region |-> TRUE, caller |-> TRUE,
         limit |-> TRUE, seal |-> TRUE, order |-> TRUE, current |-> TRUE,
         rows |-> 100, fits |-> TRUE]
Witnesses == {Good, [Good EXCEPT !.allowed = FALSE],
              [Good EXCEPT !.actor = FALSE], [Good EXCEPT !.selector = "exact"],
              [Good EXCEPT !.requested = FALSE], [Good EXCEPT !.region = FALSE],
              [Good EXCEPT !.caller = FALSE], [Good EXCEPT !.limit = FALSE],
              [Good EXCEPT !.seal = FALSE], [Good EXCEPT !.order = FALSE],
              [Good EXCEPT !.current = FALSE], [Good EXCEPT !.rows = 101],
              [Good EXCEPT !.rows = 0],
              [Good EXCEPT !.fits = FALSE]}
NoProof == [kind |-> "none", params |-> Good, sampled |-> 0, bound |-> 0]
NoPublication == [shown |-> FALSE, assembled |-> 0, current |-> TRUE]
Init ==
    /\ snapshot = 0 /\ origin = 0 /\ seen = 0
    /\ proof = NoProof /\ publication = NoPublication

Allowed(f) == f.allowed /\ f.actor /\ f.selector = "all" /\ f.requested
SafePage(f) == 0 < f.rows /\ f.rows <= 100 /\ f.fits /\ f.order /\ f.current
Witness(kind, f, sampled, bound) ==
    [kind |-> kind, params |-> f, sampled |-> sampled, bound |-> bound]

First(f) ==
    /\ seen = 0
    /\ Allowed(f) /\ SafePage(f)
    /\ origin' = snapshot
    /\ seen' = 1
    /\ proof' = Witness("first", f, snapshot, snapshot)
    /\ UNCHANGED <<snapshot, publication>>

Continue(f) ==
    /\ seen > 0 /\ seen < Last
    /\ Allowed(f) /\ SafePage(f)
    /\ f.seal /\ f.region /\ f.caller /\ f.limit
    /\ snapshot = origin
    /\ seen' = seen + 1
    /\ proof' = Witness("next", f, snapshot, origin)
    /\ UNCHANGED <<snapshot, origin, publication>>

ReplaceInventory ==
    /\ snapshot = 0 /\ snapshot' = 1
    /\ UNCHANGED <<origin, seen, proof, publication>>

Refresh ==
    /\ seen' = 0 /\ origin' = snapshot
    /\ publication' = NoPublication
    /\ UNCHANGED <<snapshot, proof>>

Publish ==
    /\ seen = Last
    /\ publication' = [shown |-> TRUE, assembled |-> seen,
                         current |-> proof.params.current]
    /\ UNCHANGED <<snapshot, origin, seen, proof>>

Next ==
    (\E f \in Witnesses: First(f) \/ Continue(f))
    \/ ReplaceInventory \/ Refresh \/ Publish \/ UNCHANGED vars

TypeOK ==
    /\ snapshot \in {0,1} /\ origin \in {0,1} /\ seen \in 0..Last
    /\ proof \in [kind: {"none", "first", "next"}, params: Witnesses,
                   sampled: {0,1}, bound: {0,1}]
    /\ publication \in [shown: BOOLEAN, assembled: 0..Last, current: BOOLEAN]
PageAuthorized == proof.kind # "none" =>
    proof.params.allowed /\ proof.params.actor /\ proof.params.selector = "all" /\ proof.params.requested
PageBounded == proof.kind # "none" => 0 < proof.params.rows /\ proof.params.rows <= 100 /\ proof.params.fits
PageFenced == proof.kind # "none" => proof.params.order /\ proof.params.current
ContinuationBound == proof.kind = "next" =>
    proof.params.seal /\ proof.params.region /\ proof.params.caller /\
    proof.params.limit /\ proof.sampled = proof.bound
CompletePublication == publication.shown =>
    publication.assembled = Last /\ publication.current
=============================================================================
