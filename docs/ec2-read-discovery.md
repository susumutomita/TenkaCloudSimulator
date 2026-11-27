# Local EC2/VPC discovery and native VPC lifecycle

`POST /`, `Content-Type: application/x-www-form-urlencoded`,
`X-Simulator-Service: ec2` and `X-Simulator-Region` select the native Query/XML
subset. Supply `Version=2016-11-15` and `Action=DescribeInstances`,
`Action=DescribeVpcs`, `Action=DescribeSubnets`, `Action=DescribeSecurityGroups`,
`Action=DescribeRouteTables`, `Action=CreateVpc`, `Action=DeleteVpc`,
the [native subnet lifecycle and public IPv4 setting](ec2-native-subnet.md), plus
the named SG, ACL and route table operations below and the [Internet Gateway configuration operations](ec2-internet-gateways.md). Discovery uses the EC2 namespace and
lower-camel-case `reservationSet/instancesSet/item`, `vpcSet/item`,
`subnetSet/item`, `securityGroupInfo/item` and `routeTableSet/item` fields.
A request body is bounded to 64 KiB.

## Discovery

Discovery reads CloudFormation resources and native EC2 networking data from the
same SQLite database, checking saved resource, region, owner and network identity.
It does not create resources or make cloud calls. The host's existing local token
and browser session authentication still applies. Selected exercise identities
need the matching `ec2:DescribeInstances`, `ec2:DescribeVpcs`,
`ec2:DescribeSubnets`, `ec2:DescribeSecurityGroups` `ec2:DescribeRouteTables` or `ec2:DescribeInternetGateways` action on `*`.
One database/token authority per team is still required; exercise identities are
not tenant isolation. Native STS session credentials are not supported here.

