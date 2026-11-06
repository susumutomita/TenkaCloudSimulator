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
| Cloud IDS (`ids.v1`) | #344 | `projects.locations.endpoints.{create,get,list,patch,delete}`, `operations.{get,list}` | Endpoints are zonal (for example `us-central1-a`) configuration metadata and report `READY` immediately. No IDS engine, load balancer, packet mirroring or threat log exists, and `endpointIp` stays absent |
| Cloud Logging (`logging.v2`) | #372 | `projects.sinks.{create,get,list,patch,delete}`, `projects.exclusions.{create,get,list,patch,delete}`, `projects.metrics.{create,get,list,delete}`, `projects.locations.buckets.{create,get,list,patch,delete}`, `buckets.views.{create,get,list,patch,delete}` | Configuration only: no log entry is written, read, routed, excluded, counted or exported. Sinks get no writer identity, `_Default` and `_Required` are not pre-created, bucket locks are refused, deleted buckets are removed immediately and `metrics.update` is not implemented |
| Cloud Monitoring (`monitoring.v3`) | #374 | `projects.alertPolicies.{create,get,list,patch,delete}`, `projects.notificationChannels.{create,get,list,patch,delete}`, `projects.uptimeCheckConfigs.{create,get,list,patch,delete}` | Server-generated IDs. No metric, incident, probe or notification exists. Channel credentials and uptime check passwords or secret headers are refused. A patch without `updateMask` updates only the supplied fields |
| Network Connectivity Center (`networkconnectivity.v1`) | #361, #362 | `projects.locations.global.hubs.{create,get,list,patch,delete}`, `projects.locations.spokes.{create,get,list,patch,delete}`, `spokes.gatewayAdvertisedRoutes.{create,get,list,patch,delete}`, `operations.{get,list}` | Hubs, spokes (including NCC gateway spokes) and gateway advertised routes are metadata that report `ACTIVE`. No route is exchanged and no linked resource is resolved. Spoke review, hub groups and route tables are not implemented |
| Network Management (`networkmanagement.v1`) | #363 | `projects.locations.global.connectivityTests.{create,get,list,patch,delete}`, `global.operations.{get,list}` | Stored test definitions only. No reachability analysis runs, `reachabilityDetails` stays absent and `rerun` is not implemented |
| Network Security (`networksecurity.v1`) | #353, #354, #364, #367 | `projects.locations.{securityProfiles,securityProfileGroups,firewallEndpoints,firewallEndpointAssociations,mirroringDeploymentGroups,mirroringDeployments,mirroringEndpointGroups,mirroringEndpointGroupAssociations,interceptDeploymentGroups,interceptDeployments,interceptEndpointGroups,interceptEndpointGroupAssociations,gatewaySecurityPolicies,urlLists,tlsInspectionPolicies,addressGroups}.{create,get,list,patch,delete}`, `gatewaySecurityPolicies.rules.{create,get,list,patch,delete}`, `operations.{get,list}` | Configuration metadata. No firewall engine, mirroring, interception, TLS inspection or proxy runs. Firewall endpoints, associations and deployments are zonal. Organization-level resources are not implemented |
| Network Services (`networkservices.v1`) | #367 | `projects.locations.gateways.{create,get,list,patch,delete}`, `operations.{get,list}` | Secure Web Proxy and mesh gateways are metadata. No proxy is deployed and no address is allocated. Media CDN, meshes and routes are not implemented |
| Service Directory (`servicedirectory.v1`) | #368 | `projects.locations.namespaces.{create,get,list,patch,delete}`, `namespaces.services.{create,get,list,patch,delete}`, `services.endpoints.{create,get,list,patch,delete}` | Registry metadata. No DNS or resolution (`services.resolve`) exists. Endpoint addresses must be literal IP addresses. Deleting a namespace or service deletes its children, as the official API does |

The capability document lists the same APIs under `gcpCatalog.apis`. Each
entry includes every implemented method, the number of official methods,
collections, create fields, the discovery revision and source hash, and the
bounds above.

## Routing and authorization

- Requests use `/_simulator/providers/gcp/<api>/<official path>`, for example
  `/_simulator/providers/gcp/alloydb/v1/projects/tenkacloud-local/locations/us-central1/clusters`.
  Set the client's `api_endpoint` to `/_simulator/providers/gcp/<api>/`.
  When several official bindings match one path, such as Cloud Logging's
  `{+parent}` aliases, the binding with the most literal segments (the
  `projects.*` method) serves it.
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
  restricts a patch to the reviewed mutable fields.
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
