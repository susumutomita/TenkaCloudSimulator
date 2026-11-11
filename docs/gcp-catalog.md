# Google Cloud service catalog

The Google API catalog serves bounded configuration metadata for 40 Google
Cloud APIs through each API's own REST paths, from one engine and one
provider-owned SQLite store. Contracts come from documentation-free subsets
of the discovery documents pinned by google-api-python-client 2.199.0
(`googleapiclient/discovery_cache/documents`). The extraction tool is
`tools/gcpmodels/extract.py`, and the reviewed method list is
`internal/gcp/catalog/selection.json`. The original license ships as
[`third_party/GOOGLE-DISCOVERY-NOTICES.txt`](../third_party/GOOGLE-DISCOVERY-NOTICES.txt)
and is served at `/_simulator/licenses/GOOGLE-DISCOVERY-NOTICES.txt`.

Every listed method is implemented against provider-owned SQLite state. Every
other official method of the same API returns HTTP 501 `UNIMPLEMENTED`.
Request fields that are not in the official schema are refused with
`INVALID_ARGUMENT`; output-only fields sent by a client are dropped, as
AIP-203 requires of servers. No database engine, network, workload or real
Google Cloud call is executed or simulated. Each API is one of two styles:
AIP resource-name APIs (`projects/{project}/locations/{location}/...`) and
flat APIs, currently Compute Engine (`projects/{project}/global/...`,
`.../regions/{region}/...`, `.../zones/{zone}/...`).

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

`/_simulator/v1/capabilities` lists the same APIs under `gcpCatalog.apis` as
compact summaries: name, version, title, Issues, the number of implemented
and official methods, the client endpoint and the per-API OpenAPI document.
It also names the project, locations and zones. The full descriptors
(methods, collections, ID sources, create fields, discovery revision and
source hash, and the bounds above) are served by
`GET /_simulator/v1/catalog/gcp`, which needs the same access token. This
keeps the capability document under 128 KiB as APIs are added.

## Routing and authorization

- Requests use `/_simulator/providers/gcp/<api>/<official path>`, for example
  `/_simulator/providers/gcp/alloydb/v1/projects/tenkacloud-local/locations/us-central1/clusters`.
  Set the client's `api_endpoint` to the advertised `endpoint`, which is
  `/_simulator/providers/gcp/<api>/` followed by the API's service path, for
  example `/_simulator/providers/gcp/alloydb/` or
  `/_simulator/providers/gcp/compute/compute/v1/` (the official client
  replaces both the root URL and the service path with it).
- When several official bindings match one path, the one with the most
  literal segments serves it, such as Cloud Logging's `projects.*` methods
  over their `{+parent}` aliases and Compute Engine
  `backendServices/listUsable` over `backendServices/{name}`. Among equally
  literal templates, the one whose literals come first wins.
- The configured project ID or project number is accepted; stored names and
  references use the project ID. Only the configured project owner
  (`--cloud-storage-owner`, default `simulator-admin`) may call a method. Any
  other subject or project receives `PERMISSION_DENIED`, and the response
  does not reveal whether the resource exists. This local ownership check is
  not Google IAM, and AWS identity policies grant nothing here.
- Supported locations are `global`, `us-central1`, `us-east1`, `us-west1`,
  `europe-west1` and `asia-northeast1`. Each location is a separate scope.
- Zones are the published zones of the supported regions: `us-central1-a`,
  `-b`, `-c`, `-f`; `us-east1-b`, `-c`, `-d`; `us-west1-a`, `-b`, `-c`;
  `europe-west1-b`, `-c`, `-d`; and `asia-northeast1-a`, `-b`, `-c`. Zonal
  AIP collections, such as Cloud IDS endpoints and Cloud NGFW firewall
  endpoints, take a zone as their location, and Compute Engine
  `zones/{zone}` paths take the same zones. Their operations are listed
  under the zone. Any other zone, and a region where a zone is expected, is
  refused with `INVALID_ARGUMENT`.
