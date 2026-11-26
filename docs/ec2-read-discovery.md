# Local EC2/VPC discovery and native VPC lifecycle

`POST /`, `Content-Type: application/x-www-form-urlencoded`,
`X-Simulator-Service: ec2` and `X-Simulator-Region` select the native Query/XML
subset. Supply `Version=2016-11-15` and `Action=DescribeInstances`,
`Action=DescribeVpcs`, `Action=DescribeSubnets`, `Action=DescribeSecurityGroups`,
`Action=DescribeRouteTables`, `Action=CreateVpc` or `Action=DeleteVpc`. Discovery uses the EC2 namespace and
lower-camel-case `reservationSet/instancesSet/item`, `vpcSet/item`,
`subnetSet/item`, `securityGroupInfo/item` and `routeTableSet/item` fields.
A request body is bounded to 64 KiB.

## Discovery

Discovery reads CloudFormation-owned resources from the same local SQLite database,
checking saved resource, region, stack ownership and network identity consistency.
It does not create resources or make cloud calls. The host's existing local token
and browser session authentication still applies. Selected exercise identities
need the matching `ec2:DescribeInstances`, `ec2:DescribeVpcs`,
`ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups` or `ec2:DescribeRouteTables` action on `*`.
One database/token authority per team is still required; exercise identities are
not tenant isolation. Native STS session credentials are not supported here.

Discovery has no filters, selected-ID queries or pagination.
Unsupported fields are rejected, not silently ignored. More than 1000 matching
resources fails the whole request, rather than returning a truncated list.

Instances expose only IDs, image/type, subnet/VPC, security-group IDs and tags.
Select an instance in the console's **EC2 instances** list to see its saved
security-group IDs and tags, including intentionally empty tag values. These
come from the existing [DescribeInstances instance fields](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_Instance.html);
the console does not attach security groups, edit tags or create an instance.
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

### Instance launch: preview is not a launch

Default simulator startup does not configure an instance runtime, and the
EC2 console has no Launch action. Native `RunInstances` and `DescribeImages`
are unsupported. A signed-in caller with `cloudformation:ValidateTemplate` can
preview the declaration of a stack containing one instance and a same-stack
instance profile, but this does not certify its AMI, capacity or runtime.
In normal startup, an authorized `CreateStack` for that declaration returns
`NotImplemented` instead of creating a stack or a metadata-only instance.

Local prerequisite check:

```sh
SIMULATOR_BINARY=/absolute/path/to/simulator python3 test/console/ec2_launch_preflight_native.py
```

The check starts a disposable authenticated simulator on numeric loopback with
in-memory state and no guest-runtime flags. It tests the preview-only browser
identity, the unconfigured runtime error, the two regional reads, and cleanup.
It does not run a browser or guest. Only add a launch action after the chosen
pinned runtime and its authorized native or CloudFormation path can demonstrably
start and clean up a guest on a qualified runner. Do not infer readiness from a
template preview or a saved runtime-state label.

Primary references: [ValidateTemplate](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ValidateTemplate.html),
[CreateStack](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStack.html)
and [RunInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_RunInstances.html).

### Security-group readback

`DescribeSecurityGroups` lists CloudFormation-owned groups in the selected
region, ordered by group ID. EC2-owned standalone VPCs do not acquire groups.
Each item reports the local account ID, group ID, description, VPC ID, optional
saved tags and current ingress rules (TCP ports and canonical IPv4 CIDRs).
The group and its VPC must be typed members of the same stack. The query
reads persisted rules, not the template, and does not change state. A
single-instance scoped session cannot list security groups.

An empty region returns an empty list. Inconsistent persisted ownership or
text that cannot round-trip through XML 1.0 fails the whole request with
`InternalError`, leaving existing data unchanged. Valid Unicode, tab,
line feed, and carriage return are preserved. This read does not support
group filters/ID selectors, paging, group names, egress, IPv6, referenced
groups, or rule mutation. It does not simulate or verify packet filtering.

In the signed-in **VPC** Console, select a CloudFormation-managed VPC and
choose a group under **Resource details → Security groups**. The pane uses
the same native regional read with your current identity and shows only
groups for the selected VPC. It shows the description, TCP ports, IPv4
CIDRs and tags, including empty tag values. **Refresh security groups**
re-reads saved configuration. A refused or unsuccessful read is not shown
as an empty list. No read is sent for EC2-managed VPCs or inside a scoped
handoff. The pane cannot create, attach, edit or delete a security group.

[EC2 API reference: DescribeSecurityGroups](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSecurityGroups.html).
Focused check: `go test ./internal/aws -run '^TestEC2DescribeSecurityGroupsContract$' -count=1`.

## Route-table readback

Use `Action=DescribeRouteTables` without filters to list explicit, CloudFormation-owned
tables in this region, ordered by table ID. Each item gives the table and VPC IDs,
local owner, tags, explicit subnet associations (`main=false`) and saved, enabled
IPv4 default routes to an attached Internet Gateway. Removing the default route
through the existing trusted provider removes it from the result, including after
restart; it is not a blackhole route. Deleting the owning stack removes its tables.
The response checks the route, gateway attachment, table, subnet and same-VPC
ownership in one transaction. A missing, null or ambiguous saved route-enabled flag
is not interpreted as a deleted route. Inconsistent rows or declarations for a
discovered stack, or an exceeded bound, return `InternalError` without a partial
list. Discovery starts from surviving table, route and association rows or typed
ownership entries, not a scan of every stack template. If a stack loses all
such rows and claims, its table cannot be discovered or reported as corrupt. The normal EC2 discovery IAM check runs first; an
instance-scoped console handoff cannot enumerate tables.

Unlike [AWS DescribeRouteTables](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeRouteTables.html),
there are no implicit main tables, local routes or associations, including for
native EC2 VPCs. `state=active` describes the configured gateway route, **not**
observed packet forwarding. Table IDs, filters, pagination and dry-run are rejected.
In an ordinary signed-in **VPC** Console, select a CloudFormation-managed VPC
and look under **Route tables** in its details. Choose a table to see its enabled
default route, explicit subnet associations and tags. The Console makes the same
regional EC2 Query as the SDK with the current exercise identity, validates the
whole response, then selects the matching VPC in the browser. This selection is
not an IAM filter: the operation requires regional `ec2:DescribeRouteTables` on
`*`. A refusal or an incomplete reply is never displayed as “no tables.” A native
EC2 VPC is explicitly marked inapplicable without a lookup; scoped handoffs do
not offer the pane. Refresh, VPC, region and identity changes discard old replies.
This view adds no mutations or forwarding engine.
Check: `go test ./internal/aws -run '^TestEC2DescribeRouteTablesReadback$' -count=1`.

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
the stack is refused and the VPC is unchanged. Native creation or mutation of subnets, route tables and
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
