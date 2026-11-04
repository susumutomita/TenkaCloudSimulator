# Cloud Storage JSON API: local integration candidate

This branch registers the bounded JSON API v1 leaf at `/_simulator/providers/gcp`.
A trusted authenticated Actor is mapped to immutable configured project owners;
this is not Google IAM or OAuth. Twenty-seven isolated native tests passed normal,
race and vet, and the integrated server tests passed normal/race/vet. Actual
2-CLI/2-DB PC1440/390 browser create/upload/download/delete, denial, logout and
state isolation passed with no external requests or page errors. The finite leaf
baseline and8 intended counterexamples passed. The additional empty-legacy adoption assertion passed; the separate owner-authority
model baseline and6 intended counterexamples also passed. Native package-only
unfiltered source coverage is90.76%; this is not the whole-module figure.

Official SDK packages are absent; their real acceptance has not run. Full
repository regression/coverage, immutable formal applicability, hosted CI and
merge remain pending. No real Google credentials/resources are required or used.
See [integration boundaries](multicloud-storage.md) and
[browser evidence](acceptance/crossprovider-console.md). Source presence alone is
not a Google Cloud parity or catalog-problem completion claim.

## Composition and authority

### Trusted project factory (focused native tests passed)

`NewProjectHandler(db, FactoryConfig)` requires a `SubjectExtractor(context)`
and explicit `ProjectBinding{Project, OwnerSubject}` values. The root adapter
must validate the common authenticated actor and return only its
`ExerciseIdentity`. It supplies a fixed configured environment. This factory
does not parse credentials, headers, operator roles or AWS policies. Missing or
invalid extracted actors receive 403; invalid/expired local access-token 401
remains the outer host's responsibility.

The eight recognized permissions are exactly bucket create/list/get/delete and
object create/list/get/delete. Only the configured project's owner subject is
authorized; a nonowner called "reader" has no implicit read grant. Every check
joins persisted project number and owner binding using the operation's same
transaction and actual owning project. Unknown permissions fail closed.
Owner assignments are persisted in `gcp_storage_project_owners` and cannot be
silently changed or removed on reopen. A new owner binding refuses preexisting
unbound buckets instead of adopting them. Owner schema, project configuration
and owner validation/insertion share the constructor transaction, so failure
rolls initialization back. Configuration is copied rather than retaining a
mutable caller-owned binding slice. This is a deliberately small local owner
policy, **not Google IAM** or proof of authenticated tenant isolation.

The new factory tests passed normal/race/vet within the seventeen-test package
run on source `d3c5015`. They cover actual loopback
HTTP through a fixed-actor/prefix-preserving **fixture**, 13 literal keys,
forged-header/reader/peer/missing-context refusal, reopen, immutable bindings,
failed-initialization rollback and refusal to adopt unbound resources. The
fixture is not production Access/provider-registry integration. The existing
finite Storage model does not prove factory initialization or actor extraction.
The new `formal/ProjectAuthority.tla` is a separate **unexecuted source model**
of this GCP factory: two projects, two extracted subjects, all four fixed owner
mappings (including one subject owning both projects), one optional bucket and
unit-valued object. It models atomic constructor acceptance/refusal, immutable
persisted owners, refusal to adopt unbound resources, actual-project decisions,
denied-write frames and the exact finite permission set. Six intended mutants
cover rebinding, adoption, partial refused writes, claimed-project trust,
unknown permissions and denied writes. It does not prove extractor/credential
authentication, Azure semantics, HTTP/SQLite/Go implementation, growing project
rosters or real SDK execution. Its outside entry awaits registry integration.

`storage.New(*sql.DB, Config)` requires a non-nil `AuthorizeTx` callback, one
trusted environment and 1–100 explicitly configured synthetic project IDs and
numbers. SQLite foreign keys must be on. Reopening rejects changed project
numbers, unconfigured persisted owners, orphan objects, exceeded state budgets,
and a generation sequence behind saved objects.

