# EC2: detached custom IPv4 network ACLs

Issue [#56](https://github.com/susumutomita/TenkaCloudSimulator/issues/56)
tracks the broader VPC workflow. The simulator stores detached custom ACLs
and a bounded set of IPv4 rules inside an existing EC2-managed VPC. These are
**saved configuration only**: there is no subnet association, packet filtering,
VPC default ACL, IPv6, Terraform workflow or CloudFormation ACL lifecycle.

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
The VPC details pane manages the ACL itself; neither page associates ACLs with
subnets or filters traffic.

Both the SDK and Console use authenticated EC2 Query/XML, version `2016-11-15`.

| Operation | Supported arguments and effect |
| --- | --- |
| [CreateNetworkAcl](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateNetworkAcl.html) | `VpcId`, optional `ClientToken`, optional one `TagSpecification` of type `network-acl` with at most 50 tags. Creates one durable ACL in the caller's region under the EC2-managed parent. |
| [DescribeNetworkAcls](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeNetworkAcls.html) | All detached ACLs, up to 50 explicit ACL IDs and/or one `vpc-id` filter with up to 50 VPC IDs. Results are ordered by ACL ID. Other filters and pagination return an error. |
| [DeleteNetworkAcl](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteNetworkAcl.html) | `NetworkAclId`. Deletes only the requested detached ACL and its saved rules. Missing IDs return an error. Delete ACLs before deleting their VPC. |
| [CreateNetworkAclEntry](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateNetworkAclEntry.html) | `NetworkAclId`, `RuleNumber`, `Egress`, `Protocol`, `RuleAction`, `CidrBlock`, plus `PortRange.From` and `PortRange.To` for TCP/UDP. Adds one numbered rule. Duplicate numbers in that direction return `NetworkAclEntryAlreadyExists`. |
| [ReplaceNetworkAclEntry](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_ReplaceNetworkAclEntry.html) | Same arguments as create. Replaces only that existing direction and rule number. An identical normalized replacement leaves the saved generation unchanged. |
| [DeleteNetworkAclEntry](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DeleteNetworkAclEntry.html) | `NetworkAclId`, `RuleNumber`, `Egress`. Removes only that existing rule. Replace and delete return `InvalidNetworkAclEntry.NotFound` if it does not exist. |

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
  `portRange/to`. The ACL stays `default=false`, association-free and
  `simulatorNetworkAclMode=detached-config-only`.
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
are later removed. To downgrade, restore the earlier database copy rather
than opening the updated database with the previous binary.

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
matching retry in the same region returns the existing ACL; a changed
request with the same token receives `IdempotentParameterMismatch`.
A matching retry after ACL removal returns `InvalidNetworkAclID.NotFound`
instead of silently recreating a resource. The existing 1,000-resource
network bound and a local 4,096-receipt bound apply; deletion does not
evict the replay receipt. This is a bounded local behavior, not a claim of
identical AWS receipt retention.

The opt-in check `python test/e2e/ec2_detached_nacl.py` uses an actual local
simulator binary and SDK, isolated databases, and numeric loopback only.
It exercises detached ACL and rule lifecycles, saved-state readback, scope and
cleanup across a restart. It does not exercise network enforcement.
