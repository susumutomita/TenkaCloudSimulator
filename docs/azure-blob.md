# Azure Blob basic local subset

The bounded REST leaf is registered at `/_simulator/providers/azure`.
Its31 isolated HTTP/SQLite tests passed normal/race/vet, including owner-only
composition and single-range GET. Integrated server tests passed normal/race/vet;
actual 2-CLI/2-DB PC1440/390 browser create/upload/download/delete, peer-state,
403 and logout checks passed. The original finite model baseline and11 intended
counterexamples passed; HTTP range is separately checked by BlobRange (9,600states/8 intended controls);
root wiring and XML serialization are not proved by those models. Typed XML
serialization preserves288 legacy byte-exact payload cases and2 oversized refusals;
one fresh package profile has91.60% unfiltered source coverage, not the whole-module figure.

The pinned official Python SDK acceptance passed on 2026-11-05 for ten disclosed
exact-key cases per team, actual range downloads, ETag conditions, checksums,
pagination, denial, restart and cleanup in two authenticated CLIs. Requests'
literal `/./` and `/../` normalization is explicitly excluded and asserted, not
hidden by transport rewriting. See the current SDK section below. This focused
execution does not establish final backlog-integration regression, formal
applicability or hosted CI success. It is not Azure RBAC/Entra, a full portal
clone, ARM/Bicep, real-cloud parity or Challenge grading completion.
See [integration boundaries](multicloud-storage.md) and
[browser acceptance](acceptance/crossprovider-console.md).

## Host contract

`blob.New(db, Config{Account, Owner, Environment}, authorize)` requires an existing
SQLite database with enforced foreign keys and a non-nil transaction-aware
Authorizer. The account fixture is trusted host configuration, not a successful
Azure resource-creation API. Stored owner and cursor-secret identity cannot be
overwritten on reopen. Missing-account orphan containers/blobs are refused and
preserved, not silently adopted by a new account fixture.

The host must authenticate first, bind an explicitly provisioned Azure exercise
subject with `blob.WithActor`, and call the leaf directly. Missing/invalid actors
and mismatched configured environments deny access even when the injected
callback would grant it. Every valid native operation passes its actual actor,
operation, Azure action and persisted account owner/resource to that callback
inside its storage transaction. False means403; callback storage errors mean500.
Authorization.Permission explicitly carries a Name and Kind (Action or
DataAction). Container read/write/delete are Azure role-definition Actions;
blob read/write/delete are DataActions. Native tests use nine independent literal
permission-name/kind pairs from Microsoft REST references and the Storage Blob
Data Contributor definition, rather than the production mapping as an oracle.
Put uses blobs/write for both create and overwrite; Azure's add/action-only
create grant is not implemented as an alternative local permission. These are
permission-request metadata, not role-definition evaluation or a complete RBAC engine.
No operator-role shortcut, AWS IAM evaluation, arbitrary header principal or
implicit allow-all callback is built in. The local subject/environment are trusted
composition labels, not Entra/RBAC implementation or a tenant-authentication proof.
Separate team process/database/token authorities remain the host boundary.

A root adapter may map the shared authenticated actor's **ExerciseIdentity** into
the Azure subject and its configured environment. CredentialRole must not bypass
data authorization. The callback must read policy through the supplied transaction;
it must not open a nested transaction, wait for a guest or derive authority from
object metadata, URL tokens or request headers. Real SharedKey/Bearer credentials,
SAS and managed-identity/DefaultAzureCredential lookups are unsupported.

## Native REST subset

The account-in-path emulator shape is `/{account}/{container}/{blob}`. Requests
must supply `x-ms-version: 2023-11-03`; this is a deliberately pinned local subset,
not a claim of the latest API/SDK feature set. Root routing must preserve matching
URL.Path/RawPath and must not normalize data paths through ServeMux/path.Clean.

| Operation | Method and suffix | Successful status |
| --- | --- | --- |
| List Containers | GET `/{account}?comp=list` | 200 XML |
| Create Container | PUT `/{account}/{container}?restype=container` | 201 |
| Get Container Properties | HEAD same container URI | 200 |
| Delete Container | DELETE same container URI | 202 |
| Put BlockBlob | PUT blob URI, `x-ms-blob-type: BlockBlob` | 201 |
| Get Blob | GET blob URI | 200 bytes |
| Get Blob Properties | HEAD blob URI | 200, no body |
| List Blobs | GET container URI, `restype=container&comp=list` | 200 XML |
| Delete Blob | DELETE blob URI | 202 |