The host authenticates a credential and selects an authorized exercise identity
before constructing the local typed `storage.Actor` in Go context. `Subject`
means that **exercise identity**; an operator credential is not a data-access
bypass. The provider-neutral actor adapter is not implemented in this leaf.
The host supplies `Environment` from trusted configuration, never a request
header. Missing/mismatched actors receive 401. Actor labels alone are not
authentication or proof of team isolation. Existing deployment guidance still
requires separate team processes/SQLite/token authorities.

Every operation invokes the required callback with its permission and actual
persisted bucket-owning project inside the same serializable/immediate SQLite
transaction as its read or mutation. Callbacks use that transaction for policy
lookups; they must not commit, roll back, open another transaction or perform
external I/O. `ErrDenied` produces a generic 403, other callback failures a
generic 500. A missing bucket has `Exists=false` and an empty project, not an
owner guessed from a URL/header. Its callback runs before a 404 is returned, so
the policy can conceal absence from refused callers. Bucket creation uses the
explicit configured project; a collision also requires permission to inspect
the **existing** owner's bucket before returning 409. This conservative local
collision rule is not a claim of identical Google error-disclosure behavior.

| Operation | Local permission | Result |
| --- | --- | --- |
| `POST /storage/v1/b?project=ID` | `storage.buckets.create`; existing collision additionally `storage.buckets.get` | bucket JSON, 200 |
| `GET /storage/v1/b?project=ID` | `storage.buckets.list`, including each returned bucket | project-filtered list, 200 |
| `GET /storage/v1/b/BUCKET` | `storage.buckets.get` | bucket JSON, 200 |
| `DELETE /storage/v1/b/BUCKET` | `storage.buckets.delete` | empty bucket only, 204; nonempty 409 |
| `POST /upload/storage/v1/b/BUCKET/o?uploadType=media&name=NAME` | `storage.objects.create`; overwrite additionally `storage.objects.delete` | object metadata, 200 |
| Same upload collection with `uploadType=multipart` | same | exactly JSON metadata then media, 200 |
| `GET /storage/v1/b/BUCKET/o/NAME` | `storage.objects.get` | metadata, 200 |
| Same with `alt=media` | `storage.objects.get` | exact bytes, attachment + sandbox CSP, 200 |
| `GET /download/storage/v1/b/BUCKET/o/NAME?alt=media` | `storage.objects.get` | media alias; focused native tests passed |
| `GET /storage/v1/b/BUCKET/o` | `storage.objects.list` | ordered object list/prefixes, 200 |
| `DELETE /storage/v1/b/BUCKET/o/NAME` | `storage.objects.delete` | 204 |

The callback is an authorization seam, **not** an implementation of GCP IAM,
OAuth, service accounts, Google organization policy, ACLs or AWS IAM. The
synthetic test policy grants only the selected project's resources; it does not
assert production Google policy equivalence.

## State, requests and limits

Bucket names are global inside one configured environment, not per project.
State is always filtered by environment; bucket owner lookup determines project
authorization. Restrictive composite foreign keys prevent orphan objects and
nonempty bucket removal. No automatic adoption of another project's bucket.

Only `STANDARD` storage and the locations `US`, `EU`, `US-EAST1`, `US-WEST1`,
`ASIA-NORTHEAST1`, `EUROPE-WEST1` are accepted. Location is metadata; there is no
geographic replication. Bucket names follow bounded lowercase/DNS-like checks,
exclude IP literals and `goog`/`google`, but do not reproduce every reserved-name
heuristic. Objects preserve decoded-once UTF-8 names, including literal percent,
slashes, repeated slashes and dot segments. Object GET/DELETE use one escaped
path segment: encode slash as `%2F` and literal percent as `%25`. Unencoded
additional segments (including `/acl/entity`) are unsupported subresources and
return 501 without changing or reading a slash-named object. Encoded literal
`foo%2Facl%2Fentity` remains the object named `foo/acl/entity`. Invalid paired
`Path`/`RawPath` is refused. Empty names, exact `.`/`..`, NUL,
CR/LF and names exceeding 1024 bytes are rejected. A parent router must preserve
the paired `URL.Path`/`RawPath`, without cleaning or a second percent decode.
The strict escaped-segment rule disambiguates this local subset's routes;
Google's encoding guide recommends slash escaping but notes that encoding
requirements can vary by scenario. This is not a claim that every real Google
endpoint rejects every unencoded slash.

