# One optional private RDS generation owns a local PostgreSQL workload

This provider-internal Linux/amd64 option does not extend the AWS Query wire
shape, external endpoints, CLI defaults, or guest networking. The application
must explicitly choose `catalog.NewWithOwnedPrivatePostgres` instead of the
ordinary constructor with an independently admitted PostgreSQL bundle, a
private empty work directory, the native topology validator, and one canonical
region and instance identifier. Run one such owner per provider SQLite database
and close it before closing that database. An existing instance is not
adopted. Ordinary never-opted-in catalogs install no fence.

**Create.** Existing IAM and native private IPv4 topology checks run first.
For the nominated standalone PostgreSQL record with explicit private intent
and no contradictory version, the transaction inserts both its configuration
and a lease identifying the ARN, creation sequence, generation 1 and private
provider owner. Their commit is the linearization point. Reread the exact
lease, record and native topology before and after launching and authenticating
the owned PostgreSQL process. No SQLite transaction spans launch or teardown.
A mismatch after handshake physically closes the process before releasing
the lease. The AWS result is the already committed configuration (status
`creating`, no endpoint), including when the local workload cannot start. Do
not retry a committed Create to try to restart its host process. The trusted
fixed Probe distinguishes a healthy local process from absence or debt without
exposing an address, port, credential or arbitrary operation.

**Retirement.** An authorized Modify first checks the merged proposal and the
exact current generation in a bounded lookup. An already-invalid proposal
does not stop the workload. After discarding that lookup transaction,
authorized modifying, tagging, untagging and deleting actions retire the
exact generation before mutation. The normal Modify write repeats native and
catalog validation so an intervening topology change cannot be stored.
Physical shutdown must be confirmed before deleting the matching lease; a
later metadata write failure does not resurrect the old process. An independent
catalog writer cannot update or delete that generation while its lease
exists. The owner never reattaches after a generation change or same-name
recreation, and opening a new opted-in owner refuses an occupied work root or
any existing lease. If the workload might remain or release is unconfirmed,
keep its workspace and the durable exact-row fence, even if a later metadata
reader opens the DB without the runtime option. There is no automated debt
recovery and no PID-file adoption; operator reconciliation is outside this
option. A crash may leave private data, but does not remove the fence.

The focused authorized Query fixture uses real native SQLite network state and
PostgreSQL, including independent racing writes, authorized retirement,
post-authentication topology loss, and an injected occupied slot. It does not
prove production CLI composition, any guest-to-PostgreSQL path, or full RDS.