Containers are account/environment-scoped, lowercase DNS-like names3–63 without
consecutive hyphens. Blob names are opaque after exactly one URL decode:1–1024
Unicode characters, bounded flat path segments and no forbidden controls.
Leading/repeated slashes, dot segments, percent text, plus, spaces and UTF-8 are
not collapsed into adjacent names. Tests hold thirteen colliding-neighbor names
simultaneously. Encoded account/container separators are invalid; encoded separators
inside a blob name remain data. The original RequestURI is not used for leaf routing.

PUT replaces the full body and metadata; it does not append. Every successful write
gets a fresh opaque ETag even for the same body. Blob conditions support a single
strong If-Match/If-None-Match tag or wildcard, one condition at a time. Failed
write conditions return412 without mutation; a matching read If-None-Match
returns304. Lists/date conditions and conditional container operations are not
implemented. Staged blocks, PageBlob/AppendBlob, snapshots/versions, leases,
copy, soft deletion, encryption/tiers and access-policy operations fail explicitly.

Delete Container removes its contained blobs atomically. Local cleanup is
synchronous while the response is202; real Azure deletion is asynchronous.
Container-exists409 and missing container/blob404 use Azure-style XML errors.
Infrastructure errors are500 with no raw SQL/policy diagnostic. Unsupported native
operations are501; unsupported headers/query fields, types and repeated options
fail rather than pretending they were applied.

## Bounds and response safety

Local bounds are8MiB/blob,64MiB retained variable payload,1000 containers,10000
blobs,1000 host account fixtures,32 metadata fields and8KiB encoded metadata JSON.
Retained budget includes blob bodies, names, content types and serialized metadata,
plus container names/metadata, across accounts/environments in the provider DB.
These are simulator limits, not Azure quotas or a SQLite file/WAL size promise.
Conditional admission, budget check and writes share one transaction. Overwrites
subtract the old retained footprint; errors preserve previous bytes/metadata/ETag.
Valid over-cap legacy rows are preserved and remain readable/deletable; new writes
must fit the current limits. Metadata names are ASCII identifiers, values visible
ASCII and keys canonicalized lowercase (a documented local simplification).

List supports prefix, signed opaque markers, maxresults and include=metadata.
Pages are capped locally at1000 entries and16MiB encoded XML. Markers bind the
account/environment/operation/container/prefix and survive restart using the saved
account secret. They do not grant authority. Listings are live, not immutable
snapshots; changing the dataset between pages can change results. Metadata-heavy
pages may require a smaller maxresults. Delimiter/hierarchical listing is unsupported.
ServiceEndpoint in list XML is a local relative account path. The pinned Python
SDK acceptance successfully parsed the actual list/pagination responses; this
does not establish handling by other clients or versions.

HTML/SVG blob bodies are returned as attachments with CSP sandbox/nosniff.
Response sizes and XML encoding are checked before a successful listing is written;
HEAD never writes an object body. Local download presentation differs intentionally
from public Azure static website hosting, which is unsupported.

## Historical component and model verification

Source tests cover required authorization/missing actor, actual transaction owner,
all nine operations, same-name accounts, persistence, thirteen literal neighbors,
conditional writes/ETag changes, concurrent create-only and last-budget-slot races,
physical bytes/peer accounting, rollback/ignored writes, orphan preservation,
corrupt state, marker binding, empty bodies and non-executing HTML presentation.
At component checkpoint `35c83a5`, all12 tests passed normally (1.484s) and
under race (11.601s), with GOMAXPROCS1, -p1, -timeout60s and offline dependency
resolution. Go formatting and package vet passed. At follow-up native checkpoint
`f298ee5`, all13 tests passed normally (2.283s), under race (11.850s), and
package vet passed with the same bounds. This includes nine independent literal
permission names/kinds and the conditional GET/DELETE authorization/refusal
state assertions described below. The permission port now carries explicit
`Permission{Name, Kind}` rather than an ambiguous action string. These are native
httptest/SQLite results, not a running CLI/provider integration. At those
checkpoints, whole-module coverage, SDK execution, actual prefixed routing,
multi-process Access, license/security and hosted CI remained separate gates;
no SDK was installed and publication was held. Those historical states are not
the current SDK execution status or a replacement for current authorization.

