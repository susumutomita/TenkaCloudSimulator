# TenkaCloud Simulator

English | [日本語](README.ja.md)

Practice cloud APIs and console workflows locally, without a cloud account.
TenkaCloud Simulator is an independent, AWS-first learning simulator with bounded
Google Cloud Storage, Azure Blob Storage and Azure Resource Manager catalog
support, plus an optional local
[Sakura Disk configuration API](docs/sakura.md). One Go binary embeds the
AWS-style console built with Cloudscape, local Google Cloud and Azure consoles, a
Scalar API reference, SQLite, and a pinned CPython/WASI runtime for Python Lambda.
Running a built binary requires no Go, Node.js, host Python, Docker, external
database, or real cloud connection. **Use dummy credentials and practice data.**

This is a documented subset of cloud behavior, not a complete AWS, Google Cloud,
or Azure replacement. Service issues and roadmap entries are plans, not working
features. See the [development status](docs/development-status.md) and each
service's limitations below.

## Acknowledgments: Kumo

We thank [Kumo](https://github.com/sivchari/kumo)'s maintainers and contributors
for sharing their work under the MIT license. Kumo informed our approach to
service registration, protocol routing, SDK-compatible endpoints, and service
wiring. TenkaCloud Simulator runs its own implementation rather than using Kumo
as a runtime backend.

Earlier revisions adapted limited native wire and lifecycle code in
[IAM](third_party/kumo-iam.json), [REST/API Gateway](third_party/kumo-apigateway.json),
[CloudWatch Logs](third_party/kumo-cloudwatch-logs.json), and
[KMS](third_party/kumo-kms.json). Those identified runtime portions have been
independently reimplemented against public AWS contracts. Historical provenance
and the [original MIT copyright and permission notice](third_party/KUMO-LICENSE.txt)
remain intact. The [replacement source record](third_party/independent-native-source.json)
describes the reviewed scope; it is not a clean-room or legal certification.

## Start locally

The embedded [interactive architecture page](docs/landing.md) is available at
`http://127.0.0.1:4566/_simulator/` after startup. Its HTML descriptions explain
the supported local subsets; optional HTML-in-Canvas drawing requires a browser
with the current API. The SDK endpoint `/` remains unchanged.

Building from source requires Go 1.26; CI pins Go 1.26.10.

```sh
make start
```

This rebuilds the current source and starts the simulator with `./practice.db`
and `./simulator-token`. It stops if Go is missing or the build fails; it does not
silently run an old binary. The equivalent explicit commands are:

```sh
make build
./dist/tenkacloud-simulator --listen=127.0.0.1:4566 \
  --state=./practice.db --token-file=./simulator-token
```

Open <http://127.0.0.1:4566/_simulator/console/> and sign in with the operator token
from `simulator-token`. A new token file uses mode `0600`; an existing private
regular file is reused. Keep this operator credential out of participant access,
Git, logs, terminal recordings, and screen sharing. If paste is unavailable, use
**Choose token file**: the browser reads a bounded local file without uploading
it, and the server authenticates when you submit. See the
[token-file format and unverified browser boundaries](docs/login-token-file.md).
The masked field includes per-field password-manager ignore hints. It adopts a
connected native value during input/focus/keyboard capture or blur, and snapshots it at
submission, including an intentional clear. Submission flushes a current explicit
paste first; adoption is a new intent that cancels older asynchronous input work.
These are compatibility measures, not proof of an extension cause or a confirmed
Dia/1Password repair. At `339c264`, the first native-input browser case passed,
but the native-clear/Enter case failed. The keyboard-capture follow-up passed both
complete 62-test browser suites at `8149cac`. The later catalog integration still requires its own complete CI.
Actual Dia/1Password and user-device clipboard behavior remain unverified.
See the [dated login CI result and remaining checks](docs/login-token-file.md#dated-validation-and-current-candidate).

Press Ctrl+C to stop. Restarting with the same state and token files reuses them.
Omitting `--state` when invoking the CLI creates a fresh in-memory database.
The startup token is displayed only on a real stdout terminal when the `CI`
environment variable is absent; redirection or even an empty `CI` suppresses it.
Hosts can also open an existing participant STS session in the Parameter Store
GUI using a [one-use console handoff](docs/console-sessions.md#participant-sts-console-handoff-version-1).
It is restricted to one parameter and current role permissions; participants do
not need the operator token.

**Account → Sign out** revokes only that browser session. More options:
[startup](docs/start.md), [sessions](docs/console-sessions.md).

## Supported services

Supported console actions, SDKs, and HTTP APIs operate on the same SQLite state.
A service having an API does not imply that it has a console, CloudFormation
adapter, or Challenge integration.

| Service | Implemented subset | Main exclusions |
| --- | --- | --- |
| [S3](docs/s3.md) | Bucket/object operations, version history, public access block, tags, and [six multipart APIs with a native Management view](docs/s3-multipart.md) | Multipart copy/checksum extensions, ACLs, general bucket policy, encryption, lifecycle and replication; path-style and 8 MiB completed objects only |
| [SNS](docs/sns.md) | Nine Query/XML operations for Standard-topic metadata, subscriptions and bounded local publish/delivery records; native SDK support | No outbound delivery, SQS adapter, console, CloudFormation or FIFO publication; local immediate confirmation |
| [SQS](docs/sqs.md) | Standard queues, a Cloudscape console, and 9 JSON 1.0 operations: CreateQueue, GetQueueUrl, ListQueues, GetQueueAttributes, DeleteQueue, SendMessage, ReceiveMessage, DeleteMessage, ChangeMessageVisibility | FIFO, batches, DLQ, SNS integration, CloudFormation, function SDK bridge, Challenge integration |
| [CloudWatch Logs](docs/cloudwatch-logs.md) | Log groups/streams, group deletion, PutLogEvents/GetLogEvents; 7 APIs and a console sharing their state | Metrics, alarms, retention settings, automatic Lambda delivery, CloudFormation, Logs Insights |
| [Secrets Manager](docs/secrets-manager.md) | CreateSecret, DescribeSecret, ListSecrets, ListSecretVersionIds (metadata only), PutSecretValue, GetSecretValue, UpdateSecretVersionStage, immediate DeleteSecret; versions and explicit reveal in the console | Real-secret storage, KMS encryption, rotation, replicas, SecretBinary, scheduled deletion, CloudFormation; dummy values are plaintext in SQLite |
| [KMS](docs/kms.md) |12 APIs: symmetric keys, local AES-GCM, Create/List/Update/DeleteAlias, same-Tx alias crypto; SDK and Cloudscape key-detail Aliases | AWS-managed aliases/alias ABAC, tags, grants, custom policies, rotation, key deletion, CloudFormation, other-service encryption; raw SQLite keys are not an HSM or at-rest guarantee |
| [Systems Manager Parameter Store](docs/console.md) | PutParameter, GetParameter, GetParameters, GetParametersByPath, DescribeParameters, DeleteParameter; current values, versions and overwrite checks | SecureString/KMS, history, labels; Standard String/StringList with text data only |
| [DynamoDB](docs/dynamodb.md) | Seven JSON 1.0 APIs: CreateTable, ListTables, DescribeTable, DeleteTable, PutItem, GetItem, DeleteItem; Cloudscape table list/create/details/delete and exact-key item writes/reads/deletes. S/N/B keys, optional sort key, partition-key attribute_exists/attribute_not_exists conditions | Query, Scan, item listing, indexes, Streams, CloudFormation, Challenge integration |
| [IAM](docs/iam.md) | 28 role/inline/trust, customer-managed policy/version/role-attachment and native instance-profile Query operations; shared permission union with Deny priority; bounded AWS-principal/ExternalId role trust | AWS-managed catalog, user/group attachments, general Conditions, permissions boundaries, SCPs; instance profiles do not execute EC2 or issue credentials |
| [Native SDK / STS](docs/sts-native.md) | Verified local SigV4 for IAM/STS/CFn/SSM; GetCallerIdentity and AssumeRole, expiry, trust and generation-bound role policy | Other signed services, presigned requests, session policies/tags, federation, real AWS identity and multi-account tenancy |
| [Lambda](docs/lambda.md) | CreateFunction, ListFunctions, GetFunctionConfiguration, UpdateFunctionCode, Invoke, DeleteFunction, AddPermission/GetPolicy/RemovePermission; Python ZIP execution through CPython 3.12.16/WASI and a bounded S3/SSM SDK bridge | Other languages, asynchronous invocation, Function URLs, arbitrary networking; 128 MiB and 1–30 second timeout |
| [CloudFormation](docs/cloudformation-rest.md) | CreateStack, DescribeStacks, DescribeStackEvents, DeleteStack; S3 Bucket, SSM Parameter, inline Python Lambda, bounded IAM Role, 5 API Gateway resource types and Lambda Permission; bounded Ref/Sub/GetAtt/DependsOn, String/Number constraints, [local SSM value parameters](docs/cloudformation-ssm-parameters.md), Equals/condition aliases, resource/output Condition and If/NoValue selection | UpdateStack, And/Or/Not, NoEcho secrecy, general resource types, full AWS asynchronous lifecycle, dedicated CloudFormation service roles |
| [API Gateway](docs/apigateway.md) | 16 Regional REST management operations, resources/methods/integrations, immutable deployments, new stages and local Lambda proxy execution | HTTP/WebSocket APIs, authorizers, API keys, editing existing stages, public endpoints |
| [WAFv2](docs/wafv2.md) | Ten regional control operations, Cloudscape rule/association editor, ordered Count/Allow/Block before API Gateway Lambda execution | CloudFront, managed groups, telemetry, WAF CloudFormation, AWS SQLi detection parity and full Challenge lifecycle |
| [Service catalog](docs/aws-catalog.md) | Model-driven configuration metadata for 112 further AWS services ([generated table](docs/aws-catalog-services.md)) through their native JSON or REST-JSON protocols, with one generic console view and per-service reference | Workloads, executions, data planes, delivery, OAuth and every unlisted official operation (HTTP 501). Products assessed as not servable are listed with evidence in the [coverage assessment](docs/coverage-assessment.md) |
| [Azure Resource Manager catalog](docs/azure-catalog.md) | Resource groups and configuration metadata for Key Vault, virtual networks/subnets/NSGs/public IPs, user-assigned identities, Log Analytics, App Configuration, Container Registry, Event Hubs, Service Bus, Cosmos DB and App Service plans through official ARM paths and api-versions, with an Azure-styled console view and per-provider reference | Data planes, keys, secrets, connection strings, routing, Azure RBAC and every unlisted official operation (HTTP 501) |

AWS regions are `us-east-1`, `us-west-2`, `ap-northeast-1`, and `eu-west-1`.
Local limits include 8 MiB per S3 object, 64 MiB across retained object versions,
4,096 UTF-8 bytes per SSM value, and 51,200 bytes per CloudFormation TemplateBody.
Each environment supports up to 1,000 buckets, 10,000 objects/versions/delete
markers, 1,000 parameters/identities, 100 stacks, and 100 resources per stack.
These are service limits, not a total process-memory bound. See service guides
for all supported fields, quotas and errors.

S3 CloudFormation supports BucketName, Versioning, PublicAccessBlock, user Tags,
Ref and Arn, and refuses to delete a bucket with retained history. Stack callers
need both stack permissions and underlying resource permissions;
`CAPABILITY_IAM` does not grant them.

SQS permits at most 10,000 physical message rows and 64 MiB of UTF-8 bodies plus
serialized attribute JSON across the process, including expired rows not yet
collected. This is not the SQLite file size, process-memory usage, or AWS billing
size. The [Google Cloud service catalog](docs/gcp-catalog.md) serves
configuration metadata for 40 Google Cloud APIs, such as AlloyDB, Compute
Engine networking and Cloud Logging, through their official discovery-document
paths and the Google Cloud console. Other Google Cloud services are not implemented; Azure adds only the bounded
[Resource Manager catalog](docs/azure-catalog.md) metadata. An explicit opt-in
[Sakura Disk configuration API and console](docs/sakura.md) support local metadata only,
without VM or block storage. The provider is disabled by default; explicit
host scope enables Sakura Cloud → Disks alongside the other cloud selectors. Using
Cloudscape does not establish full AWS console fidelity or completed comparison
against authenticated cloud consoles.

## Azure Resource Manager catalog

Azure management SDKs can set `base_url` to
`http://127.0.0.1:4566/_simulator/providers/azure-arm` and send official
`/subscriptions/...` requests to one synthetic subscription. Choose
**Azure → Services → Resource Manager** for the matching console view. The
configured Azure owner below is the only authority; this is not Azure RBAC.
See the [products, semantics and limits](docs/azure-catalog.md).

## Google Cloud Storage and Azure Blob Storage

Choose **AWS**, **Google Cloud**, or **Azure**, then search that provider's service
catalog. Cloud Storage and Blob Storage have separate navigation. English and
Japanese affect UI text only, preserving resource names, API identifiers and
user data. In a fixed synthetic project/account you can create storage,
upload, download an actual file, inspect supported metadata, and delete it.
See [scope and limitations](docs/multicloud-storage.md),
[Google Cloud console](docs/gcp-console.md), [Azure console](docs/azure-console.md),
and [official SDK acceptance](docs/acceptance/crossprovider-official-storage.md).
Two separate CLIs/DBs/tokens and desktop/390px browser flows are acceptance-test
targets; consult [exact-revision CI](https://github.com/susumutomita/TenkaCloudSimulator/actions/workflows/ci.yml)
for current results. Authenticated real-cloud screen parity remains unverified.

The default storage owner is `simulator-admin`. A host preparing a participant
environment sets `--cloud-storage-owner=learner` and issues a participant token
bound to `learner`. This local ownership setting is not Google IAM or Azure RBAC
and grants no AWS permissions. Changing an existing database's owner is rejected.

Azure Blob Properties reads an authenticated native HEAD without fetching the
blob body. **More → Container properties** shows only actual ETag, Last-Modified
and metadata; it does not invent blob-only fields. See the
[typed target model](docs/formal-azure-properties-target.md). Historical focused,
whole-Go, coverage and formal receipts for `4a586db` belong to that revision;
later revisions still require their own CI and full E2E verification.

## Create a Lambda with an IAM role

Open **Services → IAM → Roles → Create role**, choose the Lambda service trust,
then name and review the role. Add the required inline policy under the role's
Permissions. In **Lambda → Create function → Permissions → Existing role**, select
that role, deploy a ZIP in Code, then use Test to execute Python. Alternatively,
create a customer policy in IAM → Policies and use the role’s Add permissions →
Attach policies. An empty role has implicit Deny; creating a policy alone grants
nothing. AWS-managed catalog ARNs and CFN metadata grant no permissions.

Customer policies retain at most five versions and twenty attachments per role
(separate from ten inline policies). Default switches update every attached
role’s effective union atomically; an aggregate-limit or storage failure rolls
back the entire change. `ListPolicies` requires explicit `Scope=Local`; paths
other than `/`, the AWS catalog and unsupported filters return501. See the
[policy lifecycle and exact local limits](docs/iam.md).

IAM implements a bounded Action/Resource wildcard matcher with explicit Deny
precedence. General Condition/Principal/NotAction/NotResource, resource policies,
permissions boundaries and SCPs are unsupported. The bounded Lambda resource
policy grants only local API Gateway Invoke with a SourceArn. Legacy
CloudFormation roles and Local identities use separate ownership/trust models;
mutual name adoption is rejected. Catalog ARNs such as
`SignInLocalDevelopmentAccess` and `AWSLambdaBasicExecutionRole` are metadata,
not permission grants. See [IAM details](docs/iam.md).

## API reference and SDKs

After signing in, open `/_simulator/docs/`. Examples are **static documentation,
not current saved state**. Navigation separates AWS, Google Cloud, Azure and a
Sakura Disk configuration section. Its [five native Disk operations](docs/sakura.md)
require the explicitly enabled local provider and configured owner; documentation
discovery does not enable it or grant permission. Shared AWS endpoint guides explain the
required routing fields; see [reference navigation](docs/api-reference.md).
Use **Test Request → Send** to inspect the real status,
headers and body. Native formats remain intact: S3 XML/bytes, SSM JSON 1.1,
SQS root POST with `X-Amz-Target: AmazonSQS.<operation>` and JSON 1.0, and IAM/
CloudFormation Query/XML. **Read fields** explains supported live S3 metadata
responses; it does not inspect object bodies.

```sh
# Operator only: do not put the token itself in shell history or share output.
SIM_TOKEN=$(cat simulator-token)
curl -H "X-Simulator-Token: $SIM_TOKEN" -X PUT http://127.0.0.1:4566/practice-bucket
curl -H "X-Simulator-Token: $SIM_TOKEN" http://127.0.0.1:4566/
unset SIM_TOKEN
```

AWS SDK clients need an explicit local endpoint, region, dummy `TCSIM…` keys, and a request
hook adding `X-Simulator-Token`. Dummy signatures select the wire protocol; they
do not authenticate access. Real AWS-style keys, AWS session tokens and presigned
URLs are rejected. See [connection examples](test/platform/README.md) and
[actual SDK tests](test/e2e/check.py).

An acknowledged Secrets create/update closes and clears its form before the
independent metadata read. A failed read can be refreshed without replaying the
write; a new form gets a fresh request token. The bounded
[ACK/read contract](docs/formal-secrets-mutation-refresh.md) excludes
unacknowledged network ambiguity and is not a JavaScript refinement proof.

## Isolate teams and run challenges

Allocate a distinct process, SQLite database and operator token for each team/
environment. The operator issues 15-minute identity-bound participant tokens
through `POST /_simulator/v1/access`; participants cannot switch to another
identity or admin. Each IAM action is still authorized. Binding to localhost
alone does not provide authentication or tenant isolation.

The host owns user authentication, team allocation and proxy authorization.
Browser credentials are separate from SDK credentials and expire within
15 minutes or the participant's earlier expiry. Logging out leaves other
browsers and SDK tokens valid. A proxy with its own session must implement its
own logout. Participant/browser sessions expire on process restart; a private
operator token file can be reused. This does not isolate files from the same OS
user. See [security boundaries](SECURITY.md).

The [challenge compatibility table](docs/catalog.md) records hello-world import,
separate team startup, SSM retrieval, actual host grading and cleanup through a
development integration. It does not establish complete problem support or
automatic integration with standard TenkaCloud startup. Do not enable problems
whose required capabilities are missing.

Numeric loopback is the default. `--allow-remote` is an explicit option for a
trusted authenticated proxy/isolated network; it adds no TLS, authentication, or
tenant isolation. `--unsafe-no-auth` is for loopback development only.

## Development and verification

```sh
make before-commit       # format/vet, asset/module checks, race, statement90%, build
# Enforce source-line90% too, after generating coverage.out above:
go run github.com/boumenot/gocover-cobertura@v1.4.0 < reports/coverage.out > reports/coverage.xml
python3 scripts/check-coverage.py reports/coverage.out reports/coverage.xml
make cross-check         # compile for 5 OS/architecture targets
python3 -m venv /tmp/tcsim-tests
/tmp/tcsim-tests/bin/pip install -r scripts/requirements-test.txt
/tmp/tcsim-tests/bin/playwright install chromium
/tmp/tcsim-tests/bin/python scripts/get-audit-tools.py /tmp/tcsim-audit
AXE_SCRIPT=/tmp/tcsim-audit/axe.min.js make e2e PYTHON=/tmp/tcsim-tests/bin/python
TLA_JAR=/path/to/tla2tools.jar make formal
```

CI enforces both statement and unfiltered source-line coverage at **90%**.
`make before-commit` checks statement coverage; CI's Cobertura step and the two
commands above enforce the source-line floor. SDK/browser workflows, restart,
team isolation, Japanese KMS encrypt/decrypt, licenses, dependency security,
embedded assets and finite models also have checks. Cross-compilation does not
prove execution on another OS. Python, Node.js and Java are development tools,
not binary runtime dependencies. Regeneration must preserve locks and original
rights: [console](tools/console/README.md), [Scalar](tools/scalar/README.md),
[Python/WASI runtime](tools/python/README.md).

The combined catalog/login inventory requires **55 finite models and 450
intentional defect controls (505 required executions)**; exact-HEAD whole-gate
validation after this main integration is pending. Main's 52/415 inventory is
preserved: the earlier 50/398 plus CatalogResourceLifecycle (12 controls) and
CatalogConsoleEpoch (5). LoginInputIntent adds one model and 14 controls. SNSAdmission/SNSLifecycle
add two finite normalized-admission/local-record models and twenty-one controls;
these do not prove Go/SQL refinement or external delivery.
The earlier landing, Sakura, Secrets metadata and DynamoDB console models remain
registered. Any 463-execution or 102-pure-test receipt at `8149cac` belongs to
that pre-merge base and does not validate this combined revision. Registration
is not an execution receipt. The IAM scenario checks two requests
from each of four reachable seeds. A broader four-request trial timed out and
is not claimed as verified. TLC examines bounded safety properties and checks
that targeted mutants produce real counterexamples. It does not prove Go/JS
refinement, cryptography, full cloud specifications or authenticated console
parity. Historical receipts and combined exact-revision results are separate.
See [formal validation](formal/README.md), [CI](docs/ci.md),
[backlog acceptance](docs/acceptance/backlog-services.md), and the
[standalone architecture](docs/design/0001-go-standalone.md).

On 2026-11-06, the two landing models passed their scoped 19 executions at
`4382ec4`; the full 448-case attempt at that revision failed at DynamoDBAdmission.
The embedded landing page passed eight desktop/mobile HTML and fallback browser
cases at `fad3b8a`, including six strict accessibility audits. Installed Chromium
154 lacks the current HTML-in-Canvas API: this verifies ordinary HTML/fallback,
not Chrome 157 drawing, other engines or a complete simulator release. These
dated results do not replace later exact-head Go, E2E, formal or hosted checks.
The admission model trusts host/origin/authentication verdicts; the selection
model excludes URL parsing, focus, layout, SVG geometry and JavaScript refinement.

Historical CFN evidence: the isolated condition SANY/baseline/fourteen controls
passed at `f69fb46`; its 365-case source preparation is historical. At `c298972`,
all 48 models/381 controls (429 executions), full Go race/build and one-profile
statement/source coverage of 94.94%/90.32% passed. Its documentation-only successor
`414c902` passed all 61 E2E cases in 980.726 seconds, reflow and start checks.
“Documentation-only” describes that historical successor, not the current landing
implementation. These receipts and their then-pending hosted checks belong to
those revisions. The original crypto fixture also passed the two-CLI SDK helper
at `2d76646` (73 SDK calls/responses and nine auxiliary HTTP pairs across four CLI
incarnations). None proves host scoring, output secrecy, every physical resource
deletion, Go/SQL refinement or success of the current 482-case combined gate.

## License and attribution

The project is [MIT licensed](LICENSE). Original LICENSE/NOTICE texts for Go
modules, Cloudscape, Scalar, CPython/WASI and other bundled components ship at
`/_simulator/licenses/` and must be preserved on redistribution. This is not an
official AWS service and does not use AWS logos. See the
[design and handoff guides](docs/handoff/README.md).

## S3 multipart integration, 2026-11-08

This revision adds the S3MultipartState model and five controls while preserving every model and control from current main. Full validation of the integrated revision remains required; earlier execution receipts are historical. See [multipart scope and limits](docs/s3-multipart.md).