Only the documented selectors are supported: security groups and network ACLs
accept IDs or `vpc-id`. Route tables accept an exact ID, a regional list or
[bounded pages](#route-table-readback), with an optional explicit subnet filter
for lists. Internet gateways accept IDs or [bounded unfiltered pages](ec2-internet-gateways.md).
Other EC2 reads do not support pagination. Over-bound results fail rather than
returning an unmarked truncated list.

Instances expose only IDs, image/type, subnet/VPC, security-group IDs and tags.
Select an instance in the console's **EC2 instances** list to see its saved
security-group IDs and tags, including intentionally empty tag values. These
come from the existing [DescribeInstances instance fields](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_Instance.html);
the discovery view does not attach security groups or edit tags. Creation uses
the separate opt-in workflow below.
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
operator’s **Create instance stack** button is disabled with that explanation.
Native `RunInstances` and `DescribeImages`
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
SIMULATOR_BINARY=/absolute/path/to/simulator JSDOM_MODULE=/absolute/path/to/jsdom \
  python3 test/console/ec2_instance_stack_launch_native.py
```

The prerequisite check uses a disposable authenticated simulator on numeric
loopback with no runtime flags. It tests the preview-only identity, the
unconfigured runtime error, regional readback and cleanup without a browser
or guest. The JSDOM preview fixture mounts the served app and uses the real
ValidateTemplate API. The separate form fixture checks the default operator,
participant visibility and a genuine local CloudFormation refusal, with
explicitly synthetic responses for the successful readback UI. Neither
fixture proves a guest boot.

The separate **EC2 instances → Create instance stack** form appears only for
the authenticated local operator with the administrator exercise identity and
no scoped handoff. To enable it, start with the [explicit Battle runtime](start.md#explicit-battle-runtime-linux-and-macos)
and an independently trusted pinned bundle. The live discovery flag says the
runtime is configured to accept attempts, not that a particular image or guest
is healthy. The form sends one regional `CreateStack` with one private
`t3.micro`, the selected configured AMI alias, same-stack VPC/subnet and a
security group without ingress rules, and an automatically named stack-owned IAM role/profile.
`CAPABILITY_IAM` explicitly acknowledges those resources, not an IAM grant.
It neither downloads an AMI nor configures public access. Delete the stack
through CloudFormation to remove its resources.

The console distinguishes an accepted stack ID from confirmed completion.
After acknowledgement it checks `DescribeStacks` at that exact ID, requires
`CREATE_COMPLETE`, and matches the typed instance, VPC, subnet, group, image
and instance type against the same region’s existing `DescribeInstances` read.
An uncertain response is not replayed; with a valid receipt, **Check stack and
instance status** repeats only the reads. An accepted or complete stack and the
saved EC2 record are not independent evidence that its guest is currently live.
The regional readback is limited to an 8 MiB response; exceeding that bound
leaves confirmation unavailable, not an empty result or a new create.

Primary references: [ValidateTemplate](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_ValidateTemplate.html),
[CreateStack](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStack.html),
[DescribeStacks](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_DescribeStacks.html),
[DescribeInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeInstances.html)
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
implicit egress, native attachment, or execution.

For this detached profile, `AuthorizeSecurityGroupIngress`,
`AuthorizeSecurityGroupEgress`, `RevokeSecurityGroupIngress` and
`RevokeSecurityGroupEgress` accept an explicit `GroupId`, exactly one
`IpPermissions` entry with protocol `tcp`, an inclusive port range of 1–65535,
and one canonical `IpRanges.CidrIp` contained within the owning VPC's IPv4 CIDR.
Each operation requires its own `ec2:` action on the exact group's ARN.
A duplicate tuple or a missing revoke returns a refusal, not success. Up to
60 rules per direction are saved; describing the group reports both actual
saved directions (egress is omitted when empty). Rules survive database
reopen and are removed atomically with that group. They do not appear in the
guest network-policy record and do not affect packets.

The native APIs reject CloudFormation-owned groups, IPv6, UDP/ICMP, all-protocol
or externally addressed ranges, group/prefix-list targets, multiple permissions
or ranges in one call, tags and descriptions on rules, group-name selectors,
rule-ID forms, and `DryRun=true`. `DescribeSecurityGroupRules` and
`ModifySecurityGroupRules` are outside this subset. Use `DescribeSecurityGroups`
to verify saved tuples; local mutation receipts are not firewall attestations.

In an ordinary signed-in Console, select a VPC in **VPC → Resource details →
Security groups**. CloudFormation-owned VPCs show their saved rules and tags
without mutation controls. Where the simulator advertises all three detached
native actions, EC2-managed VPCs can create a named group with a description,
view saved metadata and tags, and delete a selected group after confirmation.
When the bounded rule profile is advertised, the Console also lists both
directions, supports separate **Add inbound rule** and **Add outbound rule**
controls, and removes a selected tuple in its own confirmation dialog.
The Console reports changes only after a scoped read verifies the result.
After an unclear response, refresh the list before retrying. This pane does
not offer default groups, tag edits, attachment, or traffic effects.
An instance-scoped Console handoff does not receive these controls. API
permissions still apply to the selected identity. A refusal or incomplete
reply is not an empty list.

Opt-in served Console check: `SIMULATOR_BINARY=/absolute/path/to/simulator
python3 test/e2e/ec2_native_security_groups_gui.py` with installed Chromium,
Playwright and pinned SDK dependencies, on numeric loopback only.
The dedicated rules test `python3 test/console/ec2_native_sg_rules_sdk.py` uses
the pinned SDK and, when `JSDOM_MODULE` is supplied, exercises the exact served
application against the same disposable database. It does not qualify browser
layout/Chromium or network enforcement.

[CreateSecurityGroup](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateSecurityGroup.html),
[DescribeSecurityGroups](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSecurityGroups.html),
[DeleteSecurityGroup](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteSecurityGroup.html).

### Detached network ACL discovery and metadata

`CreateNetworkAcl`, `DescribeNetworkAcls` and `DeleteNetworkAcl` support the
[explicit detached configuration contract](ec2-detached-network-acls.md) in an
EC2-managed VPC. Group/ACL mutations and reads are refused in a single-instance
handoff. The dedicated Network ACLs page and the native EC2 API share the
same saved IPv4 rule configuration for detached custom ACLs. ACLs do not
create a default ACL, bind a subnet, or filter traffic. See the
[rule workflow and exact API limits](ec2-detached-network-acls.md#rule-profile-and-readback).

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

Use `Action=DescribeRouteTables` without filters to list CloudFormation and native
tables in this region, ordered by table ID. Each item gives the table and VPC IDs,
local owner, tags and explicit subnet associations (`main=false`).
CloudFormation tables can show one saved, enabled IPv4 default route to an
attached Internet Gateway; native tables have no route entries. Removing that
default route through the existing trusted provider removes it from the result,
including after restart; it is not a blackhole route. Deleting the owning stack
removes its tables.
The response checks the route, gateway attachment, table, subnet and same-VPC
ownership in one transaction. A missing, null or ambiguous saved route-enabled flag
is not interpreted as a deleted route. Inconsistent rows or declarations for a
discovered stack, or an exceeded bound, return `InternalError` without a partial
list. Discovery starts from surviving table, route and association rows or typed
ownership entries, not a scan of every stack template. If a stack loses all
such rows and claims, its table cannot be discovered or reported as corrupt. The normal EC2 discovery IAM check runs first; an
instance-scoped console handoff cannot enumerate tables.

For [new EC2-owned VPCs](#native-vpc-lifecycle), the main table has one system
association with `main=true` and no `subnetId`. Every table in that VPC has the
same read-only IPv4 CIDR route (`gatewayId=local`, `origin=CreateRouteTable`,
`state=active`). A route marked `active` still describes saved configuration,
**not** observed packet forwarding. The only supported filter is
`association.subnet-id`: specify one to eight exact lowercase 8- or 17-hex
subnet IDs. The values are ORed, and a match returns the entire route table.
The selector considers only current explicit associations, both native and
CloudFormation-managed. A well-formed no-match returns an empty list. The
filter runs **after** the same regional ownership and state validation; it does
not narrow IAM scope, hide inconsistent state, or include implicit main-table use.
The separate exact `RouteTableId.1` selector (SDK `RouteTableIds=[id]`) returns
at most one complete table, retaining the same regional validation and IAM.
An exact no-match is `InvalidRouteTableID.NotFound`. Combining the exact ID
and pagination returns `InvalidParameterCombination`. Exact ID plus a filter,
multiple IDs, other filters, and dry-run remain unsupported.

For the signed-in regional list, AWS Query-compatible `MaxResults=5..100`
opts into bounded pages. The API sorts by table ID and returns `nextToken`
only if more selected tables remain. Use that value as `NextToken`, with the
same actor, region, subnet selection and optional original page size; reordering
the same OR values is allowed. Each page requires the normal IAM grant
and is based on the whole validated regional projection before filtering.
A changed projection, changed selection or simulator restart invalidates the
token (`InvalidNextToken`): start the list again, rather than combining partial
snapshots. This pagination is read-only and stores no copies of snapshots.
Page XML is limited to 16 MiB and exact readback XML to 512 KiB; an oversized
result fails before a successful body. Calls with neither page parameter still
return one complete legacy list within the existing 1,000-resource storage
bound. Use pages for large inventories. The pinned local [SDK and served reference fixture](../test/console/ec2_route_table_pages_sdk.py)
checks a regional inventory above the single-page byte limit. The [Console fixture](../test/console/ec2_route_table_pages_console.py)
checks complete reads and unconfirmed writes in JSDOM against the same local API.

```python
for page in ec2.get_paginator("describe_route_tables").paginate(
    PaginationConfig={"PageSize": 100},
    # Optional, explicit subnet association only:
    # Filters=[{"Name": "association.subnet-id", "Values": [subnet_id]}],
):
    tables = page["RouteTables"]
```

In an ordinary signed-in **VPC** Console, select a CloudFormation-managed VPC
and look under **Route tables** in its details. Choose a table to see its enabled
default route, explicit subnet associations and tags. **Filter by subnet ID**
sends the same selector (one exact ID); clear it to return to the full view.
Incomplete IDs do not issue a read, and previous results are not shown while the
selector is changing. The Console makes the same
regional EC2 Query as the SDK with the current exercise identity. Both panes
collect and validate all 100-table pages before showing the selected VPC;
malformed, stale or incomplete continuations cannot publish a prefix. This selection is
not an IAM filter: the operation requires regional `ec2:DescribeRouteTables` on
`*`. A refusal or incomplete reply is never displayed as “no tables.” The
CloudFormation view offers no mutations. Scoped handoffs do not offer either
route table pane. Refresh, subnet, VPC, region and identity changes discard old replies.

For a verified **EC2-managed** VPC, **Route tables** supports local
[CreateRouteTable](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateRouteTable.html)
and [DeleteRouteTable](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteRouteTable.html).
Table creation/deletion and subnet association writes wait for a complete fresh
regional readback before reporting success; an unconfirmed write requires refresh,
not a replay. IPv4 route changes retain the separate exact-table readback.
For new VPCs, the pane distinguishes the main table, its read-only `local`
route, and subnets using the main table implicitly. That last list requires an
authorized `DescribeSubnets` read reconciled with the complete route table list.
If it cannot verify both, it does not show an empty fallback list as success.
The main table cannot be deleted directly. For other tables, the exact ID is
required to confirm deletion. Create accepts the selected
`VpcId`, an idempotency `ClientToken`, and optionally a Name tag. IAM checks
the VPC and created table resource, including `ec2:CreateTags` if a tag is
saved. Delete checks the exact table resource. A VPC with a custom table
cannot be deleted before that table. The persisted ledger retains up to 4,096
create token digests to prevent replay after deletion. A custom table starts
with no explicit associations or saved gateway routes; the `local` route
does not count toward the [16 saved IPv4 destinations](ec2-native-default-route.md).
Propagation and forwarding are not provided.

### Native subnet route table associations

Use the same authenticated local EC2 Query `POST /`, with `Version=2016-11-15`,
the selected region, and an existing **EC2-managed** route table and IPv4 subnet
in the same VPC and region. Use the host-issued simulator token or a signed-in
session; dummy SDK credentials do not authenticate. Single-instance handoffs
cannot read or change these associations. Use a local simulator endpoint in
an SDK and supply the `X-Simulator-Token` header as in the check below; never
point this workflow at AWS.

| Action | Supported request members | Successful EC2 Query XML |
| --- | --- | --- |
| [AssociateRouteTable](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_AssociateRouteTable.html) | Required `RouteTableId` and `SubnetId`; optional `DryRun=false` | `AssociateRouteTableResponse`: `requestId`, `associationId` (`rtbassoc-` plus 17 lowercase hex characters) and `associationState/state=associated` |
| [ReplaceRouteTableAssociation](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ReplaceRouteTableAssociation.html) | Required current `AssociationId` and new `RouteTableId`; optional `DryRun` | `ReplaceRouteTableAssociationResponse`: `requestId`, `newAssociationId` (different `rtbassoc-` ID), and `associationState/state=associated` |
| [DisassociateRouteTable](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DisassociateRouteTable.html) | Required `AssociationId`; optional `DryRun=false` | `DisassociateRouteTableResponse`: `requestId` and `return=true` |

Each action requires its EC2 operation grant on the concrete
`arn:aws:ec2:<region>:000000000000:route-table/<id>` and
`arn:aws:ec2:<region>:000000000000:subnet/<id>`. Disassociate resolves the pair
privately from the saved association. Replace resolves the same pair and also
requires permission on the destination table. An exercise identity gets
`UnauthorizedOperation` (403) for a denied or unresolved association; only
the effective local administrator gets `InvalidAssociationID.NotFound` (400)
for an absent native ID.

A subnet can have at most one explicit association; a table can have several
subnets. Repeating an associate call for the same pair returns its current ID,
including after restart. Associating to a different table with `AssociateRouteTable`
returns `Resource.AlreadyAssociated`; use `ReplaceRouteTableAssociation` to
change it without an intermediate detach. The destination must be a
different native table in the same VPC, region and simulator database.
A successful replacement atomically retires the old ID and associates the same
subnet to the new table with a fresh ID; other associations and saved routes
are unchanged. A retry with the retired ID does not reapply the change.
`DryRun=true` on replacement is a permission check against the requested
table and current association's table and subnet; no destination existence,
topology or commit is checked. A matching grant yields `DryRunOperation`, not
a successful write. The other two actions accept only `DryRun=false`.

Use `DescribeRouteTables` (requires `ec2:DescribeRouteTables` on `*`) to confirm
the relationship: `routeTableSet/item/associationSet/item` includes
`routeTableAssociationId`, `routeTableId`, `subnetId` and `main=false`. Use the
`association.subnet-id` filter for the destination, or read source and
destination by exact ID. The read-only API Reference preset uses the same
regional Query. In the signed-in Console, **Change subnet route table** offers
only saved explicit associations and other native tables from the current VPC.
It shows success only after a complete current regional read confirms the new
ID, absence of the retired ID, and other associations on both tables. Refresh
after an unconfirmed reply; the Console does not replay the write.

Remove an association before deleting its table or subnet; both deletes
otherwise return `DependencyViolation`. Replacement reuses one slot in the
same 1,000-resource network bound. `GatewayId`, `PublicIpv4Pool`, `ClientToken`,
main-table/gateway association replacement and CloudFormation mutations remain
outside this subset. Disassociation removes the explicit row. In a new native
VPC, that subnet then uses the main table in the saved configuration; there is
no new explicit association in the Query response. Older VPCs without a main
stay unchanged. There is no packet forwarding, guest or reachability guarantee.

Check with the repository-pinned boto3/botocore from `scripts/requirements-test.txt`:

```sh
SIMULATOR_BINARY=/absolute/path/to/simulator python3 test/console/ec2_native_route_association_sdk.py
SIMULATOR_BINARY=/absolute/path/to/simulator python3 test/console/ec2_native_route_association_replace_sdk.py
JSDOM_MODULE=/path/to/jsdom SIMULATOR_BINARY=/absolute/path/to/simulator \
  python3 test/console/ec2_native_route_association_replace_sdk.py --console
```

Both scripts use authenticated simulator processes over numeric loopback, not
an AWS account or guest. The optional Console check executes the served bundle in
JSDOM, not a full browser.

## Native VPC lifecycle

`CreateVpc` and `DeleteVpc` manage VPCs owned by EC2 itself, in the same SQLite
state that the console, SDK clients and CloudFormation use. They are what the
console's **VPC → Create VPC → VPC only** and **Delete VPC** call. A VPC is control-plane
configuration only: no packet path, VM or DNS service is created.

| Operation | Supported members | Result |
| --- | --- | --- |
| `CreateVpc` | `CidrBlock` (IPv4, `/16`–`/28`; host bits are cleared as in CloudFormation), `TagSpecification.1` with `ResourceType=vpc` and up to 50 tags, `InstanceTenancy=default`, `DryRun=false` | `vpc` with `vpcId` (`vpc-` + 17 hex), `cidrBlock`, `tagSet` and `simulatorManagedBy=ec2`. DNS support is on and DNS hostnames are off. The VPC and its main table are stored atomically (two network slots). |
| `DeleteVpc` | `VpcId`, `DryRun=false` | `return` `true`; the VPC and its main table, if present, are removed atomically if no other network resource depends on either. |

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
while any native subnet, group, ACL, custom table, or saved route still depends on it. Remove the
children through their own APIs, including [native gateways](ec2-internet-gateways.md) and [native IPv4 routes](ec2-native-default-route.md).
[Local DNS settings](ec2-vpc-dns-attributes.md) are available through
`DescribeVpcAttribute` and `ModifyVpcAttribute`. VPC endpoints and tag edits are not implemented.

As in the [AWS main and local route model](https://docs.aws.amazon.com/vpc/latest/userguide/subnet-route-tables.html),
new native VPCs have one main route table and a `local` route in each table.
The native VPC and main have stable persisted ownership; the main is deleted
with its parent only. Startup and reads never upgrade legacy VPCs. An operator can
[explicitly repair one saved native VPC on a stopped copy](ec2-legacy-native-main-repair.md).
Do not open repaired state with a binary older than the repair. Restore the
untouched pre-repair file if needed. Main-table reassignment and secondary CIDRs
are not supported.

Exercise identities need `ec2:CreateVpc` on `arn:aws:ec2:<region>:000000000000:vpc/*`,
plus `ec2:CreateTags` on the same resource when tags are supplied, and
`ec2:DeleteVpc` on `arn:aws:ec2:<region>:000000000000:vpc/<vpc-id>`. Authorization is
checked before state is read or written; a denied request changes nothing. A
console session bound to one instance cannot create or delete VPCs. Each team still
needs its own process, database and token authority.

## Console: VPC and one subnet

**VPC → Create VPC** opens a dedicated settings page. **VPC only** is selected
by default and uses native `CreateVpc`. **VPC and more**, available to the
signed-in simulator administrator, creates one new VPC and one subnet in a
CloudFormation stack, not the full AWS wizard. The option is hidden for participant sessions, selected exercise
identities, scoped handoffs, and unauthenticated development mode. Participant
browser sessions cannot read CloudFormation outputs to verify the result.
Enter a stack name and two IPv4 CIDRs; the subnet is within the VPC and uses the region's
first local Availability Zone. Each range is /16–/28 and host bits are cleared
by the server. A Name tag is optional. This does not attach to an existing VPC
or create a route table, security group, gateway, VM or live packet path.
IPAM, IPv6, encryption and automatic NAT gateway creation are not offered.
Only the optional Name tag is editable in this form.

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

Opt-in Chromium UI check using the candidate’s embedded console and actual
authenticated loopback APIs (not a guest or AWS):
```sh
SIMULATOR_BINARY=/absolute/path/to/simulator PLAYWRIGHT_MODULE=/absolute/path/to/playwright-core \
  node test/console/ec2_vpc_create_browser.cjs
```
Use `SCREENSHOTS_DIR` to retain synthetic desktop and mobile examples. Select
the Chromium distribution for the installed Playwright version.

Primary references: [Create a VPC](https://docs.aws.amazon.com/vpc/latest/userguide/create-vpc.html),
[CreateStack](https://docs.aws.amazon.com/AWSCloudFormation/latest/APIReference/API_CreateStack.html),
[Subnet resource](https://docs.aws.amazon.com/AWSCloudFormation/latest/TemplateReference/aws-resource-ec2-subnet.html),
[DescribeSubnets](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeSubnets.html).
