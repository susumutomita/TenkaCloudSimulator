# Regional VPC console pages

Open **VPC** in the simulator console and use the sidebar to reach **Subnets**,
**Route tables**, **Internet gateways**, **Network ACLs**, and **Security groups**.
The separate pages need an ordinary signed-in local console session with the
corresponding EC2 Query operations advertised. The current exercise identity
still needs the server's IAM permission for each actual resource and operation.

| Page | Saved configuration in this simulator |
| --- | --- |
| VPCs | [Native IPv4 VPCs and the limited CloudFormation option](ec2-read-discovery.md#console-vpc-and-one-subnet). Select a VPC to view [DNS settings](ec2-vpc-dns-attributes.md) or open its [read-only Resource map](#read-only-resource-map). |
| Subnets | [Regional inventory, native IPv4 creation and verified route-table association](ec2-native-subnet.md). On eligible native subnets, use Actions to [change the saved network ACL](ec2-detached-network-acls.md#change-a-subnets-saved-network-acl). |
| Route tables | Tags, saved IPv4 routes, and subnet associations. Native operations are available where backed by the [route table](ec2-read-discovery.md#route-table-readback) and [route](ec2-native-default-route.md) APIs. |
| Internet gateways | [Regional inventory, detached creation/deletion, and VPC attachment/detachment](ec2-internet-gateways.md). CloudFormation-owned gateways remain read-only here. |
| Network ACLs | [Stored ACLs, IPv4 rules and associations](ec2-detached-network-acls.md). Rules are editable only while a custom ACL is detached. This does not filter traffic. |
| Security groups | Select a VPC to see stored groups. [Native detached groups and supported TCP/IPv4 rules](ec2-read-discovery.md#security-group-readback) can be managed where advertised; CloudFormation groups are read-only here. |

The links use the selected region. A resolved subnet can link to its actual
route table and VPC. Route-table references only appear as links to destinations
available in this session. A security group is opened under its exact parent
VPC. Back and Forward revisit the prior address; Reload confirms the resource
from the API again. Changing region returns to the corresponding inventory,
without reusing another region's selection. After a confirmed mutation the
console can replace the address with the saved resource or, on deletion, the
remaining inventory. An unconfirmed write is not automatically replayed.

DNS resolution and DNS hostnames are separate saved VPC settings. **Edit VPC
settings** writes one attribute per request and reads it back. A previous
change can remain if a later request fails; the console shows the confirmed
state rather than retrying a write. A CloudFormation-owned VPC is read-only in
this pane. These settings do not run a DNS server or assign DNS names.

## Read-only Resource map

In **VPCs**, select a VPC and choose **Resource map** (**リソースマップ**).
**Details** is the default tab and still contains DNS settings. The map
requires an ordinary signed-in local session, a VPC confirmed by
`DescribeVpcs` in the current region, and at least one supported map read
advertised in that session. It does not appear in a restricted handoff session.

- **Subnet to route table:** Shows the recorded explicit association where
  confirmed. Without one, it shows an implicit association only when the
  subnet and route-table inventories agree and the simulator confirms the
  native main table and its matching IPv4 `local` route. It does not infer a
  main table for a CloudFormation-owned VPC or a legacy VPC that lacks that
  record.
- **IPv4 route to internet gateway:** Shows a saved destination and its gateway
  only when the target is confirmed as attached to that same VPC. Detached
  gateways and gateways attached to another VPC are not displayed as targets.

The map independently reads `DescribeSubnets`, `DescribeRouteTables`, and
`DescribeInternetGateways` as available. It does not take an atomic snapshot
across those reads. A denied, unavailable, or inconsistent inventory is not
shown as empty. If only some routes can be matched, the map shows the
confirmed relationships and reports that others could not be confirmed.
Links appear only for destinations supported in the current session and do
not grant additional permissions.

Use **Refresh resource map** to request fresh data. Pending results are
discarded when you leave the tab or change the VPC or region. A save in
**Details**, such as a DNS update, prevents switching tabs until the
result has been read back or the attempt has ended. The map displays
saved simulator configuration only. It does not route traffic, verify
Internet access, or launch an EC2 instance.

AWS references: [route tables](https://docs.aws.amazon.com/vpc/latest/userguide/VPC_Route_Tables.html),
[internet gateways](https://docs.aws.amazon.com/vpc/latest/userguide/VPC_Internet_Gateway.html),
[network ACLs](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-network-acls.html), and
[security groups](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-security-groups.html).
The corresponding simulator settings do **not** forward packets, establish
Internet access, evaluate guest reachability, or turn a native subnet and
security group into an EC2 launch target.
