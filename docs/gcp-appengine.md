# App Engine

The Google Cloud provider serves a bounded native subset of the
**App Engine Admin API v1** and a local data plane that runs deployed
**Python 3.12 standard-environment** versions in the embedded CPython/WASI
sandbox. Admin requests, the console and official clients use the same HTTP
paths and the same SQLite state. No Google account, real App Engine, network
access or billing is involved, and this is not Google IAM.

Contracts come from the documentation-free subset of the `appengine.v1`
discovery document (revision 20260717) that google-api-python-client 2.199.0
ships. `tools/gcpmodels/appengine.py` extracts it into
`internal/gcp/appengine/model.json`; its license is in
[`third_party/GOOGLE-DISCOVERY-NOTICES.txt`](../third_party/GOOGLE-DISCOVERY-NOTICES.txt).

## Try it

With the operator token from [Start and sign in](../README.md#start-and-sign-in):

1. Save this as `main.py` and put it at the root of a ZIP file:

   ```python
   import os

   def app(environ, start_response):
       start_response("200 OK", [("Content-Type", "text/plain; charset=utf-8")])
       return [("Hello from %s/%s" % (os.environ["GAE_SERVICE"], os.environ["GAE_VERSION"])).encode()]
   ```

2. Upload the ZIP to a Cloud Storage bucket of the configured project
   (`tenkacloud-local` by default), with the console or the JSON API.
3. Open **Google Cloud → App Engine**, create the application in a location,
   and deploy version `v1` of service `default` from
   `https://storage.googleapis.com/<bucket>/<object>`.
4. Choose **Open** for the version. The page shows `Hello from default/v1`.

The same steps with google-api-python-client use
`build('appengine', 'v1', static_discovery=True, client_options={'api_endpoint': '<origin>/_simulator/providers/gcp/appengine/'})`
and a transport that adds `X-Simulator-Token`, as in
[`test/e2e/gcp_appengine_sdk.py`](../test/e2e/gcp_appengine_sdk.py).

## Admin API

Requests use `/_simulator/providers/gcp/appengine/v1/...`, the official paths
under that endpoint. The capability document advertises the subset under
`gcpAppEngine`, and the [API reference](/_simulator/docs/gcp-appengine.html)
serves an OpenAPI document generated from the same embedded subset.

| Resource | Methods | Local behavior |
| --- | --- | --- |
| Application `apps/{project}` | `create`, `get`, `patch` | One application per project, with `id` equal to the project ID and `locationId` (default `us-central`). It cannot be created twice (`ALREADY_EXISTS`) and, as in App Engine, there is no delete method. `patch` changes only `servingStatus` (`SERVING` or `USER_DISABLED`). `defaultHostname` is `PROJECT.REGION_ID.r.appspot.com` |
| Locations `apps/{project}/locations` | `list`, `get` | `asia-northeast1` (`an`), `europe-west` (`ew`), `us-central` (`uc`) and `us-east1` (`ue`); standard environment only |
| Services `apps/{project}/services` | `list`, `get`, `patch`, `delete` | Created by their first version, which receives all traffic. The first service must be `default`, and `default` cannot be deleted. `patch` changes `split` only: `shardBy` `IP` (or unspecified), `COOKIE` or `RANDOM`, allocations of existing versions in (0, 1] that add up to 1, with at most two decimals for IP and three otherwise. `migrateTraffic=true` is refused. Deleting a service deletes its versions |
| Versions `.../services/{service}/versions` | `create`, `list`, `get`, `patch`, `delete` | See [Deploying a version](#deploying-a-version). `view=FULL` adds `deployment` and `envVariables`. `patch` changes `servingStatus` (`SERVING` or `STOPPED`) only. A version that receives traffic cannot be deleted (`FAILED_PRECONDITION`), so a service keeps at least one version |
| Firewall `apps/{project}/firewall/ingressRules` | `create`, `list`, `get`, `patch`, `delete`, `batchUpdate` | Rules have a priority from 1 through 2147483646, `ALLOW` or `DENY`, a `sourceRange` (`*`, an IP address or CIDR) and a description of at most 400 characters. The default rule (priority 2147483647, `*`) cannot be deleted; only its action changes. `list` accepts `matchingAddress`. `batchUpdate` replaces every rule and resets the default action to `ALLOW` unless the request includes the default rule |
| Operations `apps/{project}/operations` | `list`, `get` | Application, service and version mutations return a completed `google.longrunning.Operation` with `OperationMetadataV1`. The newest 1,000 stay readable |

- The other 37 official methods, including `apps.repair`, `listRuntimes`,
  instances, domain mappings, authorized domains and certificates,
  `exportAppImage` and the `projects.locations.applications` aliases, return
  HTTP 501 `UNIMPLEMENTED`.
- Only the configured project owner (`--cloud-storage-owner`, default
  `simulator-admin`) may call a method. Another subject or another project
  receives `PERMISSION_DENIED` before any existence check, and nothing changes.
- Unknown request fields and official fields outside this subset (for example
  `handlers`, `entrypoint`, `automaticScaling` or `authDomain`) are refused with
  `INVALID_ARGUMENT`. Output-only fields are ignored. Patch requests need an
  `updateMask`; official fields outside the mask are ignored.
- Lists page with `pageSize` (default 50, at most 1,000) and an opaque
  `pageToken`. `filter` is refused when set.
- Errors use `google.rpc.Status` with the canonical HTTP mapping.

## Deploying a version

`versions.create` copies Python sources from Cloud Storage objects of the
configured project, as App Engine copies deployment files. A later change or
deletion of the objects does not change the version.

- `id` follows the official rule: lowercase letters, digits and hyphens;
  `default`, `latest` and the `ah-` prefix are reserved. Service and version
  IDs may not contain `-dot-`, which the hostnames use.
- `runtime` must be `python312` and `env` `standard` (or omitted).
- `deployment.zip.sourceUrl` names one ZIP object, or `deployment.files` maps
  relative paths to `sourceUrl` objects with an optional `sha1Sum` that must
  match. Source URLs use `https://storage.googleapis.com/<bucket>/<object>`.
  `container` and `cloudBuildOptions` are refused.
- Sources must be `.py` files with safe relative paths: at most 63 files,
  1 MiB expanded and 256 KiB compressed. `main.py` must define the WSGI callable
  `app`, the default `main:app` entrypoint. Static files, `requirements.txt`
  and third-party packages are not supported.
- `envVariables` holds at most 32 variables and 4 KiB. `GAE_*`, `X_GOOGLE_*`,
  `GOOGLE_CLOUD_PROJECT` and `PORT` are set by the runtime.
- Local budgets: 10 services per application, 20 versions per service and 50
  versions per application (`RESOURCE_EXHAUSTED`).

## Serving requests

Deployed versions answer at
`/_simulator/providers/gcp/appspot/<hostname>/<path>`, where `<hostname>` is an
official App Engine hostname of the configured project:

| Hostname | Target |
| --- | --- |
| `PROJECT.REGION_ID.r.appspot.com` or `PROJECT.appspot.com` | The `default` service, through its split |
| `SERVICE-dot-PROJECT...` | That service through its split, or else that version of `default` |
| `VERSION-dot-SERVICE-dot-PROJECT...` | That version, even without traffic |

1. The request needs an authenticated simulator session (token or console
   sign-in), the local equivalent of reaching the application. It does not need
   the admin owner.
2. An unknown hostname, service or version is `404`. There is no soft routing
   to the default service. A disabled application or a stopped version is `503`.
3. Firewall rules are evaluated in priority order against the client address of
   the TCP connection; the first match decides, and `DENY` is `403` before any
   code runs. Behind a proxy that address is the proxy's.
4. The split chooses a version: `IP` hashes the client address (stable per
   client), `COOKIE` reads the `GOOGAPPUID` cookie (0–999) and sets one when it is
   missing, and `RANDOM` draws a number.
5. The version runs once in a fresh sandbox: CPython 3.12/WASI with 128 MiB
   guest memory, no network, host filesystem or processes, and a 10 second
   deadline (`504`). Module state does not persist between requests. A raised
   exception is `500` with its type and message; an invalid WSGI response or a
   sandbox failure is `502`.
6. The application receives the method, decoded `PATH_INFO`, query string, body
   (at most 64 KiB) and headers (at most 32 KiB), with `SCRIPT_NAME` set to the
   local mount so WSGI frameworks build local links. Simulator tokens,
   `Authorization`, simulator cookies and hop-by-hop headers are removed.
7. The response keeps the status, body and only `Content-Type`,
   `Content-Language`, `Content-Disposition`, `ETag`, `Last-Modified` and `Vary`
   headers. `Set-Cookie`, `Location` and security headers are dropped. Every
   response carries `Content-Security-Policy: ... sandbox` without scripts, so
   deployed HTML never runs with the simulator origin's authority, and
   `X-Simulator-AppEngine-Version: SERVICE/VERSION` names the version that ran.

stdout and stderr are discarded; there is no Cloud Logging, request log or
instance resource.

## Dependencies and responsibilities

- **Cloud Storage (required):** deployment sources are read from the native
  [Cloud Storage provider](gcp-storage.md) of the same process and project.
  Buckets such as `staging.PROJECT.appspot.com` are not created automatically.
- **Project and IAM:** the project and its owner are fixed host configuration.
  There are no organizations, folders, API enablement, IAM roles or service
  accounts.
- **Not used:** Cloud Build, Artifact Registry, Cloud Logging, Firestore or
  Datastore, Identity-Aware Proxy and custom domains are not involved.

## Console

Open **Google Cloud → App Engine** after signing in. The view creates the
application, deploys versions from a Cloud Storage source URL, shows services,
versions and traffic, stops, starts and deletes versions, sends all traffic to
one version, manages firewall rules and opens the local version URL. It uses
the same admin API requests as SDKs.

## State, persistence and teams

State lives in provider-owned SQLite tables (`gcp_appengine_*`). Foreign keys
keep the graph ordered: a version that receives traffic, a service with
versions and an application with services or rules cannot be deleted out of
order. Writes run in one serializable transaction each, and state survives a
restart with `--state`. Each team needs its own process, database and token
authority; another team's binary holds none of this state. Because the
application cannot be deleted, start from a new database to remove it.

## Verification

- `go test ./internal/gcp/appengine` covers every reviewed method, refused
  fields and methods, owner-only authorization before existence, pagination,
  operations retention, deployment validation and copying, split arithmetic,
  deletion guards, budgets, concurrent creation, restart, firewall evaluation,
  hostname routing, split sharding, gateway errors, header filtering and one
  real WSGI run in the embedded interpreter. The served reference is checked
  against the embedded subset.
- `go test ./internal/server` deploys from the native Cloud Storage provider
  through the authenticated route, refuses another subject, serves after the
  source object is deleted and after a database reopen, and checks capabilities.
- `test/e2e/gcp_appengine_sdk.py` uses google-cloud-storage and
  google-api-python-client against two real binaries: zip and files deployments,
  real requests through hostnames, splits and the firewall, stopped and disabled
  states, malformed, unsupported, conflicting and missing requests, refusal of
  another subject and project without state change, an empty second team,
  restart and cleanup.
- `test/e2e/gcp_appengine_console.py` drives the console in Chromium at 1440 px
  and 390 px with axe and confirms that the discovery client sees the same state.
- The finite `AppEngineTraffic` TLA+ model checks that splits name only
  deployed versions and add up to the whole, that the default service comes
  first and stays, that the application is permanent, that refused callers and
  other teams are unchanged, and that served requests passed a route and the
  firewall. Thirteen mutations each break one of these properties. It does not
  model Go, SQL, HTTP, the sandbox, sharding or quotas and is not a proof of the
  implementation.

Unverified: parity with the authenticated Google Cloud console, real App Engine
routing details (soft routing, dispatch rules, instance URLs), scaling,
instances, warmup, request deadlines and quotas of real App Engine, and every
unlisted method.
