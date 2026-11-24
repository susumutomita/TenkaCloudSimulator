# Local EC2/VPC discovery and native VPC lifecycle

`POST /`, `Content-Type: application/x-www-form-urlencoded`,
`X-Simulator-Service: ec2` and `X-Simulator-Region` select the native Query/XML
subset. Supply `Version=2016-11-15` and `Action=DescribeInstances`,
`Action=DescribeVpcs`, `Action=CreateVpc` or `Action=DeleteVpc`. Responses use the
EC2 namespace and lower-camel-case `reservationSet/instancesSet/item` and
`vpcSet/item` fields respectively. A request body is bounded to 64 KiB.

## Discovery

Discovery reads CloudFormation-owned resources from the same local SQLite database,
checking saved resource, region, stack ownership and network identity consistency.
It does not create resources or make cloud calls. The host's existing local token
and browser session authentication still applies. Selected exercise identities
need the matching `ec2:DescribeInstances` or `ec2:DescribeVpcs` action on `*`.
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
console's **VPC → Create VPC** and **Delete VPC** call. A VPC is control-plane
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