Uploads accept media or a small two-part `multipart/related` envelope. Query
`name` overrides multipart metadata `name`; the URL bucket overrides metadata
`bucket`. Unknown/case-incorrect JSON fields, duplicate fields, nulls, malformed
Unicode, excessive nesting and unsupported query options fail with 400. Metadata
supports at most 16 string entries, keys 1–64 bytes, values at most 256 bytes.
Content types must be valid and contain no CR/LF. Provided base64 CRC32C
(Castagnoli, big-endian) and MD5 must match the actual bytes. MD5 is protocol
metadata, not an authentication mechanism.

Each successful upload allocates an increasing positive, environment-local
64-bit generation in the same transaction as the object. Only the live object
is saved. `ifGenerationMatch=0` creates only when absent; a positive value must
match the live generation. It also applies to object GET/DELETE. Refusal is 412
without a sequence or object change. Generation history, historical revision retrieval,
metageneration conditions and AWS version IDs are not implemented. The new
`generation=POSITIVE_INTEGER` selector on object GET/DELETE (including download
GET) selects **only the current existing generation**; a stale/missing revision
returns 404, never the current object's bytes or a deletion of its replacement.
If also supplied, `ifGenerationMatch` still applies to that current object.
Generation zero, noncanonical or out-of-range selectors return 400. This does
not store or retrieve historical revisions. Identical
generation numbers in different environments do not identify the same object.

Hard ceilings per environment: 100 buckets, 10,000 live objects, 1 MiB per
object and 64 MiB aggregate media plus serialized custom metadata. A trusted
factory may lower them. Metadata bodies are at most 64 KiB; multipart envelopes
at most object limit plus 64 KiB. Budget failures return 429 (body too large:
413). SQLite persistence is not encryption at rest or a durability SLA.
Checksums and metadata are validated before saved media is returned; corrupt
state fails closed with generic 500. Sensitive object values are not logged.

Lists support `prefix`, `maxResults=1..1000`, opaque actor/scope/filter-bound
`pageToken`; object lists additionally support `delimiter=/`. Items and common
prefixes share the page size. Tokens are local cursors, not Google tokens and
not an authorization capability; every page is reauthorized. Concurrent writes
can alter later pages; snapshot pagination is not promised. `prettyPrint` and
`projection=noAcl` are accepted. `projection=full` is explicitly rejected while
ACL representation is unsupported.

No resumable uploads, ACL/IAM policy endpoints, PATCH/UPDATE, version history,
soft delete, retention, lifecycle, folders, compose/copy/rewrite, encryption
keys, requester-pays, signed URLs, gRPC/XML API, range reads or HTTP ETag
preconditions. No console is provided by this source slice. Unsupported options
are errors rather than success placeholders.

## Evidence and remaining checks

New raw `httptest` contracts in `internal/gcp/storage/storage_test.go` passed
using `GOMAXPROCS=1 GOFLAGS=-p=1 GOPROXY=off`, `go test -count=1 -timeout=60s`
and the same command with `-race`, targeting `./internal/gcp/storage` only.
The latest thirteen-test runtime was 1.272s normally and 11.827s under race.
Package `go vet` also
passed. An initial compile found an unused test import, removed before rerun;
no test was skipped and no failure was converted to a pass.

- `TestStorageExactCollidingKeysAndRestart`: 13 simultaneous literal/percent/
  Unicode keys, exact list set, distinct bodies, deletion neighbors and reopen.
