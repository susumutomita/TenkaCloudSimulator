# Cloud DNS v1: bounded local scope and acceptance

This source contains a finite Cloud DNS control plane for [#343](https://github.com/susumutomita/TenkaCloudSimulator/issues/343).
It does not establish full Cloud DNS compatibility, official SDK compatibility,
a release, or completion of the service issue. Standard repository gates remain
pending. The [work record](../work/gcp-dns-acceptance-343.md) identifies tested
revisions; a later document-only commit does not inherit an exact-HEAD receipt.

## Native operation scope

| Collection | Supported official operations |
| --- | --- |
| Managed zones | `dns.managedZones.create`, `dns.managedZones.get`, `dns.managedZones.list`, `dns.managedZones.delete` |
| Changes | `dns.changes.create`, `dns.changes.get`, `dns.changes.list` |
| RRsets | `dns.resourceRecordSets.get`, `dns.resourceRecordSets.list` |

Bindings follow the static `dns.v1` discovery document in the pinned development
package `google-api-python-client==2.199.0`, revision `20260714`, original SHA256
`98e5e8539715e33c0c4fbd6bc44d6b76b3004196839f8610feba7d33783066c4`.
This is contract provenance, not a completed official-client test.
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

This is an intended API sequence, not evidence of a socket or SDK run in the
current environment. A proposed Python discovery client would explicitly set
its local provider root and static discovery, disable retries and restrict its
transport to the owned numeric-loopback origin. No DNS SDK runner is delivered
or verified here. Existing SDK dependency pins are in
[scripts/requirements-test.txt](../../scripts/requirements-test.txt).
Terraform with a future pinned HashiCorp Google provider, `dns_custom_endpoint`
and Changes-based record replacement remains an unverified future acceptance
candidate; no provider version, execution or compatibility is claimed.

## Finite limits and explicit refusals

Only public metadata zones are supported. Mutable record types are A, AAAA,
CNAME, NS, PTR, MX and a restricted plain quoted TXT form. Names are lowercase,
absolute ASCII domains within the zone. Apex NS/SOA are protected; apex CNAME
and CNAME coexistence are refused. DNSSEC, private/forwarding/peering zones,
routing policies, arbitrary record types and direct RRset writes are unsupported.
Unsupported methods return 501; unsupported fields/query options are rejected.
There is no DNS listener, name resolution, propagation or outbound cloud call.
`done` denotes the synchronous local change, with `isServing=false`.

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

## Existing evidence and remaining acceptance

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
The frozen [socket oracle](../../internal/server/gcp_dns_test.go) has not passed
socket execution. Neither recorder nor direct-leaf results substitute for it.

The [factory workflow](../contributing/software-factory.md) registers DNS and
SQS, not Azure. Full `make before-commit`, protocol/persistence `make e2e`,
whole-module race/coverage, hosted CI and DNS formal/model/mutation/applicability
work remain pending. No skip or exemption for DNS runtime code has been accepted;
see [formal policy](../../formal/README.md#required-gate-and-applicability).
No refused listener retry, SDK/E2E, Terraform, external asset acquisition or
full gate was performed for the documentation update.
