# Sakura Disk configuration metadata

Sakura is **disabled by default**. This optional provider implements five local
IaaS API 1.1 Disk configuration operations. It does not allocate storage, create
VMs, attach disks, boot an OS, provide SSH/block I/O, or report physical
`Availability`. The local Scalar reference documents only these five Disk
operations. The original local configuration console uses the same five native
operations and configured owner. Neither the console nor this reference claims
authenticated real Sakura console fidelity.
Object Storage management and its separate S3-compatible API, Server, networking
and AppRun are unsupported; AWS CreateBucket is not Sakura Object Storage.

## Explicit startup

Build the normal binary with `make build`, then select all four trusted scope
values and dedicated team files:

```sh
./dist/tenkacloud-simulator --listen=127.0.0.1:4566 \
  --state=./sakura-team-a.db --token-file=./sakura-team-a.token \
  --enable-sakura-disks --sakura-environment=team-a \
  --sakura-project=practice --sakura-zone=is1a --sakura-owner=learner
```

The CLI validates scope before opening a listener or creating DB/token files.
Missing scope, scope flags without enablement, and `--unsafe-no-auth` with Sakura
are rejected. Existing default commands continue to start AWS/GCP/Azure only.
The token is a simulator-generated local operator credential, not a Sakura key.
Use the existing authenticated participant issuance flow for identity `learner`.

Hosts must allocate **separate processes, DBs and token authorities per team**, and
choose distinct trusted environments. A DB permanently binds environment,
project, zone and owner; a different configured tuple is refused, including after
restart. Identical tuples pointed at the same DB cannot distinguish separate
host token authorities; this is not a DB lease or an additional tenancy boundary.
Do not share the operator token with participants or put it in logs/Git.

## Native HTTP contract

The mounted endpoint in this example is:

`/_simulator/providers/sakura/cloud/zone/is1a/api/cloud/1.1/disk`

| Operation | Method and suffix | Local behavior |
|---|---|---|
| List | `GET /disk` | `From`, `Count`, `Total`, `Disks`; count omitted/0 means the remaining rows up to100 |
| Create | `POST /disk` | `Disk` metadata saved atomically, HTTP201 and a fresh decimal-string ID |
| Read | `GET /disk/{id}` | Exact saved metadata, HTTP404 if absent |
| Update | `PUT /disk/{id}` | Name/description update, omitted values preserved, empty description clears |
| Delete | `DELETE /disk/{id}` | Remove the actual owned local row; retired numeric IDs are never reused |

List parameters use a single JSON query object, raw or once URL-encoded, for
example `?%7B%22From%22%3A0%2C%22Count%22%3A1%7D`. Form query strings and extra
filters/sort are rejected. IDs and syntax are validated rather than normalized.
A minimal create body is:

```json
{"Disk":{"Name":"practice","Plan":{"ID":4},"SizeMB":20480}}
```

The host authenticates the opaque simulator token. The factory reads the typed
`provider.Actor` context and compares **ExerciseIdentity** with the configured
owner; neither operator credentials nor caller headers bypass that check. Missing
or wrong actors are HTTP403. The fixed public Basic marker
`TCSIMSAKURA:dummy-local-only` is a protocol marker, **not authorization**;
missing/wrong markers after a valid actor are HTTP401. Real Sakura credentials
are neither needed nor accepted as that marker. Trusted `server.NewWithProviders`
embedding still requires `server.NewAccess`; embedding alone does not create an
owner actor. Raw environment/project/zone headers cannot retarget this provider.

Malformed/ambiguous input is HTTP400, an unknown or unsupported option is501,
a missing resource/wrong URL zone is404, quota exhaustion is406, and saved-scope,
corruption or storage faults are sanitized500. These bounded local errors are not
claimed to match every real-service error. Duplicate/case-conflicting JSON fields,
unsupported options and failed writes leave the saved resource/allocator frame
unchanged. Private generations and fixed ownership fields are not returned.

Limits are100 active disks, exactly20480 logical MB, Plan ID4 and virtio only;
local names are1–64 Unicode runes and descriptions0–512, without control
characters. Requests are limited to64KiB and queries to8KiB. No huge backing file
is created. Neutral empty Tags/Icon/KMS/placement markers do not implement those
features; see the exact [community SDK no-icon subset](sakura-sdk-compatibility.md).
Nonempty tags, real icons/KMS, disk copies, archives, resize and attachment remain
unsupported.

These are deliberate local deviations from the broader public API. The primary
Disk reference includes other plans, optional source/copy and server-related
fields, Availability, and an update example containing Description null. This
local slice requires Plan4/SizeMB20480 for empty metadata creation, rejects null
descriptions, and omits physical/readiness fields. No live-cloud differential
comparison is implied by using the same endpoint names.

## API reference examples

The **Sakura Cloud → Sakura Disk configuration** group exposes exactly five
native operations on the collection and ID paths. The OpenAPI 3.1 schemas contain
configuration metadata and local error envelopes; they do not advertise VM or
storage readiness. The group also explains default-disabled availability.

