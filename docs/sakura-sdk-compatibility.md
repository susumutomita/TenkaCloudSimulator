# Sakura Disk metadata and the community SDK

This local, opt-in provider implements bounded Disk configuration metadata. It does
not allocate block storage, boot a VM, attach an Icon, or report physical Availability.

The community `sacloud/sacloud-sdk-go` serializer at commit
`d8e98d8762e049e3ffd40bc70667cf9ad20029c0` emits `Icon: {"ID": 0, "Tags": []}`
for an unset Icon. A real local CLI request initially received HTTP 501 for this
shape. The adapter now recognizes it as an explicit no-icon marker. Existing
`Icon: null` and `{}` remain accepted; numeric `ID: 0` may omit `Tags`, or include
exactly an empty array. String `"0"`, other numbers, booleans, null IDs, null Tags,
nonempty Tags, unknown members, and real Icon IDs remain unsupported (HTTP 501).
These neutral markers do not change persisted metadata or create Icon support.

The compatibility fixture uses the pinned community SDK's real request serializer
and response/error decoder through its public middleware seam, with a terminal
transport restricted to allocated numeric-loopback endpoints. This does not verify
the stock authentication, retry, default-endpoint behavior, or high-level waiters.
It does not establish differential equivalence with the real Sakura service.
SDK acquisition and test dependencies belong to an isolated development module;
the runtime module does not depend on this SDK. Runtime acceptance results must be
reported separately from source inspection and the original native HTTP probes.


## Reproducible development and hosted gate

The fixture lives in `test/sdk/sakura`, with its own `go.mod` and `go.sum`.
Its SDK version resolves exactly to the commit above; it adds no dependency to
the runtime module. The upstream community SDK is Apache-2.0 licensed; source
and notices remain in the downloaded module, rather than being copied into the
runtime or its embedded assets.

From the repository root (Go and Python 3 required):

```sh
make build
(cd test/sdk/sakura && go mod download && go mod verify && go vet ./... && go test -race ./... && go build -mod=readonly -o ../../../dist/sakura-sdk .)
python3 test/e2e/sakura_sdk.py --binary dist/tenkacloud-simulator --client dist/sakura-sdk --report reports/sakura-sdk.json
```

The runner starts two regular opt-in CLI processes with distinct temporary DBs,
token authorities and environments. Each receives nine real SDK calls using a
host-issued participant credential: create, read, paged list, update, unsupported
Icon create (501), persisted read, delete, deleted read (404), and final list.
Native HTTP verifies sentinel preservation and final cleanup; actual SQLite
frames verify peer preservation. Generated tokens travel only over client stdin
and local HTTP, never command arguments or reports. The report records the
binary hash and SDK revision without request/response bodies or credentials.
A failed operation, child timeout or cleanup assertion fails the runner.

The terminal middleware disables profile loading and ambient SDK environment,
never enters the stock middleware chain, and dials only the allocated numeric
loopback endpoint with proxies and redirects disabled. Unit tests check route
refusal and redirect handling. It does not test stock SDK credentials, endpoint
rewriting, retries or waiters; native lifecycle/restart tests remain separate.

`.github/workflows/sakura-sdk.yml` runs the same commands unconditionally on PRs
and main pushes, with pinned checkout/setup/artifact actions and read-only GitHub
permissions. It is a separate workflow to avoid changes to shared CI ownership.
Making its named job a branch-protection required check is a repository setting;
this change does not alter those settings or claim a hosted pass before execution.
