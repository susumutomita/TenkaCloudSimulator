# Official storage SDK acceptance — source candidate

Owner: grader_http, delegated cross-provider acceptance scope. Base: public main
`b61d0299a2139661f01ef29ae5826e4c070cc7bf`; isolated branch
`test/crossprovider-official-sdk-20261104`. Only this document, new unique
`test/e2e/crossprovider_official_storage.py`, and a metadata receipt are owned.
Existing provider/auth/root server/README/registry files remain untouched.
All new merges are held. This is not runtime acceptance or provider support.

## Planned invocation and failure contract

After a separately granted execution window, an already built integration binary
and already approved official Python dependencies are required:

```sh
python test/e2e/crossprovider_official_storage.py --binary /absolute/path/simulator --provider both
```

`--provider gcp` or `azure` independently selects one SDK. Missing selected
packages produces `FAIL/NOT-RUN`, names the missing packages and exits 2 before
creating a process or making a request. A runtime/assertion error exits 1 and
reports only its type; exception text and CLI output are withheld because they
can include sensitive request headers. There is no skip, fake SDK, install,
network bootstrap, dependency fallback or pass on infrastructure failure.
No command above has been executed for this candidate.

Two authenticated numeric-loopback CLI processes use separate temporary SQLite
files, persistent operator-token files and the exact same resource names. The
binary must be the forthcoming registered candidate; public b61 alone has no
GCP/Azure leaves and must fail these tests. Health wait is 25s, shutdown 8s,
per-call SDK timeouts 3s, whole script deadline 300s, retries disabled, list
results capped at 100. Network connections and name resolution are restricted
to the two explicitly allocated numeric-loopback ports; proxy environment
variables are temporarily removed. This test harness restriction is not an
OS-level simulator sandbox. A raced port allocation fails acceptance rather than
weakening assertions. Startup and cleanup errors are failures.

The actual existing provider prefix is `/_simulator/providers/`, not a new
`/providers/` alias: GCP endpoint `/_simulator/providers/gcp`; Azure account URL
`/_simulator/providers/azure/tenkacloudlocal`. Configured GCP project is
`tenkacloud-local`. Never change the server wire path to accommodate a mistaken
harness. SDK serialization, HTTP transfer and error parsing are real official
implementations; no object calls are replaced by raw HTTP. Raw requests are
limited to CLI health and the existing participant-token control API.

## Assertions and precise limits

- Two teams create identical bucket/container/key sets with distinct contents.
  Each team persists through SIGTERM/restart with the same SQLite/token files,
  then exact reads/checksums/list pagination and conditional deletes clean up.
- Simultaneous names include slash, percent-text, encoded-neighbor text,
  repeated slash, dot segments, leading/trailing slash, plus/space and Unicode.
  Exact bytes and the complete key set must survive SDK transport and routing.
- Missing access token is 401; the other process's operator token is 401.
  A participant bound to `storage-reader` must receive 403 on a write, with the
  final complete key set/bytes unchanged. This does **not** establish a working
  reader role: the current owner-only factories deny every nonowner, even reads.
  Authorized-reader GET plus denied mutation requires a real provider policy
  extension and additional tests, not an AWS IAM grant or invented RBAC claim.
- GCP metadata MD5 is compared to a local exact-body oracle and download asks
  the real SDK to verify MD5. CAS checks failed create-only overwrite, unchanged
  generation, successful matching-generation overwrite with a fresh generation,
  and stale-generation deletion refusal. Azure uses MD5 ContentSettings, failed
  If-Match overwrite, unchanged ETag, successful matching overwrite with fresh
  ETag, stale delete refusal, then checks persisted checksum and bytes.
- GCP US/EU bucket location metadata is checked. These are location attributes,
  not physical regional deployment or cross-region replication. Azure location
  is not exposed by this data-plane account fixture; no region-isolation claim.
- Restart and cleanup read actual object APIs, not SQLite internals. Error
  assertions inspect official SDK HTTP status (401/403/404/412). An unsupported
  wire option, transport failure, state failure or serializer incompatibility is
  a failing acceptance result, never an expected substitute for a positive read.

