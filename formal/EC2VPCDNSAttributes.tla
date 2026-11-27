-------------------------- MODULE EC2VPCDNSAttributes --------------------------
EXTENDS Naturals

\* Each request is evaluated twice against one state scope. Include all four
\* initial native DNS combinations: enabling names requires support at that
\* moment; the two knobs are not a single multi-field write.
VARIABLES prior, db, req, tries, reply
vars == <<prior, db, req, tries, reply>>
VPC == {"first", "neighbor", "stack"}
Attr == {"support","hostnames"}
Ops == {"read","write"}
Gate == {"ok","other-region","ownership-conflict","storage-fault"}
Req == [op: Ops, grantedOp: Ops, id: VPC, grantedID: VPC, attr: Attr,
        on: BOOLEAN, gate: Gate]
Row(s,h) == [support |-> s, hostnames |-> h, gen |-> 0]
Init ==
  /\ \E s,h \in BOOLEAN : prior = [v \in VPC |-> CASE v = "first" -> Row(s,h)
                                         [] v = "neighbor" -> Row(TRUE,FALSE)
                                         [] OTHER -> Row(TRUE,TRUE)]
  /\ db = prior /\ req \in Req /\ tries = 0 /\ reply = "none"

\* Admission for this transition. The frame and the following invariants
\* concern a single request, not an atomic save of both DNS attributes.
Granted == req.id = req.grantedID /\ req.op = req.grantedOp
Owned == req.id # "stack"
CanSetNames == req.attr # "hostnames" \/ ~req.on \/ db[req.id].support
Admit == Granted /\ req.gate = "ok" /\ (req.op = "read" \/ (Owned /\ CanSetNames))
NeedsWrite == req.op = "write" /\ db[req.id][req.attr] # req.on
Serve ==
  /\ tries < 2 /\ tries' = tries + 1
  /\ db' = IF Admit /\ NeedsWrite
            THEN [db EXCEPT ![req.id][req.attr] = req.on, ![req.id].gen = @ + 1]
            ELSE db
  /\ reply' = IF ~Admit THEN "refused"
               ELSE IF req.op = "read" THEN (IF db[req.id][req.attr] THEN "on" ELSE "off") ELSE "saved"
  /\ UNCHANGED <<prior,req>>
Next == Serve \/ (tries = 2 /\ UNCHANGED vars)

Rows == [VPC -> [support: BOOLEAN, hostnames: BOOLEAN, gen: 0..2]]
TypeOK == /\ prior \in Rows /\ db \in Rows /\ req \in Req /\ tries \in 0..2
          /\ reply \in {"on","off","none","refused","saved"}

\* These independently describe the expected receipt against the pre-state.
Usable == req.grantedOp = req.op /\ req.grantedID = req.id /\ req.gate = "ok"
LegalWrite == req.id \in {"first","neighbor"}
              /\ ~(req.attr = "hostnames" /\ req.on /\ ~prior[req.id].support)
Expected == IF ~Usable THEN "refused"
            ELSE IF req.op = "read" THEN (IF prior[req.id][req.attr] THEN "on" ELSE "off")
            ELSE IF LegalWrite THEN "saved" ELSE "refused"
ExactReceipt == tries = 0 \/ reply = Expected
Frame == /\ db["stack"] = prior["stack"]
         /\ \A v \in VPC \ {req.id}: db[v] = prior[v]
RefusalOrReadUnchanged == tries = 0 \/ Expected = "saved" \/ db = prior
ExactSetting == tries = 0 \/ Expected # "saved" \/
                \A a \in Attr : db[req.id][a] =
                  IF a = req.attr THEN req.on ELSE prior[req.id][a]
NoSpuriousRevision ==
  db[req.id].gen =
    IF tries > 0 /\ Expected = "saved" /\ prior[req.id][req.attr] # req.on
    THEN prior[req.id].gen+1 ELSE prior[req.id].gen
=============================================================================
