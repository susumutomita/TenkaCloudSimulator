# Local EC2/VPC discovery and native VPC lifecycle

`POST /`, `Content-Type: application/x-www-form-urlencoded`,
`X-Simulator-Service: ec2` and `X-Simulator-Region` select the native Query/XML
subset. Supply `Version=2016-11-15` and `Action=DescribeInstances`,
`Action=DescribeVpcs`, `Action=DescribeSubnets`, `Action=DescribeSecurityGroups`,
`Action=DescribeRouteTables`, `Action=CreateVpc`, `Action=DeleteVpc`,
`Action=CreateSubnet` or `Action=DeleteSubnet`, plus the named detached SG and ACL
operations below. Discovery uses the EC2 namespace and
lower-camel-case `reservationSet/instancesSet/item`, `vpcSet/item`,
`subnetSet/item`, `securityGroupInfo/item` and `routeTableSet/item` fields.
A request body is bounded to 64 KiB.

## Discovery

Discovery reads CloudFormation resources and native EC2 networking data from the
same SQLite database, checking saved resource, region, owner and network identity.
It does not create resources or make cloud calls. The host's existing local token
and browser session authentication still applies. Selected exercise identities
need the matching `ec2:DescribeInstances`, `ec2:DescribeVpcs`,
`ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups` or `ec2:DescribeRouteTables` action on `*`.
One database/token authority per team is still required; exercise identities are
not tenant isolation. Native STS session credentials are not supported here.

Only the documented SG and ACL selectors support IDs or the `vpc-id` filter;
there is no pagination. Other fields are rejected. Over-bound results fail rather
than returning a truncated list.

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

In **EC2 instances → Preview CloudFormation template**, paste inline YAML/JSON
(up to 51,200 UTF-8 bytes), or load the example and choose **Validate template**.
The ordinary Console sends only the regional `ValidateTemplate` query with your
current exercise identity; this action is not offered in single-instance handoffs.
The result shows declared parameter names, their `NoEcho` flags and default
presence (not values), and any IAM acknowledgement. Editing the draft, closing
the preview, or changing region or identity discards pending and old results.
Template content is not saved by this view. This is not the EC2 Launch Templates
API, a launch-permission or AMI check, or a runtime readiness test.

Local prerequisite check:

