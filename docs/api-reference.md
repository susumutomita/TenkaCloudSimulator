# Local API reference navigation

Open `/_simulator/docs/` after signing in to the local simulator. The reference
uses the embedded Scalar1.76.4 renderer and OpenAPI3.1 document; it does not load
remote fonts, scripts, proxy services or cloud APIs. Session cookies remain
same-origin, authentication persistence is disabled, and curl/SDK examples still
require a host-issued simulator token. Dummy AWS wire credentials are not login
credentials. Never enter real cloud credentials.

The navigation groups reference material under **AWS**, **GCP**, **Azure**,
**Sakura**, and a separate **Simulator** control section. These are OpenAPI
`x-tagGroups`; they do not register providers, grant permission, create endpoints
or change SDK semantics. Existing paths, operation IDs, operation tags, headers,
schemas, statuses and static request/response examples are preserved.

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

## Other clouds and unavailable services

GCP and Azure retain their separately implemented provider paths and synthetic
host-configured project/account. Their authorization is the local owner-binding
subset, not Google IAM or Azure RBAC. Read the provider limits; a visible
reference entry does not establish full official SDK or authenticated console
parity.

Sakura is an explicit **Not implemented** status section. It has no simulator
operations, endpoints, authorization adapter or claimed SDK compatibility, and
provides no Test Request operation. Do not treat discoverability as availability.

## Verification scope

Issue [#421](https://github.com/susumutomita/TenkaCloudSimulator/issues/421)
tracks this navigation increment. The static detector validates exact cloud
assignment, known61 operation IDs/34 paths, documentation-only tags and the
absence of Sakura operations. Its `--baseline` option compares the entire native
OpenAPI projection while excluding only top-level tag/group metadata; it must
be supplied to claim preservation against a particular revision. Negative
fixtures exercise missing/duplicate/wrong-cloud groups and modified native
schema/example/operation tags.

The new browser helper is intended to check the actual embedded renderer on
desktop1440/mobile390, keyboard/focus, strict axe, visible cloud/service/status
sections and real Scalar AWS/GCP/Azure list responses after native writes to the
same isolated CLI. It does not claim SDK compatibility by itself. Existing
official SDK and live S3 helpers remain separate acceptance cases.

At this source checkpoint, the new browser helper and empty documentation-tag
rendering are **unexecuted**. Pinned bundle inspection confirms the x-tagGroups
navigation code, but browser behavior is not inferred from source existence.
Full quality/E2E/formal gates and current-HEAD hosted CI remain integration
requirements. No CSP relaxation, dependency update or remote asset is required.

Primary Scalar references:

- [OpenAPI extensions](https://scalar.com/products/api-references/openapi): x-tagGroups for3.0/3.1; native parent nesting requires3.2 and is not used here.
- [Runtime configuration](https://scalar.com/products/api-references/configuration): document sources and renderer configuration.
