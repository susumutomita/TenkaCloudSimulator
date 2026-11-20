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

The catalog serves 113 services. The [generated service table](aws-catalog-services.md)
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
Amazon RDS is served over the Query protocol. Services that use the EC2 or
REST-XML protocols are not served by the catalog yet.
Products assessed as not servable at all, with evidence, are listed in the
[coverage assessment](coverage-assessment.md).

## Routing and authorization

- JSON protocols are routed by the official `X-Amz-Target` prefix.
- REST-JSON and Query protocols are routed by the SigV4 credential-scope
  service. Unsigned first-party console and API-reference requests use
  `X-Simulator-Service` instead. Signatures are never verified; use dummy
  `TCSIM` credentials. The catalog claims a Query form POST only when that
  service is a catalog Query service (`rds`), so IAM, STS, SNS, Elastic Load
  Balancing, CloudFormation and EC2 requests keep their native handlers.
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

Choose **Services → Service catalog** in the console. The view lists, creates,
describes and deletes resources with each service's native requests, including
child resources such as Amplify branches. Complex official members remain
available through SDKs and the API reference.

For Query services (RDS) the console sends the same form bodies as botocore
(`Action`, `Version`, `SubnetIds.SubnetIdentifier.1`, an empty list as
`Name=`) to `/` with `X-Simulator-Service`, and decodes the XML response with
the output shapes the descriptors publish (`xmlShapes`, `output`,
`resultWrapper`, and per-member `key`, `itemKey`, `emptyKey`, `flattened`).
Errors show the service's `Code` and `Message`. Every RDS kind can be listed,
created from its string, number, boolean and string-list members, opened
(through its `Describe*` call by name, with `ListTagsForResource` tags) and
deleted. The console does not edit tags, `Filters`, or other structure members
such as `Tags` on create, and has no update form (for example
`ModifyDBSubnetGroup`); use an SDK or the API reference for those.

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
  succeed at least once, an unpermitted participant is refused before any
  change (with the fixture's `denied` code, `AccessDenied` for Query services
  and `AccessDeniedException` by default), a second team stays empty, state
  survives restart and cleanup leaves nothing behind.
- `test/e2e/aws_catalog_console.py` drives the console and reference in Chromium
  with axe at 1440 px and 390 px, checking shared native state, including an
  RDS lifecycle for every kind through the console and RDS reference **Send**
  requests checked with boto3.
- `internal/aws/catalog/testdata/query_serializer.json` holds request bodies
  produced by botocore's own Query serializer
  (`tools/awsmodels/query_vectors.py`); the Go decoder and the console request
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
