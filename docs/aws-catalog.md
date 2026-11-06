# AWS service catalog

The catalog serves bounded configuration metadata for additional AWS services
through each service's own native protocol. Contracts come from documentation-free
subsets of the pinned official botocore 1.41.6 models; see
[design 0007](design/0007-model-driven-aws-catalog.md).

Every listed operation is implemented against provider-owned SQLite state. Every
other official operation of the same service returns HTTP 501
`UnknownOperationException`. Input members that the simulator would ignore are
refused with the operation's own validation exception. No workload, data plane,
network, OAuth or real AWS call is executed or simulated.

## Services

| Service | Issue | Protocol | Implemented operations | Bounds |
| --- | --- | --- | --- | --- |
| AWS CodeConnections | #297 | JSON 1.0 | Create/Get/List/DeleteConnection, Create/Get/List/Update/DeleteHost, Tag/Untag/ListTagsForResource | Connections and hosts stay `PENDING`; no OAuth or provider app installation; repository links and sync configurations are not implemented |
| AWS Step Functions | #83 | JSON 1.0 | Create/Describe/List/Update/DeleteStateMachine, Create/Describe/List/DeleteActivity, Tag/Untag/ListTagsForResource | Definitions pass a structural ASL check; executions, versions, aliases and activity tasks are not run |
| Amazon Elastic Container Registry | #111 | JSON 1.1 | CreateRepository, DescribeRepositories, PutImageTagMutability, PutImageScanningConfiguration, DeleteRepository, Tag/Untag/ListTagsForResource | No images, layers, registry tokens or scanning |
| AWS Amplify | #147 | REST-JSON | Create/Get/List/Update/DeleteApp, Create/Get/List/Update/DeleteBranch, Tag/Untag/ListTagsForResource | No repository connection, build, deployment, hosting or domain; tokens and basic-auth credentials are refused; apps with branches cannot be deleted |

The capability document lists the same services under `services.catalog`,
including every implemented operation, the number of official operations, the
model source hash and the bounds above.

## Routing and authorization

- JSON protocols are routed by the official `X-Amz-Target` prefix.
- REST-JSON protocols are routed by the SigV4 credential-scope service. Unsigned
  first-party console and API-reference requests use `X-Simulator-Service`
  instead. Signatures are never verified; use dummy `TCSIM` credentials.
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

The [catalog API reference](/_simulator/docs/catalog.html) serves one OpenAPI
document per service, generated from the same embedded official shapes.

## Verification

- `go test ./internal/aws/catalog` covers every service lifecycle, every scalar
  type and HTTP binding, specification and model consistency, storage faults,
  concurrency guards, budgets, pagination, client tokens and generated
  reference freshness.
- `test/e2e/aws_catalog.py` runs every bound operation through boto3 against two
  separate binaries, databases and token authorities, with participant policy
  refusals, cross-team token refusal, restart persistence and cleanup.
- `test/e2e/aws_catalog_console.py` drives the console and reference in Chromium
  with axe at 1440 px and 390 px, checking shared native state.
- `formal/CatalogResourceLifecycle.tla` checks bounded store invariants with 12
  mutation controls. It does not prove the Go implementation.

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