Sign into the local reference first, or supply the host-issued
`X-Simulator-Token`. For its Basic Auth fields use only public username
`TCSIMSAKURA` and password `dummy-local-only`. This Basic marker is not host
authentication, owner authorization, real Sakura IAM or a place for cloud keys.
An operator must select the configured exercise identity; a participant stays
bound to its own identity. Examples containing `is1a` or Disk ID`1` are static:
use the configured zone and the IDs returned by actual Create/List requests.

The standard OpenAPI query builder cannot encode the unnamed JSON query syntax.
List without a query works; for paging, manually append the once-encoded JSON
suffix shown above. The reference deliberately does not invent `?From=…&Count=…`
parameters. GET/DELETE require no body and do not accept extra query fields.
The structural schemas cannot express every lexical JSON/UTF8/duplicate-member
rule enforced by the native parser; their descriptions retain those limits.
Host401 JSON, participant-selection403 text, and disabled-provider404 text differ
from the enabled leaf's local JSON error envelope. Those distinctions are kept
rather than presenting an AWS/S3 fallback or a fabricated common error.

Reference schemas/examples have static regression checks. Served Scalar Send,
actual SDK effects, private host token/session behavior and full runtime gates
remain separate acceptance evidence; selecting a static example is not a request
and does not demonstrate a saved resource.

## Verification scope

Before this main integration, the owned source checkpoints exercised21 Disk
normal/race tests; factory6 and flags4; server3 and CLI2; an actual native HTTP
probe over two standalone processes with27 request/response pairs; and the pinned
community SDK over two standalone endpoints with9 SDK plus4 native requests per
endpoint. SDK cleanup and peer preservation were checked against actual SQLite.
Those historical receipts are not a current-main global gate pass.

The native two-CLI probe is now a mandatory Conformance wrapper. The community SDK
fixture remains an isolated development module outside runtime dependencies and
is **not yet a mandatory hosted SDK gate**. It verifies the real serializer and
response decoder through a terminal loopback-only middleware, not stock SDK
credentials, default endpoint rewriting, retries, waiters or real-cloud behavior.
The initial real SDK501 for its no-icon default was preserved before the narrow
compatibility fix. No console materials or real cloud resources were used.

The current registry has40 baselines and286 controls (326 executions), preserving
main36/234. SakuraDiskMetadata/SakuraDiskAdmission add22 controls,
SakuraStartupAdmission adds13 and SakuraDisksViewEpoch adds17. All four scoped
baselines and52 controls passed separately; the full integrated326 cases remain
pending. Historical282-case source registrations are not current-revision proof.
The UI model retains14 properties and the original15 faults; two targeted
publication controls supplement the broad read-guard faults. Its first wrong
expected invariant is retained, then all17 named controls produced real traces.
Metadata models abstract atomic resource/allocator/binding/peer frames. Admission
models validated common Actor mapping, fixed configured realm, dummy-marker and
request/lookup ordering. The Startup model separately checks abstract parsed presence/validity, explicit
opt-in/authenticated-mode admission before effects, and the successful optional
construction/registration sequence. Its initial TLC baseline stopped with an
enumeration error before states; the same324-input finite domain was rewritten
as explicit membership. Corrected source passed independent SANY, baseline and
all13 named controls; integrated326-case execution is still required. The models do
not prove private context keys, host token cryptography, concrete Go flag/regex/
UTF8 parsing, exact JSON neutral markers, full Registry composition, later startup
failures or cleanup, constructor atomicity, SQLite/locking/crashes, DB leases,
VM/storage reality, actual100 quota or Go refinement. Native tests separately cover those implemented boundaries;
unimplemented behavior does not become supported through a model registration.

Public primary API references: [Disk API1.1](https://manual.sakura.ad.jp/cloud-api/1.1/disk/)
and [documented startup-script tuple](https://manual.sakura.ad.jp/cloud/startup-script/about.html).
The compatibility client is the Apache-2.0 community
[`sacloud-sdk-go` pinned source](https://github.com/sacloud/sacloud-sdk-go/tree/d8e98d8762e049e3ffd40bc70667cf9ad20029c0),
not an official-service compatibility certification or a runtime engine.

## Local Disk console

After the provider is explicitly enabled, sign in with a host-issued participant
credential whose trusted exercise identity matches `--sakura-owner`, then choose
**Sakura Cloud → Disks**. The view lists real saved configurations and supports
Create, name/description changes and confirmed Delete. The console shares the
native API's SQLite authority, fixed project/environment/zone and quotas. It does
not offer an owner selector, physical storage, attachment or VM operations.

The separate UI acceptance ran native HTTP against two isolated local CLI/DB/token
authorities at1440 and390 widths, including same-mount locale draft preservation,
real read-after-ACK failure/retry and consumed older-body rejection. Both widths
had zero serious/critical axe findings and no external requests. These focused
receipts do not replace current combined Go/E2E/formal/hosted verification.


Current mandatory acceptance inventory: **57 Conformance cases**, preserving all53
incoming main cases and adding the Sakura CLI, console, static-reference and
actual served-reference/Scalar flows. The live reference wrapper passes
`--browser` explicitly; native examples alone do not substitute for Send.
Full combined57-case execution at this merged source is still pending.

The UI model’s exact generated `app.js` binding is limited to the separately
modeled Sakura child/mount inputs. The canonical22-package build and asset
digests bridge that artifact; finite checking does not prove a JavaScript
compiler, CSS, neutral shell/catalog or unrelated cloud services.
