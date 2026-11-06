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
| Artifact Registry (`artifactregistry.v1`) | #379 | `projects.locations.repositories.{create,get,list,patch,delete}`, `operations.get` | Repositories are configuration metadata. No package, image, file or version is stored, no registry endpoint is served and `sizeBytes` stays 0. Remote and virtual repositories never contact an upstream; upstream credentials must be Secret Manager version names. Cleanup policies and scanning never run. Mode, format, KMS key, remote and virtual configuration cannot be updated |
| Cloud Build (`cloudbuild.v1`) | #381 | `projects.locations.triggers.{create,get,list,patch,delete}`, `workerPools.{create,get,list,patch,delete}`, `operations.get` | Triggers and private worker pools are configuration metadata. No build runs, no event is received and no worker VM is provisioned; worker pools report `RUNNING` immediately. A trigger's ID equals its name. Inline KMS-encrypted secrets are refused and Secret Manager references are never resolved. `allowMissing` deletes are refused. Cloud Build v2 is not served |
| Cloud Deploy (`clouddeploy.v1`) | #382 | `projects.locations.{deliveryPipelines,targets,customTargetTypes,deployPolicies}.{create,get,list,patch,delete}`, `operations.{get,list}` | Pipelines, targets, custom target types and deploy policies are configuration metadata. No release, rollout, render, job or automation runs and no runtime target is contacted. Pipeline conditions are not computed. `allowMissing` is refused |
| Cloud Workstations (`workstations.v1`) | #384 | `projects.locations.workstationClusters.{create,get,list,patch,delete}`, `workstationConfigs.{create,get,list,patch,delete}`, `workstations.{create,get,list,patch,delete}`, `operations.{get,list}` | Clusters, configurations and workstations are configuration metadata. No control plane, gateway, VM or container is created; workstations stay `STATE_STOPPED` and start, stop and access tokens are not implemented. Deleting a parent with children requires `force=true` |
| Developer Connect (`developerconnect.v1`) | #385 | `projects.locations.connections.{create,get,list,patch,delete}`, `connections.gitRepositoryLinks.{create,get,list,delete}`, `accountConnectors.{create,get,list,patch,delete}`, `operations.{get,list}` | Connections, links and account connectors are configuration metadata. No Git provider, OAuth flow, webhook or proxy is contacted or created. Credentials must be Secret Manager version names; custom OAuth client secrets and proxies are refused. Only labels, annotations and `disabled` can be updated. A connection with links cannot be deleted |
| Secure Source Manager (`securesourcemanager.v1`) | #388 | `projects.locations.instances.{create,get,list,delete}`, `repositories.{create,get,list,patch,delete}`, `operations.{get,list}` | Instances and repositories are configuration metadata. No Git server, host name or URI exists and instances report `ACTIVE` immediately. `initialConfig`, listing by `instance` and `allowMissing` deletes are refused |
| Firebase App Hosting (`firebaseapphosting.v1`) | #357 | `projects.locations.backends.{create,get,list,patch,delete}`, `operations.{get,list}` | Backends are configuration metadata. No build, rollout, Cloud Run service, domain or traffic split is created and no URI is served. Connected repositories are never watched. `showDeleted` is refused |
| Blockchain Node Engine (`blockchainnodeengine.v1`) | #320 | `projects.locations.blockchainNodes.{create,get,list,patch,delete}`, `operations.{get,list}` | Nodes are configuration metadata. No Ethereum client, validator, storage or endpoint runs, connection information is not reported and nodes report `RUNNING` immediately. Only labels can be updated |
| Cluster Director (`hypercomputecluster.v1`) | #321 | `projects.locations.clusters.{create,get,list,patch,delete}`, `operations.{get,list}` | Clusters are configuration metadata. No VM, accelerator, network, storage or Slurm orchestrator is provisioned and no job runs. Only description and labels can be updated |
| Google Cloud VMware Engine (`vmwareengine.v1`) | #324 | `projects.locations.vmwareEngineNetworks.{create,get,list,patch,delete}`, `networkPolicies.{create,get,list,patch,delete}`, `operations.{get,list}` | Networks and network policies are configuration metadata. No private cloud, vCenter, NSX or network service is provisioned and networks report `ACTIVE` immediately. Only `STANDARD` networks are accepted; locations and network references are not checked. Edge services ranges must be RFC 1918 `/26`. Private clouds live in zone locations and are not served |
| VM Manager (`osconfig.v1`) | #326 | `projects.patchDeployments.{create,get,list,patch,delete}` | Patch deployments are configuration metadata. No patch job runs and no execution time is computed; pause, resume and patch jobs are not implemented. Exactly one schedule is required and schedules cannot be updated. OS policy assignments live in zone locations and are not served |
| Workload Manager (`workloadmanager.v1`) | #328 | `projects.locations.evaluations.{create,get,list,patch,delete}`, `operations.{get,list}` | Evaluations are configuration metadata. No execution, scan, rule result or BigQuery export runs, scheduled or on demand |
| Filestore (`file.v1`) | #333 | `projects.locations.instances.{create,get,list,patch,delete}`, `instances.snapshots.{create,get,list,patch,delete}`, `backups.{create,get,list,patch,delete}`, `operations.{get,list}` | Instances, snapshots and backups are metadata. No NFS server, file share, address or file data exists, and resources report `READY` immediately. A backup must name an existing `sourceInstance` but captures nothing. Restores, replication, file shares from backups and tag bindings are refused. Deletion protection blocks delete; an instance with snapshots needs `force=true` |
| Google Cloud NetApp Volumes (`netapp.v1`) | #335 | `projects.locations.storagePools.{create,get,list,patch,delete}`, `volumes.{create,get,list,patch,delete}`, `volumes.snapshots.{create,get,list,patch,delete}`, `backupVaults.{create,get,list,patch,delete}`, `backupPolicies.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: no ONTAP system, NFS, SMB or block endpoint, capacity accounting or data. A volume must name an existing storage pool, which cannot be deleted while named; derived fields such as `serviceLevel` are not populated. Active Directory, KMS, restore, backup, replication, cache, block device and cross-region vault settings are refused |
| Memorystore for Redis (`redis.v1`) | #339 | `projects.locations.instances.{create,get,list,patch,delete}`, `clusters.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: no Redis node, endpoint, AUTH string, certificate or data. Instances report `READY` and clusters `ACTIVE` immediately. Backup restores, replication, ACL policies, CA pools, maintenance triggers and tag bindings are refused. Deletion protection blocks cluster delete |
| Memorystore for Memcached (`memcache.v1`) | #339 | `projects.locations.instances.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: no Memcached node, discovery endpoint or data. Only `displayName` is updatable; parameter, upgrade and maintenance methods are unimplemented |
| Backup and DR Service (`backupdr.v1`) | #377 | `projects.locations.backupVaults.{create,get,list,patch,delete}`, `backupPlans.{create,get,list,patch,delete}`, `managementServers.{create,get,list,delete}`, `operations.{get,list}` | Metadata only: no backup is scheduled or taken, no data is stored, and no management console or appliance is deployed. A plan must name an existing vault, which cannot be deleted while named unless `ignoreBackupPlanReferences=true`. Vault lock times are refused and `allowMissing` on delete is not honored |
| Backup for GKE (`gkebackup.v1`) | #329 | `projects.locations.backupPlans.{create,get,list,patch,delete}`, `restorePlans.{create,get,list,patch,delete}`, `operations.{get,list}` | Metadata only: the GKE cluster name is checked only for its format, and nothing is backed up or restored. A restore plan must name an existing backup plan. Deactivated plans and locked retention policies are refused because the locks are not enforced |
| Firestore (`firestore.v1`) | #338 | `projects.databases.{create,get,list,patch,delete}`, `databases.backupSchedules.{create,get,list,patch,delete}`, `databases.collectionGroups.indexes.{create,get,list,delete}`, `databases.operations.{get,list}` | Database administration metadata only: no document store, query engine, index build or backup. Composite indexes report `READY` immediately without duplicate checks. Documents, export, import, clone, restore, field and user credential methods are unimplemented. Deleting a database removes its backup schedules and indexes; deletion protection blocks delete. Operations live under `projects/{project}/databases/{database}/operations` |
| Spanner (`spanner.v1`) | #340 | `projects.instances.{create,get,list,patch,delete}`, `instances.databases.{create,get,list,patch,dropDatabase}`, `databases.backupSchedules.{create,get,list,patch,delete}`, `instances.operations.{get,list}`, `databases.operations.{get,list}` | Metadata only: no compute capacity, storage, schema, session, SQL or backup. Google-managed instance configurations are not listed; an instance records its `config` as given. Database IDs come from the `CREATE DATABASE` statement; extra DDL and proto descriptors are refused. Deleting an instance removes its databases unless one has drop protection, which also blocks `dropDatabase` |
| Managed Service for Apache Kafka (`managedkafka.v1`) | #399 | `projects.locations.clusters.{create,get,list,patch,delete}`, `clusters.topics.{create,get,list,patch,delete}`, `connectClusters.{create,get,list,patch,delete}`, `operations.{get,list}` | Configuration metadata only: no broker, partition, message, consumer group or connector exists, and clusters report `ACTIVE` immediately. Capacity is checked against the documented minimums. Topic partition counts cannot be changed. Delete topics before their cluster. ACLs, consumer groups, connectors and schema registries return 501 |
| Datastream (`datastream.v1`) | #397 | `projects.locations.connectionProfiles.{create,get,list,patch,delete}`, `privateConnections.{create,get,list,delete}`, `privateConnections.routes.{create,get,list,delete}`, `streams.{create,get,list,patch,delete}`, `operations.{get,list}` | No source or destination is contacted and connectivity is never validated. No VPC peering, PSC interface or route is created, and a private connection needs exactly one of them. Passwords, private keys, client secrets, security tokens and raw secret values are refused. Streams stay `NOT_STARTED`, so any other state is refused and `streams.run` returns 501. Deleting a private connection that has routes requires `force=true` |
| Dataproc Metastore (`metastore.v1`) | #396 | `projects.locations.services.{create,get,list,patch,delete}`, `services.backups.{create,get,list,delete}`, `federations.{create,get,list,patch,delete}`, `operations.{get,list}` | No Hive metastore, endpoint or database exists. Backups hold no metadata, and restore, import, export and migration return 501. Resource Manager tags and deletion protection are refused. Delete backups before their service |
| Dataform (`dataform.v1`) | #394 | `projects.locations.repositories.{create,get,list,patch,delete}`, `repositories.workspaces.{create,get,list,delete}`, `repositories.releaseConfigs.{create,get,list,patch,delete}`, `repositories.workflowConfigs.{create,get,list,patch,delete}` | No Git repository, file or BigQuery job exists. Nothing is compiled or invoked, and cron schedules never fire. Folders, the admin grant and release compilation results are refused. A repository with children cannot be deleted |
| Cloud Data Fusion (`datafusion.v1`) | #390 | `projects.locations.instances.{create,get,list,patch,delete}`, `instances.dnsPeerings.{create,list,delete}`, `operations.{get,list}` | No CDAP, pipeline, endpoint or DNS zone exists, and instances report `ACTIVE` immediately. Tags are refused, and restart and version listing return 501. Deleting an instance that has DNS peerings requires `force=true` |
| Looker (Google Cloud core) (`looker.v1`) | #402 | `projects.locations.instances.{create,get,list,patch,delete}`, `operations.{get,list}` | No Looker application, URI or IP address exists, and instances report `ACTIVE` immediately. OAuth client secrets are refused. Deletion is immediate rather than soft. Backups, restart, import, export, restore and undelete return 501 |
| Managed Service for Apache Airflow (`composer.v1`) | #403 | `projects.locations.environments.{create,get,list,patch,delete}`, `operations.{get,list}` | The ID is the full name in the create body. No Airflow, GKE cluster, bucket or database exists, and environments report `RUNNING` immediately. The `state` field is refused, and only `labels` can be updated (nested mask paths are refused). Airflow commands and snapshots return 501 |
| Knowledge Catalog (`dataplex.v1`) | #400 | `projects.locations.lakes.{create,get,list,patch,delete}`, `lakes.zones.{create,get,list,patch,delete}`, `lakes.zones.assets.{create,get,list,patch,delete}`, `entryGroups`, `entryTypes`, `aspectTypes` and `glossaries` `.{create,get,list,patch,delete}`, `glossaries.{categories,terms}.{create,get,list,patch,delete}`, `operations.{get,list}` | No bucket, dataset or entry is read or published. Discovery, scans, tasks and metadata jobs never run, and asset, metastore, aspect type and glossary parent references are not resolved. Delete children before their lake, zone or glossary. Entries, data products and taxonomies return 501 |
| Lakehouse (`biglake.v1`) | #401 | `projects.locations.catalogs.{create,get,list,delete}`, `catalogs.databases.{create,get,list,patch,delete}`, `catalogs.databases.tables.{create,get,list,patch,delete}` | Hive-style metadata only: no storage, Iceberg file or query engine. Deletes return the deleted resource. Delete children before their parent. `tables.list` returns full tables for every view. Rename and the Iceberg REST catalog return 501 |
| Managed Service for Apache Spark (`dataproc.v1`) | #404 | `projects.locations.autoscalingPolicies.{create,get,list,update,delete}`, `projects.locations.sessionTemplates.{create,get,list,patch,delete}` | Configuration only: no cluster, batch, session or autoscaler runs. Updates replace the whole policy or template and cannot change its ID. Clusters, jobs, batches, sessions and workflow templates return 501, as does `projects.regions.*` |
| Cloud IDS (`ids.v1`) | #344 | `projects.locations.endpoints.{create,get,list,patch,delete}`, `operations.{get,list}` | Endpoints are zonal (for example `us-central1-a`) configuration metadata and report `READY` immediately. No IDS engine, load balancer, packet mirroring or threat log exists, and `endpointIp` stays absent |
| Cloud Logging (`logging.v2`) | #372 | `projects.sinks.{create,get,list,patch,delete}`, `projects.exclusions.{create,get,list,patch,delete}`, `projects.metrics.{create,get,list,delete}`, `projects.locations.buckets.{create,get,list,patch,delete}`, `buckets.views.{create,get,list,patch,delete}` | Configuration only: no log entry is written, read, routed, excluded, counted or exported. Sinks get no writer identity, `_Default` and `_Required` are not pre-created, bucket locks are refused, deleted buckets are removed immediately and `metrics.update` is not implemented |
| Cloud Monitoring (`monitoring.v3`) | #374 | `projects.alertPolicies.{create,get,list,patch,delete}`, `projects.notificationChannels.{create,get,list,patch,delete}`, `projects.uptimeCheckConfigs.{create,get,list,patch,delete}` | Server-generated IDs. No metric, incident, probe or notification exists. Channel credentials and uptime check passwords or secret headers are refused. A patch without `updateMask` updates only the supplied fields |
| Network Connectivity Center (`networkconnectivity.v1`) | #361, #362 | `projects.locations.global.hubs.{create,get,list,patch,delete}`, `projects.locations.spokes.{create,get,list,patch,delete}`, `spokes.gatewayAdvertisedRoutes.{create,get,list,patch,delete}`, `operations.{get,list}` | Hubs, spokes (including NCC gateway spokes) and gateway advertised routes are metadata that report `ACTIVE`. No route is exchanged and no linked resource is resolved. Spoke review, hub groups and route tables are not implemented |
| Network Management (`networkmanagement.v1`) | #363 | `projects.locations.global.connectivityTests.{create,get,list,patch,delete}`, `global.operations.{get,list}` | Stored test definitions only. No reachability analysis runs, `reachabilityDetails` stays absent and `rerun` is not implemented |
| Network Security (`networksecurity.v1`) | #353, #354, #364, #367 | `projects.locations.{securityProfiles,securityProfileGroups,firewallEndpoints,firewallEndpointAssociations,mirroringDeploymentGroups,mirroringDeployments,mirroringEndpointGroups,mirroringEndpointGroupAssociations,interceptDeploymentGroups,interceptDeployments,interceptEndpointGroups,interceptEndpointGroupAssociations,gatewaySecurityPolicies,urlLists,tlsInspectionPolicies,addressGroups}.{create,get,list,patch,delete}`, `gatewaySecurityPolicies.rules.{create,get,list,patch,delete}`, `operations.{get,list}` | Configuration metadata. No firewall engine, mirroring, interception, TLS inspection or proxy runs. Firewall endpoints, associations and deployments are zonal. Organization-level resources are not implemented |
| Network Services (`networkservices.v1`) | #367 | `projects.locations.gateways.{create,get,list,patch,delete}`, `operations.{get,list}` | Secure Web Proxy and mesh gateways are metadata. No proxy is deployed and no address is allocated. Media CDN, meshes and routes are not implemented |
| Service Directory (`servicedirectory.v1`) | #368 | `projects.locations.namespaces.{create,get,list,patch,delete}`, `namespaces.services.{create,get,list,patch,delete}`, `services.endpoints.{create,get,list,patch,delete}` | Registry metadata. No DNS or resolution (`services.resolve`) exists. Endpoint addresses must be literal IP addresses. Deleting a namespace or service deletes its children, as the official API does |
| Compute Engine (`compute.v1`): Virtual Private Cloud, Persistent Disk, instance templates, Cloud Router and Cloud NAT, Cloud VPN, Google Cloud Armor (and Enterprise policy settings), Cloud Load Balancing, Cloud CDN, Partner Interconnect, Network Service Tiers on forwarding rules | #371, #322, #332, #355, #351, #356, #358, #359, #350, #342, #345, #347, #365 | `networks.{insert,get,list,patch,delete}`, `subnetworks.{insert,get,list,patch,delete,expandIpCidrRange,setPrivateIpGoogleAccess}`, `firewalls.{insert,get,list,patch,delete}`, `routes.{insert,get,list,delete}`, `disks.{insert,get,list,delete,setLabels,resize}`, `snapshots.{insert,get,list,delete,setLabels}`, `instanceTemplates.{insert,get,list,delete}`, `routers.{insert,get,list,patch,delete}`, `vpnGateways`/`externalVpnGateways`/`vpnTunnels` `.{insert,get,list,delete,setLabels}`, `securityPolicies.{insert,get,list,patch,delete,setLabels}`, `healthChecks.{insert,get,list,patch,delete}`, `backendServices.{insert,get,list,patch,delete,setSecurityPolicy}`, `backendBuckets.{insert,get,list,patch,delete}`, `urlMaps.{insert,get,list,patch,delete}`, `targetHttpProxies.{insert,get,list,patch,delete}`, `globalForwardingRules.{insert,get,list,patch,delete,setTarget,setLabels}`, `interconnectAttachments.{insert,get,list,patch,delete,setLabels}`, `globalOperations`/`regionOperations`/`zoneOperations` `.{get,list,wait}` | Configuration metadata only: no packet is routed, filtered, translated or load balanced, no VM runs, no BGP or IKE session exists, no probe runs and no block storage or cache exists. Only custom mode networks; no default network or system routes. References must exist, and a referenced resource cannot be deleted. No address is allocated: forwarding rules state `IPAddress`, NAT uses `AUTO_ONLY`, HA VPN interfaces carry no IP. Customer-supplied encryption keys, VPN shared secrets, router MD5 keys and IAP client secrets are refused and never stored. Security policy rules are fixed at creation (`addRule`/`patchRule`/`removeRule` return `UNIMPLEMENTED`). Partner attachments stay `PENDING_PARTNER`. Instances, Dedicated/Cross-Cloud/Cross-Site Interconnect and the project default network tier are not simulated |

