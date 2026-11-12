# Start the local simulator

From the source checkout, with Go1.26 installed:

```sh
make start
```

This builds **current source** and runs the simulator in the foreground at
`http://127.0.0.1:4566/_simulator/console/`. Authentication is enabled. On an
interactive stdout terminal with `CI` completely unset, startup displays the
operator token for sign-in and saves the same value in `./simulator-token`.
Redirected output, pipes, `/dev/null` and CI output contain the file location and
sign-in guidance but no token value. Even `CI=''` or `CI=false` suppresses it.
Protect terminal scrollback/screen recordings containing the deliberately shown
token, and never share the operator token with participants. Integrations should
read the private file without logging its contents.

## State, token and overrides

Default paths are relative to the checkout root, including `make -C <checkout>`:

| Setting | Default | Meaning |
| --- | --- | --- |
| START_LISTEN | 127.0.0.1:4566 | Numeric loopback address/port only; IPv6 loopback is also accepted |
| START_STATE | ./practice.db | Persistent SQLite state; reuses the existing file |
| START_TOKEN_FILE | ./simulator-token | Existing private regular file is reused, without token rotation |
| GO | go | Go executable name or exact path; paths containing spaces are supported |

```sh
make start START_LISTEN=127.0.0.1:4567 \
  START_STATE=/private/path/team.db \
  START_TOKEN_FILE=/private/path/operator-token
```

New parent directories/files are created with a private umask; the token file is
0600. Existing files and permissions are not reset, erased or automatically
repaired. The CLI rejects symlinks, public permissions, invalid lengths and
nonregular operator-token files. Use separate state and token paths. Default
paths already match the previous README manual command; no migration or copying
is performed. The defaults are ignored by Git; custom private token paths must
also be kept out of version control.

`START_STATE=:memory:` deliberately opts into an ephemeral database; normal
`make start` persists state. Each team still needs its own process/DB/token
boundary and host authorization. Participant tokens remain ephemeral. Startup
does not expose externally, configure real AWS, deploy or provide a daemon.
There is no free-form extra-flags override to disable auth or allow remote
binding through `make start`.

Ctrl+C sends SIGINT to the foreground terminal process group; SIGTERM to make
is forwarded to the foreground CLI. Both stop its listener/state. When sending
SIGINT programmatically, target the CLI or its process group, not make alone.
Restart preserves the operator token and SQLite resources. There is no automatic
browser launch or background process to clean up. Port-in-use and invalid token
errors are printed without token contents.

## Fresh builds and existing binaries

Every `make start` invokes Go's incremental build cache, with CGO disabled and
the host OS/architecture (ignoring leftover cross-build GOOS/GOARCH settings).
It stages and atomically installs a complete executable. A compiler error, missing
Go or empty/nonexecutable compiler output **does not run an older dist binary**.
The previous executable and state/token files are preserved on build failure.
If Go cannot be found, install it or set `GO=/path/to/go`; this requirement also
applies when dist is already present. First builds may need the normal pinned Go
module/toolchain downloads; runtime does not need Go, Node, Python or Docker.

For a verified prebuilt binary without a compiler, run it explicitly:

```sh
./dist/tenkacloud-simulator --listen=127.0.0.1:4566 \
  --state=./practice.db --token-file=./simulator-token --show-token
```

The CLI's default remains file-only. `--show-token` is an explicit opt-in and
independently requires a real stdout terminal and `CI` unset. Noninteractive
output is suppressed even with that flag. Combining it with `--unsafe-no-auth`
is rejected. On other console platforms where terminal detection has not been
implemented, output stays file-only. Linux behavior is tested with actual PTYs;
macOS/Windows helpers require their native console APIs and are cross-compiled,
not claimed executed by Linux tests. `make start` itself uses a POSIX shell and
GNU make; Windows users can use WSL or the standalone binary.

`make build`, `make dev` and direct CLI commands retain their previous behavior.
In particular, `make dev` is not the new persistent-start alias: the CLI's direct
`--state` default remains ephemeral memory.

## Verification and finite model

`make test-start` uses real source builds in a temporary checkout, then checks
unbuilt/built launch, stale-source refresh, missing/failed compiler refusal,
private persistent files, restart data, actual PTY/CI/redirection output,
authentication and SIGINT/SIGTERM cleanup. Tests use their own temporary tokens;
reports contain booleans only. It is also run by `make e2e`. CLI Go tests cover
terminal detection and explicit-display/authentication boundaries.