- A collection's parent is the scope its official path names: for
  `location` APIs `projects/{project}/locations/{location}`, for `project`
  APIs `projects/{project}`, or `projects/{project}/locations/{location}`
  when the path names a location (Cloud Logging buckets), and for flat APIs
  `global`, `regions/{region}` or `zones/{zone}`. Literal locations in an
  official path, such as Network Connectivity Center `locations/global`
  hubs, stay literal.

## Request semantics

- Methods that return `google.longrunning.Operation` complete synchronously.
  The operation is stored with `done: true` and the resource (or `Empty`) as
  its response, and remains readable through `operations.get/list`. The
  response `@type` uses the protobuf message name, which may differ from the
  discovery schema name (Dataplex `Lake` for `GoogleCloudDataplexV1Lake`).
- Operations are named under the scope the official operations methods use,
  such as `projects/{project}/locations/{location}/operations` or, for APIs
  that keep them per resource, `projects/{project}/databases/{database}/operations`.
- New resource IDs come from a create query parameter (`clusterId`), from a
  body field (Cloud Logging sink `name`, Dataproc autoscaling policy `id`),
  or from a wrapped request (Spanner `instanceId`, or the `CREATE DATABASE`
  statement). Where the body field carries the full resource name, as for
  Cloud Composer environments, it must name the request parent. Short-name
  APIs, such as Cloud Logging sinks and Compute Engine, store the bare ID in
  `name`. Other IDs are server-generated. Client-chosen IDs follow AIP-122
  unless the API documents other rules.
- Implicit parent segments, such as Firestore `collectionGroups/{id}`, are
  validated but not stored; their children are deleted with the stored
  parent.
- The parent is read again inside the write transaction, so a concurrent
  delete cannot orphan a new child.
- `requestId` replays the stored operation of an identical request for 24
  hours. A different request with the same `requestId` is refused.
  `validateOnly=true` validates and stores nothing.
- `etag` guards patch and delete (`ABORTED` on mismatch). `updateMask`
  restricts a patch to the reviewed mutable fields; a mask-less patch updates
  the supplied fields. Wrapped updates, such as Spanner's
  `UpdateInstanceRequest`, carry the mask in the body.
- Full-replacement updates (Dataproc `PUT` and mask-less `PATCH`) keep the
  ID and output fields, clear the writable fields the body omits, check the
  resource's required fields and refuse an update mask, as a parameter or a
  wrapped field.
- Relative resource names are scoped per API, as full resource names are, so
  an AlloyDB cluster and a Cluster Director cluster can share a name. A
  delete whose official response is the resource, as in BigLake, returns the
  deleted resource.
- An official query parameter that a reviewed method does not implement, such
  as `allowMissing` on a delete or a list filter by `instance`, is refused
  with `INVALID_ARGUMENT` when set to anything but its default value.
- Reviewed reference fields, including nested and list paths such as
  `nats[].subnetworks[].name`, must name an existing resource of the
  collections they allow in the same API and project. They are checked at
  creation and whenever an update writes the field, inside the write
  transaction, and stored with the project ID (or as a `selfLink` in flat
  APIs). A missing resource is `NOT_FOUND`. A blocking reference keeps the
  named resource from being deleted (`FAILED_PRECONDITION`) unless the
  official override parameter, such as `ignoreBackupPlanReferences=true`,
  is set; references from the resource's own subtree do not block. Other
  references do not block, as a snapshot outlives its source disk.
- Reviewed conflict rules, such as overlapping subnetwork ranges in one
  network, are checked against every other resource of the collection on
  create and update.
- Deletion protection fields block delete with `FAILED_PRECONDITION` until a
  patch clears them. Deleting a parent with children fails with
  `FAILED_PRECONDITION` unless the API cascades: with `force=true` where the
  official delete has a `force` parameter, and always where it has none
  (as in Spanner, Firestore and Service Directory). A cascade is refused
  while a child is protected.
- Errors use `google.rpc.Status` with the canonical HTTP mapping, for example
  `ALREADY_EXISTS` 409 and `FAILED_PRECONDITION` 400.

### Flat APIs (Compute Engine style)

