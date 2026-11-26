# Optional owned PostgreSQL workload

Ordinary startup remains metadata-only: neither default startup nor
`--rds-private-postgres-topology` alone starts PostgreSQL. The trusted local
operator can opt into one owned private PostgreSQL generation on Linux/amd64
as a non-root user. Existing RDS IAM checks still apply, and qualifying
private creates and modifications still undergo topology validation. The
startup option grants no request permissions. It adds no public RDS readiness,
SQL proxy, endpoint, credentials, guest connectivity, or UI capability.

For an already approved PostgreSQL 15.20 bundle and its independently approved
tree SHA-256, place this JSON in an absolute private regular file (at most
4 KiB; mode 0600 recommended). Replace the illustrative paths and pin:

```json
{
  "bundleDirectory": "/trusted/pgsql",
  "bundleSha256": "<independently approved lowercase SHA-256>",
  "workRoot": "/private/owned-postgres-work",
  "region": "us-east-1",
  "dbInstanceIdentifier": "example-db"
}
```

Use a separate, existing, idle mode-0700 work root. The state path must be an
existing, absolute, operator-owned mode-0600 regular SQLite file with one link
in an operator-owned mode-0700 directory. The paths must satisfy the local
owner, permission and disjointness checks; no host database URL is accepted.
Then start the simulator on authenticated numeric loopback:

```sh
./dist/tenkacloud-simulator --listen 127.0.0.1:4566 \
  --state /private/state/simulator.sqlite --token-file /private/state/simulator-token \
  --rds-private-postgres-topology --rds-owned-postgres-config /private/rds.json
```

Do not combine this mode with `--allow-remote`, `--unsafe-no-auth` or
`--battle-runtime-config`. Starting the server only validates and admits the
configuration. A qualifying, IAM-authorized `CreateDBInstance` for the
nominated region and identifier must commit valid native private topology and
its exact ownership fence before the internal runtime attempts startup. An
existing instance is not adopted. The RDS API still reports configuration,
not successful PostgreSQL launch. If owned teardown cannot be confirmed,
shutdown returns an error and retains the SQLite ownership fence and private
work state; an ordinary restart does not adopt that debt.

`OpenBundle` recognizes only the bounded Linux/amd64 layout and fixed executable
names (`bin/initdb`, `bin/postgres`, `bin/psql`) plus `lib/libpq.so.5`. The pin
is SHA-256 of the literal `tcsim-pg15-bundle-v1\0linux/amd64\n` followed by the
lexical depth-first install-tree entries (including `.`). Each record is type
(`d`, `f` or `l`), relative name, three-digit octal permissions and content
SHA-256 in lowercase hex (a literal same-directory relative target for links,
empty for directories), separated by NUL, ending with LF.

Only trusted local owners and safe permissions are accepted. ELF inputs may use
the native OS libc/libm and the pinned libpq, with no outside bundle search
path. The digest and four executable/library identities are rechecked locally;
the ordinary OS and bundle owner are trusted, not sandboxed.

`Start` needs a private mode-0700 work parent and does not change existing
data. It claims one fixed slot in that parent and refuses to adopt an
existing slot, including retained debt. It creates a disposable cluster, uses
only numeric IPv4 loopback and checks actual SCRAM authentication with a random, private synthetic role and
fixed read-only SQL that verifies the cluster, version and binding. Synthetic
credentials are not arguments, environment values, returned capabilities or
logs. `Probe` permits the same bounded check only. `Close` revokes new probes,
drains the active bounded check, then attempts exact direct-child shutdown and
requires both clean exit and disappearance of the postmaster lock before
deleting the private directory. Unknown launch/init/shutdown retains private
workspace and returns failure; it never adopts a child by name or PID file.
This does not promise recovery after a killed supervisor or host crash.

For an already approved bundle with a verification pin obtained independently:

```sh
TCSIM_PG_TEST_BUNDLE=/trusted/pgsql TCSIM_PG_TEST_PIN="$APPROVED_POSTGRES_TREE_SHA256" \
  go test ./internal/rdsruntime -run '^TestActualOwnedPostgreSQL$' -v
```

This is a focused real PostgreSQL lifecycle check, not a guest or RDS
acceptance test. Without the two explicit test inputs the real test skips
and the package's offline defensive tests still run.
