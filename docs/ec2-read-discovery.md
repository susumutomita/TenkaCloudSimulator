# Local EC2/VPC discovery and native VPC lifecycle

`POST /`, `Content-Type: application/x-www-form-urlencoded`,
`X-Simulator-Service: ec2` and `X-Simulator-Region` select the native Query/XML
subset. Supply `Version=2016-11-15` and `Action=DescribeInstances`,
`Action=DescribeVpcs`, `Action=DescribeSubnets`, `Action=CreateVpc` or `Action=DeleteVpc`.
Discovery responses use the EC2 namespace and lower-camel-case
`reservationSet/instancesSet/item`, `vpcSet/item` and `subnetSet/item` fields.
A request body is bounded to 64 KiB.

## Discovery

Discovery reads CloudFormation-owned resources from the same local SQLite database,
checking saved resource, region, stack ownership and network identity consistency.
It does not create resources or make cloud calls. The host's existing local token
and browser session authentication still applies. Selected exercise identities
need the matching `ec2:DescribeInstances`, `ec2:DescribeVpcs` or
`ec2:DescribeSubnets` action on `*`.
One database/token authority per team is still required; exercise identities are
not tenant isolation. Native STS session credentials are not supported here.

Discovery has no filters, selected-ID queries or pagination.
Unsupported fields are rejected, not silently ignored. More than 1000 matching
resources fails the whole request, rather than returning a truncated list.

Instances expose only IDs, image/type, subnet/VPC, security-group IDs and tags.
UserData, runtime plans and internal access information are never returned.
`instanceState` is deliberately absent: successful provisioning or a saved
`ready` journal entry does not prove a live running VM. The extension
`simulatorRuntimeState` is the recorded `launch_pending`, `ready` or `seal_pending`
value; absent/unrecognized state is `unknown`. VPC CIDR and tags are saved local
configuration, not AWS availability evidence, so VPCs have no `state` field. Deleted ownership disappears from
results; inconsistent surviving ownership fails closed.

`DescribeVpcs` returns CloudFormation-owned and EC2-owned VPCs together, ordered by
VPC ID. Each item carries the local extension `simulatorManagedBy`:
`cloudformation` (delete it by deleting its stack) or `ec2` (created by
`CreateVpc`, deletable with `DeleteVpc`). SDKs ignore the extension.

## Native VPC lifecycle

`CreateVpc` and `DeleteVpc` manage VPCs owned by EC2 itself, in the same SQLite
state that the console, SDK clients and CloudFormation use. They are what the
console's **VPC → Create VPC → VPC only** and **Delete VPC** call. A VPC is control-plane
configuration only: no packet path, VM or DNS service is created.

| Operation | Supported members | Result |
| --- | --- | --- |
| `CreateVpc` | `CidrBlock` (IPv4, `/16`–`/28`; host bits are cleared as in CloudFormation), `TagSpecification.1` with `ResourceType=vpc` and up to 50 tags, `InstanceTenancy=default`, `DryRun=false` | `vpc` with `vpcId` (`vpc-` + 17 hex), `cidrBlock`, `tagSet` and `simulatorManagedBy=ec2`. DNS support is on and DNS hostnames are off, as AWS defaults for a non-default VPC |
| `DeleteVpc` | `VpcId`, `DryRun=false` | `return` `true`; the row is removed in one transaction |

Errors use AWS codes: `MissingParameter`, `InvalidParameterValue` (malformed CIDR
or tags), `InvalidVpc.Range` (netmask outside `/16`–`/28`), `InvalidVpcID.Malformed`,
`InvalidVpcID.NotFound` (also for another region's VPC), `DependencyViolation`
and `VpcLimitExceeded` (the shared local bound of 1000 network resources).
`DryRun=true`, IPv6, IPAM, dedicated tenancy, a second tag specification and every
other member are rejected with `UnsupportedOperation`, never ignored or reported as
`DryRunOperation`.

Ownership is never shared. `DeleteVpc` on a CloudFormation-owned VPC fails with
`UnsupportedOperation`; delete its stack instead. A CloudFormation subnet, route
table, security group or gateway attachment cannot reference an EC2-owned VPC, so
the stack is refused and the VPC is unchanged. Native subnets, route tables,
gateways, `ModifyVpcAttribute` and tag edits after creation are not implemented.

Exercise identities need `ec2:CreateVpc` on `arn:aws:ec2:<region>:000000000000:vpc/*`,
plus `ec2:CreateTags` on the same resource when tags are supplied, and
`ec2:DeleteVpc` on `arn:aws:ec2:<region>:000000000000:vpc/<vpc-id>`. Authorization is
checked before state is read or written; a denied request changes nothing. A
console session bound to one instance cannot create or delete VPCs. Each team still
needs its own process, database and token authority.

## Console: VPC and one subnet

When signed in as the simulator administrator, **VPC → Create VPC → VPC and
one subnet (CloudFormation-managed)** creates both resources in a new local
stack. The option is hidden for participant sessions, selected exercise
identities, scoped handoffs, and unauthenticated development mode. Participant
browser sessions cannot read CloudFormation outputs to verify the result.
Enter a stack name and two IPv4 CIDRs; the subnet is within the VPC and uses the region's
first local Availability Zone. Each range is /16–/28 and host bits are cleared
by the server. A Name tag is optional. This does not attach to an existing VPC
or create a route table, security group, gateway, VM or live packet path.

The Console sends a fixed template with `AWS::EC2::VPC` and `AWS::EC2::Subnet`
using the ordinary authenticated `CreateStack` route. Both resources require
their existing CloudFormation and EC2 permissions; no role is supplied by the
form. The Console reports success only after `DescribeStacks` confirms the
same stack ID, completed status and both output IDs. The saved VPC and subnet
are also available through native `DescribeVpcs` and `DescribeSubnets`. If a
request was interrupted or readback was denied, check CloudFormation before
trying again. Delete the stack there to remove both resources.

Focused checks:
- `node --test tools/console/test/ec2-vpc-subnet.test.mjs` after installing the pinned Console development dependencies.
- `SIMULATOR_BINARY=/absolute/path/to/simulator python3 test/console/ec2_vpc_subnet_native.py` in an isolated loopback runner with Node. It signs into a real simulator and checks the form-generated request through the native APIs, including denial, persisted readback and removal. It does not drive a browser.

Primary references: [CreateStack](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStack.html),
[Subnet resource](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-subnet.html),
[DescribeSubnets](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSubnets.html).
