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
| Managed Service for Apache Kafka (`managedkafka.v1`) | #399 | `projects.locations.clusters.{create,get,list,patch,delete}`, `clusters.topics.{create,get,list,patch,delete}`, `connectClusters.{create,get,list,patch,delete}`, `operations.{get,list}` | Configuration metadata only: no broker, partition, message, consumer group or connector exists, and clusters report `ACTIVE` immediately. Capacity is checked against the documented minimums. Topic partition counts cannot be changed. Delete topics before their cluster. ACLs, consumer groups, connectors and schema registries return 501 |
| Datastream (`datastream.v1`) | #397 | `projects.locations.connectionProfiles.{create,get,list,patch,delete}`, `privateConnections.{create,get,list,delete}`, `privateConnections.routes.{create,get,list,delete}`, `streams.{create,get,list,patch,delete}`, `operations.{get,list}` | No source or destination is contacted and connectivity is never validated. No VPC peering, PSC interface or route is created. Passwords, private keys, client secrets, security tokens and raw secret values are refused. Streams stay `NOT_STARTED`, so any other state is refused and `streams.run` returns 501. Deleting a private connection that has routes requires `force=true` |
| Dataproc Metastore (`metastore.v1`) | #396 | `projects.locations.services.{create,get,list,patch,delete}`, `services.backups.{create,get,list,delete}`, `federations.{create,get,list,patch,delete}`, `operations.{get,list}` | No Hive metastore, endpoint or database exists. Backups hold no metadata, and restore, import, export and migration return 501. Resource Manager tags and deletion protection are refused. Delete backups before their service |
| Dataform (`dataform.v1`) | #394 | `projects.locations.repositories.{create,get,list,patch,delete}`, `repositories.workspaces.{create,get,list,delete}`, `repositories.releaseConfigs.{create,get,list,patch,delete}`, `repositories.workflowConfigs.{create,get,list,patch,delete}` | No Git repository, file or BigQuery job exists. Nothing is compiled or invoked, and cron schedules never fire. Folders, the admin grant and release compilation results are refused. A repository with children cannot be deleted |
| Cloud Data Fusion (`datafusion.v1`) | #390 | `projects.locations.instances.{create,get,list,patch,delete}`, `instances.dnsPeerings.{create,list,delete}`, `operations.{get,list}` | No CDAP, pipeline, endpoint or DNS zone exists, and instances report `ACTIVE` immediately. Tags are refused. Deleting an instance that has DNS peerings requires `force=true` |
| Looker (Google Cloud core) (`looker.v1`) | #402 | `projects.locations.instances.{create,get,list,patch,delete}`, `operations.{get,list}` | No Looker application, URI or IP address exists, and instances report `ACTIVE` immediately. OAuth client secrets are refused. Deletion is immediate rather than soft. Backups, restart, import, export, restore and undelete return 501 |
| Managed Service for Apache Airflow (`composer.v1`) | #403 | `projects.locations.environments.{create,get,list,patch,delete}`, `operations.{get,list}` | The ID is the full name in the create body. No Airflow, GKE cluster, bucket or database exists, and environments report `RUNNING` immediately. The `state` field is refused, and only `labels` can be updated (nested mask paths are refused). Airflow commands and snapshots return 501 |
| Knowledge Catalog (`dataplex.v1`) | #400 | `projects.locations.lakes.{create,get,list,patch,delete}`, `lakes.zones.{create,get,list,patch,delete}`, `lakes.zones.assets.{create,get,list,patch,delete}`, `entryGroups`, `entryTypes` and `aspectTypes` `.{create,get,list,patch,delete}`, `operations.{get,list}` | No bucket, dataset or entry is read or published. Discovery, scans, tasks and metadata jobs never run, and asset references are not resolved. Delete assets before their zone and zones before their lake. Entries, glossaries, data products and taxonomies return 501 |
| Lakehouse (`biglake.v1`) | #401 | `projects.locations.catalogs.{create,get,list,delete}`, `catalogs.databases.{create,get,list,patch,delete}`, `catalogs.databases.tables.{create,get,list,patch,delete}` | Hive-style metadata only: no storage, Iceberg file or query engine. Deletes return the deleted resource. Delete children before their parent. `tables.list` returns full tables for every view. Rename and the Iceberg REST catalog return 501 |
| Managed Service for Apache Spark (`dataproc.v1`) | #404 | `projects.locations.autoscalingPolicies.{create,get,list,update,delete}`, `projects.locations.sessionTemplates.{create,get,list,patch,delete}` | Configuration only: no cluster, batch, session or autoscaler runs. Updates replace the whole policy or template and cannot change its ID. Clusters, jobs, batches, sessions and workflow templates return 501, as does `projects.regions.*` |

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
  restricts a patch to the reviewed mutable fields. Full-replacement
  updates (Dataproc `PUT` and mask-less `PATCH`) keep the ID and output
  fields and clear the writable fields the body omits.
- Resource names are scoped by API, so two services may each own
  `projects/p/locations/l/instances/i`. A delete whose official response is
  the resource, as in BigLake, returns the deleted resource.
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
