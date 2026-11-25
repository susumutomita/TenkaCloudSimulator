# Local EC2/VPC discovery and native VPC, subnet and instance lifecycle

`POST /`, `Content-Type: application/x-www-form-urlencoded`,
`X-Simulator-Service: ec2` and `X-Simulator-Region` select the native Query/XML
subset. Supply `Version=2016-11-15` and one of these actions:

| Kind | Actions |
| --- | --- |
| Discovery | `DescribeInstances`, `DescribeVpcs`, `DescribeSubnets` |
| VPC | `CreateVpc`, `DeleteVpc` |
| Subnet | `CreateSubnet`, `DeleteSubnet` |
| Instance | `RunInstances`, `TerminateInstances` |

Responses use the EC2 namespace and lower-camel-case `reservationSet/instancesSet/item`,
`vpcSet/item` and `subnetSet/item` fields. A request body is bounded to 64 KiB.
Every other action, including `StartInstances`, `StopInstances` and
`RebootInstances`, returns `UnsupportedOperation`.

## Discovery

Discovery reads CloudFormation-owned and EC2-owned resources from the same local
SQLite database, checking saved resource, region, ownership and network identity
consistency. It does not create resources or make cloud calls. The host's existing
local token and browser session authentication still applies. Selected exercise
identities need the matching `ec2:DescribeInstances`, `ec2:DescribeVpcs` or
`ec2:DescribeSubnets` action on `*`.
One database/token authority per team is still required; exercise identities are
not tenant isolation. Native STS session credentials are not supported here.

Discovery has no filters, selected-ID queries or pagination.
Unsupported fields are rejected, not silently ignored. More than 1000 matching
resources fails the whole request, rather than returning a truncated list.

Every item carries the local extension `simulatorManagedBy`: `cloudformation`
(delete it by deleting its stack) or `ec2` (created by a native create action and
removed by the matching native delete action). SDKs ignore the extension. Results
of both owners are returned together, ordered by resource ID.

Instances expose only IDs, image/type, subnet/VPC, security-group IDs and tags.
UserData, runtime plans and internal access information are never returned.
`instanceState` is deliberately absent: successful provisioning, a saved
`ready` journal entry or a launch record does not prove a live running VM. The
extension `simulatorRuntimeState` is the recorded `launch_pending`, `ready` or
`seal_pending` value of a CloudFormation-owned instance, `not_launched` for an
EC2-owned launch record, and `unknown` when absent or unrecognized. VPC CIDR and
tags are saved local configuration, not AWS availability evidence, so VPCs have no
`state` field. Subnets report `state` `available` and their `availabilityZone`.
Deleted ownership disappears from results; inconsistent surviving ownership fails
closed.

## Native VPC and subnet lifecycle

`CreateVpc`, `DeleteVpc`, `CreateSubnet` and `DeleteSubnet` manage networks owned
by EC2 itself, in the same SQLite state that the console, SDK clients and
CloudFormation use. They are what the console's **VPC → Create VPC**,
**VPC → Subnets → Create subnet** and the matching delete controls call. A VPC or
subnet is control-plane configuration only: no packet path, route, address pool,
VM or DNS service is created.

| Operation | Supported members | Result |
| --- | --- | --- |
| `CreateVpc` | `CidrBlock` (IPv4, `/16`–`/28`; host bits are cleared as in CloudFormation), `TagSpecification.1` with `ResourceType=vpc` and up to 50 tags, `InstanceTenancy=default`, `DryRun=false` | `vpc` with `vpcId` (`vpc-` + 17 hex), `cidrBlock`, `tagSet` and `simulatorManagedBy=ec2`. DNS support is on and DNS hostnames are off, as AWS defaults for a non-default VPC |
| `DeleteVpc` | `VpcId`, `DryRun=false` | `return` `true`; the row is removed in one transaction |
| `CreateSubnet` | `VpcId` (an EC2-owned VPC of the request region), `CidrBlock` (IPv4, `/16`–`/28`, inside the VPC CIDR, not overlapping another subnet of the VPC; host bits are cleared), `AvailabilityZone` (the region name plus `a`, `b` or `c`; default `a` when omitted, an empty value is rejected), `TagSpecification.1` with `ResourceType=subnet`, `DryRun=false` | `subnet` with `subnetId` (`subnet-` + 17 hex), `vpcId`, `cidrBlock`, `state` `available`, `availabilityZone`, `tagSet` and `simulatorManagedBy=ec2` |
| `DeleteSubnet` | `SubnetId`, `DryRun=false` | `return` `true`; the row is removed in one transaction |