The prepared AzureBlobState model bounds two account scopes, one container per
scope, two abstract keys/values, three accepted writes and small abstract row/byte
caps. It checks typed-authority refusal, frame/cascade integrity, exact abstract
reads, Put conditional admission, fresh ETags and failure rollback. Its Get and
Delete transitions are unconditional: this model does not establish DELETE
If-Match refusal or GET If-None-Match304 behavior. A separate native test compares
all persisted object fields/peers after a wrong DELETE412, checks304 has no body,
and requires a denied caller to receive403 (never304) without object identity.
Azure permission-name/kind mapping is also a native literal-table check, not
modeled Azure RBAC. It does not prove
URL decoding, actual UTF-8/metadata arithmetic, crypto, SQL isolation/crashes,
legacy over-cap states, pagination, real Azure RBAC or Go refinement. At model checkpoint `ec1a1ea`, isolated pinned-TLC execution checked4,726,996
generated/24,897 distinct states (depth8,28seconds), and all11 specified mutants
produced their exact named invariant violation with exit12 and a concrete trace.
The normal baseline exited0. Heap512MiB, worker1 and per-case60seconds were not
relaxed. Earlier evaluation/timeout failures are retained as failed diagnostics.
Transition-label deduplication preserves the complete successor assignments,
including snapshots/status/response, and does not remove bounds/properties.
Root still owns official registry/applicability integration and the full merge
validation gate; this isolated run is not a registered whole-repository proof.

Primary Microsoft references inspected without sign-in or real resource creation:

