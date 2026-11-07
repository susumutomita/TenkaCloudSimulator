# Azure Resource Manager catalog

The Azure Resource Manager (ARM) catalog serves bounded configuration metadata
for Azure resource providers through the official ARM REST paths. Contracts come
from documentation-free subsets of the stable swagger files at commit
`8254fa29b6360b48084554122a7c49f6c4237d84` of
[github.com/Azure/azure-rest-api-specs](https://github.com/Azure/azure-rest-api-specs)
(MIT). For each provider the api-version is the default of the pinned
`azure-mgmt-*` package in `scripts/requirements-test.txt`. The extraction tool is
`tools/azuremodels/extract.py`; the reviewed operations are
`internal/azure/arm/selection.json`. The original license ships as
[`third_party/AZURE-REST-API-SPECS-NOTICES.txt`](../third_party/AZURE-REST-API-SPECS-NOTICES.txt)
and is served at `/_simulator/licenses/AZURE-REST-API-SPECS-NOTICES.txt`.

Every listed operation is implemented against provider-owned SQLite state. Every
other official operation of the same api-version returns HTTP 501
`NotImplemented`. Request members outside the official definition are refused
with `InvalidRequestContent`. No workload, data plane, network, secret, Entra ID
token or real Azure call is executed or simulated. Issue #316 indexes the
product plans; this catalog is the P0 engine and its first products, not
the completion of #316.

## Products

| Product | Namespace and api-version | Bound operations | Bounds |
| --- | --- | --- | --- |
| Resource groups | `Microsoft.Resources` 2025-04-01 | `ResourceGroups_{CreateOrUpdate,Get,CheckExistence,Update,Delete,List}` | One synthetic subscription. Deleting a group synchronously deletes its catalog resources unless a resource outside it still references one. `forceDeletionTypes`, `$filter`, export, move and the generic `Resources_*` operations are refused or 501 |
| Key Vault | `Microsoft.KeyVault` 2026-05-15 | `Vaults_{CreateOrUpdate,Get,Update,Delete,ListByResourceGroup,ListBySubscription}` | No vault data plane: no keys, secrets, certificates or `vaultUri` endpoint. `tenantId` and access-policy tenants must be the simulator tenant. `createMode` `recover` is refused; deletion is immediate (no soft delete or purge) |
| Virtual Network | `Microsoft.Network` 2026-03-01 | `VirtualNetworks_{CreateOrUpdate,Get,UpdateTags,Delete,List,ListAll}`, `Subnets_{CreateOrUpdate,Get,Delete,List}`, `NetworkSecurityGroups_{CreateOrUpdate,Get,UpdateTags,Delete,List,ListAll}`, `PublicIPAddresses_{CreateOrUpdate,Get,UpdateTags,Delete,List,ListAll}` | Address metadata only: nothing is routed or filtered and no public address is allocated (`ipAddress` stays absent). Valid non-overlapping CIDRs; subnets inside the address space and not overlapping. Subnets are child resources and are not embedded in the virtual network; security rules are inline with unique names and priorities. A group referenced by a subnet cannot be deleted. Peering, gateways, NAT, route tables, DDoS plans and `$expand` are refused or 501 |
| Managed identities | `Microsoft.ManagedIdentity` 2024-11-30 | `UserAssignedIdentities_{CreateOrUpdate,Get,Update,Delete,ListByResourceGroup,ListBySubscription}` | Generated `principalId` and `clientId`; no Entra service principal, token or credential. Other catalog resources may assign them through `identity.userAssignedIdentities`; deleting an assigned identity is allowed, as in Azure |
| Log Analytics | `Microsoft.OperationalInsights` 2025-07-01 | `Workspaces_{CreateOrUpdate,Get,Update,Delete,ListByResourceGroup,List}` | No ingestion, query, table, agent or shared key. Retention 30–730 days. Deletion is immediate (`force` changes nothing) |
| App Configuration | `Microsoft.AppConfiguration` 2024-06-01 | `ConfigurationStores_{Create,Get,Update,Delete,ListByResourceGroup,List}` | No key-value data plane, endpoint, access key or connection string. `Recover` and customer-managed keys are refused |
| Container Registry | `Microsoft.ContainerRegistry` 2025-11-01 | `Registries_{Create,Get,Update,Delete,ListByResourceGroup,List}` | No image, repository, login server, token or admin credential, even with `adminUserEnabled`. Classic SKU and non-Premium network rules are refused |
| Event Hubs | `Microsoft.EventHub` 2026-01-01 | `Namespaces_{CreateOrUpdate,Get,Update,Delete,ListByResourceGroup,List}`, `EventHubs_{CreateOrUpdate,Get,Delete,ListByNamespace}` | No endpoint, event, consumer group, authorization rule, SAS key or connection string. Generated partition IDs; 1–32 partitions that cannot decrease. Capture, geo-replication and customer-managed keys are refused |
| Service Bus | `Microsoft.ServiceBus` 2026-01-01 | `Namespaces_{CreateOrUpdate,Get,Update,Delete,ListByResourceGroup,List}`, `Queues_{CreateOrUpdate,Get,Delete,ListByNamespace}` | No endpoint, message, session, topic, authorization rule, SAS key or connection string; message counts stay 0. Forwarding must name another queue in the namespace; partitioning cannot change |
| Azure Cosmos DB | `Microsoft.DocumentDB` 2026-03-15 | `DatabaseAccounts_{CreateOrUpdate,Get,Update,Delete,ListByResourceGroup,List}` | No database engine, endpoint, database, container, key or connection string. Distinct simulator regions with failover priorities 0..n-1. `Restore`, restore parameters and customer-managed keys are refused |
| App Service | `Microsoft.Web` 2025-05-01 | `AppServicePlans_{CreateOrUpdate,Get,Update,Delete,ListByResourceGroup,List}` | Plan metadata only: no worker, site or deployment; `numberOfSites` stays 0. The official plan PATCH has no tags. App Service Environments, Kubernetes environments, registry adapters, storage mounts and install scripts are refused |

The capability document lists the same providers under `azureCatalog` as compact
summaries (namespace, api-version, title, issue, operation counts and types).
`GET /_simulator/v1/catalog/azure` serves the full descriptors: every bound
operation with its official path, query parameters, list item and nextLink
names, console form fields, defaults, references, bounds and source files.

## Routing and authorization

- SDK clients set `base_url` to `<endpoint>/_simulator/providers/azure-arm` and send
  the official `/subscriptions/...` paths. The registered provider is `azure-arm`;
  Blob Storage remains `azure`.
- The subscription is `00000000-0000-4000-8000-000000000001` and the tenant
  `00000000-0000-4000-8000-000000000000`, fixed per environment. Their binding to
  the environment and owner is persisted; a different owner on the same database
  is refused at startup.
- Only the configured Azure owner (`--cloud-storage-owner`, default
  `simulator-admin`) may call an operation. Any other subject or subscription
  receives 403 `AuthorizationFailed`, and the response does not reveal whether the
  resource exists. This is local owner authority, not Azure RBAC or Entra ID, and
  AWS identity policies grant nothing. A missing access token returns 401
  `AuthenticationFailed` in the ARM error envelope.
- `api-version` is required and must equal the provider's reviewed version
  (`MissingApiVersionParameter`, `InvalidApiVersionParameter`). Generic official
  routes without a namespace literal, such as `Resources_GetById`, apply only to
  requests that carry their own api-version.
- Literal path segments and resource names are case-insensitive, as in ARM. A
  resource keeps the casing it was created with.
- Supported regions are `eastus`, `eastus2`, `westus2`, `westeurope`,
  `northeurope`, `japaneast`, `japanwest` and `southeastasia`; display names such
  as `Japan East` normalize.

## Request semantics

- Create-or-update returns 201 on create (or 200 when the operation declares no
  201) and 200 on update. Long-running operations complete synchronously with
  `properties.provisioningState` `Succeeded`, so `azure-mgmt` pollers finish
  without polling. Delete returns 200, 204 or 202 as the operation declares; a
  missing resource returns 204 where declared, otherwise 404.
- Bodies are validated against the official definitions: unknown members,
  wrong types, enum values (case-insensitive, stored canonically), patterns,
  lengths, ranges, formats, discriminators and required members. Read-only
  members are dropped, write-only members are never returned, create-only
  members cannot change (`PropertyChangeNotAllowed`), and responses are
  projected onto the official response definition. `x-ms-secret` and
  password-format members are refused and never stored; the official
  operations that return or accept keys remain unbound.
- PATCH merges objects member by member and replaces maps such as `tags`,
  arrays and scalars, then revalidates the result.
- Server members are generated: `id`, `name`, `type`, `systemData`, read-only
  `etag`, `provisioningState`, and per-type identifiers such as identity
  principals or Log Analytics `customerId`. System-assigned identities receive a
  principal in the simulator tenant; user-assigned identities must name live
  `Microsoft.ManagedIdentity` resources.
- Reviewed references must name live catalog resources
  (`InvalidResourceReference`). A blocking reference, such as a subnet's network
  security group, refuses deletion of its target with 409.
- Errors use the ARM envelope `{"error":{"code","message"}}`, for example
  `ResourceNotFound`, `ResourceGroupNotFound`, `ParentResourceNotFound`,
  `InvalidRequestContent`, `LocationNotAvailableForResourceType`, `Conflict` and
  `QuotaExceeded`.
- Lists page with absolute `nextLink` URLs carrying an opaque `$skiptoken` bound to
  the list scope; `$top` and `$skip` are honoured where the operation declares
  them. Pages default to 100 items.

## Limits

Requests are limited to 256 KiB and 32 nesting levels or 4,096 JSON nodes, stored
resource documents to 64 KiB, all ARM catalog metadata to 32 MiB and 10,000
resources. Each type also has a per-parent limit, such as 100 resource groups or
25 key vaults per group. Names that are DNS-global in Azure (key vaults,
registries, App Configuration stores, messaging namespaces, Cosmos DB accounts)
are unique within the environment. These are local limits, not Azure quotas.

## Console and API reference

Choose **Azure → Services → Resource Manager**. The Azure-styled view lists
resource groups and every reviewed type, selects the resource group and parent,
creates resources from the official form fields, shows the stored JSON, and
deletes with an inline confirmation. It uses the same native ARM requests and
descriptors. At 390 px the type menu collapses behind a disclosure button.
Complex official members remain available through SDKs and the API reference.

The [Azure catalog API reference](/_simulator/docs/azure-catalog.html) serves one
OpenAPI 3.1 document per provider, generated from the same embedded subsets.

## Design

ARM resources are addressed by hierarchical IDs, versioned per provider and
described by swagger definitions, so the engine binds reviewed operations to
roles (create-or-update, get, head, patch, delete, list) on resource types and
derives behaviour from the official paths and definitions. Two alternatives were
considered:

1. A hand-written handler per resource type. It gives full control but repeats
   routing, validation, paging and persistence for every product of #316.
2. A schema-less generic ARM store that accepts any provider, type and body. It
   would invent operations and silently accept unsupported members, which the
   working contract forbids.

The chosen model-driven engine keeps official contracts and explicit 501
refusals while adding products through reviewed specifications and small
per-type hooks for semantics the definitions cannot express (address spaces,
references, tenants, partitions).

## Verification

- `go test ./internal/azure/arm` covers every product hook, routing and
  api-version rule, decoder rule, reference, identity, paging, budget, storage
  fault and fail-closed specification check, plus the regenerated reference,
  selection and notice mirror.
- `test/e2e/azure_catalog_fixtures.py` drives every bound operation through the
  pinned `azure-mgmt-*` SDKs against two binaries with separate databases and
  token authorities. A local azure-core policy adds `X-Simulator-Token` and
  refuses non-loopback origins; the credential is a dummy. The response header
  `X-Simulator-Azure-Operation` proves which official operation each call
  reached. Another subject, a wrong api-version and an unbound operation are
  refused, a second team stays empty, state survives restart and cleanup
  removes everything.
- `test/e2e/azure_catalog_console.py` checks the console and the served reference
  in Chromium at 1440 px and 390 px with axe, and confirms the SDK sees
  console-made state.

Unverified: authenticated Azure portal parity, live Azure differential
behaviour, Azure RBAC, Azure Policy, real quotas, asynchronous LRO polling paths
(`Azure-AsyncOperation`/`Location`), other SDK languages and every unlisted
official operation. The formal models do not yet declare these sources in scope.

## Adding a provider

1. Add a reviewed specification in `internal/azure/arm/providers_*.go`, then run
   `CATALOG_WRITE_SELECTION=1 go test ./internal/azure/arm -run TestSelection`
   and add the swagger files of the api-version to `selection.json`.
2. Check out the pinned commit (sparse, blobless) outside the repository and run
   `python -I tools/azuremodels/extract.py <checkout>`.
3. Regenerate the reference with
   `CATALOG_WRITE_DOCS=1 go test ./internal/azure/arm -run TestServedReference`.
4. Add native tests, an SDK fixture under `test/e2e/azure_catalog_fixtures/`, pin
   the SDK in `scripts/requirements-test.txt` and document the bounds here.
