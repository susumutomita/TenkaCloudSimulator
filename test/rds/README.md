# RDS PostgreSQL SQL prerequisite

`write_nonce.sql` inserts and commits a fresh 64-character hex run nonce. `read_nonce.sql` must be executed by a different connection after commit. Both receive the nonce via the caller-supplied session setting `tc_rds.run_nonce`. The expected row is the SHA-256 of the nonce, computed independently by the caller. Returning a value from the writer alone is not proof the data was committed.

To verify the SQL itself on a disposable PostgreSQL engine, run:

```sh
python3 test/rds/verify_local_sql.py --pg-bin /absolute/path/to/postgresql/bin
```

This optional check needs Python 3, `initdb`, and `postgres` on a Unix host, not a database service. It starts a new PostgreSQL standalone backend per step on a private temporary data directory. The SQL runs with the permissions of a non-superuser role explicitly granted schema usage and column-level insert and select on the fixture table. It tests a missing nonce, commit, a read after a full process restart, and the absence of a rolled-back row after another restart; SQL errors fail the check. Only aggregate evidence is printed. There is no listener, cloud call, or new runtime dependency for the simulator.

PostgreSQL standalone mode does **not** authenticate a client and does not run inside an EC2 guest. This check cannot certify RDS provisioning, the guest's SQL client, the VM network, ownership isolation, or #130 as a whole. The full path requires an approved EC2 guest plus an owner- and generation-bound PostgreSQL backend and separately authenticated sessions through the simulator's canonical guest command route.

The optional `--tcp-scram` mode additionally requires the matching `psql` and permission to use a local IPv4 socket. It starts real PostgreSQL only on `127.0.0.1` at a dynamically chosen port (Unix sockets disabled), creates a disposable database, a random bootstrap username, and a synthetic SCRAM login. It makes a separate `psql` process for each operation. On each connection it reads a random, server-owned cluster name and compares it locally before sending any fixture SQL. A different name or no proof aborts without sending writes. The check also uses valid credentials with a deliberately mismatched expected name and verifies that no row was written. It checks the committed row before and after a full server restart, verifies that a different password fails with the server's SQLSTATE `28P01`, and checks rollback invisibility after reopening. It neither accepts caller credentials nor falls back to standalone mode when TCP is unavailable.

```sh
python3 test/rds/verify_local_sql.py --pg-bin /absolute/path/to/postgresql/bin --tcp-scram
```

TCP mode is **host-only**; it proves local PostgreSQL wire behavior, not EC2, the canonical guest command, or simulator-backed RDS.