Errors use AWS codes: `MissingParameter`, `InvalidParameterValue` (malformed CIDR,
tags or Availability Zone), `InvalidVpc.Range` and `InvalidSubnet.Range` (netmask
outside `/16`–`/28`, or a subnet outside its VPC), `InvalidSubnet.Conflict`
(overlapping subnet), `InvalidVpcID.Malformed`, `InvalidVpcID.NotFound`,
`InvalidSubnetID.Malformed`, `InvalidSubnetID.NotFound` (`NotFound` also for
another region's resource), `DependencyViolation`, and `VpcLimitExceeded` or
`SubnetLimitExceeded` (the shared local bound of 1000 network resources).
`DryRun=true`, IPv6, IPAM, dedicated tenancy, `AvailabilityZoneId`, a second tag
specification and every other member are rejected with `UnsupportedOperation`,
never ignored or reported as `DryRunOperation`.

`DeleteVpc` fails with `DependencyViolation` while the VPC has a subnet, and
`DeleteSubnet` while the subnet holds a launch record. Native route tables,
gateways, security groups, `ModifyVpcAttribute`, `ModifySubnetAttribute` and tag
edits after creation are not implemented.

## Native instance launch records

`RunInstances` saves one **launch record** below an EC2-owned subnet and
`TerminateInstances` removes it. They are what the console's
**EC2 → Launch instances** and **Terminate (delete) instance** call. A launch
record is the saved request: image ID, instance type, subnet, VPC and tags. No
guest, IP address, disk, network interface, security group or metadata service
exists for it, and it never starts or becomes `running`. This is true even when
the simulator was started with a Battle runtime: only an `AWS::EC2::Instance` in a
CloudFormation stack launches a guest there. A launch record is not a substitute
for an exercise that needs a running Linux server.

| Operation | Supported members | Result |
| --- | --- | --- |
| `RunInstances` | `ImageId` (`ami-` + 8 or 17 hex; only the format is checked, no image is looked up or downloaded), `InstanceType` (`t3.micro` or `t3.small`, required), `MinCount=1`, `MaxCount=1`, `SubnetId` (an EC2-owned subnet of the request region), `TagSpecification.1` with `ResourceType=instance` and up to 50 tags, `ClientToken` (1–64 printable ASCII characters without spaces), `DryRun=false` | `ownerId` and one `instancesSet/item` with `instanceId` (`i-` + 17 hex), `imageId`, `instanceType`, `subnetId`, `vpcId`, an empty `groupSet`, `tagSet`, `simulatorRuntimeState=not_launched` and `simulatorManagedBy=ec2`. No `instanceState`, address, DNS name, launch time or reservation ID is returned |
| `TerminateInstances` | `InstanceId.1` (exactly one), `DryRun=false` | One `instancesSet/item` with `instanceId` and `currentState` `terminated` (code 48). `previousState` is absent because no runtime state was ever recorded. The record is removed in one transaction and no longer appears in `DescribeInstances` |

A repeated `ClientToken` with the same image, type, subnet and tags returns the
record it first created instead of a second one; the same token with different
values fails with `IdempotentParameterMismatch`. A token is scoped to its region
and is forgotten when its record is terminated. The official SDKs send a fresh
token with every `RunInstances` call.

Errors use AWS codes: `MissingParameter`, `InvalidAMIID.Malformed`,
`InvalidParameterValue` (another instance type, a malformed client token or tags),
`InvalidSubnetID.Malformed`, `InvalidSubnetID.NotFound`,
`InvalidInstanceID.Malformed`, `InvalidInstanceID.NotFound`,
`IdempotentParameterMismatch` and `InstanceLimitExceeded` (the local bound of 100
launch records). Any other count than one, a second instance ID, `DryRun=true`, key
pairs, security groups, user data, instance profiles, block devices, network
interfaces, placement, launch templates and every other member are rejected with
`UnsupportedOperation`, never ignored.

## Ownership

Ownership is never shared or adopted in either direction:

- The native delete actions refuse CloudFormation-owned resources with
  `UnsupportedOperation`: `DeleteVpc`, `DeleteSubnet` and `TerminateInstances` on a
  stack's VPC, subnet or instance. Delete its stack instead.
- `CreateSubnet` refuses a CloudFormation-owned VPC, and `RunInstances` refuses a
  CloudFormation-owned subnet, with `UnsupportedOperation`.
- A CloudFormation subnet, route table, security group, gateway attachment,
  route table association or instance cannot reference an EC2-owned VPC or subnet,
  so the stack is refused and the EC2-owned resource is unchanged. A stack
  instance must name a subnet and security groups created by its own stack, in one
  VPC; any other `SubnetId` or `SecurityGroupIds`, including a missing ID or
  another stack's resource, fails `CreateStack` with `ValidationError` inside the
  create transaction, before a guest launch starts and before anything is stored.
  An instance ID that is already a launch record fails with
  `AlreadyExistsException`.
- Deleting a stack never removes an EC2-owned resource.

A launch record references its subnet with a SQLite foreign key, so a subnet row
that holds a record cannot be removed by any path. An EC2-owned row that carries a
stack claim, a subnet without an EC2-owned VPC, or a record whose stored columns
disagree makes the affected read and write actions fail closed with
`InternalError`.

## Authorization

Authorization is checked before state is read or written; a denied request changes
nothing. Exercise identities need:

| Action | IAM action on resource |
| --- | --- |
| `CreateVpc` | `ec2:CreateVpc` on `arn:aws:ec2:<region>:000000000000:vpc/*` |
| `DeleteVpc` | `ec2:DeleteVpc` on `arn:aws:ec2:<region>:000000000000:vpc/<vpc-id>` |
| `CreateSubnet` | `ec2:CreateSubnet` on `…:vpc/<vpc-id>` and on `…:subnet/*` |
| `DeleteSubnet` | `ec2:DeleteSubnet` on `…:subnet/<subnet-id>` |
| `RunInstances` | `ec2:RunInstances` on `…:instance/*`, on `arn:aws:ec2:<region>::image/<image-id>` and on `…:subnet/<subnet-id>` |
| `TerminateInstances` | `ec2:TerminateInstances` on `…:instance/<instance-id>` |

When tags are supplied, `ec2:CreateTags` is also needed on the new resource:
`…:vpc/*`, `…:subnet/*` or `…:instance/*`. `RunInstances` does not check a network
interface, volume or security group resource, because none is created. A console
session bound to one instance can only read that instance; it cannot create or
delete VPCs, subnets or launch records. Such a session never lists a launch
record, even when it was bound to a record's ID, because a record has no guest to
connect to. Each team still needs its own process, database and token authority.

## Verification

- `go test ./internal/aws -run TestEC2` and `go test ./internal/server -run EC2`:
  wire format, validation, IAM evaluation, ownership, idempotency, bounds and
  storage failures.
- `python3 test/e2e/ec2_native_vpc.py` and `python3 test/e2e/ec2_native_instance.py`:
  the official Python SDK against the real binary.
- `python3 test/e2e/ec2_readonly_console.py` and
  `python3 test/e2e/ec2_native_instance_console.py`: a real browser drives the
  console forms and the SDK reads the same state back.
- `python3 test/e2e/ec2_scalar_send.py`: the embedded API reference client sends
  the lifecycle with the browser session, and the SDK reads the same state back.

Terraform is not supported for these resources. The AWS provider reads each
resource back by ID or filter and waits for an instance to reach `running`; local
discovery has no selected-ID read, and a launch record never runs.