Compute Engine (`compute.v1`) addresses resources with separate path
parameters (`projects/{project}/zones/{zone}/disks/{disk}`,
`.../regions/{region}/...`, `.../global/...`) and takes the resource name in
the request body.

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
  unchanged `name` back, or restating an immutable field with its stored
  value, is accepted; a different `name` is refused.
- Custom methods that set fields (for example `setLabels` or `resize`) write
  every field of their request; an omitted field is cleared. A supplied
  `fingerprint` or `labelFingerprint` must match the stored value
  (`ABORTED` 409 otherwise, where Compute Engine returns 412) and is
  regenerated after the change.
- Lists page with `maxResults` (default 50, capped at 1,000) and
  `pageToken`; `filter` and `orderBy` are refused when non-empty.
- References accept a full URL, a partial URL such as
  `global/networks/default`, or a resource name, and are stored as the
  `selfLink`. A reference to another collection or a malformed value is
  `INVALID_ARGUMENT`, and deleting a resource that another one still uses is
  `FAILED_PRECONDITION` 400, as Compute Engine's
  `resourceInUseByAnotherResource`.

## Limits

Requests are limited to 256 KiB, stored resource documents to 64 KiB and all
Google API catalog metadata to 32 MiB; the metadata budget is checked after
every insert and update, inside the write. The 10,000 most recent operations
are kept across all APIs; older ones are pruned and then return `NOT_FOUND`.
Each collection also has its own resource limit, such as 20 AlloyDB clusters
per location. Pages default to 50 items and are capped at 1,000. Requests
nest at most 24 levels and 4,096 JSON values. These are local limits, not
Google Cloud quotas.

## Console and API reference

In the Google Cloud console, choose **Services → Service catalog**. The view
loads the descriptors from `/_simulator/v1/catalog/gcp`, then lists,
creates, inspects and deletes resources with the same native requests. It
builds each parent from the collection's official path: the project, a
location, region or zone selector where the path has one, and one selector
per parent level. Body IDs are sent in the create body (as the full name
where the API expects it), and short names resolve under the listed parent.
Collections under an implicit parent, such as Firestore indexes, are listed
under an ID you type and are read-only, as are collections whose create
request needs nested fields (for example Spanner instances); the view says
so. Deletion asks for an inline confirmation. Complex official fields remain
available through SDKs and the API reference.

The [Google Cloud catalog API reference](/_simulator/docs/gcp-catalog.html)
serves one OpenAPI document per API, generated from the same embedded
discovery subsets.

## Not served

