# SimpleDB: bounded formal coverage

These models describe the first five native operations. They are finite
contracts, not proofs that Go, SQLite, XML or an SDK implement them.

- Lifecycle covers abstract domain and item mutations, including rollback,
  restart and isolation. Its controls detect early visibility and lost
  existing values in an append.
- PageCursor covers the strict ordered page from current rows, with account,
  region and instance-key epoch bound to genuinely issued cursors. There is
  no cross-page snapshot. Its controls detect inclusion of the anchor and
  acceptance of a prior epoch; the independent observation stays unchanged.
- QueryAdmission covers the dispatch decision and abstract result or error
  category. A trusted actor is the local context identity; its model flag also
  represents availability of the identity service. The policy decision is
  independent of that flag. Scope is server-configured, not selected by
  identity metadata or by the caller. The two controls exercise a single
  synthetic malformed input class and an incorrect abstract scope selection,
  respectively; the expected failures are MalformedNeverDispatches and
  ConfiguredScopeBinding, with other expectation predicates unchanged.

Raw decoding, exact validation bounds, encoding, signature and IAM
evaluation, router precedence, Gateway dependency and server initializer
wiring, and native/cloud compatibility are separate obligations. Existing
shared files retain their prior model mappings; those mappings do not prove
the new integration glue. Tool execution and merge evidence are separate
from this registration.
