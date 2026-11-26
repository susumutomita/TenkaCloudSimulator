# Local API reference navigation

Open `/_simulator/docs/` after signing in to the local simulator. The reference
uses the embedded Scalar1.75.1 renderer and OpenAPI3.1 document; it does not load
remote fonts, scripts, proxy services or cloud APIs. Session cookies remain
same-origin, authentication persistence is disabled, and curl/SDK examples still
require a host-issued simulator token. Dummy AWS wire credentials are not login
credentials. Never enter real cloud credentials.

The navigation groups reference material under **AWS**, **Google Cloud**, **Azure**,
**Sakura Cloud**, and a separate **Simulator** control section. These are OpenAPI
`x-tagGroups`; they do not register providers, grant permission, create endpoints
or change SDK semantics. Existing paths, operation IDs, operation tags, headers,
schemas, statuses and static request/response examples are preserved.

## AWS services and the shared endpoint

S3, Lambda, native IAM and API Gateway retain their native operation sections.
Systems Manager, CloudFormation, KMS, SQS, CloudWatch Logs, Secrets Manager and
DynamoDB also have short **documentation-only service guides**. These explain
which existing shared root POST template and routing headers to use. They are
not separate API operations or proof of additional service coverage.

Native EC2 Query is documented in the
[EC2/VPC guide](ec2-read-discovery.md), including the
[explicit subnet route table association API](ec2-read-discovery.md#native-subnet-route-table-associations).
For read-only instance discovery, choose the shared root POST, select
`application/x-www-form-urlencoded` and its **EC2 DescribeInstances** example,
set `X-Simulator-Service: ec2`, clear `X-Amz-Target`, and choose a local region.
Send with the existing signed-in identity and `ec2:DescribeInstances` on `*`.
The XML lists saved metadata (possibly empty), not running-VM evidence. No EC2
launch, mutation, filter or pagination is provided by this preset.

The **EC2 DescribeInternetGateways** example on the same shared Query endpoint
uses the same EC2 headers and requires `ec2:DescribeInternetGateways` on `*`.
It reads the first page of regional [saved internet gateway configuration](ec2-internet-gateways.md)
with `MaxResults=100`, including recorded VPC attachments from CloudFormation.
Use the returned `nextToken` as `NextToken` to continue in the same scope. It
does not create, attach or route through a gateway.

The **EC2 DescribeRouteTables** example requires `ec2:DescribeRouteTables` on
`*`. It reads the first page of the same [saved route tables](ec2-read-discovery.md#route-table-readback)
with `MaxResults=100`. For the next page, copy XML `nextToken` into `NextToken`
with the same authenticated local caller, region and query. For single-table readback, remove
`MaxResults` and `NextToken` before entering one `RouteTableId.1`. The reference
does not change a table or test routes.

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

## AWS JSON response display

The live response body displays `application/x-amz-json-1.0` and
`application/x-amz-json-1.1` as JSON text, including SSM GetParameter results.
The original Content-Type, status and API bytes remain unchanged. This uses the
embedded Scalar response-body extension, without replacing the native response
or treating every unknown MIME type as JSON. For valid UTF-8 AWS JSON, text is
not parsed and serialized again, so large numeric literals and whitespace are
preserved in the display and download. HTML-like values
are shown as text in Scalar's read-only code editor. Binary response downloads
retain their existing behavior.

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
adding only the five Sakura operations/two paths. Static tests preserve all old
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
