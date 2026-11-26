# Bounded EC2/CloudFormation network contracts (#467)

Reviewed baseline: [signed main `8772cc3`][main-reviewed] (2026-11-26).
The first #467 inventory used `d2dd8bca`; it predates native route-table
readback and explicit native subnet associations. This is a contract and
evidence record, not an expansion of the [EC2/VPC API](ec2-read-discovery.md).
[#56][vpc-owner] tracked native VPC work; [#465][vm-owner] and [#570][instance-placement]
cover VM composition and EC2 instance placement, not this network-only lifecycle.

## Sources and execution evidence

- **AWS specification:** the official service and CloudFormation references
  below define request shapes and resource relationships. Simulator-specific
  errors, immediate SQLite commits and lack of implicit routes are local
  boundaries, not claims of AWS parity.
- **Comparison source, not an executed oracle:** AWS Emulator
  [`7753983`][aws-emulator-pin]. Its [CreateRoute handler][emulator-create-route]
  checks destination and target exclusivity. Its [CFN network template][emulator-cfn]
  links VPC, subnet, route table and association and also declares a gateway
  endpoint. The simulator tests were authored independently. No upstream code
  or fixtures were copied, and neither AWS Emulator nor real AWS was run.
- **CFN SDK evidence:** [#562][parity-pr] and its [hosted platform-audit
  job][parity-ci] on `bcff9a708335bb0dbea706c775d3189e52e8f0a1` show all five
  then-existing `NetworkContract` tests passing (10 tests in the whole runner).
  The [CFN restart test][restart-pr] also uses two successive processes on one
  SQLite file. The [participant policy test][cfn-test] uses a real local session,
  created by the operator, and separate participant and operator SDK clients.
  No EC2 instance or regional infrastructure is provisioned.
- **Native association evidence:** [#560][association-pr] adds a separate
  [pinned SDK fixture][association-test] and [Go wire tests][association-wire].
  The fixture was exercised on the joined implementation predecessor recorded
  in that PR; it uses two separate simulator processes, stores and token
  authorities, and restarts one store. The PR's [hosted CI][association-ci]
  passed on `db08028986483500444c9c3b554d2a703e9d39ed`. The native SDK fixture
  is opt-in, not another `NetworkContract` method in `platform-audit`.

## Operation, input, state and test matrix

Unless stated otherwise, CFN evidence below refers to the [`NetworkContract` class][cfn-test].
A passing API call or returned association ID is not proof of packet forwarding.

| Operation and input | Expected local response and state | Official source | Evidence |
| --- | --- | --- | --- |
| CFN `CreateStack`: noncanonical VPC CIDR, forward VPC/subnet/table/association references | VPC CIDR becomes `10.67.0.0/16`. The VPC, subnet, table and association physical IDs agree with EC2 native Describe. The subnet/table belong to the returned VPC; the association names the exact pair with `Main=false`. | [VPC][cfn-vpc], [Subnet][cfn-subnet], [RouteTable][cfn-table], [SubnetRouteTableAssociation][cfn-association], [DescribeRouteTables][describe-tables] | `test_refs_canonical_cidr_and_scoped_cleanup` |
| CFN `AWS::EC2::Route`: `203.0.113.19/0` and a declared internet gateway attachment | Completed local stack; EC2 readback contains the canonical `0.0.0.0/0`, the gateway ID from CFN, `Origin=CreateRoute` and configured `State=active`. The status does not measure reachability. | [Route][cfn-route], [DependsOn][cfn-depends-on] | `test_refs_canonical_cidr_and_scoped_cleanup` |
| CFN route with two targets, IPv4 plus IPv6 destinations, missing target or a numeric CIDR | `ValidationError`; existing stack, VPC, subnet and route-table snapshots are unchanged. NAT, IPv6 and prefix-list routing are not positive local cases. | [Route properties and target exclusivity][cfn-route] | `test_invalid_network_create_preserves_existing_state` |
| CFN missing route table/gateway, cross-VPC subnet/table pair, or a duplicate canonical destination in one table | Non-success; the exposed snapshots are unchanged. The missing-table and duplicate-route stack names can be reused. The tests deliberately do not require a particular AWS error for these relationship failures. | [Route table, gateway and canonical destination][create-route], [Same-VPC association][cfn-association] | `test_invalid_network_create_preserves_existing_state` |
| CFN `DeleteStack` with another completed network stack present | Deleted stack/VPC/subnet/table and its association disappear; the other stack's outputs and subnet/table records stay unchanged. Another region was empty; deleting the survivor leaves all four SDK inventories empty. | [DeleteStack][delete-stack], [DescribeRouteTables][describe-tables] | `test_refs_canonical_cidr_and_scoped_cleanup` |
| Restart the simulator with the same SQLite file after CFN `CreateStack` with an explicit subnet association | CFN outputs and committed `CREATE_COMPLETE` event physical IDs still match the EC2 VPC, subnet, table and explicit association. Deleting by the original stack ID leaves those inventories empty; retained `DELETE_COMPLETE` events identify the same resources. | [DescribeStacks][describe-stacks], [DescribeStackEvents][describe-events], [StackEvent][stack-event], [DescribeVpcs][describe-vpcs], [DescribeSubnets][describe-subnets], [DescribeRouteTables][describe-tables] | `test_stack_network_persists_across_process_restart`; graceful process restart of the local control plane, not a crash or real AWS deletion timing |
| EC2 `DescribeRouteTables`, `CreateRoute`, `CreateRoute` with `DryRun=true`, and `DeleteRoute` against the saved table | Regional and one exact-ID Describe return the CFN route and its explicit association. Actual route mutations return `UnsupportedOperation`. An authorized `DryRun` is permission-only (`DryRunOperation`); it cannot adopt the table. All exposed inventories remain unchanged. | [DescribeRouteTables][describe-tables], [CreateRoute][create-route], [DeleteRoute][delete-route] | `test_native_route_table_read_and_rejected_mutations` |
| Native `CreateRoute`, exact table readback and `DeleteRoute` | Up to 16 explicit canonical IPv4 destinations per native table, optionally /0, to the one attached native gateway of that table’s VPC. Exact changes preserve sibling routes and use the same SQLite graph through EC2 reads and Console. No replacement or cross-owner adoption. Route or detach/table cleanup is verified with restart and separate team persistence. | [Native IPv4 route contract and AWS references](ec2-native-default-route.md) | [Pinned SDK and signed-in Console checks](../test/console/ec2_native_default_route_sdk.py); configuration only, not packet forwarding |
| EC2 `CreateVpc` without a route table, then a separate bare CFN table | The native VPC does not synthesize a main table. The independently created CFN table has no routes or associations. AWS local routes are not synthesized. | [CreateVpc][create-vpc], [AWS main and local routes][aws-route-tables] | `test_native_vpc_has_no_implicit_table_and_bare_table_has_no_children` |
| Native `AssociateRouteTable(RouteTableId, SubnetId)` on an existing same-VPC EC2-owned pair | Exact pair permissions apply; the association is explicit and has a stable ID across retries and restart. A different table for the same subnet is rejected. EC2 reads the typed association from its own regional store; it does not adopt a CFN association. | [AssociateRouteTable][associate-table], [DescribeRouteTables][describe-tables], [Same-VPC relationship][cfn-association] | [#560][association-pr]: [native SDK fixture][association-test] and [wire tests][association-wire] |
| EC2 `DescribeRouteTables` with `association.subnet-id` | One to eight exact IDs union the whole tables for current explicit associations, for either owner. Absent, detached or stale subnet IDs do not match; another region cannot match. No change to regional integrity checks or IAM. | [DescribeRouteTables][describe-tables], [Filter][ec2-filter], [RouteTableAssociation][ec2-association-type] | `test_route_table_filter_observes_only_live_explicit_subnet_associations`; signed-in CFN console uses the same filter |
| Regional EC2 `DescribeRouteTables` with `MaxResults` and `NextToken` | Full regional validation precedes selection and bounded XML pages, including optional explicit subnet OR-filter. A changed actor, region, filter, limit, projection or process cannot continue the old page. Exact-ID and legacy unpaged reads are separate. Console publication and table/association write confirmation require the complete current list. | [DescribeRouteTables][describe-tables], [EC2 Query pagination](https://docs.aws.amazon.com/ec2/latest/devguide/Query-Requests.html) | [Pinned SDK and served reference](../test/console/ec2_route_table_pages_sdk.py) observe a valid 181-table inventory above the single-page byte budget, and [served Console](../test/console/ec2_route_table_pages_console.py) exercises the native and CloudFormation views and uncertain readback in JSDOM. |
| Native `DisassociateRouteTable(AssociationId)` and dependent deletes | One exact saved relation disappears. An unauthorized caller cannot remove it; table/subnet deletion is refused while it exists, then succeeds after removal. A separate team's snapshot is unchanged. No fallback to an implicit main table. | [DisassociateRouteTable][disassociate-table] (AWS **does** fall back to the main table), [DeleteRouteTable][delete-table] | [#560][association-pr]: [native SDK fixture][association-test] |
| CFN `CreateStack` / `DeleteStack`: valid local participant token with the stack actions and route-table association permissions on the table but not its subnet | Both calls get the local CFN `AccessDenied` (HTTP 403) for the missing subnet permission. CFN/EC2 inventories and captured live history do not change. The operator can use the rejected name, read the resulting explicit association, and clean up without changing the other stack. | [CFN access control][cfn-access], [Associate IAM][associate-iam], [Disassociate IAM][disassociate-iam], [SubnetRouteTableAssociation][cfn-association] | `test_participant_missing_subnet_grant_preserves_stack_network`; local policy and synchronous refusal, not AWS service-role/error parity |
| CFN `CreateStack` without any local token | HTTP 401 text, not AWS Query XML; SDK parsing fails and all inventories stay unchanged. | [Simulator access contract](../AGENTS.md), not AWS authentication | `test_missing_local_token_cannot_create_network` |
| Internet Gateway allocation, native VPC link and detached deletion | Native EC2 allocation is detached. Native Describe and CFN agree on the existing stack gateway ID and its saved VPC link; native Delete refuses that gateway and preserves its stack and route. The native gateway and a configured native VPC link survive a process restart. Another VPC or the second gateway cannot take an occupied endpoint, a wrong detach pair leaves the link unchanged, and neither attached parent can be deleted. After detaching, both parents can be removed. Both endpoints require exact IAM permission. Two team stores stay independent. The paginated SDK/console run includes an inventory too large for one console response, and verifies exact-ID write readback and stale-page suppression. | [AWS Create/Describe/Attach/Detach/Delete and local limits](ec2-internet-gateways.md) | [Pinned SDK and signed-in console receipt](../test/console/ec2_internet_gateway_sdk.py); no native route creation or traffic |

## Reproduce and interpret

Using the repository-pinned boto3/botocore in `scripts/requirements-test.txt`,
build the simulator and use the existing loopback-only entry points:

```sh
make build
python3 test/platform/sdk-lifecycle.py -v
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" python3 test/console/ec2_native_route_association_sdk.py
SIMULATOR_BINARY="$PWD/dist/tenkacloud-simulator" JSDOM_MODULE=/path/to/jsdom \
  python3 test/console/ec2_route_table_subnet_filter.py
```

The CFN suite has eight methods, including restart, participant refusal and the subnet filter,
with eight invalid-template and three native-to-CFN admission subcases. It uses
fresh local token/store state and dummy SDK signing credentials. It is imported by the
[platform runner][platform-runner], not gated on the optional full E2E job.

**Still outside these receipts:** replacement routes, routes to local or reserved ranges, IPv6, prefix lists and alternate targets, full AWS IAM condition parity, and gateway
[VPC endpoints][cfn-endpoint]. The participant refusal covers one local
policy that omits the subnet grant, not all IAM policies, service roles or
organizations. The CFN restart receipt covers an orderly shutdown, not process
loss in the middle of an operation or a separate team. Snapshot equality and failed-name
reuse cover exposed state, not an exhaustive audit of hidden SQLite rows. The
public comparator was read only; no emulator differential run, AWS operation,
real VM, packet flow, or challenge scoring is established here. #467 remains open.

[main-reviewed]: https://github.com/susumutomita/TenkaCloudSimulator/tree/8772cc3c419e5951fb5bd0d66d586e9f8350ef32
[vpc-owner]: https://github.com/susumutomita/TenkaCloudSimulator/issues/56
[vm-owner]: https://github.com/susumutomita/TenkaCloudSimulator/pull/465
[instance-placement]: https://github.com/susumutomita/TenkaCloudSimulator/pull/570
[aws-emulator-pin]: https://github.com/aws/aws-emulator/tree/7753983c6e495ec3612695b1336bf4936fc7b7fd
[emulator-create-route]: https://github.com/aws/aws-emulator/blob/7753983c6e495ec3612695b1336bf4936fc7b7fd/services/service-com-amazonaws-ec2-amazonec2/src/ops/create_route.rs#L173-L365
[emulator-cfn]: https://github.com/aws/aws-emulator/blob/7753983c6e495ec3612695b1336bf4936fc7b7fd/core/router/tests/all/cloudformation_ec2_network_stack_routes.rs#L271-L328
[parity-pr]: https://github.com/susumutomita/TenkaCloudSimulator/pull/562
[parity-ci]: https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/45776139395/job/139760135713
[association-pr]: https://github.com/susumutomita/TenkaCloudSimulator/pull/560
[association-ci]: https://github.com/susumutomita/TenkaCloudSimulator/actions/runs/45774002539
[restart-pr]: https://github.com/susumutomita/TenkaCloudSimulator/pull/573
[cfn-test]: ../test/e2e/ec2_network_contract.py
[association-test]: ../test/console/ec2_native_route_association_sdk.py
[association-wire]: ../internal/aws/ec2_route_association_wire_test.go
[platform-runner]: ../test/platform/sdk-lifecycle.py
[cfn-vpc]: https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-vpc.html
[cfn-subnet]: https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-subnet.html
[cfn-table]: https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-routetable.html
[cfn-association]: https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-subnetroutetableassociation.html
[cfn-route]: https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-route.html
[cfn-depends-on]: https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/aws-attribute-dependson.html
[create-route]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateRoute.html
[delete-route]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteRoute.html
[delete-stack]: https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DeleteStack.html
[create-vpc]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateVpc.html
[describe-stacks]: https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStacks.html
[describe-events]: https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStackEvents.html
[stack-event]: https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_StackEvent.html
[describe-vpcs]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeVpcs.html
[describe-subnets]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSubnets.html
[describe-tables]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeRouteTables.html
[ec2-filter]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_Filter.html
[ec2-association-type]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_RouteTableAssociation.html
[cfn-access]: https://docs.aws.amazon.com/AWSCloudFormation/latest/UserGuide/control-access-with-iam.html
[associate-iam]: https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#amazonec2-AssociateRouteTable
[disassociate-iam]: https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#amazonec2-DisassociateRouteTable
[associate-table]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_AssociateRouteTable.html
[disassociate-table]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DisassociateRouteTable.html
[delete-table]: https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteRouteTable.html
[aws-route-tables]: https://docs.aws.amazon.com/vpc/latest/userguide/subnet-route-tables.html
[cfn-endpoint]: https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-vpcendpoint.html
