# Google Cloud service catalog

The Google API catalog serves bounded configuration metadata for additional
Google Cloud services through each API's own REST paths. Contracts come from
documentation-free subsets of the discovery documents pinned by
google-api-python-client 2.199.0 (`googleapiclient/discovery_cache/documents`).
The extraction tool is `tools/gcpmodels/extract.py`, and the reviewed method
list is `internal/gcp/catalog/selection.json`. The original license ships as
[`third_party/GOOGLE-DISCOVERY-NOTICES.txt`](../third_party/GOOGLE-DISCOVERY-NOTICES.txt)
and is served at `/_simulator/licenses/GOOGLE-DISCOVERY-NOTICES.txt`.

Every listed method is implemented against provider-owned SQLite state. Every
other official method of the same API returns HTTP 501 `UNIMPLEMENTED`.
Request fields that are not in the official schema are refused with
`INVALID_ARGUMENT`; output-only fields sent by a client are dropped, as
AIP-203 requires of servers. No database engine, network, workload or real
Google Cloud call is executed or simulated.

## APIs

| API | Issue | Implemented methods | Bounds |
| --- | --- | --- | --- |
| AlloyDB for PostgreSQL (`alloydb.v1`) | #336 | `projects.locations.clusters.{create,get,list,patch,delete}`, `clusters.instances.{create,get,list,patch,delete}`, `clusters.users.{create,get,list,patch,delete}`, `operations.{get,list}` | Clusters, instances and users are configuration metadata. No PostgreSQL engine, endpoint, storage or backup exists, and instances report `READY` immediately. User passwords are refused and never stored. Deleting a cluster with children requires `force=true` |

The capability document lists the same APIs under `gcpCatalog.apis`. Each
entry includes every implemented method, the number of official methods,
collections, create fields, the discovery revision and source hash, and the
bounds above.

## Routing and authorization

- Requests use `/_simulator/providers/gcp/<api>/<official path>`, for example
  `/_simulator/providers/gcp/alloydb/v1/projects/tenkacloud-local/locations/us-central1/clusters`.
  Set the client's `api_endpoint` to `/_simulator/providers/gcp/<api>/`.
- The configured project ID or project number is accepted. Only the
  configured project owner (`--cloud-storage-owner`, default
  `simulator-admin`) may call a method. Any other subject or project receives
  `PERMISSION_DENIED`, and the response does not reveal whether the resource
  exists. This local ownership check is not Google IAM, and AWS identity
  policies grant nothing here.
- Supported locations are `global`, `us-central1`, `us-east1`, `us-west1`,
  `europe-west1` and `asia-northeast1`. Each location is a separate scope.

## Request semantics

- Methods that return `google.longrunning.Operation` complete synchronously.
  The operation is stored with `done: true` and the resource (or `Empty`) as
  its response, and remains readable through `operations.get/list`.
- `requestId` replays the stored operation of an identical request for 24
  hours. A different request with the same `requestId` is refused.
  `validateOnly=true` validates and stores nothing.
- `etag` guards patch and delete (`ABORTED` on mismatch). `updateMask`
  restricts a patch to the reviewed mutable fields.
- Errors use `google.rpc.Status` with the canonical HTTP mapping, for example
  `ALREADY_EXISTS` 409 and `FAILED_PRECONDITION` 400.

### Flat APIs (Compute Engine style)

APIs such as Compute Engine (`compute.v1`) address resources with separate
path parameters (`projects/{project}/zones/{zone}/disks/{disk}`,
`.../regions/{region}/...`, `.../global/...`) and take the resource name in
the request body.

- Zones are the published zones of the supported regions: `us-central1-a`,
  `-b`, `-c`, `-f`; `us-east1-b`, `-c`, `-d`; `us-west1-a`, `-b`, `-c`;
  `europe-west1-b`, `-c`, `-d`; and `asia-northeast1-a`, `-b`, `-c`. Other
  zones and regions are refused with `INVALID_ARGUMENT`.
- Created resources carry their official `kind`, a server-assigned 19-digit
  `id`, `selfLink` (`https://www.googleapis.com/compute/v1/...`),
  `creationTimestamp` and, where the schema has them, `zone` or `region`
  URLs. `name` holds the short name.
- Mutations return the API's own operation resource (for example
  `compute#operation`) with `status: DONE`, `progress: 100`,
  `operationType` (the method name, such as `insert` or `setLabels`),
  `targetLink` and `targetId`. Operations are stored under the scope of the
  resource and remain readable through `zoneOperations`, `regionOperations`
  or `globalOperations` `get`, `list` and `wait`. `requestId` is echoed as
  `clientOperationId` and replays the stored operation.
- `patch` without `updateMask` applies a JSON merge patch (RFC 7396): nested
  objects merge and arrays replace. `null` members are refused. Sending the
  unchanged `name` back is accepted; a different `name` is refused.
- Custom methods that set fields (for example `setLabels` or `resize`) write
  every field of their request; an omitted field is cleared. A supplied
  `fingerprint` or `labelFingerprint` must match the stored value
  (`ABORTED` 409 otherwise, where Compute Engine returns 412) and is
  regenerated after the change.
- Lists page with `maxResults` (default 50, capped at 1,000) and
  `pageToken`; `filter` and `orderBy` are refused when non-empty.
- Reviewed reference fields (for example a subnetwork's `network`) must name
  an existing resource of the same API in the configured project, as a full
  URL, a partial URL such as `global/networks/default`, or a resource name.
  The value is stored as the `selfLink`. A missing resource is `NOT_FOUND`,
  another collection or a malformed value is `INVALID_ARGUMENT`, and a
  resource that another one still references cannot be deleted
  (`FAILED_PRECONDITION` 400, as Compute Engine's
  `resourceInUseByAnotherResource`).

## Limits

Requests are limited to 256 KiB, stored resource documents to 64 KiB and all
Google API catalog metadata to 32 MiB. The 10,000 most recent operations are
kept across all APIs; older ones are pruned and then return `NOT_FOUND`.
Each collection also has its own resource limit, such as 20 AlloyDB clusters
per location. Pages default to 50 items and are capped at 1,000. These are
local limits, not Google Cloud quotas.

## Console and API reference

In the Google Cloud console, choose **Services → Service catalog**. The view
lists, creates, inspects and deletes resources with the same native requests,
including child collections under a selected parent. Deletion asks for an
inline confirmation. Complex official fields remain available through SDKs and
the API reference.

The [Google Cloud catalog API reference](/_simulator/docs/gcp-catalog.html)
serves one OpenAPI document per API, generated from the same embedded
discovery subsets.

## Verification

- `go test ./internal/gcp/catalog` covers every reviewed method, discovery
  model consistency, type conversion, authorization, `requestId` replay,
  `validateOnly`, `etag` and `updateMask` handling, cascade deletion, budgets,
  pagination and the regenerated reference documents.
- `test/e2e/gcp_catalog_fixtures.py` drives every reviewed method through the
  official google-api-python-client with its static discovery documents. It
  runs against two real binaries with separate databases and token
  authorities. Another subject and another project are refused, a second team
  stays empty, state survives restart, and cleanup removes everything.
- `test/e2e/gcp_catalog_console.py` checks the console and the served reference
  in a real browser at 1440 px and 390 px with axe, and confirms that the
  discovery client sees the same state.
- The formal `CatalogResourceLifecycle` and `CatalogConsoleEpoch` models
  declare these sources in scope. They cover only the store and view
  projections described in [formal validation](formal-validation.md).

Unverified: authenticated Google Cloud console parity, real service quotas,
IAM conditions, organization policies and every unlisted official method.
