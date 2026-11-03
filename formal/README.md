# Finite state model

`ObjectStore.tla` models two buckets and two keys, shared object ownership, create/put/delete transitions, and an abstract identity decision `allow /\ ~deny`. `ObjectStore.cfg` checks type correctness, no orphan objects, no unauthorized mutation, and explicit-deny precedence.

Run `TLA_JAR=/path/to/tla2tools.jar make formal`. The jar is TLC 1.7.4 from the official tlaplus/tlaplus release and checked against `tla2tools.sha256`. The script also removes the nonempty-delete guard in a temporary mutant and requires TLC to detect a NoOrphans violation. No Java/TLC dependency is needed at application runtime.

Correspondence: Create → Store.Create; Put → Store.Put; DeleteObject → Store.Delete; DeleteBucket → Store.DeleteBucket; object parent relation → SQLite FK and transaction; authorization predicate → selected principal policy decision before provider operations. Go tests exercise those concrete paths and concurrency.

The model abstracts object bytes, time, limits, policy patterns/ARN/action matching, policy document parsing, SQLite internals, crashes, external-process writers, and HTTP/UI behavior. It verifies 125 distinct reachable states for this finite configuration, not an unbounded theorem or refinement proof. There are no liveness claims. Policy state in the model is a two-Boolean abstraction; full IAM semantics are not proven.

No `Poteto` toolchain or configuration was found in the cloud checkout or its available repository instructions. That name is not assumed to mean TLC, Apalache, or a proof assistant.