The capability document lists the same APIs under `gcpCatalog.apis`. Each
entry includes every implemented method, the number of official methods,
collections, create fields, the discovery revision and source hash, and the
bounds above.

## Routing and authorization

- Requests use `/_simulator/providers/gcp/<api>/<official path>`, for example
  `/_simulator/providers/gcp/alloydb/v1/projects/tenkacloud-local/locations/us-central1/clusters`.
  Set the client's `api_endpoint` to the advertised `endpoint`, which is
  `/_simulator/providers/gcp/<api>/` followed by the API's service path, for
  example `/_simulator/providers/gcp/alloydb/` or
  `/_simulator/providers/gcp/compute/compute/v1/` (the official client
  replaces both the root URL and the service path with it).
  When several official bindings match one path, the one with the most
  literal segments serves it, such as Cloud Logging's `projects.*` methods
  over their `{+parent}` aliases and Compute Engine
  `backendServices/listUsable` over `backendServices/{name}`.
- The configured project ID or project number is accepted. Only the
  configured project owner (`--cloud-storage-owner`, default
  `simulator-admin`) may call a method. Any other subject or project receives
  `PERMISSION_DENIED`, and the response does not reveal whether the resource
  exists. This local ownership check is not Google IAM, and AWS identity
  policies grant nothing here.
- Supported locations are `global`, `us-central1`, `us-east1`, `us-west1`,
  `europe-west1` and `asia-northeast1`. Each location is a separate scope.
  Zonal collections, such as Cloud IDS endpoints and Cloud NGFW firewall
  endpoints, use zones of those regions (`us-central1-a` to `us-central1-z`).

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
  fields, clear the writable fields the body omits and refuse an update
  mask.
- Relative resource names are scoped per API, as full resource names are, so
  an AlloyDB cluster and a Cluster Director cluster can share a name. A
  delete whose official response is the resource, as in BigLake, returns the
  deleted resource.
- An official query parameter that a reviewed method does not implement, such
  as `allowMissing` on a delete or a list filter by `instance`, is refused
  with `INVALID_ARGUMENT` when set instead of being ignored.
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
- Some references are checked only at creation and do not block deletion,
  as a snapshot outlives its source disk. Reviewed conflict rules, such as
  overlapping subnetwork ranges in one network, are checked against every
  other resource of the collection on create and update.
- A merge patch may restate an immutable field with its stored value.

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