These open Google Cloud Issues have no catalog API. The reason is either the
pinned corpus or the service itself. App Engine (#318) is not a catalog API:
its single permanent application and code deployments are served by the native
[App Engine provider](gcp-appengine.md), which runs Python versions.

| Issue | Service | Reason |
| --- | --- | --- |
| #319 | Batch | The only create method, `jobs.create`, runs a workload |
| #323 | Google Cloud Universal Ledger | No discovery document in the pinned corpus |
| #325 | License Manager | No discovery document in the pinned corpus (`licensing.v1` is Workspace license assignment) |
| #330 | Cloud Managed Lustre | No discovery document in the pinned corpus |
| #331 | Cloud Storage | Served by the native Cloud Storage provider ([guide](gcp-storage.md)), not the catalog |
| #334 | Cloud Storage for Firebase | `firebasestorage.v1beta` only links Cloud Storage buckets to Firebase; those buckets belong to the native Cloud Storage provider |
| #337 | Bigtable | `instances.create` creates an instance and its map of clusters in one request, which the one-resource create cannot represent |
| #341 | Cloud SQL | `sqladmin.v1` flat paths have no `global`, `regions` or `zones` scope, which the flat conventions require, and many instance methods are lifecycle actions (`restart`, `failover`, `clone`, `import`, `executeSql`) |
| #343 | Cloud DNS | `dns.v1` flat paths have no `global`, `regions` or `zones` scope, and a record set is named by two segments (`rrsets/{name}/{type}`) |
| #346, #348, #349 | Dedicated, Cross-Cloud and Cross-Site Interconnect | `interconnects.insert` orders a physical circuit at an interconnect location, and Cross-Site Interconnect uses wire groups; only Partner Interconnect attachments are served |
| #352 | Cloud Network Insights | No discovery document in the pinned corpus |
| #360 | Media CDN | The `edgeCache*` resources of `networkservices.v1` expose only IAM policy methods |
| #366 | Private Service Connect | Endpoints are regional forwarding rules and producers use `serviceAttachments`; neither is bound (only global forwarding rules are) |
| #369 | Spectrum Access System | `sasportal.v1alpha1` names resources under `customers/` and `nodes/`, not `projects/` |
| #370 | Telecom Network Automation | No discovery document in the pinned corpus |
| #373 | Error Reporting | No create method: groups come from reported events (`events.report`), and `groups.update` only changes their tracking state |
| #375 | Cloud Profiler | `profiles.create` and `createOffline` collect profile data from a profiling agent; there is no configuration resource |
| #376 | Cloud Trace | The only writes, `traces.batchWrite` and `spans.createSpan`, record trace data |
| #378 | Android Device Streaming | `testing.v1` `deviceSessions.create` opens a streaming session on a test device |
| #380 | Assured Open Source Software | No discovery document in the pinned corpus |
| #383 | Cloud Source Repositories | The pinned discovery document marks the service end of sales from June 17, 2024 |
| #386 | Firebase Test Lab | `testMatrices.create` creates and runs a matrix of tests on devices |
| #387 | Google Device Cloud | No discovery document in the pinned corpus |
| #389 | BigQuery | Datasets and tables hold row data (`tabledata.insertAll`) and jobs run queries (`jobs.insert`, `jobs.query`) |
| #391 | Cloud Life Sciences | The only resource method, `pipelines.run`, runs a workload, and operations exist only for runs |
| #392, #393 | Data Studio, Data Studio Pro | No discovery document in the pinned corpus |
| #395 | Dataflow | Every resource-creating method runs or launches a pipeline, and the paths use `{projectId}` and `{location}` without AIP resource names |
| #398 | Earth Engine | No discovery document in the pinned corpus |

Served Issues also leave parts out: Cloud Build v2 (#381; the engine serves
one version per API name, so v1 is served), VMware Engine private clouds
(#324) and OS policy assignments (#326), which live in zone locations,
Compute Engine instances (#322; `instances.insert` boots a VM, so instance
templates carry the configuration), Firestore documents (#338), and the
methods each row's bounds name.

## Verification

- `go test ./internal/gcp/catalog` covers every reviewed method, discovery
  model consistency, type conversion, authorization, `requestId` replay,
  `validateOnly`, `etag` and `updateMask` handling, full replacement, body
  and wrapped IDs, references, conflicts, zones, route choice, cascade
  deletion, budgets, pagination and the regenerated reference documents.
- `go test ./internal/server` keeps the capability document under 128 KiB
  with every API registered and checks the descriptor endpoint.
- `test/e2e/gcp_catalog_fixtures.py` drives every reviewed method through the
  official google-api-python-client with its static discovery documents. It
  runs against two real binaries with separate databases and token
  authorities. Another subject and another project are refused, a second team
  stays empty, state survives restart, and cleanup removes everything.
- `test/e2e/gcp_catalog_console.py` checks the console and the served
  reference in a real browser at 1440 px and 390 px with axe, for AlloyDB
  parents and children, Cloud Logging sinks (body IDs and short names) and
  buckets (a location in a project-scope path), Cloud IDS endpoints (zones),
  Compute Engine networks, and the read-only notes for Firestore indexes and
  Spanner instances, and confirms that the discovery client sees the same
  state.
- The formal `CatalogResourceLifecycle` and `CatalogConsoleEpoch` models
  declare these sources in scope. They cover only the store and view
  projections described in [formal validation](formal-validation.md).

Unverified: authenticated Google Cloud console parity, real service quotas,
IAM conditions, organization policies and every unlisted official method.
