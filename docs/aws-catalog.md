# AWS service catalog

The catalog serves bounded configuration metadata for additional AWS services
through each service's own native protocol: JSON 1.0/1.1, REST-JSON or Query. Contracts come from documentation-free
subsets of the pinned official botocore 1.41.6 models; see
[design 0007](design/0007-model-driven-aws-catalog.md).

Every listed operation is implemented against provider-owned SQLite state. Every
other official operation of the same service returns HTTP 501
`UnknownOperationException`. Input members that the simulator would ignore are
refused with the operation's own validation exception. No workload, data plane,
network, OAuth or real AWS call is executed or simulated.

## Services

The catalog serves 117 services. The [generated service table](aws-catalog-services.md)
lists each service's Issues, protocol, implemented operations and bounds; it is
checked against the reviewed specifications by `go test ./internal/aws/catalog`.
The capability document lists the same services under `services.catalog`,
including every implemented operation, the number of official operations, the
model source hash and the bounds.

Services whose official API needs behavior the engine cannot express truthfully
remain unimplemented rather than approximated, for example Managed Service for
Apache Flink, AWS Data Pipeline, Amazon SWF, AWS Batch, CodeStar
Notifications, Amazon Managed Grafana, AWS Organizations, Service Quotas,
Application Auto Scaling, Amazon Cognito, AWS Network Firewall, AWS Control
Tower, AWS Launch Wizard, Amazon Keyspaces and Amazon Comprehend Medical.
Amazon RDS, Amazon ElastiCache, Amazon Redshift, Amazon EC2 Auto Scaling and
AWS Elastic Beanstalk are served over the Query protocol. Services that use the
EC2 or REST-XML protocols are not served by the catalog yet.

Aurora, Neptune and DocumentDB are managed through the RDS API, and the boto3
`neptune`, `docdb` and `rds` clients send the same Actions signed as `rds`
(Version `2014-10-31`). What they can use is the RDS catalog's configuration
only: DB subnet groups, DB cluster parameter groups, DB parameter groups
(Aurora and Neptune; DocumentDB has none), tags and, for the `rds` client,
option groups for non-Aurora engines. The `neptune` client can use 13 of the 16
bound operations and the `docdb` client 10, since neither model has option
groups and DocumentDB has no DB parameter groups. No cluster, instance,
snapshot, global cluster or data plane exists for any of the three products
(those operations return 501 `UnknownOperationException`), the RDS refusals
apply (a non-empty `Filters`, subnet group names that do not start with a
letter), and parameter group families such as `aurora-mysql8.0`, `neptune1.3`
and `docdb5.0` are stored without being checked. The RDS entry in the
[generated service table](aws-catalog-services.md) lists Issues #122, #124 and
#128 for this configuration surface only; their proposed cluster, instance and
snapshot scope is not served.
Products assessed as not servable at all, with evidence, are listed in the
[coverage assessment](coverage-assessment.md).

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

## Limits

Requests are limited to 256 KiB, stored resource descriptions to 64 KiB, all
catalog metadata to 32 MiB, tags to 50 per resource and client tokens to 10,000
retained for 24 hours. Each specification also bounds resources per kind and
region (for example 25 Amplify apps). These are local limits, not AWS quotas.

## Console and API reference

Choose **Services → Service catalog** in the console. For every catalog
service the view sends the service's own native requests and reads every
result back from the service:

- **List, open and delete** each resource kind, including child resources such
  as Amplify branches. A detail opens through the read operation, a
  `Describe*` call by name, or the listed description.
- **Create** with every input member of the create operation: text, number and
  boolean fields, comma-separated string lists, `Tags` as `key=value` lines,
  and other structures, maps and lists of structures (for example ElastiCache
  `AuthenticationMode`) as a JSON value with official member names. The
  console checks such values against the official input shape (types,
  required and unknown members, enums, lengths and ranges) before sending.
- **Edit** a resource whose specification binds an update operation (for
  example `ModifyDBSubnetGroup`, `ModifyUser`, `UpdateApp` or
  `PutImageScanningConfiguration`). The form starts from the current
  description, offers the operation's members except identifying, fixed and
  account members and resource members outside the reviewed update fields,
  and sends only changed and required members. Members the bounded model
  still refuses (for example ElastiCache `Passwords`) are refused by the
  service with its error.
- **Add and remove tags** on the detail view through the bound tag and untag
  operations. Tags are read with the list-tags operation or, without one
  (Redshift), from the description; a resource whose shape carries no ARN is
  tagged by the simulator ARN the descriptor's `arnTemplate` gives.

The console cannot clear an optional value in an edit (an empty field is
refused; use an SDK), does not pre-fill members the description names
differently (RDS `SubnetIds` against `Subnets`), offers one update operation
per resource kind (ECR repositories edit `PutImageScanningConfiguration`
only), does not page through tags or edit list filters, and runs no
operation the specification leaves unbound. Errors show the service's `Code`
and `Message`.

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
  with axe at 1440 px and 390 px, checking shared native state, including an
  RDS lifecycle for every kind through the console and RDS reference **Send**
  requests checked with boto3.
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
