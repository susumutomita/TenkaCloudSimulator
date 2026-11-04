# Local multi-cloud storage candidate

This branch composes separately implemented Google Cloud Storage JSON API and Azure Blob Storage REST handlers with the existing local access boundary. Native composition, the two-CLI browser acceptance, full-module regression and both coverage thresholds pass. Immutable formal checks also pass for the recorded code input; current exact-HEAD hosted results are tracked in Draft PR406. Official SDK execution remains unverified because its required packages are absent. This remains an unmerged development candidate, and its presence here is not evidence that all catalog problems or all cloud services work.

## Access and fixed scopes

The CLI still binds numeric loopback and requires a private local access token. `NewAccess` places a validated, immutable credential/exercise Actor in the request context. Provider adapters use its exercise identity and the host-configured environment, not arbitrary HTTP headers, the AWS policy table or an operator-role shortcut.

The top utility shows only the configured, read-only project or storage account; AWS region choices belong to the AWS views. Scope selection/provisioning remains unsupported.

Default synthetic scopes are Google project `tenkacloud-local`/number `100000000001`, Azure account `tenkacloudlocal`, environment `local`; their explicit local owner is `simulator-admin`. Other selected identities receive403. A trusted CLI host can select `--cloud-storage-owner=learner` when creating a fresh environment, then issue a participant token bound to `learner`. The flag validates a1–64 character ASCII exercise identity before opening a listener, database or token file. An existing persisted owner cannot be changed by reusing the same database with a different flag. The default owner remains `simulator-admin`.

A trusted Go host can use `server.NewWithCloudStorage(path, cfg)` to bind a participant identity as the local project/account owner; real participant credentials remain bound to that identity. This is owner-only local authorization, not Google IAM, Azure RBAC, OAuth, Entra, SharedKey or SAS. Persisted ownership cannot be silently reassigned on restart. A separate process, database and token authority per team remains necessary.

`server.New`/`NewWithCloudStorage` alone do not authenticate requests. Hosts must wrap `NewAccess`. Missing authenticated Actor context is denied by the new providers, even if a forged exercise header names the owner. The existing AWS library composition behavior remains separate.

## Native routes

- Google: `/_simulator/providers/gcp/storage/v1/b?project=tenkacloud-local`; metadata/object collection beneath `/b/{bucket}/o`, media upload beneath `/upload/storage/v1/b/{bucket}/o`, download beneath `/download/storage/v1/b/{bucket}/o/{object}?alt=media`.
- Azure: `/_simulator/providers/azure/tenkacloudlocal?comp=list`; container operations beneath `/{container}?restype=container`; blob operations beneath `/{container}/{blob}`. Every native request requires `x-ms-version: 2023-11-03`.

Both retain actual protocol responses and the same SQLite state as the new console views. Object keys are data: the provider registry preserves paired decoded/escaped paths, without cleaning or second decoding. Encoded provider identifiers are refused. UI controls explicitly refuse keys that a browser would normalize rather than changing the target silently.

Google supports eight logical bucket/object operations, media/multipart upload up to1MiB, checksum validation and generation conditions. Resumable uploads, ACLs and arbitrary project provisioning are unsupported. Only the current stored generation exists; stale explicitly addressed generations do not return newer bytes. Azure supports nine container/blob operations, BlockBlob uploads up to8MiB, ETag conditions and bounded single-range GET. Staged blocks, suffix/multiple ranges, account provisioning and cloud authorization are unsupported. Local container deletion synchronously cascades; it is not the cloud asynchronous lifecycle.

## Screens and reference

Services opens `Google Cloud · Cloud Storage` or `Azure · Blob Storage`. Each view exposes its fixed project/account, lists stored resources, performs create/upload/download/delete, and shows refusals. The console uses local APIs and embedded assets. Public primary guides informed the operation order. Authenticated Google/Azure screens were not inspected, and exact visual parity is not claimed. See [Google view](gcp-console.md) and [Azure view](azure-console.md).

Scalar includes provider-specific native request/response documentation. Samples remain static examples; Test Request→Send obtains live data. No response is converted to a fictitious cross-cloud format.

## Verification boundaries

[Official SDK acceptance](acceptance/crossprovider-official-storage.md) requires real official packages and refuses to pass when dependencies are missing. They are currently absent in this executor; no SDK execution or compatibility success is claimed. Existing same-lock Cloudscape/React build dependencies were reused without installing anything, and the unchanged main console reproduced byte-for-byte before adding the views.

Registry models are finite safety abstractions, not mathematical implementation proofs. ProviderActor covers trusted actor emission; GCPStorageState and AzureBlobState cover their explicitly documented state/authorization/atomicity abstractions. New HTTP routing, SDK serializers, UI and host composition require their own executable tests. The new factory/range/URI behavior is not proved merely by registering leaf models. New public merges remain on hold; no real cloud changes or deployment is performed.
