# Local API reference navigation

Open `/_simulator/docs/` after signing in to the local simulator. The reference
uses the embedded Scalar1.76.4 renderer and OpenAPI3.1 document; it does not load
remote fonts, scripts, proxy services or cloud APIs. Session cookies remain
same-origin, authentication persistence is disabled, and curl/SDK examples still
require a host-issued simulator token. Dummy AWS wire credentials are not login
credentials. Never enter real cloud credentials.

The navigation groups reference material under **AWS**, **Google Cloud**, **Azure**,
**Sakura Cloud**, and a separate **Simulator** control section. These are OpenAPI
`x-tagGroups`; they do not register providers, grant permission, create endpoints
or change SDK semantics. Existing paths, operation IDs, operation tags, headers,
schemas, statuses and static request/response examples are preserved by closed extension checks. The Metrics addition below extends only the existing shared AWS operation.

## AWS services and the shared endpoint

S3, Lambda, native IAM and API Gateway retain their native operation sections.
Systems Manager, CloudFormation, KMS, SQS, CloudWatch Logs, Secrets Manager and
DynamoDB also have short **documentation-only service guides**. These explain
which existing shared root POST template and routing headers to use. They are
not separate API operations or proof of additional service coverage.

The stable internal tag `KMS local symmetric keys` now displays as **Shared AWS
JSON/Query endpoint**, accurately reflecting its existing
`awsRegionalOperation`. Its underlying tag name and operation remain intact.
JSON1.0, JSON1.1 and URL-encoded Query retain their different Content-Type,
X-Amz-Target/Action and Version requirements. Selecting a named body example does
not automatically update those routing fields.

**Examples are static samples, not saved state.** Test Request → Send reads or
mutates the actual local state according to the selected operation. The existing
live S3 response explanation remains available and distinguishes executed native
XML/bytes from static examples. Inspect actual returned IDs, QueueUrls,
ReceiptHandles and ciphertext before reusing them; placeholders are not real
resources.

## CloudWatch Metrics forms

The **CloudWatch Metrics** section documents three native URL-encoded Query
actions on the existing root POST: `PutMetricData`, `ListMetrics` and
`GetMetricStatistics`, all with `Version=2010-08-01`. Set
`X-Simulator-Service: monitoring`, choose a supported Region and clear stale
`X-Amz-Target` or another service's Authorization value. Selecting a body example
does not update these headers or grant IAM permission. The actual operation still
requires a local session/token and the corresponding `cloudwatch:*` action.

Three named form examples and XML result/error examples are static, never the
current stored metric. Replace the statistics time-window placeholders before
Send. Reads share the same local state as the native client and console. See the
[bounded Metrics contract, steps, limits and acceptance status](cloudwatch-metrics.md).
This reference change does not claim that the currently blocked strict Scalar
accessibility flow passes.

## Other clouds and unavailable services

GCP and Azure retain their separately implemented provider paths and synthetic
host-configured project/account. Their authorization is the local owner-binding
subset, not Google IAM or Azure RBAC. Read the provider limits; a visible
reference entry does not establish full official SDK or authenticated console
parity.

Sakura exposes only the opt-in **Disk configuration** subset: five native API1.1
operations on the configured zone, with no VM or block-storage effects. The
reference remains visible when the provider is disabled; actual requests then
fail rather than create a provider. Local host token/cookie authority and the
configured trusted owner are separate from the required dummy Basic protocol
marker. Never enter a real Sakura key. See [the exact supported fields, query
format and authentication boundaries](sakura.md).

## Verification scope

Issue [#421](https://github.com/susumutomita/TenkaCloudSimulator/issues/421)
tracks the original navigation increment. The current detector requires66
operation IDs and36 paths, preserving the original61 operation IDs/34 paths and
adding only the five Sakura operations/two paths. Metrics keeps those 66/36 identities and adds three schemas to the root POST, for 121 schemas. Its exact inverse must recover the complete pinned public baseline; there is no prefix-based exclusion or changed old-field allowance. Static tests preserve all old
schemas, examples and operation assignments. The original `--baseline` comparison
still rejects a native-surface change; the approved Sakura addition has its own
strict original-projection preservation checks, not an ignored difference.
Negative fixtures exercise missing/duplicate/wrong-cloud groups and changed
native fields. The preexisting asset-validation prefix remains byte-exact after
normalizing only the reviewed five-ID closed-set extension.

Existing browser helpers exercise real Scalar AWS/GCP/Azure responses after
native writes to the same local state, separately from static examples. Current
Sakura live Send and full combined acceptance are integration checks; source
presence or a passing schema test is not an executed-request result. Neither the
renderer nor the tests claim full SDK or authenticated cloud-console parity.