- [Blob REST API](https://learn.microsoft.com/en-us/rest/api/storageservices/blob-service-rest-api)
- [Storage Blob Data Contributor Actions/DataActions](https://learn.microsoft.com/en-us/azure/role-based-access-control/built-in-roles/storage#storage-blob-data-contributor)
- [Create Container](https://learn.microsoft.com/en-us/rest/api/storageservices/create-container)
- [Delete Container and emulator URL](https://learn.microsoft.com/en-us/rest/api/storageservices/delete-container)
- [Put Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/put-blob)
- [Get Blob](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob)
- [List Blobs](https://learn.microsoft.com/en-us/rest/api/storageservices/list-blobs)
- [List Containers](https://learn.microsoft.com/en-us/rest/api/storageservices/list-containers2)
- [Conditional headers](https://learn.microsoft.com/en-us/rest/api/storageservices/specifying-conditional-headers-for-blob-service-operations)
- [Naming and metadata](https://learn.microsoft.com/en-us/rest/api/storageservices/naming-and-referencing-containers--blobs--and-metadata)
- [Supported prior API versions](https://learn.microsoft.com/en-us/rest/api/storageservices/previous-azure-storage-service-versions)
- [Python BlobServiceClient constructor/API-version override](https://learn.microsoft.com/en-us/python/api/azure-storage-blob/azure.storage.blob.blobserviceclient?view=azure-python)

Microsoft documentation describes the service/SDK contracts; it is not execution
evidence. The bounded SDK result comes from the actual acceptance recorded below,
not those references. Kumo's pinned architecture
was consulted as design reference only; it contains AWS implementations rather than
an Azure backend. No upstream service code or credential-discovery logic was copied.

## Trusted local composition factory

`NewOwnedHandler(db, cfg, extractor)` fixes one synthetic account, environment and
owner in trusted host configuration. The required extractor receives only a Go
context and returns an authenticated exercise identity; the common-provider
adapter maps `provider.Actor.ExerciseIdentity`, never credential role. The factory
replaces any previous leaf actor. Missing/invalid identity refuses with403. Its
required transaction-aware policy grants only the configured owner, actual saved
owner/resource and the exact finite nine-operation permission set. A different
identity called “reader” has no grant under this owner-only policy; this is not an
Azure Reader role implementation. Header labels and operator roles cannot bypass
it. This does not implement Azure RBAC, Entra, SharedKey or SAS authentication.

At component checkpoint `0d86933`, all14 package tests passed normally
(1.598s), under race (12.156s), and package vet passed (GOMAXPROCS1, -p1,
offline dependency resolution, timeout60s). The real-loopback HTTP tests mount the existing provider registry behind
trusted context fixture middleware, check provider-prefixed lifecycle, fixed
same-named account/key separation across environments, nonowner/missing-context
refusal with unchanged saved state, SQLite reopen/ETag persistence and attachment
download. That checkpoint was not actual server Access authentication. Its base
registry cleared RawPath, so escaped-delimiter/percent fidelity then awaited
the root registry fix. The subsequent native/browser and official SDK checks
are separate evidence; the leaf factory itself did not edit shared server,
registry, UI or authentication code.

### Executed official Python SDK subset

The previous missing-dependency/source-only plan is superseded by actual
`azure-storage-blob==12.31.1`, `azure-core==1.41.0` and `requests==2.34.2`
execution. The acceptance uses the real provider/account-prefixed endpoint,
`api_version="2023-11-03"`, anonymous credentials and the supported local-token
hook. It passed actual create/upload/list/download/delete, ten exact names per
team, two independently authenticated processes, denied nonowner writes,
MD5/ETag conditions, pagination, restart and cleanup. The real range-request
download and relative list ServiceEndpoint are retained, not replaced with raw
HTTP. Staged uploads, other API versions and all SDK defaults are not claimed.

The original twelve-name source candidate had not accounted for Requests
normalizing `a/./b` to `a/b` and `a/../b` to `b` before transmission. These two
cases are explicitly excluded from the positive SDK lifecycle and reported in
`excluded_sdk_key_cases`. An independent real-SDK/PreparedRequest check requires
exactly those two transformations and unchanged preparation of every other key.
The native thirteen-neighbor tests and full GCP twelve-key oracle are unchanged;
no client path rewriting manufactures exact-name parity.

See [official SDK acceptance](acceptance/crossprovider-official-storage.md) for
the complete configuration and commands. Azure-only execution passed against
clean main `f5dba05`; the subsequent combined GCP/Azure run passed against the
root integration working binary. These focused results do not establish the
final committed integration's full regression/formal/hosted CI status.

## Single-range download subset

GET Blob accepts the documented `bytes=start-end` and `bytes=start-` forms in
Range or x-ms-range, with x-ms-range taking precedence. Valid selections return206
and exact Content-Range/Content-Length; an end beyond the object is clamped,
and a start at/beyond the end returns416. Suffix and multiple ranges are rejected
with400, as are unsupported date conditions and range headers on other operations.
The entire bounded blob is verified before selecting response bytes; this is not
streaming unbounded storage. Strong ETag conditions and authorization execute
before range selection. Invalid/denied reads do not change stored state.

A partial response omits whole-blob Content-MD5. Explicit
`x-ms-range-get-content-md5: true` returns the selected bytes' MD5 only for a
specified range of at most4MiB; absent range, unsupported Boolean values or a
larger selection return400. CRC64 range checksums remain unsupported. Full GET
retains its whole-blob MD5. Accept-Ranges is bytes; version remains pinned to
2023-11-03. Existing bounded TLA transitions do not model HTTP range parsing,
checksums or these response headers; native boundary tests are separate evidence.

References: [Get Blob response and range checksum rules](https://learn.microsoft.com/en-us/rest/api/storageservices/get-blob)
and [Azure's two documented range formats](https://learn.microsoft.com/en-us/rest/api/storageservices/specifying-the-range-header-for-blob-service-operations).
These specifications and native range checks alone do not establish actual
Azure differential or SDK success; the separate executed Python acceptance
provides the bounded SDK download evidence described above.

At native checkpoint `e6cb13d`, all16 package tests including the owner-only
factory and range boundaries passed normally (2.258s), under race (11.688s),
and package vet passed. GOMAXPROCS1, -p1, offline dependency resolution and the
60-second test deadline were unchanged. Tests independently assert closed/open
ranges, x-ms-range precedence, clamping,416, unsupported suffix/multiple ranges,
repeated headers, empty objects, conditional304/412, exact4MiB range MD5, rejected
oversize checksum and denied caller responses with unchanged full persisted
state. This is historical native httptest/SQLite and fixture-loopback evidence
only; SDK, authenticated server composition and whole-repository CI were still
pending at `e6cb13d`. Later SDK execution is recorded separately above, without
claiming fresh final integration CI success.
