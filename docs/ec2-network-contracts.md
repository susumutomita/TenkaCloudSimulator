# Bounded EC2/CloudFormation network contracts (#467)

Owner: Issue #467 contract-test worker. Base: main
`4be504d6eaf42c4f7e2d939d3b325e44374d802e`; dedicated branch
`test/467-route-contracts`. Ownership is limited to this document,
`test/e2e/ec2_network_contract.py` and
`test/e2e/fixtures/ec2-network-contract.json`. No runtime, compiler, shared
Gateway, CI, authentication, isolation or coverage gate changes.

## Oracle and scope

Official AWS references were checked on 2026-11-18 (the live pages do not expose
an immutable documentation revision). The fixture and assertions were authored
independently; no AWS Emulator code, fixture, dependency or expected response was
copied. The comparison reference requested by the Issue remains AWS Emulator
`7753983c6e495ec3612695b1336bf4936fc7b7fd`, not an executed oracle. No AWS Emulator
or real AWS comparison was run. AWS asynchronous stack statuses, rollback events
and eventual consistency are not asserted: this simulator has a synchronous,
transactional subset. Local `ValidationError`/`UnsupportedOperation`/token errors
are local expectations, not claims about AWS's error selection or timing.

The existing `internal/aws/ec2_network_contract_test.go` preserves the original
Battle template; `ec2_network_compile_test.go` checks compilation and
`ec2_network_topology_test.go` checks internal relationships. The new SDK test
adds real binary, authenticated numeric-loopback, Query/XML parsing, shared
CFN/EC2 VPC state and failure/survivor observations. It uses no VM resources.

## Operation/input/response/state/source matrix

Each row maps to the named `NetworkContract` test. Success rows use
`test_refs_canonical_cidr_and_scoped_cleanup`; failure rows use
`test_invalid_network_create_preserves_existing_state` unless specified.

| Operation and input | Expected response and state change | Official source | Tenka seam / evidence |
| --- | --- | --- | --- |
| CFN CreateStack with VPC CIDR `10.67.123.45/16` | CFN VPC Ref equals EC2 DescribeVpcs VpcId; GetAtt CidrBlock and EC2 CidrBlock both `10.67.0.0/16` | [VPC CidrBlock and Ref](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-vpc.html), [DescribeVpcs](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeVpcs.html) | `compileStackNetwork`, `StackStore.ec2Read`; SDK success test |
| CFN subnet/table/association with forward Ref references | Subnet VpcId GetAtt equals VPC Ref; table Ref equals RouteTableId GetAtt; association Ref equals Id GetAtt | [Subnet](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-subnet.html), [RouteTable](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-routetable.html), [association](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-subnetroutetableassociation.html) | `compileStackNetwork`, `createEC2Network`; SDK success test. Native subnet/table Describe is unavailable; their actual native state is **not** verified |
| CFN Route with explicit attachment dependency and default IPv4/IGW target | Stack completes in the local synchronous subset; no guest or packet assertion | [Route](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-route.html), [DependsOn gateway attachment](https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-attribute-dependson.html) | `compileNetworkResource`, `createEC2Network`; SDK success test |
| CFN Route with GatewayId plus NatGatewayId, or IPv4 plus IPv6 destination | Local ValidationError; existing CFN stacks and visible EC2 VPC state unchanged | [Route target/destination rules and property types](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-route.html) | `compileNetworkResource`; SDK rejection subtests. IPv6 and NAT are unsupported locally even when individually valid on AWS |
| CFN Route with missing GatewayId or numeric destination | Local ValidationError; exposed state unchanged | [Route target requirement and String properties](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-route.html) | `compileNetworkResource`; SDK rejection subtests; input is inside TemplateBody so SDK validation does not prevent the request |
| CFN Route with absent table or absent gateway | Local InternalFailure (500, known error-classification gap); no extra stack or VPC visible, survivor unchanged; corrected request can reuse rejected name | [CreateRoute RouteTableId/GatewayId](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateRoute.html) | `networkParent`, `createEC2Network`; SDK rejection subtests. These assert local atomic rollback, not AWS asynchronous rollback equivalence |
| CFN association where subnet and table reference different VPCs | Local InternalFailure (500, known error-classification gap); stacks and visible VPC state unchanged | [association same-VPC requirement](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-subnetroutetableassociation.html) | `createEC2Network`; SDK rejection subtest |
| EC2 CreateRoute, CreateRoute DryRun=True, DescribeRouteTables | Explicit local UnsupportedOperation, unchanged CFN/EC2 exposed state; never fake success or DryRunOperation | [CreateRoute/DryRun](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateRoute.html) | `serveEC2Read`; `test_unsupported_native_routes_never_report_success_or_mutate`. **Compatibility gap:** native routes, route-table discovery and AWS DryRun semantics unavailable |
| CFN CreateStack without local access token | Local HTTP 401 text (SDK ResponseParserError); no new stack/VPC | Local policy: [working contract](../AGENTS.md) | server access-token boundary; `test_missing_local_token_cannot_create_network`; not an AWS authentication claim |
| Delete selected stack while another network stack exists; then delete survivor | Selected CFN stack absent and its VPC absent from EC2; survivor outputs and VPC remain; final lists empty | [DeleteStack](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeleteStack.html), [DescribeVpcs](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeVpcs.html) | `StackStore.deleteChecked`, `StackStore.ec2Read`; SDK success test. Does not establish that unexposed subnet/table rows are absent |

## Reproduce and interpretation

From a checkout with the existing `scripts/requirements-test.txt` dependencies:

```sh
make build
python3 test/e2e/ec2_network_contract.py
make before-commit
make e2e
```

The SDK script runs four unittest methods, with seven malformed-template and
three unsupported-native-operation subcases. Each method starts a fresh process,
temporary DB and token authority using the existing E2E harness; every SDK client
has an explicit local endpoint, dummy credentials, disabled proxies and retries.
Cleanup is registered before assertions; the harness terminates only its own
process. A failing assertion is never converted to a skip or success. This focused
script is manually invoked; global CI was deliberately left unchanged.

Outputs/GetAtt agreement cannot establish actual route-table/association native
Describe consistency because those endpoints are unavailable on this base. Full
native route validation (destination normalization/duplicates, target/VPC
attachment checks and dependency cleanup), participant denial, restart, separate
team authorities, hidden-row rollback and packet/VM behavior remain outside the
new test's evidence. Existing containment and isolation suites stay unchanged.
This increment does not close all acceptance items in #467.

Observed gaps: relationship validation errors currently become HTTP 500
`InternalFailure` instead of a caller validation error; missing local-token
responses are HTTP 401 text and cannot be parsed as AWS Query XML. The suite
labels these as local observations and does not change runtime behavior to mask
them. These are not successful AWS conformance results.