- `TestStorageActualOwnerAuthorizationAndEnvironment`: same-name environmental
  state, peer project refusal, fake headers/missing actor, owner-aware lookup.
- `TestStorageOverwritePermissionsGenerationAndBudgetRollback`: create/delete
  permissions, stale/zero generation, nonempty deletion and rejected budget frame.
- `TestStorageMultipartChecksumsAndPagination`: supplied checksums, query-name
  precedence, attachment/CSP and page actor/filter binding.
- Strict-input, real SQLite trigger rollback/corruption, concurrent CAS and
  missing-owner/constructor tests cover additional rejection paths.

Review additions (normal/race passed):
`TestStorageIndependentChecksumVectorsAndRefusalFrame` uses fixed expected
CRC32C/MD5 values from primary test vectors, not the implementation helper;
bad CRC and MD5 uploads must leave all Storage tables, sequence and aggregate
budget unchanged. `TestStorageGenerationNeverReusedAcrossDeletesAndRestart`
checks object/bucket recreation and two reopens, strictly increasing generations,
and stale generation conditions refusing GET, DELETE and upload without state
change. `GenerationFreshness` additionally checks successful allocation against
the prior sequence and prohibits sequence rollback on other operations.
The route test `TestStorageObjectSegmentDoesNotSwallowUnsupportedSubresources`
also passed: ACL-looking unencoded GET/DELETE refuse without a complete
database-frame change, while encoded literal deletion removes only its exact
key. The exact new test against the previous controller in a separate owned
worktree failed as intended (`HTTP 204 want 501` on raw DELETE), demonstrating
the regression detector's sensitivity. That negative-control failure is
separate from the new controller's passing tests. The finite model does not
cover URLs.

`formal/GCPStorageState.tla` is a **separate finite abstraction**, not a Go
refinement proof. It models two environments, two projects, one global bucket
name per environment, two object keys, unit-size payloads, one-object/one-unit
budget, and a two-generation exploration cutoff. The cutoff is not an API
generation limit. Synthetic project-equality policy abstracts the required
transaction callback. It does not prove GCP IAM, SQLite implementation, raw path
decoding, checksums, cryptography, HTTP, SDKs, pagination, persistence or auth
composition. The outside-repository entry fragment retains the original six
mutants and adds generation reuse/reset controls. Independent TLC 1.7.4 checking
of all eight used one worker, one active processor,
512 MiB maximum heap and the unchanged 60-second per-case timeout. The baseline
completed with 45,697 distinct states (8,810,049 generated), exit 0, in 29.981s.
The state counts match the prior run because all state variables, actions and
finite assignments were retained; only an invariant was added.
Each of the eight mutants exited 12 with its named invariant violation and an
actual trace: denied create / `DeniedFrame`, nonempty deletion / `NoOrphans`,
ignored generation / `GenerationRefusal`, ignored budget / `Budget`, wrong
environment read / `ReadIsolation`, ignored owner / `OtherProjectUnchanged`,
generation reuse and sequence reset / `GenerationFreshness`.
Classifier controls rejected a wrong invariant name, a successful exit offered
as a mutation result, and a parser failure offered as a baseline result;
two more controls reject the freshness traces offered under different invariant
names. All five classification controls rejected their incorrect inputs.
No bounds, property, configuration or timeout was weakened. This independent
run is **not a registered merge-gate pass**. Shared model registry and
applicability integration belong to the root integrator.

Official Go SDK source was inspected only, not installed or executed:
`googleapis/google-cloud-go` commit
`d081c0c95af1566b2284d2c6c9a43e2c87606dc9`, storage 1.70.0 (2026-10-22).
`storage/http_client.go` requests `projection=full` on common metadata reads;
`storage/writer.go` defaults to 16 MiB resumable chunks. Those Go SDK defaults are
outside this first leaf. Therefore **default Go SDK flow is not supported or
verified**, even though raw media/multipart JSON routes exist. A later bounded
SDK slice must implement truthful representations/upload sessions with their
own authorization, persistence, TTL and budgets, then execute actual SDK tests.

