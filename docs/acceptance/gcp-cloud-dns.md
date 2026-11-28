# Cloud DNS v1: bounded local scope and acceptance

This source contains a finite Cloud DNS control plane for [#343](https://github.com/susumutomita/TenkaCloudSimulator/issues/343).
The [work record](../work/gcp-dns-acceptance-343.md) identifies the early
implementation revisions. The SDK check below exercises a public zone and an A
record against a locally built binary.

## Native operation scope

| Collection | Supported official operations |
| --- | --- |
| Managed zones | `dns.managedZones.create`, `dns.managedZones.get`, `dns.managedZones.list`, `dns.managedZones.delete` |
| Changes | `dns.changes.create`, `dns.changes.get`, `dns.changes.list` |
| RRsets | `dns.resourceRecordSets.get`, `dns.resourceRecordSets.list` |

Bindings follow the static `dns.v1` discovery document in the pinned development
package `google-api-python-client==2.199.0`, revision `20260714`, original SHA256
`98e5e8539715e33c0c4fbd6bc44d6b76b3004196839f8610feba7d33783066c4`.
See Google's [changes contract](https://docs.cloud.google.com/dns/docs/reference/rest/v1/changes)
and [error definitions](https://docs.cloud.google.com/dns/docs/error-messages).

Use the local provider root `http://127.0.0.1:<port>/_simulator/providers/gcp/`.
Native paths begin `dns/v1/projects/{project}/managedZones`; do not double the
`dns` segment. The default project is `tenkacloud-local`, and the default owner
is `simulator-admin`. The host's configured project/owner and authenticated
`provider.Actor.ExerciseIdentity` determine access. Caller headers do not grant
project ownership. This is local owner-only authorization, not Google IAM.
Hosts allocate separate processes, SQLite databases and token authorities per team.
Use only disposable local access tokens and practice values, never cloud credentials.

Google's public-zone workflow starts with a project, an enabled Cloud DNS API
and permissions for the operation. In the Google Cloud Console, open Cloud DNS,
create a public zone, then open the zone to add or edit record sets. The local
Console follows that order. It uses a preconfigured project owner and stores
zone and record metadata in SQLite. Optional private networking, public
delegation and DNS resolution are outside this workflow.
[Zone guide](https://docs.cloud.google.com/dns/docs/zones) ·
[Record guide](https://docs.cloud.google.com/dns/docs/records) ·
[Google roles and permissions](https://docs.cloud.google.com/dns/docs/access-control)

## Conceptual local CRUD sequence

1. Start a local instance using the normal [startup guide](../start.md) and its
   local access token (`X-Simulator-Token`). Create a public zone with `name`,
   absolute lowercase `dnsName`, and `description`.
2. Get/list the zone and read its synthetic apex NS/SOA. Get accepts its name
   or returned decimal ID. These nameservers do not serve DNS.
3. Submit `changes.create` with RRset `additions`. Read the completed change,
   change history, and exact records through RRset get/list.
4. Replace a record by one change containing its exact old value in `deletions`
   and its new value in `additions`. Mismatching deletion data returns
   `412 preconditionFailed`. Verify the new value and absence of the old value.
5. Remove user RRsets with exact deletion changes before deleting the zone.
   Nonempty deletion is refused. Check absence after deletion; a recreated
   zone has a fresh ID and does not inherit prior receipts or page tokens.

## Run the discovery-client acceptance check

From the repository root, build the simulator and use a Python environment
with [the pinned test dependencies](../../scripts/requirements-test.txt).

```sh
make build
python test/e2e/test_gcp_dns_sdk.py
python test/e2e/gcp_dns_sdk.py
```

The first command after the build checks the request paths produced by the
installed discovery client without sending them. Set the client's
`api_endpoint` to the provider root with its trailing slash. The static DNS
discovery document adds `dns/v1/` to each method path.

The [SDK runner](../../test/e2e/gcp_dns_sdk.py) starts a fresh simulator and
uses its local token and numeric-loopback endpoint. It creates a public zone
and an A record, reads the committed change and record, checks that another
principal receives HTTP 403 and that the owner still sees the same data, then
deletes the record and zone and checks that the zone list is empty. Set
`SIMULATOR_BINARY` to exercise a specific build. The runner uses static
discovery and turns off redirects and retries. Terraform provider compatibility
is tracked separately.

## List managed zones in the API reference

The served Cloud DNS reference exposes a read-only **List managed zones**
request. To verify it, set `SIMULATOR_BINARY` to the local build and
`CHROMIUM_EXECUTABLE` to the Playwright Chromium executable, then run
`python3 test/e2e/gcp_dns_scalar_send.py` in a loopback-only test environment
with Chromium sandbox support. The fixture creates a disposable zone, sends
one cookie-authenticated GET from Scalar, checks the zone in the JSON and
rendered response, and deletes it through the native endpoint.

## Finite limits and explicit refusals

Only public metadata zones are supported. Mutable record types are A, AAAA,
CNAME, NS, PTR, MX, SRV and a restricted plain quoted TXT form. Names are lowercase,
absolute ASCII domains within the zone. Apex NS/SOA are protected; apex CNAME
and CNAME coexistence are refused. DNSSEC, private/forwarding/peering zones,
routing policies, arbitrary record types and direct RRset writes are unsupported.
Unsupported methods return 501; unsupported fields/query options are rejected.
There is no DNS listener, name resolution, propagation or outbound cloud call.
`done` denotes the synchronous local change, with `isServing=false`.

SRV records use the same Changes and RRset operations with the
[RFC 2782](https://datatracker.ietf.org/doc/html/rfc2782) form
`priority weight port target`. Each numeric field is canonical unsigned decimal
from 0 to 65535, separated by a single space. The target is a lowercase absolute
DNS name or `.` for service unavailable. For example, an addition may contain
`{"name":"_submission._tcp.example.test.","type":"SRV","ttl":300,"rrdatas":["0 1 587 mail.example.test."]}`.
To replace or remove the record set, submit its exact previous TTL and values
in `deletions`. No target lookup, alias verification, endpoint selection or
DNS serving is performed. In the owner Console, open **Cloud DNS → Zones** and
select a zone. **Add SRV record** accepts one or more priority, weight, port
and target rows; **Edit** and **Delete** apply to the entire selected SRV
record set. Edits send one change with the previously displayed TTL and values
in `deletions`, not a delete followed by a create. The Console confirms an
exact change receipt and re-reads the zone before reporting success. If a
write is uncertain or the record has changed, refresh before trying again.
Legacy capability descriptors without `changes.create` keep record sets
read-only. This is local metadata; there are no Scalar write controls.

State is held in provider-owned SQLite tables. Final RRsets and complete receipt
sequence/documents are checked inside the transaction before commit; divergence
rolls back. Limits include 32 zones/project, 256 RRsets/zone, 64 entries/change,
32 rrdatas/RRset, 256 KiB request bodies, and page sizes 1..100. Normal change
history is capped at 128 entries, with finite deletion-only cleanup thereafter.
A database-wide weighted 2 MiB state budget reserves space for deletion receipts.
Up to 64 trusted project bindings are persisted; changing an existing owner is
refused. These are local budgets, not Google quotas.

Page tokens are bound to database authority, project, zone generation,
collection and filters. Traversal is offset-based and does not guarantee a
stable snapshot across concurrent changes. No cloud consistency or SQL/Go
formal refinement proof is claimed.

## Acceptance evidence

| Exact revision | Observed result |
| --- | --- |
| `5def7d7b025ad702c50af5755dfeeebc78e1a33a` | Leaf focused tests, scoped race/vet and leaf factory acceptance/verify passed; no composed-route proof from these checks |
| `ff7524eef4485cf3dd35ed5b5defbd600a18707c` source | Socket composition attempt blocked at listener creation (`bind: operation not permitted`); no HTTP result or socket route proof |
| `c639d09d5ea4c8f6c8fbbe31bdf8532f3cbf1124` | Recorder DNS creation reached production authentication/dispatcher and returned 404; authentication and existing GCP route controls passed |
| `2923e23be102d85245864c2fc77d7df88a8c9167` | Connected recorder suite, leaf focused tests and focused vet passed |
| `4e4185534fad64f58badf94ab0ff1e11e0832d02` | DNS-only preparation: leaf tests and recorder suite exited 0; read-only registry validation passed; 15 source blobs matched `2923e23`, only two factory files differed |

The [leaf oracle](../../internal/gcp/dns/factory_lifecycle_test.go) covers SQLite
rollback, restart, scoped tokens and exact data after retry/concurrent success.
The [handler oracle](../../internal/server/gcp_dns_handler_test.go) separately
covers production middleware/dispatcher composition through request/recorder.
The historical socket attempt in the table used the
[server composition test](../../internal/server/gcp_dns_test.go). Current
[factory checks](../contributing/software-factory.md) and
[formal validation](../../formal/README.md#required-gate-and-applicability) track
their own source revisions.

On `7ed6a01afce07140bac0c0553e52968b24c61bd5`, the pinned Google SDK, the
Console and the read-only Scalar Send all exercised a clean local build.
A separate two-process check created independent same-name zones, rejected a
peer token, read the saved records and change receipts after restart, and
removed both zones. The browser walkthrough covered keyboard operation and
SRV changes at 1365 and 390 CSS pixels. Participant requests returned 403
and a stale edit returned 412. Four pages at each width fit the viewport and
passed the exercised axe 4.11.0 WCAG A/AA rules. The walkthrough used the
local Console. The public Google guides above supply the workflow reference.
[Verification record](https://github.com/susumutomita/TenkaCloudSimulator/pull/621)