## Explicit public SDK subset and known blockers

Google Python Client uses public `client_options.api_endpoint`,
`AnonymousCredentials` (no ADC discovery) and public `extra_headers` for the
local simulator token. No real Google OAuth credential is used. `projection=noAcl`
is explicit for metadata/list. Small `upload_from_string` uses the SDK multipart
path, with MD5 and generation conditions. Fresh `bucket.blob(name)` references
are used when selecting the current live object: a loaded SDK Blob adds its
`generation` as a version selector. The native leaf now accepts an explicitly
addressed current generation; older generations return404 and history remains
unsupported. Generation preconditions are separate from version history. This is a disclosed public usage constraint, not a hidden
serializer rewrite. Large/resumable uploads and full ACL projection remain
unsupported; **default official Go storage SDK compatibility is not established**.
No claims about every Python default or a particular installed SDK version are
possible until actual versioned execution.

Azure uses public account URL, `api_version='2023-11-03'`, anonymous credential
(no SharedKey/SAS/Entra lookup), and public `raw_request_hook` for the simulator
token. A hook also verifies the intended numeric host/port. SDK account-in-path
handling with the prefixed emulator URL must be verified by actual execution.
The actual `download_blob().readall()` is deliberately retained. Official Azure
SDK sends range headers; the native leaf now supports the documented bounded
single-range forms, with auth/ETag preflight and native MD5/length tests. The real
SDK download remains unexecuted, so native support must not be called SDK
compatibility. This script must not strip Range or substitute raw GET.
The official API-version override is explicit; newer default versions are not
accepted implicitly. Staged blocks/large uploads and advanced Azure features
remain outside this small-object slice.

Dependencies are currently absent in the inspected Python interpreter (receipt
alongside this document). The official SDK candidate has only source/metadata checks and a fail-closed
dependency preflight. Separate native/browser acceptance ran two real CLIs and
passed; that is not an SDK lifecycle pass. Whole Go/race/build and the same-profile
94.95% statement/90.46% source floors passed. Final immutable formal applicability
and hosted CI remain pending. Reader-positive cloud roles are outside the
owner-only policy. Root owns final integration/test scheduling.
Kumo was not used as source or runtime dependency.

## Primary references inspected 2026-11-04

These are upstream reference contracts, not evidence of local conformance.
No upstream implementation was copied.

- [Google Python Client: endpoint, credentials, extra_headers](https://docs.cloud.google.com/python/docs/reference/storage/latest/google.cloud.storage.client.Client)
- [Google Blob: upload, download, conditions, checksums](https://docs.cloud.google.com/python/docs/reference/storage/latest/google.cloud.storage.blob.Blob)
- [Google Bucket: public noAcl projection](https://github.com/googleapis/google-cloud-python/blob/2b4575f36f1441792e51efb9394b636940ed539d/packages/google-cloud-storage/google/cloud/storage/bucket.py)
- [Google Blob generation-bound requests](https://github.com/googleapis/google-cloud-python/blob/2b4575f36f1441792e51efb9394b636940ed539d/packages/google-cloud-storage/google/cloud/storage/blob.py)
- [Azure BlobServiceClient: endpoint/API version](https://learn.microsoft.com/en-us/python/api/azure-storage-blob/azure.storage.blob.blobserviceclient?view=azure-python)
- [Azure BlobClient: public condition/download options](https://learn.microsoft.com/en-us/python/api/azure-storage-blob/azure.storage.blob.blobclient?view=azure-python)
- [Azure public request/response hooks](https://learn.microsoft.com/en-us/python/api/overview/azure/storage-blob-readme?view=azure-python#optional-configuration)
- [Azure local endpoint construction](https://github.com/Azure/azure-sdk-for-python/blob/main/sdk/storage/azure-storage-blob/azure/storage/blob/_shared/base_client.py)

Current local contracts are [Cloud Storage](../gcp-storage.md),
[Blob Storage](../azure-blob.md), [trusted Actor](../provider-auth-context.md)
and [native/browser acceptance](crossprovider-console.md).
