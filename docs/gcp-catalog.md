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
| Filestore (`file.v1`) | #333 | `projects.locations.instances.{create,get,list,patch,delete}`, `instances.snapshots.{create,get,list,patch,delete}`, `backups.{create,get,list,patch,delete}`, `operations.{get,list}` | Instances, snapshots and backups are metadata. No NFS server, file share, address or file data exists, and resources report `READY` immediately. A backup must name an existing `sourceInstance` but captures nothing. Restores, replication, file shares from backups and tag bindings are refused. Deletion protection blocks delete; an instance with snapshots needs `force=true` |
| Google Cloud NetApp Volumes (`netapp.v1`) | #335 | `projects.locations.storagePools.{create,get,list,patch,delete}`, `volumes.{create,get,list,patch,delete}`, `volumes.snapshots.{create,get,list,patch,delete}`, `backupVaults.{create,get,list,patch,delete}`, `backupPolicies.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: no ONTAP system, NFS, SMB or block endpoint, capacity accounting or data. A volume must name an existing storage pool, which cannot be deleted while named; derived fields such as `serviceLevel` are not populated. Active Directory, KMS, restore, backup, replication, cache, block device and cross-region vault settings are refused |
| Memorystore for Redis (`redis.v1`) | #339 | `projects.locations.instances.{create,get,list,patch,delete}`, `clusters.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: no Redis node, endpoint, AUTH string, certificate or data. Instances report `READY` and clusters `ACTIVE` immediately. Backup restores, replication, ACL policies, CA pools, maintenance triggers and tag bindings are refused. Deletion protection blocks cluster delete |
| Memorystore for Memcached (`memcache.v1`) | #339 | `projects.locations.instances.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: no Memcached node, discovery endpoint or data. Only `displayName` is updatable; parameter, upgrade and maintenance methods are unimplemented |
| Backup and DR Service (`backupdr.v1`) | #377 | `projects.locations.backupVaults.{create,get,list,patch,delete}`, `backupPlans.{create,get,list,patch,delete}`, `managementServers.{create,get,list,delete}`, `operations.{get,list}` | Metadata only: no backup is scheduled or taken, no data is stored, and no management console or appliance is deployed. A plan must name an existing vault, which cannot be deleted while named unless `ignoreBackupPlanReferences=true`. Vault lock times are refused and `allowMissing` on delete is not honored |
| Backup for GKE (`gkebackup.v1`) | #329 | `projects.locations.backupPlans.{create,get,list,patch,delete}`, `restorePlans.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: the GKE cluster name is checked only for its format, and nothing is backed up or restored. A restore plan must name an existing backup plan. Deactivated plans and locked retention policies are refused because the locks are not enforced |
| Firestore (`firestore.v1`) | #338 | `projects.databases.{create,get,list,patch,delete}`, `databases.backupSchedules.{create,get,list,patch,delete}`, `databases.operations.{get,list}` | Database administration metadata only: no document store, query engine, index build or backup. Documents, export, import, clone, restore, index, field and user credential methods are unimplemented. Deleting a database removes its backup schedules; deletion protection blocks delete. Operations live under `projects/{project}/databases/{database}/operations` |
| Spanner (`spanner.v1`) | #340 | `projects.instances.{create,get,list,patch,delete}`, `instances.databases.{create,get,list,patch,dropDatabase}`, `databases.backupSchedules.{create,get,list,patch,delete}`, `instances.operations.{get,list}`, `databases.operations.{get,list}` | Metadata only: no compute capacity, storage, schema, session, SQL or backup. Google-managed instance configurations are not listed; an instance records its `config` as given. Database IDs come from the `CREATE DATABASE` statement; extra DDL and proto descriptors are refused. Deleting an instance removes its databases unless one has drop protection, which also blocks `dropDatabase` |

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
- Operations are named under the scope the official operations methods use,
  such as `projects/{project}/locations/{location}/operations` or, for APIs
  that keep them per resource, `projects/{project}/databases/{database}/operations`.
- Reviewed reference fields must name an existing resource of the same API
  (`NOT_FOUND` otherwise). Where the official service refuses it, a resource
  that is still referenced cannot be deleted (`FAILED_PRECONDITION`).
  Deletion protection fields block delete with `FAILED_PRECONDITION` until a
  patch clears them.
- Errors use `google.rpc.Status` with the canonical HTTP mapping, for example
  `ALREADY_EXISTS` 409 and `FAILED_PRECONDITION` 400.

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
