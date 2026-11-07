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
schemas, statuses and static request/response examples are preserved.

## AWS services and the shared endpoint

S3, Lambda, native IAM and API Gateway retain their native operation sections.
The **local operation runner** above Scalar provides service/operation controls
for Systems Manager, CloudFormation, KMS, SQS, CloudWatch Logs, Secrets Manager,
DynamoDB, IAM and SNS. It validates its executable inventory against the runtime
capabilities, including catalog operations. Missing or unknown bindings block
sending and remain visible as errors; they do not produce a smaller success set.

Select a service and operation, edit the native request body, then choose
**Send to local API**. Selection rebuilds the real method, path, Content-Type,
service and target headers. JSON1.0, JSON1.1 and URL-encoded Query keep their
native formats. Query Action/Version and the selected operation must agree.
SNS Query requests use Version2010-03-31 without X-Amz-Target. There are no fake
per-operation endpoints for the shared root POST.

The runner shows the actual status, content type and bounded response text in a
separate panel. Validation failures do not send a request. An authentication or
renewal failure stops the original operation without replay. All operations use
the existing local cookie authentication, IAM evaluation and SQLite state.
Responses are rendered as text; user data and wire identifiers are not translated.

SQS helper buttons copy only actual QueueUrl/ReceiptHandle values from the
current request context, not placeholders. A changed queue, region or principal
invalidates incompatible remembered values. Inspect returned identifiers before
reusing them. SNS currently records local deliveries only: this is not outbound
delivery, SQS fan-out or FIFO publishing.

English/Japanese controls share `tenkacloud-simulator-locale` with the console.
Descriptions, controls, validation and status text are localized; API operation
names, JSON keys, ARN values and native/user content retain their original form.
The pinned Scalar renderer uses reviewed Japanese UI translations, while the
English OpenAPI prose and request/response examples remain literal source text.
Language changes must not send an API operation or erase the current draft.

The stable internal tag `KMS local symmetric keys` still displays as **Shared AWS
JSON/Query endpoint** for Scalar's single `awsRegionalOperation`. Selecting a
Scalar body example alone does not update its routing fields; use the local
operation runner for service-specific routing, or configure the native fields
explicitly in Scalar.

**Examples are static samples, not saved state.** Scalar's Test Request → Send
and the runner invoke the actual local API. The live S3 response explanation
also distinguishes executed XML/bytes from static examples; object bodies are
not parsed as bucket metadata.

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

The new runner, language synchronization and renewal hooks require exact-source
product acceptance; source presence and pure tests are not browser evidence.
The mandatory source gate is `make test-ui-source`. `make e2e` registers real
SQS/Logs sends, saved-state reads, authentication/permission failures, operation
switching, separate team processes and English/Japanese presentation. Asset
regeneration, the complete gates and applicable formal checks must also pass
before merging. Unrun or failed checks remain unfinished.

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