### Official Python SDK draft (not executed)

The official Python source moved from `googleapis/python-storage` to
`googleapis/google-cloud-python`; inspected monorepo commit
`2b4575f36f1441792e51efb9394b636940ed539d` reports storage **3.17.1**.
Its metadata reload defaults to `noAcl`; known-size uploads up to 8 MiB use
multipart. This leaf still caps payloads at 1 MiB and does not support resumable
uploads. Python blob downloads use `/download/storage/v1/...` and append a
known generation. Those source observations motivated the new alias and
current-only selector; `download_test.go` passed their raw native contract
normally and under race. URL routing/selection is outside the finite model.

The consolidated `test/e2e/crossprovider_official_storage.py` acceptance uses the
real official package and fails closed before sockets/processes if it is absent.
The earlier isolated unexecuted draft was superseded instead of keeping duplicate
unused probes in the production package tree. See
[official SDK acceptance](acceptance/crossprovider-official-storage.md) for the
bounded two-CLI lifecycle, local-only transport and unverified default behaviors.
No SDK/dependency was installed or executed. Root Actor/registry/native/browser
integration now passes, but that is not Python SDK compatibility. Full projection,
large/resumable flows, PATCH/ACL and forced bucket deletion remain outside this subset.

## Primary references

Reviewed 2026-11-04; JSON API **v1**. These are specification inputs, not evidence
of real-cloud comparison. No authenticated Cloud Console screen was inspected.

- [JSON API reference](https://docs.cloud.google.com/storage/docs/json_api/v1)
- [Bucket creation](https://docs.cloud.google.com/storage/docs/json_api/v1/buckets/insert),
  [bucket deletion](https://docs.cloud.google.com/storage/docs/json_api/v1/buckets/delete)
- [Object upload](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/insert),
  [object get](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/get),
  [object listing](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/list),
  [object deletion](https://docs.cloud.google.com/storage/docs/json_api/v1/objects/delete)
- [Object naming](https://docs.cloud.google.com/storage/docs/objects),
  [request preconditions](https://docs.cloud.google.com/storage/docs/request-preconditions)
- [JSON API path encoding](https://docs.cloud.google.com/storage/docs/request-endpoints#encoding),
  [distinct object ACL delete route](https://docs.cloud.google.com/storage/docs/json_api/v1/objectAccessControls/delete)
- [Cloud Storage checksum serialization](https://docs.cloud.google.com/storage/docs/json_api/v1/objects),
  [RFC3720 B.4 CRC examples](https://www.rfc-editor.org/rfc/rfc3720.html#appendix-B.4),
  [Go1.26.10 CRC golden numeric values](https://github.com/golang/go/blob/go1.26.10/src/hash/crc32/crc32_test.go),
  [Go1.26.10 MD5 golden digests](https://github.com/golang/go/blob/go1.26.10/src/crypto/md5/md5_test.go)
- [Pinned official SDK HTTP adapter](https://github.com/googleapis/google-cloud-go/blob/d081c0c95af1566b2284d2c6c9a43e2c87606dc9/storage/http_client.go),
  [writer](https://github.com/googleapis/google-cloud-go/blob/d081c0c95af1566b2284d2c6c9a43e2c87606dc9/storage/writer.go)
- [Pinned Python SDK blob transport](https://github.com/googleapis/google-cloud-python/blob/2b4575f36f1441792e51efb9394b636940ed539d/packages/google-cloud-storage/google/cloud/storage/blob.py),
  [metadata projection](https://github.com/googleapis/google-cloud-python/blob/2b4575f36f1441792e51efb9394b636940ed539d/packages/google-cloud-storage/google/cloud/storage/_helpers.py)

No source was copied from the SDK, Kumo or LocalStack into this leaf. No new
third-party dependency, runtime executable or outbound connection is added.