`formal/StartupTokenVisibility.tla` models one configured operator-token file,
two abstract token values and at most two captured emissions. It checks private
first creation, existing-file identity preservation, invalid-file refusal and
historical emission guards (explicit show, actual TTY, CI unset, auth enabled).
Each emission saves its own context; a later terminal/CI change cannot relabel an
older emission. Guard-removal and token-overwrite mutations test sensitivity.
This is a bounded design safety model, not a proof of OS permissions, syscalls,
randomness, file-write/crash atomicity, build freshness, process shutdown or
implementation refinement. Real file/PTY/build/HTTP tests are independent checks.


### Formal registry and source correspondence

The original model/config and three mutation anchors are registered without
changing the Startup implementation. Registry-safe mutation IDs are
`OmittedCIGuard`, `OmittedTTYGuard` and `OverwriteExistingToken`; their expected
invariants remain `HistoryVisibility`, `HistoryVisibility` and
`FileIdentityFrozen`. Existing five models/rules remain mandatory; the registry
contains seven positives and thirteen required counterexamples. The checker also
requires the Startup model and all five of its original invariants, so removing
both a declaration and its files or removing a property in both registry/CFG
cannot silently reduce protection.

| Source / existing concrete test | Bounded correspondence and limits |
|---|---|
| `cmd/simulator/main.go` / `TestTokenDisplayCannotDisableAuthentication` | Explicit display cannot opt out of authentication; this does not prove the entire HTTP/server path |
| `cmd/simulator/terminal.go` / `TestTokenDisplayRejectsRedirectsAndCI` | CI means variable **presence**, including empty/false values; non-file/nonterminal output remains file-only. Actual terminal detection is an input abstraction in TLA+ |
| `cmd/simulator/terminal_linux.go` and `terminal_linux_test.go` / `TestExplicitTokenDisplayUsesActualTerminalAndPreservesFile` | Real Linux PTY assertions separately cover explicit visibility and unchanged saved token; they are not an OS-level mathematical proof |
| Darwin/Windows/other terminal helpers | Platform implementations select the TTY input; unverified platforms conservatively return false. Native Darwin/Windows execution is not claimed by Linux/cross-compile evidence |
| `Makefile`, `scripts/start.sh`, `test/start/check_start.py` | Launcher forwards authenticated/file-only or explicitly interactive context. Real temporary build/file/PTY/HTTP/signal checks cover other behavior separately; freshness, process cleanup and file-write atomicity are not token-model guarantees |

The `go.mod` change only removes an indirect annotation for an already pinned,
directly imported dependency; versions and go.sum are unchanged. It is classified
by the separate strict metadata recognizer and direct HEAD byte/mode guards,
**not** by a token invariant or a general dependency exemption.

The current registry combines StartupTokenVisibility, SSMParameterState and the
original five protected models. Exact changed-blob records and required model/
property guards are maintained together. New integration-head Go/PTY/TLC and
hosted CI are separate checks; source-only restacking is not fresh execution.

### Explicit Linux Battle runtime

`--battle-runtime-config /absolute/private/runtime.json` selects the bounded
Linux/amd64 VM provider. It requires authenticated numeric loopback operation;
`--unsafe-no-auth` and `--allow-remote` cannot be combined with it. Default startup
still does not enable VM workloads. The configuration file and runtime directory
must be private; the directory cannot be a symlink.

The JSON contains `bundleManifest`, `bundleArchive`, `manifestSha256`,
`runtimeDirectory`, `layout` (`emulator`, `firmware`, `baseDisk`), `imageIds`,
`lifetimeUntilMs`, and `clock` (`domain`, `validFromMs`, `validUntilMs`). Paths to
files/directories are absolute. Layout entries are distinct manifest-listed
relative paths. Image IDs are explicit aliases for that pinned bundle, not
requests to download an image. Runtime lifetime is bounded to 24 hours from
startup and defaults to 8 hours when `lifetimeUntilMs` is omitted; every VM in
the runtime shares that window. Unknown or duplicate configuration fields are
rejected.

Use only an independently trusted bundle. The manifest SHA-256 and archive/file
hash checks establish integrity, not vendor authenticity or containment. Loading
configuration extracts verified assets but does not start a VM. A CloudFormation
instance becomes ready only through the configured provider's real launch path;
metadata, a stack ID, or an HTTP capability handle is not physical authority.

The clock domain and interval are **not Host clock qualification**. Host must also
supply an independently qualified conservative mapping from its original
monotonic deadlines into this domain. Without that mapping, the Host must keep
Battle unavailable. No automatic synchronizer or real-machine qualification is
provided by the configuration file.

The provider's `PublicDnsName`/IMDS hostname is a reserved `.simulator.invalid`
label, not publicly resolvable DNS. Host probing uses authenticated, owner-bound
local capabilities instead of fetching that label. Shutdown reports unresolved
cleanup as an error and retains its database/bundle rather than claiming cleanup
from an HTTP timeout.
