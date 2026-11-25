# Optional owned PostgreSQL workload

This Linux/amd64 provider-internal package is not wired to server startup, an
AWS resource, a guest or any UI. The standalone binary still has no external
database requirement. An eventual trusted host composition can opt in by
supplying a separately installed, immutable PostgreSQL 15.20 bundle and its
independently approved content pin. Never take either value from a simulator
resource or an untrusted archive. Neither a hostname nor an existing DB is
accepted. No endpoint, SQL proxy, instance readiness or credentials are
published.

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
