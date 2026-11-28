# AWS service catalog

The catalog serves bounded configuration metadata for additional AWS services
through each service's own native protocol: JSON 1.0/1.1, REST-JSON or Query. Contracts come from documentation-free
subsets of the pinned official botocore 1.41.6 models; see
[design 0007](design/0007-model-driven-aws-catalog.md).

The catalog stores resource records in provider-owned SQLite. Service Quotas
reads an enforced limit from the active B2BI resource definition. Every
other official operation outside each service’s listed subset returns HTTP 501
`UnknownOperationException`. Input members that the simulator would ignore are
refused with the operation's own validation exception. No workload, data plane,
network, OAuth or real AWS call is executed or simulated.

## Services

The catalog serves 175 services. The [generated service table](aws-catalog-services.md)
lists each service's Issues, protocol, implemented operations and bounds; it is
checked against the reviewed specifications by `go test ./internal/aws/catalog`.
The capability document lists the same services under `services.catalog`,
including every implemented operation, the number of official operations, the
model source hash and the bounds.

AWS Batch stores configuration such as compute environments, job queues,
scheduling policies and job definition revisions. Job submission, scheduling
and execution are outside its scope. [AWS Batch scope (#105)](https://github.com/susumutomita/TenkaCloudSimulator/issues/105).

Services whose official API needs behavior the engine cannot express truthfully
remain unimplemented rather than approximated, for example Managed Service for
Apache Flink, AWS Data Pipeline, Amazon SWF, CodeStar
Notifications, Amazon Managed Grafana, AWS Organizations,
Application Auto Scaling, Amazon Cognito, AWS Control
Tower, AWS Launch Wizard, Amazon Keyspaces and Amazon Comprehend Medical.
Amazon RDS, Amazon ElastiCache, Amazon Redshift, Amazon EC2 Auto Scaling and
AWS Elastic Beanstalk are served over the Query protocol. Services that use the
EC2 or REST-XML protocols are not served by the catalog yet.

Aurora, Neptune and DocumentDB are managed through the RDS API, and the boto3
`neptune`, `docdb` and `rds` clients send the same Actions signed as `rds`
(Version `2014-10-31`). What they can use is the RDS catalog's configuration
and records: DB subnet groups, DB cluster parameter groups, DB parameter groups
(Aurora and Neptune; DocumentDB has none), DB instances and DB clusters, tags
and, for the `rds` client, option groups for non-Aurora engines. DB instances
and DB clusters are stored records that stay in the `creating` state: no
database, endpoint, address or port exists, and no password is ever stored (a
password, secret or port member is refused with `InvalidParameterValue`).
The `neptune` client can use 21 of the 24 bound operations and the `docdb`
client 18, since neither model has option groups and DocumentDB has no DB
parameter groups. Reboot, failover, start, stop, snapshot, restore, replica,
backtrack, switchover and data plane operations return 501
`UnknownOperationException`, the RDS refusals apply (a non-empty `Filters`,
subnet group names that do not start with a letter), and parameter group
families such as `aurora-mysql8.0`, `neptune1.3` and `docdb5.0` are stored
without being checked. The RDS entry in the
[generated service table](aws-catalog-services.md) lists Issues #122, #124 and
#128 for this surface only; their proposed snapshot and runtime scope is not
served.
Products assessed as not servable at all, with evidence, are listed in the
[coverage assessment](coverage-assessment.md).

## B2B Data Interchange profile quota

Five read operations expose only the B2BI Profiles quota (`b2bi/L-05F6A9EF`)
in `us-east-1`, `us-west-2` and `eu-west-1`:

| Operation | Local response | IAM Resource |
| --- | --- | --- |
| ListServices | One B2B Data Interchange entry in the supported regions | `*` |
| GetAWSDefaultServiceQuota | The published AWS default, five profiles | `*` |
| ListAWSDefaultServiceQuotas | The same single published default | `*` |
| ListServiceQuotas | The account-level capacity enforced by the local CreateProfile | `*` |
| GetServiceQuota | The same applied capacity by service and quota code | Quota ARN |

The default and applied values have different sources. The shipped profile
limit is five; five existing profiles fill it, and deleting one frees a slot.
Applied reads report total capacity, never remaining slots. An embedded engine
with a different profile bound reports that bound while the published default
stays five. If the profile operation is not bound, applied Get returns
`NoSuchResourceException` and the applied list is empty; the default remains
readable. No applied value is invented from the default.

The named account-level read requires `servicequotas:GetServiceQuota` on
`arn:aws:servicequotas:<region>:<account>:b2bi/L-05F6A9EF`. The local IAM
policy supports action and resource matching. Each of the other four operations
requires its own action on literal `*`; a quota-ARN-only grant does not authorize
those operations. The
`servicequotas:service` condition key is outside the local policy contract.

The three lists accept `MaxResults` from 1 to 100. This singleton inventory never
emits a `NextToken`; a supplied foreign token is `InvalidPaginationTokenException`,
not a fabricated page. `ListServiceQuotas` accepts an exact `QuotaCode` filter
and ACCOUNT/ALL/RESOURCE levels. ACCOUNT, ALL and omission return the same
account-level row; RESOURCE returns `Quotas: []`. Unknown valid service names
return `NoSuchResourceException` on quota reads. In Tokyo, `ListServices` returns
`Services: []`, and the scoped quota lists and Gets return `NoSuchResourceException`.
This does not assert a universal AWS service inventory. The separate local B2BI
metadata catalog can still store profiles in Tokyo.

AWS lists Profiles as adjustable. `RequestServiceQuotaIncrease` accepts a desired
value above the enforced limit. With `SupportCaseAllowed=false` it saves a
`NOT_APPROVED` request. With true or the default it saves a local `PENDING`
request. A team can have one pending request per region and retain up to 1,000
requests across regions. The simulator never opens an external Support case or
raises the profile limit. The API has no client token. If the response is lost,
check history before submitting again.

`GetRequestedServiceQuotaChange` retrieves the saved ID.
`ListRequestedServiceQuotaChangeHistory` lists the last 90 days.
`ListRequestedServiceQuotaChangeHistoryByQuota` includes older requests for
this quota. Both lists return newest submissions first, accept status and
account/resource-level filters, and evaluate the caller's current IAM permission
on every page. The requests here are account-level. Page tokens belong to the
process, caller, region, operation and filters that issued them. Persisted
request IDs survive restart. Creating a request and listing by quota require
the operation's IAM action on the regional quota ARN. Looking up an ID and
listing the general history require their respective actions on `*`.

In **AWS service catalog**, choose **Service Quotas** to see the published
default and the applied account capacity in the selected region. Enter a
desired value to submit an increase request. **Refresh history** shows stored
requests and their IDs. If the submit response is lost, choose **Refresh
same-scope history** before starting a separate request. Several requests
can have the same desired value.

Open **Service Quotas API reference** at `/_simulator/docs/service-quotas.html`
to send the same operations in `us-east-1`. It uses the signed-in browser
identity. Select the operation's `X-Amz-Target` and its JSON body before
sending. Paste the returned request ID into `GetRequestedServiceQuotaChange`
to inspect the saved record.

Resource contexts, usage metrics and general quota inventory remain outside the supported scope.
[B2BI endpoints and quotas](https://docs.aws.amazon.com/general/latest/gr/b2bi-service.html) ·
[GetServiceQuota](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_GetServiceQuota.html) ·
[ListServices](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_ListServices.html) ·
[GetAWSDefaultServiceQuota](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_GetAWSDefaultServiceQuota.html) ·
[ListAWSDefaultServiceQuotas](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_ListAWSDefaultServiceQuotas.html) ·
[ListServiceQuotas](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_ListServiceQuotas.html) ·
[RequestServiceQuotaIncrease](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_RequestServiceQuotaIncrease.html) ·
[ListRequestedServiceQuotaChangeHistory](https://docs.aws.amazon.com/servicequotas/2019-06-24/apireference/API_ListRequestedServiceQuotaChangeHistory.html) ·
[Service Quotas IAM](https://docs.aws.amazon.com/service-authorization/latest/reference/list_service-quotas.html).

`test/e2e/service_quotas_profiles.py` checks requests and history alongside the applied quota and actual B2BI profile admission. `service_quotas_discovery.py` adds the four reads through two real
team processes, native IAM refusals, full-capacity replacement, reopening and
cleanup. The finite `ServiceQuotaDiscovery` model checks read projections and
unchanged abstract frames; native tests check JSON types, concrete region names
and page-size bounds. Neither is an AWS comparison or implementation proof.

## ECR repository group membership

`CreateGroup` accepts `ResourceQuery.Type` equal to `TAG_FILTERS_1_0` and a
`Query` string containing this JSON object. It matches one exact tag key and
one exact value on local private ECR repositories in the group's region.

```json
{"ResourceTypeFilters":["AWS::ECR::Repository"],"TagFilters":[{"Key":"Team","Values":["blue"]}]}
```

`GetGroupQuery` reads the stored definition by group name or ARN. `UpdateGroupQuery`
replaces it on an existing query group. `ListGroupResources` returns the current
matching repository ARNs in creation order. Creating, tagging, untagging and
deleting a repository changes membership on the next read. `MaxResults` accepts
1–50. Supply `NextToken` in the same region on the same group revision to continue.

`CreateGroup` and `ListGroups` require their respective IAM actions with `Resource` set to `"*"`. A principal with only `ListGroups` can page through the groups in its own team and region. Permissions on a named group or a group ARN pattern apply to operations that support the group resource type. [AWS Resource Groups IAM action table](https://docs.aws.amazon.com/service-authorization/latest/reference/list_resource-groups.html) · [CreateGroup](https://docs.aws.amazon.com/ARG/latest/APIReference/API_CreateGroup.html) · [ListGroups](https://docs.aws.amazon.com/ARG/latest/APIReference/API_ListGroups.html)

The eleven supported operations return the modeled HTTP `403 ForbiddenException` when the local IAM policy denies the request. The group-scoped operations check the target ARN before disclosing whether it exists. [AWS Resource Groups API](https://docs.aws.amazon.com/ARG/latest/APIReference/API_Operations.html)

The group’s own tags appear in the creation response and in `GetTags`. `Tag` and `Untag` change these stored tags. The supported plain and query groups omit `Group.ApplicationTag`, which AWS reserves for application groups. [Group response shape](https://docs.aws.amazon.com/ARG/latest/APIReference/API_Group.html)

Readers need `resource-groups:ListGroupResources` on the group ARN, along with
`tag:GetResources`, `cloudformation:DescribeStacks` and `cloudformation:ListStackResources`
on `*`. AWS lists these three additional permissions for the API. Each membership
page checks all four against one fresh principal policy snapshot. Queryless groups retain their metadata and tag operations.
In the Cloudscape service catalog, select **AWS Resource Groups** and open a group to view its saved filter, page through matching repository ARNs, or edit the tag key and value of an existing query. **Refresh query and members** reads current repository tags and permissions again.

Search, explicit member management, CloudFormation queries, service configurations,
extra tag filters and other resource types remain outside this slice. [AWS supported resource types](https://docs.aws.amazon.com/ARG/latest/userguide/supported-resources.html) ·
[resource query formats](https://docs.aws.amazon.com/ARG/latest/userguide/gettingstarted-query.html) ·
[ListGroupResources permissions and response](https://docs.aws.amazon.com/ARG/latest/APIReference/API_ListGroupResources.html).

## Routing and authorization

- JSON protocols are routed by the official `X-Amz-Target` prefix.
- REST-JSON and Query protocols are routed by the SigV4 credential-scope
  service. Unsigned first-party console and API-reference requests use
  `X-Simulator-Service` instead. Signatures are never verified; use dummy
  `TCSIM` credentials. The catalog claims a Query form POST only when that
  service is a catalog Query service (`rds`, `elasticache`, `redshift`,
  `autoscaling`, `elasticbeanstalk`), so IAM, STS, SNS, Elastic Load Balancing,
  CloudFormation and EC2 requests keep their native handlers. Auto Scaling Plans
  (`autoscaling-plans`, JSON 1.1) is a separate native handler consulted first:
  an `autoscaling` Query request reaches the catalog and an `autoscaling-plans`
  request, whatever its protocol, reaches the native handler.
- Query requests are form-encoded POSTs to `/` with `Action` and the model's
  `Version`, flattened as botocore serializes them (`Tags.Tag.1.Key`,
  `TagKeys.member.1`, `entry.N.key`, an empty value for an empty list). Every
  parameter must belong to the official input shape. Parameter names may have
  at most 50 dot-separated segments and a form at most 4,096 distinct name
  segments, and names or values containing characters XML 1.0 cannot carry
  are refused, so a stored value is never altered in a response. Responses are
  `<Action>Response` XML with the result wrapper and
  `ResponseMetadata/RequestId`; errors are `ErrorResponse/Error/Code` with the
  modeled fault's official code (for example `DBParameterGroupNotFound`) and
  status. Failures without a modeled fault use the common Query errors
  `InvalidParameterValue`, `AccessDenied` (403), `InternalFailure` (500),
  `MissingAction`, `MissingParameter`, `MalformedQueryString` and, for an Action
  the official model does not define, `InvalidAction`. Query models with
  document shapes, XML attributes, HTTP-bound members or streams are refused
  at composition.
- Each operation is authorized as `<iam-prefix>:<Operation>` against the
  resource ARN before any disclosure or change. List operations use a wildcard
  ARN for the kind. Participant tokens bind the identity.
- Regions follow the credential scope or `X-Simulator-Region`; each region is a
  separate scope. The account is always `000000000000`.

## Historical AWS services

Three entries in the local catalog use AWS API models for services that have ended. The simulator can still read and write their supported configuration records with the pinned API shapes. AWS records the retirement dates in its [full shutdown list](https://docs.aws.amazon.com/general/latest/gr/full_shutdown_services.html).

| Service | AWS end of support | Local scope and service boundary |
| --- | --- | --- |
| AWS App Mesh (#236) | September 30, 2026 | The catalog stores mesh configuration. It does not run Envoy or route traffic. [AWS App Mesh notice](https://docs.aws.amazon.com/app-mesh/latest/userguide/what-is-app-mesh.html) |
| AWS Proton (#204) | October 7, 2026 | The catalog stores service and environment template metadata. AWS states that infrastructure previously deployed through Proton remains in place. [AWS Proton notice](https://docs.aws.amazon.com/proton/latest/userguide/Welcome.html) |
| Amazon Pinpoint (#97) | October 30, 2026 | The catalog stores project metadata. AWS retired Pinpoint endpoints, segments, campaigns, journeys and analytics. AWS End User Messaging continues to support SMS, voice, mobile push, OTP and phone number validation. [Amazon Pinpoint notice](https://docs.aws.amazon.com/pinpoint/latest/userguide/welcome.html) |

## Limits

Requests are limited to 256 KiB, stored resource descriptions to 64 KiB, all
catalog metadata to 32 MiB, tags to 50 per resource and client tokens to 10,000
retained for 24 hours. Each specification also bounds resources per kind and
region (for example 25 Amplify apps). These are local limits, not AWS quotas.

## Console and API reference

Choose **Services → Service catalog** in the console. The view follows the
resource-list, create-page and resource-page pattern that public Cloudscape
documentation describes for AWS consoles. No authenticated AWS console was
available as a reference, so nothing here claims visual equivalence with any
AWS console. For every catalog service the view sends the service's own
native requests and reads every result back from the service.

- **Names.** Resource kinds and members are shown with labels derived from
  their official names (`DBSubnetGroupName` becomes "DB subnet group name",
  `dbSubnetGroup` becomes "DB subnet groups"), keeping acronyms such as DB,
  ARN, IAM, VPC, KMS and ID. There are no per-service label tables. Form
  fields and detail values also show the official member name, and pages name
  the operation they send.
- **List.** Choose a service and a resource kind tab. The table has a counted
  header with Refresh, View details, Edit, Delete and Create actions (Edit and
  Delete act on the selected row), the name, up to three informative scalar
  members of the listed rows and the ARN, a text filter over the loaded rows,
  and pages that follow the service's own page tokens. Loading, empty (with a
  Create action), no-match and error states (with Retry) are shown in the
  table. Child resources such as Amplify branches choose their parent above
  the table. A list whose official operation requires a selection (for
  example Audit Manager `controlType` or RAM `resourceOwner`) offers it as a
  selector set to the first documented value; required page sizes use their
  maximum; other required list members are asked for before listing. Each
  service's Bounds stay complete in the collapsed **Simulator scope** section.
- **Create** opens its own page with Cancel and Create. It offers every input
  member of the create operation, the resource's name first and required
  members before optional ones marked "optional": text, number and boolean
  fields, comma-separated string lists, `Tags` as `key=value` lines, and other
  structures, maps and lists of structures (for example ElastiCache
  `AuthenticationMode`) as a JSON value with official member names. Required
  members left empty and values that do not match the official input shape
  (types, required and unknown members, enums, lengths and ranges) are
  reported at their fields before anything is sent; a service fault is shown
  on the form, which keeps its values.
- **Resource page.** Opens through the read operation, a `Describe*` call by
  name, or the listed description, with the name in the header and Edit and
  Delete actions. Scalar members are labelled key-value pairs; lists of flat
  structures (such as RDS `Subnets`) are tables; flat structures are
  key-value pairs; deeper values are shown as JSON.
- **Edit** opens its own page for a resource whose specification binds an
  update operation (for example `ModifyDBSubnetGroup`, `ModifyUser`,
  `UpdateApp` or `PutImageScanningConfiguration`). The form starts from the
  current description, offers the operation's members except identifying,
  fixed and account members and resource members outside the reviewed update
  fields, and sends only changed and required members. Members the bounded
  model still refuses (for example ElastiCache `Passwords`) are refused by the
  service with its error.
- **Tags** are added and removed on the resource page through the bound tag
  and untag operations. They are read with the list-tags operation or,
  without one (Redshift), from the description; a resource whose shape
  carries no ARN is tagged by the simulator ARN the descriptor's
  `arnTemplate` gives.

The console cannot clear an optional value in an edit (an empty field is
refused; use an SDK), does not pre-fill members the description names
differently (RDS `SubnetIds` against `Subnets`), offers one update operation
per resource kind (ECR repositories edit `PutImageScanningConfiguration`
only), filters only the rows already loaded rather than sending service-side
filters, does not page through tags, has no column preferences or
multi-select bulk actions, and runs no operation the specification leaves
unbound. Errors show the service's `Code` and `Message`.

Query services (RDS, ElastiCache, Redshift, Auto Scaling and Elastic Beanstalk) receive the same form bodies as
botocore (`Action`, `Version`, `SubnetIds.SubnetIdentifier.1`,
`AuthenticationMode.Type`, `Tags.Tag.1.Key`, an empty list as `Name=`) at `/`
with `X-Simulator-Service`, and their XML responses are decoded with the
output shapes the descriptors publish (`xmlShapes`, `output`,
`resultWrapper`, and per-member `key`, `itemKey`, `emptyKey`, `flattened`).
Descriptors of every service also publish `inputShapes` (with `shape` on each
complex create, update, put or tag member), `editable` on update operations,
and `arnTemplate` or `tagsWire` where a resource needs them; they are served
at `/_simulator/v1/catalog/aws`, not in the capability document.

The [catalog API reference](/_simulator/docs/catalog.html) serves one OpenAPI
document per service, generated from the same embedded official shapes. A Query
service lists each Action as its own runnable entry `/#Action=<Name>`: the
fragment only names the entry, and **Send** performs the real form-encoded
`POST /` with the required `X-Simulator-Service` header and shows the XML
response.

## Verification

- `go test ./internal/aws/catalog` covers every service lifecycle, every scalar
  type and HTTP binding, specification and model consistency, storage faults,
  concurrency guards, budgets, pagination, client tokens and generated
  reference freshness.
- `test/e2e/aws_catalog.py` runs every bound operation of the first four services
  through boto3 against two separate binaries, databases and token authorities,
  with participant policy refusals, cross-team token refusal, restart
  persistence and cleanup.
- `test/e2e/aws_catalog_fixtures.py` runs a reviewed native lifecycle for every
  other service from `test/e2e/catalog_fixtures/`: each bound operation must
  succeed at least once (a fixture with a `model` key, such as `neptune` and
  `docdb` against `rds`, runs in its own binaries, may call only operations that
  model binds and is exempt from the every-operation rule), an unpermitted participant is refused before any
  change (with the fixture's `denied` code, `AccessDenied` for Query services
  and `AccessDeniedException` by default), a second team stays empty, state
  survives restart and cleanup leaves nothing behind.
- `test/e2e/aws_catalog_console.py` drives the console and reference in Chromium
  with axe at 1440 px and 390 px on the list, create, resource and edit pages,
  checking shared native state, including an RDS lifecycle for every kind
  through the console and RDS reference **Send** requests checked with boto3.
  It also sweeps every catalog service and resource kind: each list opens
  without an error state and each bound Create page renders its fields.
- `test/fixtures/catalog-query/serializer.json` holds request bodies
  produced by botocore's own Query serializer
  (`test/fixtures/catalog-query/generate.py`); the Go decoder and the console request
  builder are both checked against them.
- `formal/CatalogResourceLifecycle.tla` checks bounded store invariants with 12
  mutation controls, and `formal/CatalogConsoleEpoch.tla` checks view epochs,
  single writes and failure visibility with 5. They do not prove the Go or
  JavaScript implementation.

Authenticated AWS console parity and live-AWS differential behavior are not
verified.

## Adding a service

1. Add the reviewed official operations to `internal/aws/catalog/selection.json`
   through a specification in `internal/aws/catalog/services_*.go`, then run
   `CATALOG_WRITE_SELECTION=1 go test ./internal/aws/catalog -run TestSelection`.
2. Run `python tools/awsmodels/extract.py` with the pinned botocore from
   `scripts/requirements-test.txt`.
3. Regenerate the reference with
   `CATALOG_WRITE_DOCS=1 go test ./internal/aws/catalog -run TestServedReference`.
4. Add native tests and SDK evidence, list the new files in the formal source
   scope and applicability record, and document the bounds here.
