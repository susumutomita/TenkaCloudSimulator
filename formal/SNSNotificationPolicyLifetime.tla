---------------------- MODULE SNSNotificationPolicyLifetime ----------------------
EXTENDS Naturals

\* One evolving Standard/FIFO/missing topic and a populated peer in another
\* physical database have the same ARN. The peer may have the same generation.
CONSTANT MaxGen
VARIABLES topic, row, high, peer, captured, last
vars == <<topic, row, high, peer, captured, last>>

Docs == {"allowA", "allowB", "deny", "allowDeny", "denyAllow"}
Absent == [gen |-> 0, doc |-> "none"]
PeerSeed == [gen |-> 1, doc |-> "allowA"]
Writes == {"ok", "denied", "invalid", "foreignARN", "failedCommit", "canceled", "deadline"}
Reads == {"ok", "caller", "foreignARN", "badRow", "readError"}

\* Concrete parser, SQLite and IAM behavior are verified separately. allowA
\* and allowB denote distinct accepted policy bytes with the same decision.
CanSet(m) == topic = "standard" /\ m # "denied" /\ m # "invalid" /\ m # "foreignARN"
Commit(m) == m \notin {"failedCommit", "canceled", "deadline"}
NextGeneration == IF row.gen = 0 THEN high + 1 ELSE row.gen
Grant(doc) == doc \in {"allowA", "allowB"}
Inspect(m) == topic = "standard" /\ m = "ok"
OwnTicket(source) == source = "own"
SavedGeneration(source) == IF source = "own" THEN captured ELSE peer.gen

Store == [kind |-> topic, policy |-> row, watermark |-> high]
Notice(op, m, doc, gen, visible, ok) ==
    [op |-> op, mode |-> m, doc |-> doc, gen |-> gen, visible |-> visible,
     ok |-> ok, before |-> Store, oldTicket |-> captured]

Init == /\ topic = "missing" /\ row = Absent /\ high = 0
        /\ peer = PeerSeed /\ captured = 0
        /\ last = Notice("init", "ok", "none", 0, "none", FALSE)

Create(k) ==
    /\ topic = "missing"
    /\ topic' = k
    /\ last' = Notice("create", "ok", k, 0, "none", TRUE)
    /\ UNCHANGED <<row, high, peer, captured>>

SetPolicy(doc, m) ==
    /\ row.gen # 0 \/ high < MaxGen
    /\ LET accepted == CanSet(m) /\ Commit(m)
           gen == NextGeneration
       IN /\ row' = IF accepted THEN [gen |-> gen, doc |-> doc] ELSE row
          /\ high' = IF accepted /\ row.gen = 0 THEN gen ELSE high
          /\ last' = Notice("set", m, doc, 0, "none", accepted)
    /\ UNCHANGED <<topic, peer, captured>>

Delete(m) ==
    /\ topic # "missing"
    /\ LET removed == m = "ok"
       IN /\ topic' = IF removed THEN "missing" ELSE topic
          /\ row' = IF removed THEN Absent ELSE row
          /\ peer' = peer
          /\ high' = high
          /\ last' = Notice("delete", m, "none", 0, "none", removed)
    /\ UNCHANGED captured

GetPolicy(m) ==
    LET ok == topic # "missing" /\ m = "ok"
        visible == IF ok THEN row.doc ELSE "none"
    IN /\ last' = Notice("get", m, "none", 0, visible, ok)
       /\ UNCHANGED <<topic, row, high, peer, captured>>

Resolve(m) ==
    LET readable == Inspect(m)
        generation == IF readable THEN row.gen ELSE 0
        allowed == readable /\ Grant(row.doc)
    IN /\ last' = Notice("resolve", m, "none", generation, IF readable THEN "checked" ELSE "refused", allowed)
       /\ UNCHANGED <<topic, row, high, peer, captured>>

Capture ==
    /\ topic = "standard" /\ Grant(row.doc)
    /\ captured' = row.gen
    /\ last' = Notice("capture", "ok", "none", 0, "none", TRUE)
    /\ UNCHANGED <<topic, row, high, peer>>

\* A consumer comparing a previously captured grant to the current private
\* resolver. No outbox, delivery, SDK or general resource policy is modeled.
UseCaptured(source) ==
    LET generation == SavedGeneration(source)
        allowed == OwnTicket(source) /\ generation # 0 /\ generation = row.gen /\ Grant(row.doc)
    IN /\ last' = Notice("use", source, "none", generation, "none", allowed)
       /\ UNCHANGED <<topic, row, high, peer, captured>>

Restart ==
    /\ row' = row
    /\ high' = high
    /\ last' = Notice("restart", "ok", "none", 0, "none", TRUE)
    /\ UNCHANGED <<topic, peer, captured>>

Next == (\E k \in {"standard", "fifo"} : Create(k))
        \/ (\E doc \in Docs, m \in Writes : SetPolicy(doc, m))
        \/ (\E m \in {"ok", "failedCommit"} : Delete(m))
        \/ (\E m \in {"ok", "denied", "readError"} : GetPolicy(m))
        \/ (\E m \in Reads : Resolve(m))
        \/ Capture \/ (\E source \in {"own", "peer"} : UseCaptured(source))
        \/ Restart

TypeOK == /\ topic \in {"missing", "standard", "fifo"}
          /\ high \in 0..MaxGen /\ row.gen \in 0..MaxGen
          /\ row.doc \in Docs \cup {"none"} /\ captured \in 0..MaxGen
SetAdmission == last.op # "set" \/
                last.ok = (last.before.kind = "standard" /\ last.mode = "ok")
SetRefusalPreservesStore == last.op # "set" \/ last.ok \/ Store = last.before
NewGeneration == last.op # "set" \/ ~last.ok \/ last.before.policy.gen # 0 \/
                 (row.gen = last.before.watermark + 1 /\ high = row.gen)
EditKeepsGeneration == last.op # "set" \/ ~last.ok \/ last.before.policy.gen = 0 \/
                       (row.gen = last.before.policy.gen /\ high = last.before.watermark)
PolicyBytesExact == last.op # "set" \/ ~last.ok \/ row.doc = last.doc
DeleteFrame == last.op # "delete" \/
               (IF last.mode = "ok" THEN topic = "missing" /\ row = Absent /\ high = last.before.watermark
                ELSE Store = last.before)
PeerFrame == peer = PeerSeed
GetReadback == last.op # "get" \/
               (last.ok = (topic # "missing" /\ last.mode = "ok") /\
                last.visible = (IF last.ok THEN row.doc ELSE "none"))
ReadsPreserveStore == last.op \notin {"get", "resolve", "use"} \/ (Store = last.before /\ captured = last.oldTicket)
ResolverAdmission == last.op # "resolve" \/
                     last.visible = (IF topic = "standard" /\ last.mode = "ok" THEN "checked" ELSE "refused")
ResolverGeneration == last.op # "resolve" \/
                      last.gen = (IF topic = "standard" /\ last.mode = "ok" THEN row.gen ELSE 0)
ResolverDecision == last.op # "resolve" \/
                    last.ok = (topic = "standard" /\ last.mode = "ok" /\ row.doc \in {"allowA", "allowB"})
CapturedCurrentGrant == last.op # "use" \/
                        last.ok = (last.mode = "own" /\ captured > 0 /\ captured = row.gen
                                   /\ topic = "standard" /\ row.doc \in {"allowA", "allowB"})
CaptureExact == last.op # "capture" \/ captured = row.gen
RestartPreservesStore == last.op # "restart" \/ (Store = last.before /\ captured = last.oldTicket)
=============================================================================
