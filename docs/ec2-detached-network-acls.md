# EC2: IPv4 network ACLs and native subnet associations

Issue [#605](https://github.com/susumutomita/TenkaCloudSimulator/issues/605)
tracks the broader VPC workflow. The simulator stores custom IPv4 ACLs,
bounded rule configuration, per-VPC defaults for newly created native VPCs,
and a single saved association per eligible native subnet. These are
**saved configuration only**; no packet filtering, IPv6, Terraform workflow
or CloudFormation ACL lifecycle is implemented.

In the signed-in **VPC → Resource details → Network ACLs** pane, select a
verified EC2-managed VPC. If the detached ACL capability is present, you can
list, create with an optional Name, and delete by confirming the exact ACL ID.
The Console checks its scoped readback before reporting success. If a result
cannot be confirmed, refresh before retrying. For a CloudFormation VPC the
pane makes no ACL query or mutation. Instance-scoped handoffs have no controls.
To edit rules, open **VPC → Network ACLs**, select the same ACL ID and
choose **Inbound rules** or **Outbound rules**. Add, edit or delete one rule at
a time. Rule 32767 is fixed. The editor verifies the current VPC and ACL,
sends the corresponding native operation, and checks the saved rule in
`DescribeNetworkAcls`. If verification fails, refresh before trying again.
Both the VPC details pane and the rule editor restrict writes to detached
custom ACLs. Association changes start from the subnet page.

Both the SDK and Console use authenticated EC2 Query/XML, version `2016-11-15`.

| Operation | Supported arguments and effect |
| --- | --- |
| [CreateNetworkAcl](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateNetworkAcl.html) | `VpcId`, optional `ClientToken`, optional one `TagSpecification` of type `network-acl` with at most 50 tags. Creates one durable ACL in the caller's region under the EC2-managed parent. |
| [DescribeNetworkAcls](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeNetworkAcls.html) | All supported native ACLs, up to 50 explicit ACL IDs or one exact `vpc-id` filter with up to 50 VPC IDs (also accepted with ACL IDs), or one `association.subnet-id` filter with one exact subnet ID. Other filters and pagination return an error. |
| [DeleteNetworkAcl](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteNetworkAcl.html) | `NetworkAclId`. Deletes only the requested detached ACL and its saved rules. Missing IDs return an error. Delete ACLs before deleting their VPC. |
| [CreateNetworkAclEntry](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateNetworkAclEntry.html) | `NetworkAclId`, `RuleNumber`, `Egress`, `Protocol`, `RuleAction`, `CidrBlock`, plus `PortRange.From` and `PortRange.To` for TCP/UDP. Adds one numbered rule. Duplicate numbers in that direction return `NetworkAclEntryAlreadyExists`. |
| [ReplaceNetworkAclEntry](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ReplaceNetworkAclEntry.html) | Same arguments as create. Replaces only that existing direction and rule number. An identical normalized replacement leaves the saved generation unchanged. |
| [DeleteNetworkAclEntry](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteNetworkAclEntry.html) | `NetworkAclId`, `RuleNumber`, `Egress`. Removes only that existing rule. Replace and delete return `InvalidNetworkAclEntry.NotFound` if it does not exist. |

## Change a subnet's saved network ACL

Create a new EC2-managed IPv4 VPC and a native IPv4 subnet. The VPC gets a
stored default ACL, and the subnet starts with one persisted association to
it. This is not inferred during a read. Existing VPCs created before this
support, and CloudFormation-managed VPCs or subnets, are not automatically
enrolled and cannot use this association workflow.

In **VPC → Subnets**, open that subnet and open **Actions → Edit network
ACL association** in the Network ACL pane. Choose a different ACL from the
same VPC, review its saved IPv4 rules, and save. To switch back, choose the
default ACL. The Console checks the exact current association before sending
one request and matches the new receipt against a fresh API read. If
confirmation fails, refresh instead of replaying or assuming a rollback.

[ReplaceNetworkAclAssociation](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ReplaceNetworkAclAssociation.html)
takes the current `AssociationId` and destination `NetworkAclId`, plus the
standard Query API version. `DryRun=true` and extra parameters are not
supported. The response contains `newAssociationId`. A change issues a fresh
ID; sending the same target with the current ID is a no-op. The immediate
prior ID can recover the same committed target's receipt. Older IDs or a
different target with a stale ID require fresh discovery.

Both the destination `network-acl` ARN and the actual resolved `subnet` ARN
must allow `ec2:ReplaceNetworkAclAssociation`. `DescribeNetworkAcls` with
`association.subnet-id` reads the whole matching ACL and reports its saved
links as `associationSet/item` entries with ACL, subnet and association IDs.
The VPC default is marked `default=true` with one immutable allow rule
and terminal deny in each IPv4 direction. A custom ACL with a subnet is
read-only for deletion and rule changes. This operation does not change
subnet CIDR, neighbors, routes, or actual network traffic.

## Rule profile and readback

- Use `Egress=false` for inbound and `Egress=true` for outbound. Both directions
  have their own rule numbers from 1 to 32766 and a fixed limit of 20 editable
  rules. Adding a 21st returns `NetworkAclEntryLimitExceeded`.
- `RuleAction` is `allow` or `deny`. IPv4 `CidrBlock` is saved in canonical form.
  Protocols are `6` (TCP), `17` (UDP) and `-1` (all). TCP/UDP require both
  ordered ports in 0–65535. All-protocol rules take no port range. Rule and
  port numbers are unpadded decimals. IPv6, ICMP, other protocols,
  `DryRun=true` and unrecognized parameters are rejected, not ignored.
- `DescribeNetworkAcls` returns inbound and outbound in separate ascending
  sequences in `entrySet`, including the immutable terminal IPv4 deny at
  `32767` in each as with a custom ACL. TCP/UDP use `portRange/from` and
  `portRange/to`. Detached custom rows are `default=false`, association-free and
  `simulatorNetworkAclMode=detached-config-only`. Attached custom rows are
  `attached-config-only` and the stored VPC default is `default-config-only`.
- Every entry mutation checks the ACL, its native parent and CloudFormation
  ownership in one SQLite transaction, verifies the stored change and only
  then returns `return=true`. If delivery or subsequent confirmation is
  uncertain, re-read the same ACL and confirm the intended rule or absence
  before retrying. These are individual operations, not one batch edit.

## Large inventories and downgrades

The Console reads at most 16 MiB per EC2 Query response and does not paginate
these reads. On a very large inventory, try a VPC filter or use
`DescribeNetworkAcls` through the native SDK with explicit ACL IDs in
small batches. If the Console cannot confirm a write because its follow-up
read was too large, check the saved state before retrying.

Keep a pre-upgrade copy of the SQLite state if a downgrade is possible.
An older Simulator can read unchanged detached ACLs but cannot read a row after
its rule state has been modified by this version, even if all editable rules
are later removed. Once a newer binary writes default and association rows,
an older binary can neither read those relationships nor reliably delete the
marked VPCs/subnets or referenced custom ACLs. To downgrade, restore the
earlier database copy instead of opening the updated state.

## Local authorization and lifecycle

The local IAM policy must allow `ec2:CreateNetworkAcl` for the existing VPC
ARN and the region's `network-acl/*` ARN, and `ec2:CreateTags` for the ACL
if tags are provided. The real assigned ACL ARN is checked again before
writing or replaying a request. Describe uses `ec2:DescribeNetworkAcls` on
`*`; deletion and all three entry mutations each require their own `ec2:`
action on the specific ACL ARN. Instance-scoped sessions cannot use these
operations. See the [EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html).

`ClientToken` accepts 1–64 printable ASCII characters. Only its digest and
the canonical request digest are stored, atomically with the new ACL. A
matching retry in the same region returns the existing ACL with its current
saved associations, without recreating or changing it. A changed request with the same token receives `IdempotentParameterMismatch`.
A matching retry after ACL removal returns `InvalidNetworkAclID.NotFound`
instead of silently recreating a resource. The existing 1,000-resource
network bound and a local 4,096-receipt bound apply; deletion does not
evict the replay receipt. This is a bounded local behavior, not a claim of
identical AWS receipt retention.

The opt-in check `python test/e2e/ec2_detached_nacl.py` uses an actual local
simulator binary and SDK, isolated databases, and numeric loopback only.
It exercises detached ACL and rule lifecycles, saved-state readback, scope and
cleanup across a restart. It does not exercise network enforcement.