```sh
SIMULATOR_BINARY=/absolute/path/to/simulator python3 test/console/ec2_launch_preflight_native.py
# With jsdom installed, mount the served embedded Console and use its real local API:
SIMULATOR_BINARY=/absolute/path/to/simulator JSDOM_MODULE=/absolute/path/to/jsdom \
  python3 test/console/ec2_template_preview_native.py
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

`DescribeSecurityGroups` reads CloudFormation-owned groups and detached native
groups in one regional SQLite snapshot, ordered by group ID. Describe needs
`ec2:DescribeSecurityGroups` on `*`. It accepts up to 50 explicit `GroupId.N`
values and the optional `vpc-id` selector with up to 50 VPC IDs; these narrow
the response, not authorization. Other filters and paging are unsupported.
Legacy rows preserve local account, group ID, description, VPC ID, tags, and
persisted TCP/IPv4 ingress rules. Invalid ownership or text prevents a partial
success. No metadata returned here proves guest packet filtering.

On an **EC2-managed IPv4 VPC**, `CreateSecurityGroup` records a detached group
with explicit `VpcId`, `GroupName` and `GroupDescription` and optional native
tag specification. The name is unique case-insensitively within its VPC.
Creation authorizes the exact parent VPC, new security-group wildcard, and
allocated group ARN; tags require `ec2:CreateTags` on the group. A native group
has `groupName` and the local `simulatorSecurityGroupMode=detached-config-only`
marker. Delete requires the exact `GroupId` resource ARN. Stack-owned groups
cannot be adopted or deleted through this API. There is no default group,
rule mutation, egress, native attachment, or execution.

In an ordinary signed-in Console, select a VPC in **VPC → Resource details →
Security groups**. CloudFormation-owned VPCs show their saved rules and tags
without mutation controls. Where the simulator advertises all three detached
native actions, EC2-managed VPCs can create a named group with a description,
view saved metadata and tags, and delete a selected group after confirmation.
The Console reports changes only after a scoped read verifies the result.
After an unclear response, refresh the list before retrying. This pane does
not offer default groups, tag edits, rules, attachment, or traffic effects.
An instance-scoped Console handoff does not receive these controls. API
permissions still apply to the selected identity. A refusal or incomplete
reply is not an empty list.

Opt-in served Console check: `SIMULATOR_BINARY=/absolute/path/to/simulator
python3 test/e2e/ec2_native_security_groups_gui.py` with installed Chromium,
Playwright and pinned SDK dependencies, on numeric loopback only.

[CreateSecurityGroup](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateSecurityGroup.html),
[DescribeSecurityGroups](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSecurityGroups.html),
[DeleteSecurityGroup](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteSecurityGroup.html).

### Detached network ACL discovery and metadata

`CreateNetworkAcl`, `DescribeNetworkAcls` and `DeleteNetworkAcl` support the
[explicit detached configuration contract](ec2-detached-network-acls.md) in an
EC2-managed VPC. Group/ACL mutations and reads are refused in a single-instance
handoff. ACLs do not create a default ACL, do not bind a subnet, and do not
provide enforcement. The signed-in VPC Console manages only this detached metadata.

To verify all three child kinds in one owned VPC, run
`SIMULATOR_BINARY=/absolute/path/to/simulator python3 test/console/ec2_native_network_sdk.py`
with the pinned boto3/botocore versions from `scripts/requirements-test.txt`.
This uses isolated, authenticated processes over numeric loopback, including a
restart, exact grants and typed cleanup. It neither launches a guest nor talks
to AWS.

To check the actual served combination of subnet, detached groups, ACLs and route
tables alongside CloudFormation readback, run `test/console/ec2_native_subnet.py` with the built
`SIMULATOR_BINARY`, `JSDOM_MODULE` and pinned boto3/botocore. This checks
Cloudscape in JSDOM with the real API; it is not a Chromium layout assertion
or a mandatory E2E gate.

## Route-table readback

Use `Action=DescribeRouteTables` without filters to list explicit CloudFormation and
detached native tables in this region, ordered by table ID. Each item gives the table and VPC IDs,
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
`*`. A refusal or incomplete reply is never displayed as “no tables.” The
CloudFormation view offers no mutations. Scoped handoffs do not offer either
route table pane. Refresh, VPC, region and identity changes discard old replies.

For a verified **EC2-managed** VPC, **Route tables** offers local
[CreateRouteTable](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateRouteTable.html)
and [DeleteRouteTable](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteRouteTable.html).
It uses a fresh readback of the same VPC and region before reporting success;
a write without confirmed readback requires refresh. The native route pane stops
reading after 16 MiB and displays the snapshot as unavailable, not as an empty list.
When a table is present,
its exact ID is required to confirm deletion. Create accepts the selected
`VpcId`, an idempotency `ClientToken`, and optionally a Name tag. IAM checks
the VPC and created table resource, including `ec2:CreateTags` if a tag is
saved. Delete checks the exact table resource. A VPC with dependent metadata
cannot be deleted before its table. The persisted ledger retains up to 4,096
create token digests to prevent replay after deletion. These tables intentionally
have no local or default routes and no associations; native route edits,
associations, propagation and forwarding are not provided.
Check: `go test ./internal/aws -run 'TestEC2DescribeRouteTables|TestEC2NativeRouteTable' -count=1`.

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
the stack is refused and the VPC is unchanged. For an EC2-owned VPC,
[the native subnet lifecycle](ec2-native-subnet.md) is available without changing ownership.
Detached security groups, network ACLs and route tables also use the existing
EC2-owned VPC and shared 1,000-resource bound. Deletion of a native VPC is refused
while any native subnet, group, ACL or route table depends on it. Remove the
children through their own APIs. Native route entry or gateway creation,
subnet associations, `ModifyVpcAttribute`,
VPC endpoints and tag edits are not implemented.

**Existing databases remain readable when upgraded. Do not downgrade a
database after creating the new native child kinds:** earlier Simulators do not
recognize those types and can reject VPC discovery. No migration-back or
downgrade procedure has been verified.

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
